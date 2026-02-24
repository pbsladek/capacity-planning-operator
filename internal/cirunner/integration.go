package cirunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/civerify"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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
	checkLLMInsights    string
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
	alertReceiver *PortForwardSession
	managerPF     *PortForwardSession
	llmPF         *PortForwardSession
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
			checkLLMInsights:    "pending",
			checkPromAlerts:     "pending",
			checkWorkloadAlerts: "pending",
			checkAlertmanager:   "pending",
		},
	}, nil
}

func (r *IntegrationRunner) closePortForwards() {
	if r.llmPF != nil {
		r.llmPF.Close()
	}
	if r.managerPF != nil {
		r.managerPF.Close()
	}
	if r.alertReceiver != nil {
		r.alertReceiver.Close()
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
		LLMInsights:                r.state.checkLLMInsights,
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report.PVCTrendDetails, report.WorkloadBudgetDetails, report.LLMRiskChangeSummary, report.LLMBudgetRecommendations = r.printCapacityPlanInsights(ctx)
	report.AlertmanagerNotifications = r.printAlertmanagerInsights(ctx)
	if err := civerify.WriteValidationReportJSON(r.cfg.ValidationReportJSON, report); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write validation report: %v\n", err)
	}
}

func trimText(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || max <= 0 || len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func daysString(days *float64) string {
	if days == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", *days)
}

func copyFloat64Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func forecastTarget(f capacityv1.StorageBudgetForecast) string {
	if strings.EqualFold(f.Scope, "workload") {
		return fmt.Sprintf("%s/%s/%s", f.Namespace, f.Kind, f.Name)
	}
	return f.Namespace
}

func alertTarget(d civerify.AlertDetail) string {
	if d.Namespace != "" && d.PVC != "" {
		return fmt.Sprintf("%s/pvc/%s", d.Namespace, d.PVC)
	}
	if d.Namespace != "" && d.Workload != "" && d.Kind != "" {
		return fmt.Sprintf("%s/%s/%s", d.Namespace, d.Kind, d.Workload)
	}
	if d.Namespace != "" {
		return d.Namespace
	}
	return "cluster"
}

func alertReasonHint(alertName string) string {
	switch alertName {
	case "PVCUsageHigh":
		return "usage ratio above warning threshold"
	case "PVCUsageCritical":
		return "usage ratio above critical threshold"
	case "NamespaceBudgetBreachSoon":
		return "namespace budget forecast under 7d"
	case "WorkloadBudgetBreachSoon":
		return "workload budget forecast under 7d"
	default:
		return "capacity rule matched"
	}
}

func (r *IntegrationRunner) printCapacityPlanInsights(ctx context.Context) ([]civerify.PVCTrendDetail, []civerify.WorkloadBudgetDetail, string, string) {
	cp, err := getCapacityPlan(ctx, r.clients, r.cfg.PlanName)
	if err != nil {
		fmt.Printf("Detailed insights: unable to fetch CapacityPlan %q: %v\n", r.cfg.PlanName, err)
		return nil, nil, "", ""
	}

	pvcDetails := make([]civerify.PVCTrendDetail, 0, len(cp.Status.PVCs))
	if len(cp.Status.PVCs) > 0 {
		fmt.Println("Latest PVC trend summary")
		rows := append([]capacityv1.PVCSummary(nil), cp.Status.PVCs...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  pvc\tusedMiB\tusageRatio\tgrowthMiBPerMin\tsamples\talertFiring\tdaysUntilFull")
		for _, pvc := range rows {
			fmt.Fprintf(
				tw,
				"  %s\t%.2f\t%.3f\t%.2f\t%d\t%t\t%s\n",
				pvc.Name,
				float64(pvc.UsedBytes)/(1024.0*1024.0),
				pvc.UsageRatio,
				(pvc.GrowthBytesPerDay/1440.0)/(1024.0*1024.0),
				pvc.SamplesCount,
				pvc.AlertFiring,
				daysString(pvc.DaysUntilFull),
			)
			pvcDetails = append(pvcDetails, civerify.PVCTrendDetail{
				Namespace:         pvc.Namespace,
				Name:              pvc.Name,
				UsedBytes:         pvc.UsedBytes,
				UsedMiB:           float64(pvc.UsedBytes) / (1024.0 * 1024.0),
				UsageRatio:        pvc.UsageRatio,
				GrowthBytesPerDay: pvc.GrowthBytesPerDay,
				GrowthMiBPerMin:   (pvc.GrowthBytesPerDay / 1440.0) / (1024.0 * 1024.0),
				SamplesCount:      pvc.SamplesCount,
				AlertFiring:       pvc.AlertFiring,
				DaysUntilFull:     copyFloat64Ptr(pvc.DaysUntilFull),
			})
		}
		_ = tw.Flush()
	}

	workloadDetails := make([]civerify.WorkloadBudgetDetail, 0, len(cp.Status.WorkloadForecasts))
	if len(cp.Status.WorkloadForecasts) > 0 {
		fmt.Println("Latest workload budget forecast")
		rows := append([]capacityv1.StorageBudgetForecast(nil), cp.Status.WorkloadForecasts...)
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Namespace != rows[j].Namespace {
				return rows[i].Namespace < rows[j].Namespace
			}
			if rows[i].Kind != rows[j].Kind {
				return rows[i].Kind < rows[j].Kind
			}
			return rows[i].Name < rows[j].Name
		})
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  workload\tbudgetMiB\tusedMiB\tusageRatio\tgrowthMiBPerMin\tdaysUntilBreach\tprojectedBreachAt")
		for _, f := range rows {
			projected := "n/a"
			if f.ProjectedBreachAt != nil {
				projected = f.ProjectedBreachAt.Time.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(
				tw,
				"  %s\t%.2f\t%.2f\t%.3f\t%.2f\t%s\t%s\n",
				forecastTarget(f),
				float64(f.BudgetBytes)/(1024.0*1024.0),
				float64(f.UsedBytes)/(1024.0*1024.0),
				f.UsageRatio,
				(f.GrowthBytesPerDay/1440.0)/(1024.0*1024.0),
				daysString(f.DaysUntilBreach),
				projected,
			)
			workloadDetails = append(workloadDetails, civerify.WorkloadBudgetDetail{
				Namespace:         f.Namespace,
				Kind:              f.Kind,
				Name:              f.Name,
				Target:            forecastTarget(f),
				BudgetBytes:       f.BudgetBytes,
				BudgetMiB:         float64(f.BudgetBytes) / (1024.0 * 1024.0),
				UsedBytes:         f.UsedBytes,
				UsedMiB:           float64(f.UsedBytes) / (1024.0 * 1024.0),
				UsageRatio:        f.UsageRatio,
				GrowthBytesPerDay: f.GrowthBytesPerDay,
				GrowthMiBPerMin:   (f.GrowthBytesPerDay / 1440.0) / (1024.0 * 1024.0),
				DaysUntilBreach:   copyFloat64Ptr(f.DaysUntilBreach),
				ProjectedBreachAt: projected,
			})
		}
		_ = tw.Flush()
	}
	llmRiskChangeSummary := strings.TrimSpace(cp.Status.LLMRiskChangeSummary)
	llmBudgetRecommendations := strings.TrimSpace(cp.Status.LLMBudgetRecommendations)
	if llmRiskChangeSummary != "" {
		fmt.Println("Plan-level LLM risk-change insight")
		fmt.Printf("  %s\n", llmRiskChangeSummary)
	}
	if llmBudgetRecommendations != "" {
		fmt.Println("Plan-level LLM budget recommendations")
		fmt.Printf("  %s\n", llmBudgetRecommendations)
	}
	return pvcDetails, workloadDetails, llmRiskChangeSummary, llmBudgetRecommendations
}

func (r *IntegrationRunner) printAlertmanagerInsights(ctx context.Context) []civerify.AlertNotificationDetail {
	if r.alertVerifier == nil {
		fmt.Println("Alertmanager notification summary: unavailable (alert verifier not initialized)")
		return nil
	}
	details, err := r.alertVerifier.AlertmanagerCapacityAlertDetails(ctx)
	if err != nil {
		fmt.Printf("Alertmanager notification summary: unable to fetch alerts: %v\n", err)
		return nil
	}
	if len(details) == 0 {
		fmt.Println("Alertmanager notification summary: no active capacity alerts at report time")
		return nil
	}
	sort.Slice(details, func(i, j int) bool {
		if details[i].AlertName != details[j].AlertName {
			return details[i].AlertName < details[j].AlertName
		}
		return alertTarget(details[i]) < alertTarget(details[j])
	})
	fmt.Println("Alertmanager capacity notifications")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  alert\tstate\tseverity\ttarget\twhy\tsummary")
	reportRows := make([]civerify.AlertNotificationDetail, 0, len(details))
	for _, d := range details {
		target := alertTarget(d)
		why := alertReasonHint(d.AlertName)
		fmt.Fprintf(
			tw,
			"  %s\t%s\t%s\t%s\t%s\t%s\n",
			d.AlertName,
			d.State,
			d.Severity,
			target,
			why,
			trimText(d.Summary, 110),
		)
		reportRows = append(reportRows, civerify.AlertNotificationDetail{
			AlertName:   d.AlertName,
			State:       d.State,
			Severity:    d.Severity,
			Namespace:   d.Namespace,
			PVC:         d.PVC,
			Kind:        d.Kind,
			Workload:    d.Workload,
			Target:      target,
			Why:         why,
			Summary:     d.Summary,
			Description: d.Description,
			StartsAt:    d.StartsAt,
			UpdatedAt:   d.UpdatedAt,
		})
	}
	_ = tw.Flush()
	return reportRows
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

func pvBackendPath(pv *corev1.PersistentVolume, namespace, pvcName string) (string, error) {
	switch {
	case pv.Spec.HostPath != nil && strings.TrimSpace(pv.Spec.HostPath.Path) != "":
		return strings.TrimSpace(pv.Spec.HostPath.Path), nil
	case pv.Spec.Local != nil && strings.TrimSpace(pv.Spec.Local.Path) != "":
		return strings.TrimSpace(pv.Spec.Local.Path), nil
	default:
		return "", fmt.Errorf("pv %s for pvc %s/%s is not hostPath/local-backed", pv.Name, namespace, pvcName)
	}
}

func pvTargetNode(pv *corev1.PersistentVolume) string {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return ""
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == corev1.LabelHostname && expr.Operator == corev1.NodeSelectorOpIn && len(expr.Values) > 0 {
				return strings.TrimSpace(expr.Values[0])
			}
		}
	}
	return ""
}

