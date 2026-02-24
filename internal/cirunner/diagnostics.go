package cirunner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/civerify"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

type DiagnosticsRunner struct {
	cfg     Config
	clients *Clients

	promPF  *PortForwardSession
	alertPF *PortForwardSession
}

func NewDiagnosticsRunner(cfg Config) (*DiagnosticsRunner, error) {
	clients, err := BuildClients()
	if err != nil {
		return nil, err
	}
	return &DiagnosticsRunner{cfg: cfg, clients: clients}, nil
}

func (r *DiagnosticsRunner) closePortForwards() {
	if r.promPF != nil {
		r.promPF.Close()
	}
	if r.alertPF != nil {
		r.alertPF.Close()
	}
}

func marshalYAML(obj interface{}) string {
	if obj == nil {
		return ""
	}
	raw, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Sprintf("marshal error: %v\n", err)
	}
	return string(raw)
}

func (r *DiagnosticsRunner) writeCapture(path string, fn func() (string, error)) {
	content, err := fn()
	if err != nil {
		content = fmt.Sprintf("capture error: %v\n", err)
	}
	_ = writeFile(path, content)
}

func getPodLogs(ctx context.Context, c *Clients, namespace, pod string, tail int64) (string, error) {
	req := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{TailLines: &tail})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	raw, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func httpGET(ctx context.Context, url string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
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

func (r *DiagnosticsRunner) capturePrometheusAPI(ctx context.Context, outDir string) {
	pf, err := StartPodPortForward(r.clients, r.cfg.MonitoringNamespace, "prometheus-kube-prometheus-stack-prometheus-0", 19090, 9090)
	if err != nil {
		r.writeCapture(filepath.Join(outDir, "logs", "prometheus-port-forward.log"), func() (string, error) {
			return "", err
		})
		return
	}
	r.promPF = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		r.writeCapture(filepath.Join(outDir, "logs", "prometheus-port-forward.log"), func() (string, error) {
			return "", err
		})
		return
	}
	defer r.promPF.Close()

	base := "http://127.0.0.1:19090"
	r.writeCapture(filepath.Join(outDir, "prometheus", "ready.txt"), func() (string, error) {
		return httpGET(ctx, base+"/-/ready", 10*time.Second)
	})
	r.writeCapture(filepath.Join(outDir, "prometheus", "alerts.json"), func() (string, error) {
		return httpGET(ctx, base+"/api/v1/alerts", 10*time.Second)
	})
	r.writeCapture(filepath.Join(outDir, "prometheus", "rules.json"), func() (string, error) {
		return httpGET(ctx, base+"/api/v1/rules", 10*time.Second)
	})
	r.writeCapture(filepath.Join(outDir, "prometheus", "targets.json"), func() (string, error) {
		return httpGET(ctx, base+"/api/v1/targets", 10*time.Second)
	})
	r.writeCapture(filepath.Join(outDir, "prometheus", "status-config.json"), func() (string, error) {
		return httpGET(ctx, base+"/api/v1/status/config", 10*time.Second)
	})

	queries := map[string]string{
		"query_capacity_alerts.json":       `ALERTS{alertname=~"PVCUsageHigh|PVCUsageCritical|NamespaceBudgetBreachSoon|WorkloadBudgetBreachSoon",alertstate=~"pending|firing"}`,
		"query_up_controller_manager.json": `up{job=~".*controller-manager.*"}`,
		"query_capacity_metrics.json":      `{__name__=~"capacityplan_.*"}`,
		"query_kubelet_pvc_used.json":      `kubelet_volume_stats_used_bytes{namespace="default"}`,
		"query_kubelet_pvc_capacity.json":  `kubelet_volume_stats_capacity_bytes{namespace="default"}`,
		"query_pvc_request_bytes.json":     `kube_persistentvolumeclaim_resource_requests_storage_bytes{namespace="default"}`,
	}
	for file, query := range queries {
		q := query
		r.writeCapture(filepath.Join(outDir, "prometheus", file), func() (string, error) {
			// Use raw API response compatibility via manual HTTP.
			u := fmt.Sprintf("%s/api/v1/query?query=%s", base, url.QueryEscape(q))
			return httpGET(ctx, u, 10*time.Second)
		})
	}
	_ = writeFile(filepath.Join(outDir, "logs", "prometheus-port-forward.log"), pf.Logs())
}

