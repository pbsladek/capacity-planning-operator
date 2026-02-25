package cirunner

import "testing"

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" a, b ,,c,")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d mismatch: got=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestLoadConfigUsesEnvOverrides(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "mycluster")
	t.Setenv("PLAN_NAME", "myplan")
	t.Setenv("CI_WORKLOADS_CSV", "one,two")
	t.Setenv("IMPORT_RETRY_COUNT", "3")
	t.Setenv("CI_ENABLE_LLM", "true")
	t.Setenv("CI_LLM_PROVIDER", "ollama")
	t.Setenv("CI_LLM_MODEL", "llama3.1:8b")
	t.Setenv("CI_LLM_TIMEOUT_SECONDS", "120")
	t.Setenv("CI_VALIDATION_SOFT_FAIL", "true")

	cfg := LoadConfig()
	if cfg.ClusterName != "mycluster" {
		t.Fatalf("ClusterName=%q", cfg.ClusterName)
	}
	if cfg.PlanName != "myplan" {
		t.Fatalf("PlanName=%q", cfg.PlanName)
	}
	if cfg.ImportRetryCount != 3 {
		t.Fatalf("ImportRetryCount=%d", cfg.ImportRetryCount)
	}
	if len(cfg.CIWorkloads) != 2 || cfg.CIWorkloads[0] != "one" || cfg.CIWorkloads[1] != "two" {
		t.Fatalf("CIWorkloads=%v", cfg.CIWorkloads)
	}
	if !cfg.LLMEnabled {
		t.Fatalf("LLMEnabled=%v", cfg.LLMEnabled)
	}
	if cfg.LLMProvider != "ollama" {
		t.Fatalf("LLMProvider=%q", cfg.LLMProvider)
	}
	if cfg.LLMModel != "llama3.1:8b" {
		t.Fatalf("LLMModel=%q", cfg.LLMModel)
	}
	if cfg.LLMTimeoutSeconds != 120 {
		t.Fatalf("LLMTimeoutSeconds=%d", cfg.LLMTimeoutSeconds)
	}
	if !cfg.ValidationSoftFail {
		t.Fatalf("ValidationSoftFail=%v", cfg.ValidationSoftFail)
	}
	if cfg.KubePromValuesExtraFile != "hack/ci/kube-prom-values-alerting.yaml" {
		t.Fatalf("KubePromValuesExtraFile=%q", cfg.KubePromValuesExtraFile)
	}
	if cfg.AlertReceiverImage != "capacity-alert-receiver:ci" {
		t.Fatalf("AlertReceiverImage=%q", cfg.AlertReceiverImage)
	}
	if cfg.NightlyRuleName != "ci-always-firing" {
		t.Fatalf("NightlyRuleName=%q", cfg.NightlyRuleName)
	}
	if cfg.NightlyAlertReceiverPort != 29080 {
		t.Fatalf("NightlyAlertReceiverPort=%d", cfg.NightlyAlertReceiverPort)
	}
	if cfg.MonitoringRolloutTimeout != 900 {
		t.Fatalf("MonitoringRolloutTimeout=%d", cfg.MonitoringRolloutTimeout)
	}
	if cfg.AlertmanagerExpectedReceiver != "ci-webhook" {
		t.Fatalf("AlertmanagerExpectedReceiver=%q", cfg.AlertmanagerExpectedReceiver)
	}
	if cfg.AlertmanagerExpectedIntegration != "webhook" {
		t.Fatalf("AlertmanagerExpectedIntegration=%q", cfg.AlertmanagerExpectedIntegration)
	}
}

func TestConfigPollIntervalDefault(t *testing.T) {
	cfg := Config{PollIntervalSeconds: 0}
	if got := cfg.PollInterval().Seconds(); got != 5 {
		t.Fatalf("PollInterval=%v", cfg.PollInterval())
	}
}