func ensureUniqueBackendPath(pathOwner map[string]string, backendPath, pvcName string) error {
	if owner, exists := pathOwner[backendPath]; exists && owner != pvcName {
		return fmt.Errorf(
			"duplicate PV backend path %q resolved for pvc %s and pvc %s; each PVC must have a unique backend path",
			backendPath, owner, pvcName,
		)
	}
	pathOwner[backendPath] = pvcName
	return nil
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
		backendPath, err := pvBackendPath(pv, namespace, pvcName)
		if err != nil {
			return nil, err
		}
		if err := ensureUniqueBackendPath(pathOwner, backendPath, pvcName); err != nil {
			return nil, err
		}
		mounts = append(mounts, pvcBackendMount{NodeName: pvTargetNode(pv), Path: backendPath})
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
	if dep == nil {
		return fmt.Errorf("manager deployment is nil")
	}
	name := strings.TrimSpace(dep.Name)
	if name == "" {
		return fmt.Errorf("manager deployment name is empty")
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := r.clients.Clientset.AppsV1().Deployments(r.cfg.OpNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		updated := current.DeepCopy()
		changed, err := r.applyManagerDeploymentSettings(updated)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		_, err = r.clients.Clientset.AppsV1().Deployments(r.cfg.OpNamespace).Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("updating manager deployment: %w", err)
	}
	return nil
}

func (r *IntegrationRunner) applyManagerDeploymentSettings(dep *appsv1.Deployment) (bool, error) {
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return false, fmt.Errorf("manager deployment has no containers")
	}
	changed := false
	container := &dep.Spec.Template.Spec.Containers[0]
	if container.Image != r.cfg.OperatorImage {
		container.Image = r.cfg.OperatorImage
		changed = true
	}
	if container.ImagePullPolicy != corev1.PullNever {
		container.ImagePullPolicy = corev1.PullNever
		changed = true
	}

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
		changed = true
	}

	sortedArgs := append([]string(nil), container.Args...)
	sort.Strings(container.Args)
	for i := range sortedArgs {
		if sortedArgs[i] != container.Args[i] {
			changed = true
			break
		}
	}

	if dep.Spec.Template.Spec.ServiceAccountName == "" {
		dep.Spec.Template.Spec.ServiceAccountName = "k8s-operator-controller-manager"
		changed = true
	}
	return changed, nil
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

func httpPostJSON(ctx context.Context, url string, payload any, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
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
	rows := append([]capacityv1.PVCSummary(nil), cp.Status.PVCs...)
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  pvc\tusedBytes\tusedMiB\tsamples\tusageRatio\tgrowthBytesPerDay\tgrowthMiBPerMin")
	for _, pvc := range rows {
		gpm := pvc.GrowthBytesPerDay / 1440.0
		fmt.Fprintf(
			tw,
			"  %s\t%d\t%.2f\t%d\t%.6f\t%.6g\t%.2f\n",
			pvc.Name,
			pvc.UsedBytes,
			float64(pvc.UsedBytes)/(1024.0*1024.0),
			pvc.SamplesCount,
			pvc.UsageRatio,
			pvc.GrowthBytesPerDay,
			gpm/(1024.0*1024.0),
		)
	}
	_ = tw.Flush()
}

