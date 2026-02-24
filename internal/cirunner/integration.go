package cirunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/civerify"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type integrationState struct {
	startedAt                    time.Time
	obsStartedAt                 time.Time
	obsFinishedAt                time.Time
	snapshots                    int
	maxGrowingPVCs               int
	sawCapacityToRequestMismatch bool

	checkContext        string
	checkPromEndpoint   string
	checkManagerRollout string
	checkPlanReconcile  string
	checkTrendSignal    string
	checkGrowthCompare  string
	checkPromRule       string
	checkManagerMetrics string
	checkPromAlerts     string
	checkWorkloadAlerts string
	checkAlertmanager   string

	sawCapacityAlerts bool
}

type IntegrationRunner struct {
	cfg     Config
	clients *Clients
	state   integrationState

	promPF        *PortForwardSession
	alertPF       *PortForwardSession
	managerPF     *PortForwardSession
	promClient    *civerify.PrometheusClient
	alertVerifier *civerify.AlertVerifier
}

func NewIntegrationRunner(cfg Config) (*IntegrationRunner, error) {
	clients, err := BuildClients()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &IntegrationRunner{
		cfg:     cfg,
		clients: clients,
		state: integrationState{
			startedAt:           now,
			obsStartedAt:        now,
			obsFinishedAt:       now,
			checkContext:        "pending",
			checkPromEndpoint:   "pending",
			checkManagerRollout: "pending",
			checkPlanReconcile:  "pending",
			checkTrendSignal:    "pending",
			checkGrowthCompare:  "pending",
			checkPromRule:       "pending",
			checkManagerMetrics: "pending",
			checkPromAlerts:     "pending",
			checkWorkloadAlerts: "pending",
			checkAlertmanager:   "pending",
		},
	}, nil
}

func (r *IntegrationRunner) closePortForwards() {
	if r.managerPF != nil {
		r.managerPF.Close()
	}
	if r.promPF != nil {
		r.promPF.Close()
	}
	if r.alertPF != nil {
		r.alertPF.Close()
	}
}

func (r *IntegrationRunner) renderValidationReport() {
	trendSecs := int64(r.state.obsFinishedAt.Sub(r.state.obsStartedAt).Seconds())
	if trendSecs < 0 {
		trendSecs = 0
	}
	totalSecs := int64(time.Since(r.state.startedAt).Seconds())
	if totalSecs < 0 {
		totalSecs = 0
	}
	report := civerify.ValidationReport{
		Context:                    r.state.checkContext,
		PrometheusEndpoint:         r.state.checkPromEndpoint,
		ManagerRollout:             r.state.checkManagerRollout,
		PlanReconcile:              r.state.checkPlanReconcile,
		TrendSignal:                r.state.checkTrendSignal,
		GrowthMathCrosscheck:       r.state.checkGrowthCompare,
		PromRuleContent:            r.state.checkPromRule,
		ManagerMetrics:             r.state.checkManagerMetrics,
		PrometheusCapacityAlerts:   r.state.checkPromAlerts,
		WorkloadBudgetAlerts:       r.state.checkWorkloadAlerts,
		AlertmanagerCapacityAlerts: r.state.checkAlertmanager,
		Snapshots:                  r.state.snapshots,
		PeakGrowingPVCs:            r.state.maxGrowingPVCs,
		TrendSeconds:               trendSecs,
		TotalSeconds:               totalSecs,
	}
	civerify.PrintValidationReport(os.Stdout, report)
	if r.state.sawCapacityToRequestMismatch {
		fmt.Println("Note: Prometheus kubelet volume stats capacity appears to reflect backing filesystem size (not PVC request) for at least one PVC.")
	}
	if err := civerify.WriteValidationReportJSON(r.cfg.ValidationReportJSON, report); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write validation report: %v\n", err)
	}
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type pvcBackendMount struct {
	NodeName string
	Path     string
}

func (r *IntegrationRunner) selectProvisioningNode(ctx context.Context) (string, error) {
	nodes, err := discoverK3DNodes(ctx, r.cfg.ClusterName)
	if err != nil {
		return "", fmt.Errorf("discovering k3d nodes: %w", err)
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("no k3d nodes discovered for cluster %s", r.cfg.ClusterName)
	}
	// Use the first discovered node; PVCs are CI-only test assets.
	return nodes[0], nil
}

