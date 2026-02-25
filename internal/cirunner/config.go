package cirunner

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	OpNamespace                     string
	MonitoringNamespace             string
	ClusterName                     string
	ExpectedKubeContext             string
	PlanName                        string
	OperatorImage                   string
	PromURL                         string
	KubePromValuesFile              string
	KubePromValuesExtraFile         string
	KubePromChartVersion            string
	CIManifestDir                   string
	TrendObserveSeconds             int
	UsageSnapshotInterval           int
	MinTrendObserveSeconds          int
	MinTrendSnapshots               int
	UsageRatioSanityMax             float64
	MinGrowthBytesPerMin            float64
	MinGrowingPVCs                  int
	GrowthCompareWindowSeconds      int
	GrowthCompareRelTol             float64
	GrowthCompareAbsTolBytesDay     float64
	MinGrowthComparablePVCs         int
	MinGrowthMatchingPVCs           int
	PlanSampleRetention             int
	PlanSampleInterval              string
	PlanReconcileInterval           string
	ValidationReportJSON            string
	PollIntervalSeconds             int
	PromEndpointReadyTimeout        int
	MonitoringRolloutTimeout        int
	AlertEndpointReadyTimeout       int
	AlertPropagationTimeout         int
	ManagerEndpointReadyTimeout     int
	ManagerRolloutTimeout           int
	RolloutStatusInterval           int
	CIWorkloads                     []string
	DiagnosticsOutDir               string
	ImportRetryCount                int
	LLMEnabled                      bool
	LLMSoftFail                     bool
	LLMTimeoutHardFail              bool
	ValidationSoftFail              bool
	LLMProvider                     string
	LLMModel                        string
	LLMTimeoutSeconds               int
	LLMMaxTokens                    int
	LLMOnlyAlertingPVCs             bool
	LLMOllamaURL                    string
	LLMMinInsights                  int
	LLMModelPullTimeoutSeconds      int
	LLMNamespace                    string
	LLMDeploymentName               string
	AlertReceiverImage              string
	NightlyRuleName                 string
	NightlyAlertReceiverPort        int
	AlertmanagerExpectedReceiver    string
	AlertmanagerExpectedIntegration string
}

func getenvDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func getenvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func getenvFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return f
}

func getenvBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func splitCSV(raw string) []string {
	out := make([]string, 0, 8)
	for p := range strings.SplitSeq(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func LoadConfig() Config {
	clusterName := getenvDefault("CLUSTER_NAME", "cpo-ci")
	opNS := getenvDefault("OP_NS", "k8s-operator-system")
	monNS := getenvDefault("MON_NS", "monitoring")

	workloads := splitCSV(getenvDefault("CI_WORKLOADS_CSV", "cpo-ci-steady,cpo-ci-bursty,cpo-ci-trickle,cpo-ci-churn,cpo-ci-delayed"))
	if len(workloads) == 0 {
		workloads = []string{"cpo-ci-steady", "cpo-ci-bursty", "cpo-ci-trickle", "cpo-ci-churn", "cpo-ci-delayed"}
	}
	llmEnabled := getenvBool("CI_ENABLE_LLM", false)
	llmProvider := strings.TrimSpace(os.Getenv("CI_LLM_PROVIDER"))
	if llmProvider == "" {
		if llmEnabled {
			llmProvider = "ollama"
		} else {
			llmProvider = "disabled"
		}
	}

	return Config{
		OpNamespace:                     opNS,
		MonitoringNamespace:             monNS,
		ClusterName:                     clusterName,
		ExpectedKubeContext:             getenvDefault("EXPECTED_KUBE_CONTEXT", "k3d-"+clusterName),
		PlanName:                        getenvDefault("PLAN_NAME", "ci-plan"),
		OperatorImage:                   getenvDefault("OPERATOR_IMAGE", "capacity-planning-operator:ci"),
		PromURL:                         getenvDefault("PROM_URL", "http://kube-prometheus-stack-prometheus."+monNS+".svc.cluster.local:9090"),
		KubePromValuesFile:              getenvDefault("KUBE_PROM_VALUES_FILE", "hack/ci/kube-prom-values.yaml"),
		KubePromValuesExtraFile:         getenvDefault("KUBE_PROM_VALUES_EXTRA_FILE", "hack/ci/kube-prom-values-alerting.yaml"),
		KubePromChartVersion:            getenvDefault("KUBE_PROM_STACK_CHART_VERSION", "65.8.1"),
		CIManifestDir:                   getenvDefault("CI_MANIFEST_DIR", "hack/ci/manifests"),
		TrendObserveSeconds:             getenvInt("TREND_OBSERVE_SECONDS", 480),
		UsageSnapshotInterval:           getenvInt("USAGE_SNAPSHOT_INTERVAL_SECONDS", 180),
		MinTrendObserveSeconds:          getenvInt("MIN_TREND_OBSERVE_SECONDS", 240),
		MinTrendSnapshots:               getenvInt("MIN_TREND_SNAPSHOTS", 2),
		UsageRatioSanityMax:             getenvFloat("USAGE_RATIO_SANITY_MAX", 0),
		MinGrowthBytesPerMin:            getenvFloat("MIN_GROWTH_BYTES_PER_MIN", 1024),
		MinGrowingPVCs:                  getenvInt("MIN_GROWING_PVCS", 3),
		GrowthCompareWindowSeconds:      getenvInt("GROWTH_COMPARE_WINDOW_SECONDS", 240),
		GrowthCompareRelTol:             getenvFloat("GROWTH_COMPARE_REL_TOL", 0.65),
		GrowthCompareAbsTolBytesDay:     getenvFloat("GROWTH_COMPARE_ABS_TOL_BYTES_PER_DAY", 15000000000),
		MinGrowthComparablePVCs:         getenvInt("MIN_GROWTH_COMPARABLE_PVCS", 4),
		MinGrowthMatchingPVCs:           getenvInt("MIN_GROWTH_MATCHING_PVCS", 4),
		PlanSampleRetention:             getenvInt("PLAN_SAMPLE_RETENTION", 24),
		PlanSampleInterval:              getenvDefault("PLAN_SAMPLE_INTERVAL", "5s"),
		PlanReconcileInterval:           getenvDefault("PLAN_RECONCILE_INTERVAL", "15s"),
		ValidationReportJSON:            getenvDefault("VALIDATION_REPORT_JSON", "/tmp/cpo-ci-validation-report.json"),
		PollIntervalSeconds:             getenvInt("POLL_INTERVAL_SECONDS", 5),
		PromEndpointReadyTimeout:        getenvInt("PROM_ENDPOINT_READY_TIMEOUT_SECONDS", 300),
		MonitoringRolloutTimeout:        getenvInt("MONITORING_ROLLOUT_TIMEOUT_SECONDS", 900),
		AlertEndpointReadyTimeout:       getenvInt("ALERT_ENDPOINT_READY_TIMEOUT_SECONDS", 300),
		AlertPropagationTimeout:         getenvInt("ALERT_PROPAGATION_TIMEOUT_SECONDS", 900),
		ManagerEndpointReadyTimeout:     getenvInt("MANAGER_ENDPOINT_READY_TIMEOUT_SECONDS", 180),
		ManagerRolloutTimeout:           getenvInt("MANAGER_ROLLOUT_TIMEOUT_SECONDS", 420),
		RolloutStatusInterval:           getenvInt("ROLLOUT_STATUS_INTERVAL_SECONDS", 15),
		CIWorkloads:                     workloads,
		DiagnosticsOutDir:               getenvDefault("OUT_DIR", "/tmp/cpo-ci-diagnostics"),
		ImportRetryCount:                getenvInt("IMPORT_RETRY_COUNT", 1),
		LLMEnabled:                      llmEnabled,
		LLMSoftFail:                     getenvBool("CI_LLM_SOFT_FAIL", true),
		LLMTimeoutHardFail:              getenvBool("CI_LLM_TIMEOUT_HARD_FAIL", true),
		ValidationSoftFail:              getenvBool("CI_VALIDATION_SOFT_FAIL", false),
		LLMProvider:                     llmProvider,
		LLMModel:                        getenvDefault("CI_LLM_MODEL", "mistral:7b"),
		LLMTimeoutSeconds:               getenvInt("CI_LLM_TIMEOUT_SECONDS", 90),
		LLMMaxTokens:                    getenvInt("CI_LLM_MAX_TOKENS", 256),
		LLMOnlyAlertingPVCs:             getenvBool("CI_LLM_ONLY_ALERTING_PVCS", false),
		LLMOllamaURL:                    getenvDefault("CI_LLM_OLLAMA_URL", "http://ollama.llm.svc.cluster.local:11434"),
		LLMMinInsights:                  getenvInt("CI_LLM_MIN_INSIGHTS", 1),
		LLMModelPullTimeoutSeconds:      getenvInt("CI_LLM_MODEL_PULL_TIMEOUT_SECONDS", 1200),
		LLMNamespace:                    getenvDefault("CI_LLM_NAMESPACE", "llm"),
		LLMDeploymentName:               getenvDefault("CI_LLM_DEPLOYMENT_NAME", "ollama"),
		AlertReceiverImage:              getenvDefault("ALERT_RECEIVER_IMAGE", "capacity-alert-receiver:ci"),
		NightlyRuleName:                 getenvDefault("RULE_NAME", "ci-always-firing"),
		NightlyAlertReceiverPort:        getenvInt("ALERT_RECEIVER_PORT", 29080),
		AlertmanagerExpectedReceiver:    getenvDefault("ALERTMANAGER_EXPECTED_RECEIVER", "ci-webhook"),
		AlertmanagerExpectedIntegration: getenvDefault("ALERTMANAGER_EXPECTED_INTEGRATION", "webhook"),
	}
}

func (c Config) PollInterval() time.Duration {
	seconds := c.PollIntervalSeconds
	if seconds <= 0 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}