func (r *IntegrationRunner) printGrowthSummary(cp *capacityv1.CapacityPlan) {
	now := time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("[%s] Derived growth summary (bytes/min)\n", now)
	if len(cp.Status.PVCs) == 0 {
		fmt.Println("  status.pvcs is empty")
		return
	}
	rows := append([]capacityv1.PVCSummary(nil), cp.Status.PVCs...)
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  pvc\tgrowthBytesPerMin\tgrowthMiBPerMin")
	for _, pvc := range rows {
		gpm := pvc.GrowthBytesPerDay / 1440.0
		fmt.Fprintf(tw, "  %s\t%.2f\t%.2f\n", pvc.Name, gpm, gpm/(1024.0*1024.0))
	}
	_ = tw.Flush()
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

type promPVCRawRow struct {
	Name       string
	Used       float64
	UsedOK     bool
	Cap        float64
	CapOK      bool
	Req        int64
	ReqOK      bool
	Series     float64
	SeriesOK   bool
	RatioStr   string
	CapToReq   string
	UsedStr    string
	UsedMiBStr string
	CapStr     string
	CapMiBStr  string
	ReqStr     string
	SeriesStr  string
	Mismatch   bool
}

func formatPromPVCRawRow(row *promPVCRawRow) {
	row.RatioStr = "n/a"
	if row.UsedOK && row.CapOK && row.Cap > 0 {
		row.RatioStr = fmt.Sprintf("%.6f", row.Used/row.Cap)
	}
	row.CapToReq = "n/a"
	if row.CapOK && row.ReqOK && row.Req > 0 {
		ratio := row.Cap / float64(row.Req)
		row.CapToReq = fmt.Sprintf("%.2f", ratio)
		row.Mismatch = ratio > 4
	}
	row.UsedStr = "n/a"
	row.UsedMiBStr = "n/a"
	if row.UsedOK {
		row.UsedStr = fmt.Sprintf("%.0f", row.Used)
		row.UsedMiBStr = fmt.Sprintf("%.2f", row.Used/(1024.0*1024.0))
	}
	row.CapStr = "n/a"
	row.CapMiBStr = "n/a"
	if row.CapOK {
		row.CapStr = fmt.Sprintf("%.0f", row.Cap)
		row.CapMiBStr = fmt.Sprintf("%.2f", row.Cap/(1024.0*1024.0))
	}
	row.ReqStr = "n/a"
	if row.ReqOK {
		row.ReqStr = fmt.Sprintf("%d", row.Req)
	}
	row.SeriesStr = "n/a"
	if row.SeriesOK {
		row.SeriesStr = fmt.Sprintf("%.0f", row.Series)
	}
}

func (r *IntegrationRunner) collectPromPVCRawRow(ctx context.Context, pvc string) promPVCRawRow {
	used, usedOK := r.prometheusScalar(ctx, promUsedBytesQuery("default", pvc))
	cap, capOK := r.prometheusScalar(ctx, promCapacityBytesQuery("default", pvc))
	series, seriesOK := r.prometheusScalar(
		ctx,
		fmt.Sprintf(`count(kubelet_volume_stats_used_bytes{namespace=%q,persistentvolumeclaim=%q})`, "default", pvc),
	)
	req, reqOK := r.pvcRequestedBytes(ctx, pvc)

	row := promPVCRawRow{
		Name:     pvc,
		Used:     used,
		UsedOK:   usedOK,
		Cap:      cap,
		CapOK:    capOK,
		Req:      req,
		ReqOK:    reqOK,
		Series:   series,
		SeriesOK: seriesOK,
	}
	formatPromPVCRawRow(&row)
	return row
}

func (r *IntegrationRunner) printPrometheusPVCRawSnapshot(ctx context.Context, pvcs []string) int {
	now := time.Now().UTC().Format(time.RFC3339)
	fmt.Printf("[%s] Prometheus PVC raw snapshot (default namespace)\n", now)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  pvc\tusedBytes\tusedMiB\tcapBytes\tcapMiB\treqBytes\tratio\tusedSeriesCount\tcapToReq")
	mismatchCount := 0
	for _, pvc := range pvcs {
		row := r.collectPromPVCRawRow(ctx, pvc)
		if row.Mismatch {
			mismatchCount++
		}
		fmt.Fprintf(
			tw,
			"  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name,
			row.UsedStr,
			row.UsedMiBStr,
			row.CapStr,
			row.CapMiBStr,
			row.ReqStr,
			row.RatioStr,
			row.SeriesStr,
			row.CapToReq,
		)
	}
	_ = tw.Flush()
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
	r.printGrowthInterpretation(cpGrowth, summary, opts)
	r.state.checkGrowthCompare = fmt.Sprintf("pass (%d/%d matched)", summary.Matched, summary.Comparable)
	return nil
}

func statusGrowthStats(cpGrowth []civerify.PVCGrowth) (minPerMin, maxPerMin, avgPerMin float64) {
	if len(cpGrowth) == 0 {
		return 0, 0, 0
	}
	minPerMin = cpGrowth[0].StatusBytesPerDay / 1440.0
	maxPerMin = minPerMin
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
	avgPerMin = sumPerMin / float64(len(cpGrowth))
	return minPerMin, maxPerMin, avgPerMin
}

func sortedComparisonRows(summary civerify.ComparisonSummary) []civerify.ComparisonRow {
	rows := append([]civerify.ComparisonRow(nil), summary.Rows...)
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})
	return rows
}

func comparisonMaxRelDiff(rows []civerify.ComparisonRow) (string, float64) {
	maxRelName := ""
	maxRelDiff := 0.0
	for _, row := range rows {
		if !row.HasPromData {
			continue
		}
		if row.RelDiffPct > maxRelDiff {
			maxRelDiff = row.RelDiffPct
			maxRelName = row.Name
		}
	}
	return maxRelName, maxRelDiff
}

func printGrowthComparisonRowsTable(rows []civerify.ComparisonRow) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  pvc\tstatusMiBPerMin\tpromMiBPerMin\trelDiffPct\tallowedDiffBytesPerDay\tmatch\treason")
	for _, row := range rows {
		prom := "n/a"
		if row.HasPromData {
			prom = fmt.Sprintf("%.2f", row.PromBytesPerDay/(1024.0*1024.0*1440.0))
		}
		allowed := "n/a"
		if row.HasPromData {
			allowed = fmt.Sprintf("%.12g", row.AllowedDiff)
		}
		rel := "n/a"
		if row.HasPromData {
			rel = fmt.Sprintf("%.2f", row.RelDiffPct)
		}
		match := "no"
		if row.Matched {
			match = "yes"
		}
		fmt.Fprintf(
			tw,
			"  %s\t%.2f\t%s\t%s\t%s\t%s\t%s\n",
			row.Name,
			row.StatusBytesPerDay/(1024.0*1024.0*1440.0),
			prom,
			rel,
			allowed,
			match,
			row.Reason,
		)
	}
	_ = tw.Flush()
}