func (r *IntegrationRunner) annotatePVCSelectedNode(ctx context.Context, namespace string, pvcNames []string, nodeName string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"volume.kubernetes.io/selected-node":%q}}}`, nodeName)
	for _, pvcName := range pvcNames {
		if _, err := r.clients.Clientset.CoreV1().PersistentVolumeClaims(namespace).Patch(
			ctx,
			pvcName,
			types.MergePatchType,
			[]byte(patch),
			metav1.PatchOptions{},
		); err != nil {
			return fmt.Errorf("annotating pvc %s/%s with selected node %s: %w", namespace, pvcName, nodeName, err)
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (r *IntegrationRunner) resolvePVCBackendMounts(ctx context.Context, namespace string, pvcNames []string) ([]pvcBackendMount, error) {
	mounts := make([]pvcBackendMount, 0, len(pvcNames))
	pathOwner := make(map[string]string, len(pvcNames))
	for _, pvcName := range pvcNames {
		pvc, err := r.clients.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting pvc %s/%s: %w", namespace, pvcName, err)
		}
		if pvc.Spec.VolumeName == "" {
			return nil, fmt.Errorf("pvc %s/%s has no bound volumeName", namespace, pvcName)
		}
		pv, err := r.clients.Clientset.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting pv %s for pvc %s/%s: %w", pvc.Spec.VolumeName, namespace, pvcName, err)
		}
		if pv.Spec.HostPath == nil || strings.TrimSpace(pv.Spec.HostPath.Path) == "" {
			return nil, fmt.Errorf("pv %s for pvc %s/%s is not hostPath-backed", pv.Name, namespace, pvcName)
		}
		hostPath := strings.TrimSpace(pv.Spec.HostPath.Path)
		if owner, exists := pathOwner[hostPath]; exists && owner != pvcName {
			return nil, fmt.Errorf(
				"duplicate hostPath backend %q resolved for pvc %s and pvc %s; each PVC must have a unique backend path",
				hostPath, owner, pvcName,
			)
		}
		pathOwner[hostPath] = pvcName
		targetNode := ""
		if pv.Spec.NodeAffinity != nil && pv.Spec.NodeAffinity.Required != nil {
			for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
				for _, expr := range term.MatchExpressions {
					if expr.Key == corev1.LabelHostname && expr.Operator == corev1.NodeSelectorOpIn && len(expr.Values) > 0 {
						targetNode = strings.TrimSpace(expr.Values[0])
						break
					}
				}
				if targetNode != "" {
					break
				}
			}
		}
		mounts = append(mounts, pvcBackendMount{NodeName: targetNode, Path: hostPath})
	}
	return mounts, nil
}

func (r *IntegrationRunner) prepareDedicatedPVCBackends(ctx context.Context, mounts []pvcBackendMount) error {
	nodes, err := discoverK3DNodes(ctx, r.cfg.ClusterName)
	if err != nil {
		return fmt.Errorf("discovering k3d nodes: %w", err)
	}
	nodeSet := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		nodeSet[n] = struct{}{}
	}
	pathsByNode := make(map[string][]string, len(nodes))
	for _, m := range mounts {
		if _, ok := nodeSet[m.NodeName]; ok && m.NodeName != "" {
			pathsByNode[m.NodeName] = append(pathsByNode[m.NodeName], m.Path)
			continue
		}
		for _, n := range nodes {
			pathsByNode[n] = append(pathsByNode[n], m.Path)
		}
	}
	script := `
set -eu
for dir in "$@"; do
  mkdir -p "$dir"
  if grep -qs " ${dir} " /proc/mounts; then
    umount "$dir"
  fi
  mount -t tmpfs -o size=500m tmpfs "$dir"
  chmod 0777 "$dir"