func (r *DiagnosticsRunner) captureAlertmanagerAPI(ctx context.Context, outDir string) {
	pf, err := StartPodPortForward(r.clients, r.cfg.MonitoringNamespace, "alertmanager-kube-prometheus-stack-alertmanager-0", 19093, 9093)
	if err != nil {
		r.writeCapture(filepath.Join(outDir, "logs", "alertmanager-port-forward.log"), func() (string, error) {
			return "", err
		})
		return
	}
	r.alertPF = pf
	if err := pf.WaitReady(30 * time.Second); err != nil {
		r.writeCapture(filepath.Join(outDir, "logs", "alertmanager-port-forward.log"), func() (string, error) {
			return "", err
		})
		return
	}
	defer r.alertPF.Close()

	base := "http://127.0.0.1:19093"
	r.writeCapture(filepath.Join(outDir, "alertmanager", "ready.txt"), func() (string, error) {
		return httpGET(ctx, base+"/-/ready", 10*time.Second)
	})
	r.writeCapture(filepath.Join(outDir, "alertmanager", "status.json"), func() (string, error) {
		return httpGET(ctx, base+"/api/v2/status", 10*time.Second)
	})
	r.writeCapture(filepath.Join(outDir, "alertmanager", "alerts.json"), func() (string, error) {
		return httpGET(ctx, base+"/api/v2/alerts", 10*time.Second)
	})
	_ = writeFile(filepath.Join(outDir, "logs", "alertmanager-port-forward.log"), pf.Logs())
}

func namespacedResourcesText(names []string) string {
	sort.Strings(names)
	if len(names) == 0 {
		return "\n"
	}
	return strings.Join(names, "\n") + "\n"
}

func deploymentPodName(ctx context.Context, c *Clients, namespace string, labels map[string]string) string {
	pod, err := getFirstPodByLabel(ctx, c, namespace, labels)
	if err != nil {
		return ""
	}
	return pod.Name
}

func (r *DiagnosticsRunner) captureCoreResources(ctx context.Context, outDir string) {
	r.writeCapture(filepath.Join(outDir, "meta", "timestamp.txt"), func() (string, error) {
		return time.Now().UTC().Format(time.RFC3339) + "\n", nil
	})
	r.writeCapture(filepath.Join(outDir, "meta", "kubectl-version.txt"), func() (string, error) {
		cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ci-runner\ncontext=%s\n", cfg.CurrentContext), nil
	})

	r.writeCapture(filepath.Join(outDir, "cluster", "nodes.txt"), func() (string, error) {
		nodes, err := r.clients.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return marshalYAML(nodes), nil
	})
	r.writeCapture(filepath.Join(outDir, "cluster", "pods-all.txt"), func() (string, error) {
		pods, err := r.clients.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return marshalYAML(pods), nil
	})
	r.writeCapture(filepath.Join(outDir, "cluster", "events-all.txt"), func() (string, error) {
		events, err := r.clients.Clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return marshalYAML(events), nil
	})

	r.writeCapture(filepath.Join(outDir, "cluster", "capacityplans.txt"), func() (string, error) {
		var list capacityv1.CapacityPlanList
		if err := r.clients.Controller.List(ctx, &list); err != nil {
			return "", err
		}
		names := make([]string, 0, len(list.Items))
		for _, cp := range list.Items {
			names = append(names, cp.Name)
		}
		return namespacedResourcesText(names), nil
	})
	r.writeCapture(filepath.Join(outDir, "cluster", fmt.Sprintf("capacityplan-%s.yaml", r.cfg.PlanName)), func() (string, error) {
		var cp capacityv1.CapacityPlan
		if err := r.clients.Controller.Get(ctx, types.NamespacedName{Name: r.cfg.PlanName}, &cp); err != nil {
			return "", err
		}
		return marshalYAML(cp), nil
	})
}

func dynamicListYAML(ctx context.Context, c *Clients, gvr schema.GroupVersionResource, namespace string) (string, error) {
	resource := c.Dynamic.Resource(gvr)
	if strings.TrimSpace(namespace) != "" {
		list, err := resource.Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return marshalYAML(list.Object), nil
	}
	list, err := resource.List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	return marshalYAML(list.Object), nil
}