func printGrowthComparisonMismatches(rows []civerify.ComparisonRow) {
	fmt.Println("  mismatches:")
	for _, row := range rows {
		if !row.HasPromData || row.Matched {
			continue
		}
		fmt.Printf(
			"    %s: absDiff=%.12g B/day relDiff=%.2f%% allowed=%.12g B/day basis=%s reason=%s\n",
			row.Name,
			row.AbsDiff,
			row.RelDiffPct,
			row.AllowedDiff,
			row.ToleranceBasis,
			row.Reason,
		)
	}
}

func (r *IntegrationRunner) printGrowthInterpretation(
	cpGrowth []civerify.PVCGrowth,
	summary civerify.ComparisonSummary,
	opts civerify.CompareOptions,
) {
	if len(cpGrowth) == 0 {
		return
	}
	minPerMin, maxPerMin, avgPerMin := statusGrowthStats(cpGrowth)
	noData := len(summary.Rows) - summary.Comparable
	rows := sortedComparisonRows(summary)
	maxRelName, maxRelDiff := comparisonMaxRelDiff(rows)
	fmt.Println("Growth interpretation")
	fmt.Printf(
		"  tolerances: relative=%.2f%% absolute=%.0f B/day (effective threshold per PVC: max(relative*scale, absolute))\n",
		opts.RelativeTolerance*100.0,
		opts.AbsToleranceBytesDay,
	)
	fmt.Printf("  status growth range: %.2f to %.2f MiB/min (avg %.2f MiB/min)\n", minPerMin/(1024*1024), maxPerMin/(1024*1024), avgPerMin/(1024*1024))
	fmt.Printf(
		"  cross-check match: %d/%d comparable PVCs within tolerances (no-prom-data=%d, required comparable=%d, required matches=%d)\n",
		summary.Matched,
		summary.Comparable,
		noData,
		opts.MinComparablePVCs,
		opts.MinMatchingPVCs,
	)
	printGrowthComparisonRowsTable(rows)
	if summary.Matched < summary.Comparable {
		printGrowthComparisonMismatches(rows)
	}
	if maxRelName != "" {
		fmt.Printf("  largest status-vs-prometheus delta: %s (%.2f%%)\n", maxRelName, maxRelDiff)
	}
}

func conditionStatus(conditions []metav1.Condition, condType string) string {
	for _, cond := range conditions {
		if cond.Type == condType {
			return string(cond.Status)
		}
	}
	return ""
}

func validateCapacityPlanConditions(cp *capacityv1.CapacityPlan, firstReconcile *metav1.Time) error {
	if cp.Status.LastReconcileTime == nil {
		return fmt.Errorf("capacity plan has empty status.lastReconcileTime")
	}
	if firstReconcile != nil && cp.Status.LastReconcileTime.Equal(firstReconcile) {
		return fmt.Errorf("capacity plan lastReconcileTime did not advance during trend observation")
	}
	if ready := conditionStatus(cp.Status.Conditions, "Ready"); ready != "True" {
		return fmt.Errorf("Ready condition is not True (got %q)", ready)
	}
	if promReady := conditionStatus(cp.Status.Conditions, "PrometheusReady"); promReady != "True" {
		return fmt.Errorf("PrometheusReady condition is not True (got %q)", promReady)
	}
	return nil
}