done
`
	for _, node := range nodes {
		paths := uniqueStrings(pathsByNode[node])
		if len(paths) == 0 {
			continue
		}
		args := []string{"exec", node, "sh", "-ceu", script, "--"}
		args = append(args, paths...)
		if _, err := commandOutput(ctx, "docker", args...); err != nil {
			return fmt.Errorf("preparing PVC mount backends on %s: %w", node, err)
		}
	}
	return nil
}

func (r *IntegrationRunner) installKubePrometheusStack(ctx context.Context) error {
	if err := runCommand(ctx, "helm", "repo", "add", "prometheus-community", "https://prometheus-community.github.io/helm-charts"); err != nil {
		return fmt.Errorf("helm repo add failed: %w", err)
	}
	if err := runCommand(ctx, "helm", "repo", "update"); err != nil {
		return fmt.Errorf("helm repo update failed: %w", err)
	}
	args := []string{
		"upgrade", "--install", "kube-prometheus-stack", "prometheus-community/kube-prometheus-stack",
		"--version", r.cfg.KubePromChartVersion,
		"--namespace", r.cfg.MonitoringNamespace,
		"--create-namespace",
		"--wait",
		"--timeout", "12m",
		"-f", r.cfg.KubePromValuesFile,
	}
	if strings.TrimSpace(r.cfg.KubePromValuesExtraFile) != "" {
		args = append(args, "-f", r.cfg.KubePromValuesExtraFile)
	}
	if err := runCommand(ctx, "helm", args...); err != nil {
		return fmt.Errorf("helm upgrade/install kube-prometheus-stack failed: %w", err)
	}
	return nil
}

func (r *IntegrationRunner) ensureManagerDeployment(ctx context.Context, dep *appsv1.Deployment) error {
	updated := dep.DeepCopy()
	if len(updated.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("manager deployment has no containers")
	}
	container := &updated.Spec.Template.Spec.Containers[0]
	container.Image = r.cfg.OperatorImage
	container.ImagePullPolicy = "Never"

	needArgs := map[string]struct{}{
		"--metrics-bind-address=:8080":                    {},
		"--metrics-secure=false":                          {},
		fmt.Sprintf("--prometheus-url=%s", r.cfg.PromURL): {},
		"--debug=true":                                    {},
	}
	for _, a := range container.Args {
		delete(needArgs, a)
	}
	for arg := range needArgs {
		container.Args = append(container.Args, arg)
	}
	sort.Strings(container.Args)

	if updated.Spec.Template.Spec.ServiceAccountName == "" {
		updated.Spec.Template.Spec.ServiceAccountName = "k8s-operator-controller-manager"
	}

	_, err := r.clients.Clientset.AppsV1().Deployments(r.cfg.OpNamespace).Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating manager deployment: %w", err)
	}
	return nil
}

func httpBody(ctx context.Context, url string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}
	return string(raw), nil
}

func (r *IntegrationRunner) startPrometheusPortForward(ctx context.Context) error {
	pf, err := StartPodPortForward(r.clients, r.cfg.MonitoringNamespace, "prometheus-kube-prometheus-stack-prometheus-0", 19090, 9090)
	if err != nil {
		return err
	}
	r.promPF = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		return fmt.Errorf("prometheus port-forward not ready: %w", err)
	}
	if err := waitUntil(ctx, time.Duration(r.cfg.PromEndpointReadyTimeout)*time.Second, r.cfg.PollInterval(), "Prometheus /-/ready", func(ctx context.Context) (bool, error) {
		_, err := httpBody(ctx, "http://127.0.0.1:19090/-/ready", 5*time.Second)
		if err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return err
	}
	r.promClient = civerify.NewPrometheusClient("http://127.0.0.1:19090", 5*time.Second)
	if err := waitUntil(ctx, time.Duration(r.cfg.PromEndpointReadyTimeout)*time.Second, r.cfg.PollInterval(), "Prometheus API", func(ctx context.Context) (bool, error) {
		ok, err := r.promClient.QueryInstantHasResults(ctx, "up")
		if err != nil {
			return false, nil
		}
		return ok, nil
	}); err != nil {
		return err
	}
	return nil
}

func pvcNames(workloads []string) []string {
	out := make([]string, 0, len(workloads))
	for _, w := range workloads {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		out = append(out, w+"-pvc")
	}
	return out
}

func (r *IntegrationRunner) printCapacitySnapshot(cp *capacityv1.CapacityPlan) {
	now := time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("[%s] CapacityPlan PVC snapshot (%s)\n", now, r.cfg.PlanName)
	if len(cp.Status.PVCs) == 0 {
		fmt.Println("  status.pvcs is empty")
		return
	}
	fmt.Println("  pvc usedBytes samples usageRatio growthBytesPerDay growthBytesPerMin")
	for _, pvc := range cp.Status.PVCs {
		gpm := pvc.GrowthBytesPerDay / 1440.0
		fmt.Printf("  %s %d %d %.12g %.12g %.2f\n", pvc.Name, pvc.UsedBytes, pvc.SamplesCount, pvc.UsageRatio, pvc.GrowthBytesPerDay, gpm)
	}
}

func (r *IntegrationRunner) printGrowthSummary(cp *capacityv1.CapacityPlan) {
	now := time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("[%s] Derived growth summary (bytes/min)\n", now)
	if len(cp.Status.PVCs) == 0 {
		fmt.Println("  status.pvcs is empty")
		return
	}
	fmt.Println("  pvc growthBytesPerMin")
	for _, pvc := range cp.Status.PVCs {
		fmt.Printf("  %s %.2f\n", pvc.Name, pvc.GrowthBytesPerDay/1440.0)
	}
}

func (r *IntegrationRunner) prometheusScalar(ctx context.Context, query string) (float64, bool) {
	if r.promClient == nil {
		return 0, false
	}
	v, has, err := r.promClient.QueryInstantScalar(ctx, query)
	if err != nil {
		return 0, false
	}
	return v, has
}

func promUsedBytesQuery(namespace, pvc string) string {
	return fmt.Sprintf(
		`max(kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=%q})`,
		namespace, pvc,
	)
}

func promCapacityBytesQuery(namespace, pvc string) string {
	return fmt.Sprintf(
		`max(kube_persistentvolumeclaim_resource_requests_storage_bytes{namespace=%q,persistentvolumeclaim=%q}) or max(kubelet_volume_stats_capacity_bytes{namespace=%q,persistentvolumeclaim=%q})`,
		namespace, pvc, namespace, pvc,
	)
}

func (r *IntegrationRunner) pvcRequestedBytes(ctx context.Context, pvcName string) (int64, bool) {
	pvc, err := r.clients.Clientset.CoreV1().PersistentVolumeClaims("default").Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return 0, false
	}
	qty, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if !ok {
		return 0, false
	}
	return qty.Value(), true
}

func (r *IntegrationRunner) printPrometheusPVCRawSnapshot(ctx context.Context, pvcs []string) int {
	now := time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("[%s] Prometheus PVC raw snapshot (default namespace)\n", now)
	fmt.Println("  pvc usedBytes capBytes reqBytes ratio usedSeriesCount capToReq")
	mismatchCount := 0
	for _, pvc := range pvcs {
		used, usedOK := r.prometheusScalar(ctx, promUsedBytesQuery("default", pvc))
		cap, capOK := r.prometheusScalar(ctx, promCapacityBytesQuery("default", pvc))
		series, seriesOK := r.prometheusScalar(ctx, fmt.Sprintf(`count(kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=%q})`, "default", pvc))
		req, reqOK := r.pvcRequestedBytes(ctx, pvc)

		ratioStr := "n/a"
		if usedOK && capOK && cap > 0 {
			ratioStr = fmt.Sprintf("%.6f", used/cap)
		}
		capToReq := "n/a"
		if capOK && reqOK && req > 0 {
			capToReq = fmt.Sprintf("%.2f", cap/float64(req))
			if cap/float64(req) > 4 {
				mismatchCount++
			}
		}
		usedStr := "n/a"
		if usedOK {
			usedStr = fmt.Sprintf("%.0f", used)
		}
		capStr := "n/a"
		if capOK {
			capStr = fmt.Sprintf("%.0f", cap)
		}
		seriesStr := "n/a"
		if seriesOK {
			seriesStr = fmt.Sprintf("%.0f", series)
		}
		reqStr := "n/a"
		if reqOK {
			reqStr = fmt.Sprintf("%d", req)
		}
		fmt.Printf("  %s %s %s %s %s %s %s\n", pvc, usedStr, capStr, reqStr, ratioStr, seriesStr, capToReq)
	}
	if mismatchCount > 0 {
		fmt.Printf("  note: %d PVC(s) have capBytes much larger than requested storage; kubelet stats likely reflect backing filesystem capacity\n", mismatchCount)
	}
	return mismatchCount
}

func (r *IntegrationRunner) countGrowingPVCs(cp *capacityv1.CapacityPlan) int {
	count := 0
	for _, pvc := range cp.Status.PVCs {
		if pvc.GrowthBytesPerDay/1440.0 > r.cfg.MinGrowthBytesPerMin {
			count++
		}
	}
	return count
}

func (r *IntegrationRunner) hasNonzeroUsage(cp *capacityv1.CapacityPlan) bool {
	for _, pvc := range cp.Status.PVCs {
		if pvc.UsedBytes > 0 {
			return true
		}
	}
	return false
}

func (r *IntegrationRunner) hasPrometheusNonzeroUsage(ctx context.Context, pvcs []string) bool {
	for _, pvc := range pvcs {
		used, ok := r.prometheusScalar(ctx, promUsedBytesQuery("default", pvc))
		if ok && used > 0 {
			return true
		}
	}
	return false
}

func (r *IntegrationRunner) hasInvalidUsageRatio(cp *capacityv1.CapacityPlan) bool {
	if r.cfg.UsageRatioSanityMax <= 0 {
		return false
	}
	bad := false
	for _, pvc := range cp.Status.PVCs {
		if pvc.UsageRatio > r.cfg.UsageRatioSanityMax {
			fmt.Fprintf(os.Stderr, "invalid usage ratio for %s ratio=%.6f used=%d cap=%d\n", pvc.Name, pvc.UsageRatio, pvc.UsedBytes, pvc.CapacityBytes)
			bad = true
		}
	}
	return bad
}

func (r *IntegrationRunner) prometheusHasCapacityAlerts(ctx context.Context) bool {
	if r.promClient == nil {
		return false
	}
	ok, err := r.promClient.QueryInstantHasResults(ctx, civerify.BuildPrometheusCapacityAlertsQuery())
	if err != nil {
		return false
	}
	return ok
}

func (r *IntegrationRunner) compareGrowthCalculations(ctx context.Context) error {
	cpGrowth, err := civerify.LoadCapacityPlanPVCGrowth(ctx, r.clients.Controller, r.cfg.PlanName)
	if err != nil {
		return err
	}
	opts := civerify.CompareOptions{
		RelativeTolerance:    r.cfg.GrowthCompareRelTol,
		AbsToleranceBytesDay: r.cfg.GrowthCompareAbsTolBytesDay,
		MinComparablePVCs:    r.cfg.MinGrowthComparablePVCs,
		MinMatchingPVCs:      r.cfg.MinGrowthMatchingPVCs,
	}
	window := r.cfg.GrowthCompareWindowSeconds
	summary, compareErr := civerify.CompareGrowth(ctx, cpGrowth, func(ctx context.Context, pvcName string) (float64, bool, error) {
		q := civerify.BuildPVCGrowthDerivQuery("default", pvcName, window)
		return r.promClient.QueryInstantScalar(ctx, q)
	}, opts)
	civerify.PrintGrowthSummary(os.Stdout, summary, window)
	if compareErr != nil {
		return compareErr
	}
	r.printGrowthInterpretation(cpGrowth, summary)
	r.state.checkGrowthCompare = fmt.Sprintf("pass (%d/%d matched)", summary.Matched, summary.Comparable)
	return nil
}

func (r *IntegrationRunner) printGrowthInterpretation(cpGrowth []civerify.PVCGrowth, summary civerify.ComparisonSummary) {
	if len(cpGrowth) == 0 {
		return
	}
	minPerMin := cpGrowth[0].StatusBytesPerDay / 1440.0
	maxPerMin := minPerMin
	sumPerMin := 0.0
	for _, row := range cpGrowth {
		v := row.StatusBytesPerDay / 1440.0
		if v < minPerMin {
			minPerMin = v
		}
		if v > maxPerMin {
			maxPerMin = v
		}
		sumPerMin += v
	}
	avgPerMin := sumPerMin / float64(len(cpGrowth))
	maxRelDiff := 0.0
	maxRelName := ""
	for _, row := range summary.Rows {
		if !row.HasPromData {
			continue
		}
		if row.RelDiffPct > maxRelDiff {
			maxRelDiff = row.RelDiffPct
			maxRelName = row.Name
		}
	}
	fmt.Println("Growth interpretation")
	fmt.Printf("  status growth range: %.2f to %.2f MiB/min (avg %.2f MiB/min)\n", minPerMin/(1024*1024), maxPerMin/(1024*1024), avgPerMin/(1024*1024))
	fmt.Printf("  cross-check match: %d/%d PVCs within tolerances\n", summary.Matched, summary.Comparable)
	if maxRelName != "" {
		fmt.Printf("  largest status-vs-prometheus delta: %s (%.2f%%)\n", maxRelName, maxRelDiff)
	}
}

func (r *IntegrationRunner) checkCapacityPlanPostConditions(ctx context.Context, firstReconcile *metav1.Time) error {
	cp, err := getCapacityPlan(ctx, r.clients, r.cfg.PlanName)
	if err != nil {
		return fmt.Errorf("getting capacity plan: %w", err)
	}
	if cp.Status.LastReconcileTime == nil {
		return fmt.Errorf("capacity plan has empty status.lastReconcileTime")
	}
	if firstReconcile != nil && cp.Status.LastReconcileTime.Equal(firstReconcile) {
		return fmt.Errorf("capacity plan lastReconcileTime did not advance during trend observation")
	}

	ready := ""
	promReady := ""
	for _, cond := range cp.Status.Conditions {
		if cond.Type == "Ready" {
			ready = string(cond.Status)
		}
		if cond.Type == "PrometheusReady" {
			promReady = string(cond.Status)
		}
	}
	if ready != "True" {
		return fmt.Errorf("Ready condition is not True (got %q)", ready)
	}
	if promReady != "True" {
		return fmt.Errorf("PrometheusReady condition is not True (got %q)", promReady)
	}
	if cp.Status.Summary.TotalPVCs < 5 {
		return fmt.Errorf("expected at least five PVCs in summary, got %d", cp.Status.Summary.TotalPVCs)
	}
	if len(cp.Status.TopRisks) < 1 {
		return fmt.Errorf("expected at least one top risk after trend observation")
	}
	if strings.TrimSpace(cp.Status.RiskDigest) == "" {
		return fmt.Errorf("status.riskDigest is empty")
	}
	if strings.TrimSpace(cp.Status.AnomalySummary) == "" {
		return fmt.Errorf("status.anomalySummary is empty")
	}
	if len(cp.Status.NamespaceForecasts) == 0 || cp.Status.NamespaceForecasts[0].Scope != "namespace" {
		return fmt.Errorf("expected first namespace forecast scope=namespace")
	}
	if len(cp.Status.WorkloadForecasts) == 0 || cp.Status.WorkloadForecasts[0].Scope != "workload" {
		return fmt.Errorf("expected first workload forecast scope=workload")
	}
	return nil
}

func (r *IntegrationRunner) validatePrometheusRuleContent(ctx context.Context) error {
	gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}
	name := fmt.Sprintf("capacityplan-%s", r.cfg.PlanName)
	obj, err := r.clients.Dynamic.Resource(gvr).Namespace("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting prometheusrule/%s: %w", name, err)
	}
	raw, err := json.Marshal(obj.Object)
	if err != nil {
		return err
	}
	text := string(raw)
	for _, alert := range []string{"PVCGrowthAccelerationSpike", "PVCTrendInstability", "NamespaceBudgetBreachSoon", "WorkloadBudgetBreachSoon"} {
		if !strings.Contains(text, alert) {
			return fmt.Errorf("missing %s alert in generated PrometheusRule", alert)
		}
	}
	return nil
}

func (r *IntegrationRunner) startManagerMetricsPortForward(ctx context.Context) error {
	pod, err := getFirstPodByLabel(ctx, r.clients, r.cfg.OpNamespace, map[string]string{"control-plane": "controller-manager"})
	if err != nil {
		return err
	}
	pf, err := StartPodPortForward(r.clients, r.cfg.OpNamespace, pod.Name, 18080, 8080)
	if err != nil {
		return err
	}
	r.managerPF = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		return err
	}
	return waitUntil(ctx, time.Duration(r.cfg.ManagerEndpointReadyTimeout)*time.Second, r.cfg.PollInterval(), "operator metrics endpoint", func(ctx context.Context) (bool, error) {
		body, err := httpBody(ctx, "http://127.0.0.1:18080/metrics", 5*time.Second)
		if err != nil {
			return false, nil
		}
		if !strings.Contains(body, "capacityplan_namespace_budget_days_to_breach") {
			return false, nil
		}
		if !strings.Contains(body, "capacityplan_workload_budget_days_to_breach") {
			return false, nil
		}
		if !strings.Contains(body, "capacityplan_pvc_anomaly") {
			return false, nil
		}
		return true, nil
	})
}

func (r *IntegrationRunner) startAlertmanagerPortForward(ctx context.Context) error {
	pf, err := StartPodPortForward(r.clients, r.cfg.MonitoringNamespace, "alertmanager-kube-prometheus-stack-alertmanager-0", 19093, 9093)
	if err != nil {
		return err
	}
	r.alertPF = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		return err
	}
	if err := waitUntil(ctx, time.Duration(r.cfg.AlertEndpointReadyTimeout)*time.Second, r.cfg.PollInterval(), "Alertmanager /-/ready", func(ctx context.Context) (bool, error) {
		_, err := httpBody(ctx, "http://127.0.0.1:19093/-/ready", 5*time.Second)
		if err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return err
	}
	if err := waitUntil(ctx, time.Duration(r.cfg.AlertEndpointReadyTimeout)*time.Second, r.cfg.PollInterval(), "Alertmanager /api/v2/status", func(ctx context.Context) (bool, error) {
		body, err := httpBody(ctx, "http://127.0.0.1:19093/api/v2/status", 5*time.Second)
		if err != nil {
			return false, nil
		}
		return strings.Contains(body, "cluster"), nil
	}); err != nil {
		return err
	}
	r.alertVerifier = civerify.NewAlertVerifier(r.promClient, "http://127.0.0.1:19093", 5*time.Second)
	return nil
}

func (r *IntegrationRunner) verifyAlerts(ctx context.Context) error {
	poll := r.cfg.PollInterval()
	timeout := time.Duration(r.cfg.AlertPropagationTimeout) * time.Second

	if !r.state.sawCapacityAlerts {
		if err := civerify.WaitUntil(ctx, timeout, poll, r.alertVerifier.PrometheusHasCapacityAlerts); err != nil {
			return fmt.Errorf("timed out waiting for capacity alerts in Prometheus ALERTS metric after %ds", r.cfg.AlertPropagationTimeout)
		}
		r.state.checkPromAlerts = "pass (after wait)"
	} else {
		r.state.checkPromAlerts = "pass (seen during trend observation)"
	}

	if err := civerify.WaitUntil(ctx, timeout, poll, func(ctx context.Context) (bool, error) {
		return r.alertVerifier.PrometheusHasAllWorkloadBudgetAlerts(ctx, r.cfg.CIWorkloads)
	}); err != nil {
		return fmt.Errorf("timed out waiting for WorkloadBudgetBreachSoon alerts for all workloads")
	}
	for _, w := range r.cfg.CIWorkloads {
		ok, err := r.alertVerifier.PrometheusHasWorkloadBudgetAlert(ctx, w)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("missing WorkloadBudgetBreachSoon for %s after aggregate wait", w)
		}
	}
	r.state.checkWorkloadAlerts = fmt.Sprintf("pass (%d workloads)", len(r.cfg.CIWorkloads))

	if err := civerify.WaitUntil(ctx, timeout, poll, r.alertVerifier.AlertmanagerHasCapacityAlerts); err != nil {
		return fmt.Errorf("timed out waiting for capacity alerts in Alertmanager API after %ds", r.cfg.AlertPropagationTimeout)
	}
	r.state.checkAlertmanager = "pass"
	return nil
}

func (r *IntegrationRunner) observeTrends(ctx context.Context) error {
	logStep(fmt.Sprintf("Observing storage trends for %ds", r.cfg.TrendObserveSeconds))
	r.state.obsStartedAt = time.Now()
	remaining := r.cfg.TrendObserveSeconds
	if remaining <= 0 {
		remaining = 1
	}
	pvcs := pvcNames(r.cfg.CIWorkloads)
	sawNonzeroUsage := false
	maxGrowing := 0
	snapshots := 0
	var lastCP *capacityv1.CapacityPlan
	for remaining > 0 {
		interval := r.cfg.UsageSnapshotInterval
		if interval <= 0 {
			interval = 60
		}
		if interval > remaining {
			interval = remaining
		}
		time.Sleep(time.Duration(interval) * time.Second)
		remaining -= interval
		snapshots++
		fmt.Printf("  snapshot=%d elapsed=%ds remaining=%ds\n", snapshots, int(time.Since(r.state.obsStartedAt).Seconds()), remaining)

		cp, err := getCapacityPlan(ctx, r.clients, r.cfg.PlanName)
		if err != nil {
			return fmt.Errorf("getting capacity plan during observation: %w", err)
		}
		lastCP = cp
		r.printCapacitySnapshot(cp)
		r.printGrowthSummary(cp)
		mismatchCount := r.printPrometheusPVCRawSnapshot(ctx, pvcs)
		if mismatchCount > 0 {
			r.state.sawCapacityToRequestMismatch = true
		}

		growing := r.countGrowingPVCs(cp)
		fmt.Printf("  growingPVCsAboveThreshold=%d thresholdBytesPerMin=%.0f\n", growing, r.cfg.MinGrowthBytesPerMin)
		if growing > maxGrowing {
			maxGrowing = growing
		}
		if r.hasNonzeroUsage(cp) || r.hasPrometheusNonzeroUsage(ctx, pvcs) {
			sawNonzeroUsage = true
		}
		if r.prometheusHasCapacityAlerts(ctx) {
			r.state.sawCapacityAlerts = true
		}
		if r.hasInvalidUsageRatio(cp) {
			raw, _ := json.MarshalIndent(cp, "", "  ")
			fmt.Println(string(raw))
			return fmt.Errorf("capacity plan usage ratio exceeded sanity limit (%.4f)", r.cfg.UsageRatioSanityMax)
		}

		obsElapsed := int(time.Since(r.state.obsStartedAt).Seconds())
		if remaining > 0 &&
			snapshots >= r.cfg.MinTrendSnapshots &&
			obsElapsed >= r.cfg.MinTrendObserveSeconds &&
			sawNonzeroUsage &&
			maxGrowing >= r.cfg.MinGrowingPVCs {
			fmt.Printf("  earlyStop=true reason=trend-signals-confirmed elapsed=%ds snapshots=%d\n", obsElapsed, snapshots)
			break
		}
	}
	if !sawNonzeroUsage {
		return fmt.Errorf("all PVC usedBytes remained 0 throughout trend observation; no growth signal detected from metrics")
	}
	if maxGrowing < r.cfg.MinGrowingPVCs {
		return fmt.Errorf("peak growing PVC count was %d; required at least %d PVCs above %.0f bytes/min", maxGrowing, r.cfg.MinGrowingPVCs, r.cfg.MinGrowthBytesPerMin)
	}
	r.state.obsFinishedAt = time.Now()
	r.state.snapshots = snapshots
	r.state.maxGrowingPVCs = maxGrowing
	if lastCP != nil && len(lastCP.Status.PVCs) > 0 {
		sumPerMin := 0.0
		minPerMin := lastCP.Status.PVCs[0].GrowthBytesPerDay / 1440.0
		maxPerMin := minPerMin
		for _, pvc := range lastCP.Status.PVCs {
			v := pvc.GrowthBytesPerDay / 1440.0
			sumPerMin += v
			if v < minPerMin {
				minPerMin = v
			}
			if v > maxPerMin {
				maxPerMin = v
			}
		}
		avgPerMin := sumPerMin / float64(len(lastCP.Status.PVCs))
		fmt.Println("Trend interpretation")
		fmt.Printf("  sampled snapshots: %d\n", snapshots)
		fmt.Printf("  growing PVCs above threshold: %d/%d (threshold %.0f bytes/min)\n", maxGrowing, len(lastCP.Status.PVCs), r.cfg.MinGrowthBytesPerMin)
		fmt.Printf("  latest growth range: %.2f to %.2f MiB/min (avg %.2f MiB/min)\n", minPerMin/(1024*1024), maxPerMin/(1024*1024), avgPerMin/(1024*1024))
	}
	r.state.checkTrendSignal = fmt.Sprintf("pass (nonzeroUsage=1, peakGrowingPVCs=%d)", maxGrowing)
	return nil
}

func (r *IntegrationRunner) Run(ctx context.Context) error {
	defer r.closePortForwards()
	defer r.renderValidationReport()

	logStep("Validating kubectl context")
	if strings.TrimSpace(r.clients.CurrentContext) == "" {
		return fmt.Errorf("kube current context is empty")
	}
	if r.clients.CurrentContext != r.cfg.ExpectedKubeContext {
		return fmt.Errorf("kubectl context mismatch: expected %s, got %s", r.cfg.ExpectedKubeContext, r.clients.CurrentContext)
	}
	r.state.checkContext = fmt.Sprintf("pass (%s)", r.clients.CurrentContext)

	logStep("Installing kube-prometheus-stack")
	if err := r.installKubePrometheusStack(ctx); err != nil {
		return err
	}

	logStep("Waiting for monitoring CRDs and workloads")
	if err := waitForCRDsEstablished(ctx, r.clients, []string{
		"servicemonitors.monitoring.coreos.com",
		"prometheusrules.monitoring.coreos.com",
	}, 5*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}
	if err := waitForDeploymentRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "kube-prometheus-stack-operator", 10*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}
	if err := waitForStatefulSetRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "prometheus-kube-prometheus-stack-prometheus", 10*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}
	if err := waitForStatefulSetRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "alertmanager-kube-prometheus-stack-alertmanager", 10*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}

	logStep("Validating Prometheus endpoint readiness")
	if err := r.startPrometheusPortForward(ctx); err != nil {
		return err
	}
	r.state.checkPromEndpoint = "pass"

	logStep("Deploying operator manifests")
	if err := ApplyOperatorManifests(ctx, r.clients); err != nil {
		return err
	}
	managerDep, err := getDeploymentByLabel(ctx, r.clients, r.cfg.OpNamespace, map[string]string{"control-plane": "controller-manager"})
	if err != nil {
		return err
	}
	if err := r.ensureManagerDeployment(ctx, managerDep); err != nil {
		return err
	}
	if err := waitForDeploymentRollout(ctx, r.clients, r.cfg.OpNamespace, managerDep.Name, time.Duration(r.cfg.ManagerRolloutTimeout)*time.Second, r.cfg.PollInterval()); err != nil {
		return err
	}
	r.state.checkManagerRollout = "pass"

	logStep("Creating PVC workload and CapacityPlan")
	if err := ApplyWorkloadStorageManifests(ctx, r.clients, r.cfg.CIManifestDir); err != nil {
		return err
	}
	provisionNode, err := r.selectProvisioningNode(ctx)
	if err != nil {
		return err
	}
	logStep(fmt.Sprintf("Pinning CI PVCs to node %s for provisioning", provisionNode))
	if err := r.annotatePVCSelectedNode(ctx, "default", pvcNames(r.cfg.CIWorkloads), provisionNode); err != nil {
		return err
	}
	if err := waitForPVCsBound(ctx, r.clients, "default", pvcNames(r.cfg.CIWorkloads), 5*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}
	logStep("Preparing dedicated PVC backends for kubelet volume metrics")
	mounts, err := r.resolvePVCBackendMounts(ctx, "default", pvcNames(r.cfg.CIWorkloads))
	if err != nil {
		return err
	}
	if err := r.prepareDedicatedPVCBackends(ctx, mounts); err != nil {
		return err
	}
	if err := ApplyWorkloadPodManifests(ctx, r.clients, r.cfg.CIManifestDir); err != nil {
		return err
	}
	if err := waitForPodsScheduled(ctx, r.clients, "default", r.cfg.CIWorkloads, 3*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}
	if err := ApplyCapacityPlan(ctx, r.clients, r.cfg); err != nil {
		return err
	}

	logStep("Waiting for CapacityPlan reconciliation")
	firstReconcile, err := waitForCapacityPlanReconcile(ctx, r.clients, r.cfg.PlanName, 5*time.Minute, r.cfg.PollInterval())
	if err != nil {
		return err
	}
	r.state.checkPlanReconcile = "pass"

	if err := r.observeTrends(ctx); err != nil {
		return err
	}

	logStep("Cross-checking growth calculations against Prometheus deriv()")
	if err := r.compareGrowthCalculations(ctx); err != nil {
		return err
	}

	if err := r.checkCapacityPlanPostConditions(ctx, firstReconcile); err != nil {
		return err
	}

	logStep("Validating generated PrometheusRule content")
	if err := r.validatePrometheusRuleContent(ctx); err != nil {
		return err
	}
	r.state.checkPromRule = "pass"

	logStep("Checking operator metrics for new budget/anomaly metrics")
	if err := r.startManagerMetricsPortForward(ctx); err != nil {
		return err
	}
	r.state.checkManagerMetrics = "pass"

	logStep("Checking Alertmanager readiness endpoint")
	if err := r.startAlertmanagerPortForward(ctx); err != nil {
		return err
	}

	logStep("Verifying alert pipeline (Prometheus + workload + Alertmanager)")
	if err := r.verifyAlerts(ctx); err != nil {
		return err
	}

	logStep("K3s integration checks passed")
	return nil
}

func CapacityPlanYAML(ctx context.Context, c *Clients, planName string) (string, error) {
	cp, err := getCapacityPlan(ctx, c, planName)
	if err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func ManagerDeploymentName(ctx context.Context, c *Clients, namespace string) (string, error) {
	dep, err := getDeploymentByLabel(ctx, c, namespace, map[string]string{"control-plane": "controller-manager"})
	if err != nil {
		return "", err
	}
	return dep.Name, nil
}

func CapacityPlanExists(ctx context.Context, c *Clients, name string) (bool, error) {
	_, err := getCapacityPlan(ctx, c, name)
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		return false, nil
	}
	return false, err
}

func WaitForCapacityPlan(ctx context.Context, c *Clients, name string, timeout time.Duration) error {
	return waitUntil(ctx, timeout, 5*time.Second, fmt.Sprintf("capacityplan/%s exists", name), func(ctx context.Context) (bool, error) {
		var cp capacityv1.CapacityPlan
		err := c.Controller.Get(ctx, types.NamespacedName{Name: name}, &cp)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
}