func (r *DiagnosticsRunner) captureOperatorAndMonitoring(ctx context.Context, outDir string) {
	// Operator namespace captures.
	r.writeCapture(filepath.Join(outDir, "operator", "resources.txt"), func() (string, error) {
		deps, _ := r.clients.Clientset.AppsV1().Deployments(r.cfg.OpNamespace).List(ctx, metav1.ListOptions{})
		rss, _ := r.clients.Clientset.AppsV1().ReplicaSets(r.cfg.OpNamespace).List(ctx, metav1.ListOptions{})
		pods, _ := r.clients.Clientset.CoreV1().Pods(r.cfg.OpNamespace).List(ctx, metav1.ListOptions{})
		svcs, _ := r.clients.Clientset.CoreV1().Services(r.cfg.OpNamespace).List(ctx, metav1.ListOptions{})
		return fmt.Sprintf("deployments=%d\nreplicasets=%d\npods=%d\nservices=%d\n", len(deps.Items), len(rss.Items), len(pods.Items), len(svcs.Items)), nil
	})
	r.writeCapture(filepath.Join(outDir, "operator", "deploy.yaml"), func() (string, error) {
		list, err := r.clients.Clientset.AppsV1().Deployments(r.cfg.OpNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return marshalYAML(list), nil
	})
	r.writeCapture(filepath.Join(outDir, "operator", "pods.yaml"), func() (string, error) {
		list, err := r.clients.Clientset.CoreV1().Pods(r.cfg.OpNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return marshalYAML(list), nil
	})
	r.writeCapture(filepath.Join(outDir, "operator", "services.yaml"), func() (string, error) {
		list, err := r.clients.Clientset.CoreV1().Services(r.cfg.OpNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return marshalYAML(list), nil
	})
	r.writeCapture(filepath.Join(outDir, "operator", "servicemonitors.yaml"), func() (string, error) {
		gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
		return dynamicListYAML(ctx, r.clients, gvr, r.cfg.OpNamespace)
	})
	r.writeCapture(filepath.Join(outDir, "operator", "manager-logs.txt"), func() (string, error) {
		pod := deploymentPodName(ctx, r.clients, r.cfg.OpNamespace, map[string]string{"control-plane": "controller-manager"})
		if pod == "" {
			return "", fmt.Errorf("manager pod not found")
		}
		return getPodLogs(ctx, r.clients, r.cfg.OpNamespace, pod, 1200)
	})

	// Monitoring namespace captures.
	r.writeCapture(filepath.Join(outDir, "monitoring", "resources.txt"), func() (string, error) {
		deps, _ := r.clients.Clientset.AppsV1().Deployments(r.cfg.MonitoringNamespace).List(ctx, metav1.ListOptions{})
		stss, _ := r.clients.Clientset.AppsV1().StatefulSets(r.cfg.MonitoringNamespace).List(ctx, metav1.ListOptions{})
		pods, _ := r.clients.Clientset.CoreV1().Pods(r.cfg.MonitoringNamespace).List(ctx, metav1.ListOptions{})
		svcs, _ := r.clients.Clientset.CoreV1().Services(r.cfg.MonitoringNamespace).List(ctx, metav1.ListOptions{})
		return fmt.Sprintf("deployments=%d\nstatefulsets=%d\npods=%d\nservices=%d\n", len(deps.Items), len(stss.Items), len(pods.Items), len(svcs.Items)), nil
	})
	r.writeCapture(filepath.Join(outDir, "monitoring", "prometheusrules.yaml"), func() (string, error) {
		gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}
		return dynamicListYAML(ctx, r.clients, gvr, r.cfg.MonitoringNamespace)
	})
	r.writeCapture(filepath.Join(outDir, "monitoring", "servicemonitors.yaml"), func() (string, error) {
		gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
		return dynamicListYAML(ctx, r.clients, gvr, r.cfg.MonitoringNamespace)
	})
	r.writeCapture(filepath.Join(outDir, "monitoring", "prometheusrules-all.yaml"), func() (string, error) {
		gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}
		return dynamicListYAML(ctx, r.clients, gvr, "")
	})
	r.writeCapture(filepath.Join(outDir, "monitoring", "servicemonitors-all.yaml"), func() (string, error) {
		gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
		return dynamicListYAML(ctx, r.clients, gvr, "")
	})
	r.writeCapture(filepath.Join(outDir, "monitoring", "operator-logs.txt"), func() (string, error) {
		list, err := r.clients.Clientset.CoreV1().Pods(r.cfg.MonitoringNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=kube-prometheus-stack-operator"})
		if err != nil || len(list.Items) == 0 {
			return "", fmt.Errorf("monitoring operator pod not found")
		}
		return getPodLogs(ctx, r.clients, r.cfg.MonitoringNamespace, list.Items[0].Name, 1200)
	})
	r.writeCapture(filepath.Join(outDir, "monitoring", "prometheus-logs.txt"), func() (string, error) {
		return getPodLogs(ctx, r.clients, r.cfg.MonitoringNamespace, "prometheus-kube-prometheus-stack-prometheus-0", 1200)
	})
	r.writeCapture(filepath.Join(outDir, "monitoring", "alertmanager-logs.txt"), func() (string, error) {
		return getPodLogs(ctx, r.clients, r.cfg.MonitoringNamespace, "alertmanager-kube-prometheus-stack-alertmanager-0", 1200)
	})
}

func (r *DiagnosticsRunner) Run(ctx context.Context) error {
	defer r.closePortForwards()
	outDir := strings.TrimSpace(r.cfg.DiagnosticsOutDir)
	if outDir == "" {
		outDir = "/tmp/cpo-ci-diagnostics"
	}
	for _, sub := range []string{"cluster", "operator", "monitoring", "prometheus", "alertmanager", "logs", "meta"} {
		if err := ensureDir(filepath.Join(outDir, sub)); err != nil {
			return err
		}
	}

	r.captureCoreResources(ctx, outDir)
	r.captureOperatorAndMonitoring(ctx, outDir)
	r.capturePrometheusAPI(ctx, outDir)
	r.captureAlertmanagerAPI(ctx, outDir)

	if _, err := civerify.WriteDiagnosticsSummary(outDir, r.cfg.PlanName); err != nil {
		_ = writeFile(filepath.Join(outDir, "summary.txt"), fmt.Sprintf("Capacity Planning CI Diagnostics Summary\nGeneratedAtUTC: %s\nPlanName: %s\n\n[Summary]\n- Summary generation failed: %v\n", time.Now().UTC().Format(time.RFC3339), r.cfg.PlanName, err))
	}
	fmt.Printf("Diagnostics collected in %s\n", outDir)
	return nil
}