func validateCapacityPlanStatusContent(cp *capacityv1.CapacityPlan) error {
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

func (r *IntegrationRunner) checkCapacityPlanPostConditions(ctx context.Context, firstReconcile *metav1.Time) error {
	cp, err := getCapacityPlan(ctx, r.clients, r.cfg.PlanName)
	if err != nil {
		return fmt.Errorf("getting capacity plan: %w", err)
	}
	if err := validateCapacityPlanConditions(cp, firstReconcile); err != nil {
		return err
	}
	if err := validateCapacityPlanStatusContent(cp); err != nil {
		return err
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
		if r.llmEnabled() && !strings.Contains(body, "capacityplan_llm_requests_total") {
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

func (r *IntegrationRunner) startAlertReceiverPortForward(ctx context.Context) error {
	pod, err := getFirstPodByLabel(ctx, r.clients, r.cfg.MonitoringNamespace, map[string]string{"app": "alert-receiver"})
	if err != nil {
		return err
	}
	pf, err := StartPodPortForward(r.clients, r.cfg.MonitoringNamespace, pod.Name, 19094, 8080)
	if err != nil {
		return err
	}
	r.alertReceiver = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		return err
	}
	return waitUntil(ctx, time.Duration(r.cfg.AlertEndpointReadyTimeout)*time.Second, r.cfg.PollInterval(), "alert-receiver /healthz", func(ctx context.Context) (bool, error) {
		_, err := httpBody(ctx, "http://127.0.0.1:19094/healthz", 5*time.Second)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
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

	expectedReceiver := strings.TrimSpace(r.cfg.AlertmanagerExpectedReceiver)
	expectedIntegration := strings.TrimSpace(r.cfg.AlertmanagerExpectedIntegration)
	if expectedIntegration == "" {
		expectedIntegration = "webhook"
	}

	if err := waitUntil(ctx, timeout, poll, "alertmanager route config for expected receiver", func(ctx context.Context) (bool, error) {
		statusBody, err := httpBody(ctx, "http://127.0.0.1:19093/api/v2/status", 5*time.Second)
		if err != nil {
			return false, nil
		}
		err = validateAlertmanagerRouteConfig(
			statusBody,
			expectedReceiver,
			expectedIntegration,
		)
		if err != nil {
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("alertmanager routing validation failed: %w", err)
	}

	expectedLabels := map[string]string{
		"integration": expectedIntegration,
	}
	if expectedReceiver != "" {
		expectedLabels["receiver"] = expectedReceiver
	}
	sent := 0.0
	failed := 0.0
	if err := waitUntil(ctx, timeout, poll, "alertmanager receiver delivery", func(ctx context.Context) (bool, error) {
		metricsBody, err := httpBody(ctx, "http://127.0.0.1:19093/metrics", 5*time.Second)
		if err != nil {
			return false, nil
		}
		sent = parseCounterSumWithLabels(metricsBody, "alertmanager_notifications_total", expectedLabels)
		failed = parseCounterSumWithLabels(metricsBody, "alertmanager_notifications_failed_total", expectedLabels)
		return sent > 0 && failed == 0, nil
	}); err != nil {
		return fmt.Errorf("alertmanager delivery validation failed for receiver=%q integration=%q: sent=%.0f failed=%.0f",
			expectedReceiver, expectedIntegration, sent, failed)
	}

	apiDetails, err := r.alertVerifier.AlertmanagerCapacityAlertDetails(ctx)
	if err != nil {
		return fmt.Errorf("fetching alertmanager capacity alert details: %w", err)
	}
	if err := validateAlertMetadata(apiDetails, r.cfg.CIWorkloads); err != nil {
		return fmt.Errorf("alertmanager metadata validation failed: %w", err)
	}

	receiverRecordCount := 0
	receiverAlertCount := 0
	if expectedIntegration == "webhook" {
		if err := waitUntil(ctx, timeout, poll, "alert-receiver notification payloads", func(ctx context.Context) (bool, error) {
			body, err := httpBody(ctx, "http://127.0.0.1:19094/records", 5*time.Second)
			if err != nil {
				return false, nil
			}
			records, err := parseAlertReceiverRecords(body)
			if err != nil {
				return false, nil
			}
			if records.Count < 1 || len(records.Records) < 1 {
				return false, nil
			}
			deliveredDetails, err := parseAlertReceiverDetails(records, expectedReceiver)
			if err != nil {
				return false, nil
			}
			if err := validateAlertMetadata(deliveredDetails, r.cfg.CIWorkloads); err != nil {
				return false, nil
			}
			receiverRecordCount = records.Count
			receiverAlertCount = len(deliveredDetails)
			return true, nil
		}); err != nil {
			return fmt.Errorf("alert-receiver payload validation failed: %w", err)
		}
	}

	fmt.Println("Alert pipeline interpretation")
	fmt.Printf("  routing: receiver=%s integration=%s\n", expectedReceiver, expectedIntegration)
	fmt.Printf("  alertmanager notifications: sent=%.0f failed=%.0f\n", sent, failed)
	if expectedIntegration == "webhook" {
		fmt.Printf("  delivered payloads: records=%d capacityAlerts=%d\n", receiverRecordCount, receiverAlertCount)
	}
	fmt.Printf("  metadata coverage: workload alerts validated for %d/%d workloads\n", len(r.cfg.CIWorkloads), len(r.cfg.CIWorkloads))

	r.state.checkAlertmanager = fmt.Sprintf(
		"pass (receiver=%s integration=%s sent=%.0f failed=%.0f apiAlerts=%d payloadAlerts=%d records=%d)",
		expectedReceiver,
		expectedIntegration,
		sent,
		failed,
		len(apiDetails),
		receiverAlertCount,
		receiverRecordCount,
	)
	return nil
}

type trendObservation struct {
	remaining       int
	snapshots       int
	maxGrowing      int
	sawNonzeroUsage bool
	lastCP          *capacityv1.CapacityPlan
}

func newTrendObservation(totalSeconds int) trendObservation {
	if totalSeconds <= 0 {
		totalSeconds = 1
	}
	return trendObservation{remaining: totalSeconds}
}

func (r *IntegrationRunner) trendIntervalSeconds(remaining int) int {
	interval := r.cfg.UsageSnapshotInterval
	if interval <= 0 {
		interval = 60
	}
	if interval > remaining {
		interval = remaining
	}
	return interval
}

func (r *IntegrationRunner) captureTrendSnapshot(ctx context.Context, pvcs []string, obs *trendObservation) error {
	interval := r.trendIntervalSeconds(obs.remaining)
	time.Sleep(time.Duration(interval) * time.Second)
	obs.remaining -= interval
	obs.snapshots++
	fmt.Printf("  snapshot=%d elapsed=%ds remaining=%ds\n", obs.snapshots, int(time.Since(r.state.obsStartedAt).Seconds()), obs.remaining)

	cp, err := getCapacityPlan(ctx, r.clients, r.cfg.PlanName)
	if err != nil {
		return fmt.Errorf("getting capacity plan during observation: %w", err)
	}
	obs.lastCP = cp
	r.printCapacitySnapshot(cp)
	r.printGrowthSummary(cp)
	if mismatchCount := r.printPrometheusPVCRawSnapshot(ctx, pvcs); mismatchCount > 0 {
		r.state.sawCapacityToRequestMismatch = true
	}

	growing := r.countGrowingPVCs(cp)
	fmt.Printf("  growingPVCsAboveThreshold=%d thresholdBytesPerMin=%.0f\n", growing, r.cfg.MinGrowthBytesPerMin)
	if growing > obs.maxGrowing {
		obs.maxGrowing = growing
	}
	if r.hasNonzeroUsage(cp) || r.hasPrometheusNonzeroUsage(ctx, pvcs) {
		obs.sawNonzeroUsage = true
	}
	if r.prometheusHasCapacityAlerts(ctx) {
		r.state.sawCapacityAlerts = true
	}
	if r.hasInvalidUsageRatio(cp) {
		raw, _ := json.MarshalIndent(cp, "", "  ")
		fmt.Println(string(raw))
		return fmt.Errorf("capacity plan usage ratio exceeded sanity limit (%.4f)", r.cfg.UsageRatioSanityMax)
	}
	return nil
}

func (r *IntegrationRunner) shouldStopTrendObservation(obs trendObservation) (bool, int) {
	obsElapsed := int(time.Since(r.state.obsStartedAt).Seconds())
	shouldStop := obs.remaining > 0 &&
		obs.snapshots >= r.cfg.MinTrendSnapshots &&
		obsElapsed >= r.cfg.MinTrendObserveSeconds &&
		obs.sawNonzeroUsage &&
		obs.maxGrowing >= r.cfg.MinGrowingPVCs
	return shouldStop, obsElapsed
}

func (r *IntegrationRunner) printTrendInterpretation(cp *capacityv1.CapacityPlan, snapshots, maxGrowing int) {
	if cp == nil || len(cp.Status.PVCs) == 0 {
		return
	}
	minPerMin := cp.Status.PVCs[0].GrowthBytesPerDay / 1440.0
	maxPerMin := minPerMin
	sumPerMin := 0.0
	for _, pvc := range cp.Status.PVCs {
		v := pvc.GrowthBytesPerDay / 1440.0
		sumPerMin += v
		if v < minPerMin {
			minPerMin = v
		}
		if v > maxPerMin {
			maxPerMin = v
		}
	}
	avgPerMin := sumPerMin / float64(len(cp.Status.PVCs))
	fmt.Println("Trend interpretation")
	fmt.Printf("  sampled snapshots: %d\n", snapshots)
	fmt.Printf("  growing PVCs above threshold: %d/%d (threshold %.0f bytes/min)\n", maxGrowing, len(cp.Status.PVCs), r.cfg.MinGrowthBytesPerMin)
	fmt.Printf("  latest growth range: %.2f to %.2f MiB/min (avg %.2f MiB/min)\n", minPerMin/(1024*1024), maxPerMin/(1024*1024), avgPerMin/(1024*1024))
}

func (r *IntegrationRunner) finalizeTrendObservation(obs trendObservation) error {
	if !obs.sawNonzeroUsage {
		return fmt.Errorf("all PVC usedBytes remained 0 throughout trend observation; no growth signal detected from metrics")
	}
	if obs.maxGrowing < r.cfg.MinGrowingPVCs {
		return fmt.Errorf("peak growing PVC count was %d; required at least %d PVCs above %.0f bytes/min", obs.maxGrowing, r.cfg.MinGrowingPVCs, r.cfg.MinGrowthBytesPerMin)
	}
	r.state.obsFinishedAt = time.Now()
	r.state.snapshots = obs.snapshots
	r.state.maxGrowingPVCs = obs.maxGrowing
	r.printTrendInterpretation(obs.lastCP, obs.snapshots, obs.maxGrowing)
	r.state.checkTrendSignal = fmt.Sprintf("pass (nonzeroUsage=1, peakGrowingPVCs=%d)", obs.maxGrowing)
	return nil
}

func (r *IntegrationRunner) observeTrends(ctx context.Context) error {
	logStep(fmt.Sprintf("Observing storage trends for %ds", r.cfg.TrendObserveSeconds))
	r.state.obsStartedAt = time.Now()
	pvcs := pvcNames(r.cfg.CIWorkloads)
	obs := newTrendObservation(r.cfg.TrendObserveSeconds)

	for obs.remaining > 0 {
		if err := r.captureTrendSnapshot(ctx, pvcs, &obs); err != nil {
			return err
		}
		if stop, elapsed := r.shouldStopTrendObservation(obs); stop {
			fmt.Printf("  earlyStop=true reason=trend-signals-confirmed elapsed=%ds snapshots=%d\n", elapsed, obs.snapshots)
			break
		}
	}
	return r.finalizeTrendObservation(obs)
}

func (r *IntegrationRunner) Run(ctx context.Context) error {
	defer r.closePortForwards()
	defer r.renderValidationReport()

	if err := r.validateContext(); err != nil {
		return err
	}
	if err := r.setupMonitoring(ctx); err != nil {
		return err
	}
	if err := r.deployOperator(ctx); err != nil {
		return err
	}
	firstReconcile, err := r.deployWorkloadAndPlan(ctx)
	if err != nil {
		return err
	}
	if err := r.runTrendAndPolicyChecks(ctx, firstReconcile); err != nil {
		return err
	}
	if err := r.verifyAlertPipeline(ctx); err != nil {
		return err
	}

	logStep("K3s integration checks passed")
	return nil
}

func (r *IntegrationRunner) validateContext() error {
	logStep("Validating kubectl context")
	if strings.TrimSpace(r.clients.CurrentContext) == "" {
		return fmt.Errorf("kube current context is empty")
	}
	if r.clients.CurrentContext != r.cfg.ExpectedKubeContext {
		return fmt.Errorf("kubectl context mismatch: expected %s, got %s", r.cfg.ExpectedKubeContext, r.clients.CurrentContext)
	}
	r.state.checkContext = fmt.Sprintf("pass (%s)", r.clients.CurrentContext)
	return nil
}

func (r *IntegrationRunner) setupMonitoring(ctx context.Context) error {
	rolloutTimeout := time.Duration(r.cfg.MonitoringRolloutTimeout) * time.Second
	if rolloutTimeout <= 0 {
		rolloutTimeout = 10 * time.Minute
	}
	bootstrapTimeout := 3 * time.Minute
	if rolloutTimeout < bootstrapTimeout {
		bootstrapTimeout = rolloutTimeout
	}

	logStep("Waiting for kube-system bootstrap readiness")
	if err := waitForKubeSystemBootstrap(ctx, r.clients, bootstrapTimeout, r.cfg.PollInterval()); err != nil {
		return err
	}

	logStep("Installing kube-prometheus-stack")
	if err := r.installKubePrometheusStack(ctx); err != nil {
		return err
	}

	logStep("Deploying Alertmanager webhook receiver")
	if err := ApplyAlertReceiverManifests(ctx, r.clients, r.cfg.CIManifestDir, r.cfg); err != nil {
		return err
	}
	if err := waitForDeploymentRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "alert-receiver", 5*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}

	logStep("Waiting for monitoring CRDs and workloads")
	if err := waitForCRDsEstablished(ctx, r.clients, []string{
		"servicemonitors.monitoring.coreos.com",
		"prometheusrules.monitoring.coreos.com",
	}, 5*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}
	if err := waitForDeploymentRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "kube-prometheus-stack-operator", rolloutTimeout, r.cfg.PollInterval()); err != nil {
		return err
	}
	if err := waitForStatefulSetRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "prometheus-kube-prometheus-stack-prometheus", rolloutTimeout, r.cfg.PollInterval()); err != nil {
		return err
	}
	if err := waitForStatefulSetRollout(ctx, r.clients, r.cfg.MonitoringNamespace, "alertmanager-kube-prometheus-stack-alertmanager", rolloutTimeout, r.cfg.PollInterval()); err != nil {
		return err
	}

	logStep("Validating Prometheus endpoint readiness")
	if err := r.startPrometheusPortForward(ctx); err != nil {
		return err
	}
	r.state.checkPromEndpoint = "pass"
	return nil
}

func (r *IntegrationRunner) deployOperator(ctx context.Context) error {
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
	return nil
}

func (r *IntegrationRunner) llmEnabled() bool {
	if !r.cfg.LLMEnabled {
		return false
	}
	return strings.TrimSpace(r.cfg.LLMProvider) != "" && strings.TrimSpace(r.cfg.LLMProvider) != "disabled"
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (r *IntegrationRunner) shouldHardFailLLMError(err error) bool {
	if err == nil {
		return false
	}
	if isTimeoutErr(err) {
		return r.cfg.LLMTimeoutHardFail
	}
	return !r.cfg.LLMSoftFail
}

func (r *IntegrationRunner) deployLLMBackend(ctx context.Context) error {
	if !r.llmEnabled() {
		r.state.checkLLMInsights = "skipped (provider disabled)"
		return nil
	}
	if strings.TrimSpace(r.cfg.LLMProvider) != "ollama" {
		return fmt.Errorf("unsupported CI LLM provider %q (only ollama is supported in CI runner)", r.cfg.LLMProvider)
	}

	logStep("Deploying in-cluster LLM backend (Ollama)")
	if err := ApplyLLMManifests(ctx, r.clients, r.cfg.CIManifestDir, r.cfg); err != nil {
		return err
	}
	if err := waitForDeploymentRollout(ctx, r.clients, r.cfg.LLMNamespace, r.cfg.LLMDeploymentName, 15*time.Minute, r.cfg.PollInterval()); err != nil {
		return err
	}

	pod, err := getFirstPodByLabel(ctx, r.clients, r.cfg.LLMNamespace, map[string]string{"app.kubernetes.io/name": "ollama"})
	if err != nil {
		return err
	}
	pf, err := StartPodPortForward(r.clients, r.cfg.LLMNamespace, pod.Name, 21134, 11434)
	if err != nil {
		return err
	}
	r.llmPF = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		return err
	}

	if err := waitUntil(ctx, 90*time.Second, r.cfg.PollInterval(), "Ollama /api/tags", func(ctx context.Context) (bool, error) {
		body, err := httpBody(ctx, "http://127.0.0.1:21134/api/tags", 5*time.Second)
		if err != nil {
			return false, nil
		}
		return strings.Contains(body, "models"), nil
	}); err != nil {
		return err
	}

	logStep(fmt.Sprintf("Preparing Ollama model %q", r.cfg.LLMModel))
	pullTimeout := time.Duration(r.cfg.LLMModelPullTimeoutSeconds) * time.Second
	if pullTimeout <= 0 {
		pullTimeout = 20 * time.Minute
	}
	pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	payload := map[string]interface{}{
		"model":  r.cfg.LLMModel,
		"stream": false,
	}
	if _, err := httpPostJSON(pullCtx, "http://127.0.0.1:21134/api/pull", payload, pullTimeout); err != nil {
		return fmt.Errorf("pulling ollama model %q: %w", r.cfg.LLMModel, err)
	}
	return nil
}

func (r *IntegrationRunner) deployWorkloadAndPlan(ctx context.Context) (*metav1.Time, error) {
	logStep("Creating PVC workload and CapacityPlan")
	if err := r.setupLLMBackendForWorkload(ctx); err != nil {
		return nil, err
	}
	if err := ApplyWorkloadStorageManifests(ctx, r.clients, r.cfg.CIManifestDir); err != nil {
		return nil, err
	}

	pvcList := pvcNames(r.cfg.CIWorkloads)
	provisionNode, err := r.selectProvisioningNode(ctx)
	if err != nil {
		return nil, err
	}
	logStep(fmt.Sprintf("Pinning CI PVCs to node %s for provisioning", provisionNode))
	if err := r.annotatePVCSelectedNode(ctx, "default", pvcList, provisionNode); err != nil {
		return nil, err
	}
	if err := waitForPVCsBound(ctx, r.clients, "default", pvcList, 5*time.Minute, r.cfg.PollInterval()); err != nil {
		return nil, err
	}
	logStep("Preparing dedicated PVC backends for kubelet volume metrics")
	mounts, err := r.resolvePVCBackendMounts(ctx, "default", pvcList)
	if err != nil {
		return nil, err
	}
	if err := r.prepareDedicatedPVCBackends(ctx, mounts); err != nil {
		return nil, err
	}
	if err := ApplyWorkloadPodManifests(ctx, r.clients, r.cfg.CIManifestDir); err != nil {
		return nil, err
	}
	if err := waitForPodsScheduled(ctx, r.clients, "default", r.cfg.CIWorkloads, 3*time.Minute, r.cfg.PollInterval()); err != nil {
		return nil, err
	}
	if err := ApplyCapacityPlan(ctx, r.clients, r.cfg); err != nil {
		return nil, err
	}

	logStep("Waiting for CapacityPlan reconciliation")
	firstReconcile, err := waitForCapacityPlanReconcile(ctx, r.clients, r.cfg.PlanName, 5*time.Minute, r.cfg.PollInterval())
	if err != nil {
		return nil, err
	}
	r.state.checkPlanReconcile = "pass"
	return firstReconcile, nil
}

func (r *IntegrationRunner) setupLLMBackendForWorkload(ctx context.Context) error {
	if err := r.deployLLMBackend(ctx); err != nil {
		if r.shouldHardFailLLMError(err) {
			return err
		}
		r.state.checkLLMInsights = "warn (backend setup failed: " + err.Error() + ")"
		fmt.Printf("LLM backend warning: %v\n", err)
	}
	return nil
}

func (r *IntegrationRunner) runTrendAndPolicyChecks(ctx context.Context, firstReconcile *metav1.Time) error {
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
	if err := r.verifyLLMInsights(ctx); err != nil {
		if r.shouldHardFailLLMError(err) {
			return err
		}
		r.state.checkLLMInsights = "warn (" + err.Error() + ")"
		fmt.Printf("LLM verification warning: %v\n", err)
	}
	return nil
}

func parsePrometheusCounterSum(body, metricName string) float64 {
	sum := 0.0
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, metricName+"{") && !strings.HasPrefix(line, metricName+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		sum += v
	}
	return sum
}

func parseMetricLabels(raw string) map[string]string {
	out := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	for pair := range strings.SplitSeq(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		if key == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func metricLabelsMatch(labels map[string]string, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func parseCounterSumWithLabels(body, metricName string, requiredLabels map[string]string) float64 {
	sum := 0.0
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, metricName+"{") && !strings.HasPrefix(line, metricName+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		labels := map[string]string{}
		if strings.HasPrefix(fields[0], metricName+"{") {
			openIdx := strings.Index(fields[0], "{")
			closeIdx := strings.LastIndex(fields[0], "}")
			if openIdx >= 0 && closeIdx > openIdx {
				labels = parseMetricLabels(fields[0][openIdx+1 : closeIdx])
			}
		}
		if !metricLabelsMatch(labels, requiredLabels) {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		sum += v
	}
	return sum
}

func parseAlertmanagerOriginalConfig(statusBody string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(statusBody), &payload); err != nil {
		return ""
	}
	configNode, ok := payload["config"].(map[string]any)
	if !ok {
		return ""
	}
	original, _ := configNode["original"].(string)
	return strings.TrimSpace(original)
}

type alertReceiverRecords struct {
	Count   int      `json:"count"`
	Records []string `json:"records"`
}

type alertmanagerWebhookPayload struct {
	Receiver string `json:"receiver"`
	Alerts   []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    string            `json:"startsAt"`
		EndsAt      string            `json:"endsAt"`
	} `json:"alerts"`
}

func parseAlertReceiverRecords(body string) (alertReceiverRecords, error) {
	var payload alertReceiverRecords
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return alertReceiverRecords{}, fmt.Errorf("decoding alert-receiver records: %w", err)
	}
	return payload, nil
}

func parseAlertReceiverDetails(payload alertReceiverRecords, expectedReceiver string) ([]civerify.AlertDetail, error) {
	details := make([]civerify.AlertDetail, 0, len(payload.Records))
	expectedReceiver = strings.TrimSpace(expectedReceiver)
	for _, raw := range payload.Records {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var msg alertmanagerWebhookPayload
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return nil, fmt.Errorf("decoding alert-receiver record payload: %w", err)
		}
		if expectedReceiver != "" && strings.TrimSpace(msg.Receiver) != expectedReceiver {
			return nil, fmt.Errorf("unexpected receiver in delivered payload: got %q want %q", strings.TrimSpace(msg.Receiver), expectedReceiver)
		}
		for _, alert := range msg.Alerts {
			name := strings.TrimSpace(alert.Labels["alertname"])
			switch name {
			case "PVCUsageHigh", "PVCUsageCritical", "NamespaceBudgetBreachSoon", "WorkloadBudgetBreachSoon":
				details = append(details, civerify.AlertDetail{
					AlertName:   name,
					State:       strings.TrimSpace(alert.Status),
					Severity:    strings.TrimSpace(alert.Labels["severity"]),
					Namespace:   strings.TrimSpace(alert.Labels["namespace"]),
					PVC:         strings.TrimSpace(alert.Labels["pvc"]),
					Kind:        strings.TrimSpace(alert.Labels["kind"]),
					Workload:    strings.TrimSpace(alert.Labels["workload"]),
					Summary:     strings.TrimSpace(alert.Annotations["summary"]),
					Description: strings.TrimSpace(alert.Annotations["description"]),
					StartsAt:    strings.TrimSpace(alert.StartsAt),
					UpdatedAt:   strings.TrimSpace(alert.EndsAt),
				})
			}
		}
	}
	if len(details) == 0 {
		return nil, fmt.Errorf("alert-receiver payloads did not contain capacity alerts")
	}
	return details, nil
}

func validateAlertmanagerRouteConfig(statusBody, expectedReceiver, expectedIntegration string) error {
	configText := parseAlertmanagerOriginalConfig(statusBody)
	if configText == "" {
		return fmt.Errorf("alertmanager status did not include config.original")
	}
	if expectedReceiver != "" {
		if !strings.Contains(configText, "receiver: "+expectedReceiver) ||
			!strings.Contains(configText, "- name: "+expectedReceiver) {
			return fmt.Errorf("alertmanager config does not route to expected receiver %q", expectedReceiver)
		}
	}
	switch strings.TrimSpace(expectedIntegration) {
	case "", "webhook":
		if !strings.Contains(configText, "webhook_configs:") {
			return fmt.Errorf("alertmanager config does not include webhook integration for expected receiver")
		}
	}
	return nil
}

func validateAlertMetadata(details []civerify.AlertDetail, workloads []string) error {
	if len(details) == 0 {
		return fmt.Errorf("no Alertmanager capacity alert details returned")
	}
	workloadSeen := make(map[string]struct{}, len(workloads))
	for _, d := range details {
		if strings.TrimSpace(d.Summary) == "" {
			return fmt.Errorf("alert %s missing summary annotation", d.AlertName)
		}
		switch d.AlertName {
		case "WorkloadBudgetBreachSoon":
			if strings.TrimSpace(d.Namespace) == "" || strings.TrimSpace(d.Workload) == "" || strings.TrimSpace(d.Kind) == "" {
				return fmt.Errorf("workload alert missing namespace/kind/workload labels: %+v", d)
			}
			workloadSeen[d.Workload] = struct{}{}
		case "NamespaceBudgetBreachSoon":
			if strings.TrimSpace(d.Namespace) == "" {
				return fmt.Errorf("namespace alert missing namespace label: %+v", d)
			}
		case "PVCUsageHigh", "PVCUsageCritical":
			if strings.TrimSpace(d.Namespace) == "" || strings.TrimSpace(d.PVC) == "" {
				return fmt.Errorf("pvc alert missing namespace/pvc labels: %+v", d)
			}
		}
	}
	for _, workload := range workloads {
		if _, ok := workloadSeen[workload]; !ok {
			return fmt.Errorf("alertmanager missing WorkloadBudgetBreachSoon metadata for workload %q", workload)
		}
	}
	return nil
}

func (r *IntegrationRunner) verifyLLMInsights(ctx context.Context) error {
	if !r.llmEnabled() {
		r.state.checkLLMInsights = "skipped (provider disabled)"
		return nil
	}

	cp, err := getCapacityPlan(ctx, r.clients, r.cfg.PlanName)
	if err != nil {
		return fmt.Errorf("getting capacity plan for llm verification: %w", err)
	}
	insights := 0
	for _, pvc := range cp.Status.PVCs {
		if strings.TrimSpace(pvc.LLMInsight) != "" {
			insights++
		}
	}
	planInsightSections := 0
	missingSections := make([]string, 0, 2)
	if strings.TrimSpace(cp.Status.LLMRiskChangeSummary) != "" {
		planInsightSections++
	} else {
		missingSections = append(missingSections, "llmRiskChangeSummary")
	}
	if strings.TrimSpace(cp.Status.LLMBudgetRecommendations) != "" {
		planInsightSections++
	} else {
		missingSections = append(missingSections, "llmBudgetRecommendations")
	}

	if insights >= r.cfg.LLMMinInsights && planInsightSections == 2 {
		r.state.checkLLMInsights = fmt.Sprintf("pass (pvcInsights=%d planInsights=%d/2)", insights, planInsightSections)
		return nil
	}

	metricsBody, metricsErr := httpBody(ctx, "http://127.0.0.1:18080/metrics", 5*time.Second)
	timeoutCount := 0.0
	if metricsErr == nil {
		timeoutCount = parsePrometheusCounterSum(metricsBody, "capacityplan_llm_timeouts_total")
	}
	if timeoutCount > 0 && r.cfg.LLMTimeoutHardFail {
		return fmt.Errorf("llm model timed out %.0f times (hard-fail enabled)", timeoutCount)
	}

	reasonParts := make([]string, 0, 3)
	if insights < r.cfg.LLMMinInsights {
		reasonParts = append(reasonParts, fmt.Sprintf("pvcInsights=%d below required=%d", insights, r.cfg.LLMMinInsights))
	} else {
		reasonParts = append(reasonParts, fmt.Sprintf("pvcInsights=%d", insights))
	}
	if planInsightSections < 2 {
		reasonParts = append(reasonParts, fmt.Sprintf("missingPlanInsights=%s", strings.Join(missingSections, ",")))
	}
	if timeoutCount > 0 {
		reasonParts = append(reasonParts, fmt.Sprintf("timeouts=%.0f", timeoutCount))
	}
	if metricsErr != nil {
		reasonParts = append(reasonParts, fmt.Sprintf("metricsReadErr=%v", metricsErr))
	}
	reason := strings.Join(reasonParts, ", ")

	if r.cfg.LLMSoftFail {
		r.state.checkLLMInsights = "warn (" + reason + ")"
		fmt.Printf("LLM verification warning: %s\n", reason)
		return nil
	}
	return fmt.Errorf("llm verification failed: %s", reason)
}

func (r *IntegrationRunner) verifyAlertPipeline(ctx context.Context) error {
	logStep("Checking Alertmanager readiness endpoint")
	if err := r.startAlertmanagerPortForward(ctx); err != nil {
		return err
	}
	expectedIntegration := strings.TrimSpace(r.cfg.AlertmanagerExpectedIntegration)
	if expectedIntegration == "" || expectedIntegration == "webhook" {
		if err := r.startAlertReceiverPortForward(ctx); err != nil {
			return err
		}
	}

	logStep("Verifying alert pipeline (Prometheus + workload + Alertmanager)")
	return r.verifyAlerts(ctx)
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
