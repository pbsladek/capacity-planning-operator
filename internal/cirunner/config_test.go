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
}

func TestConfigPollIntervalDefault(t *testing.T) {
	cfg := Config{PollIntervalSeconds: 0}
	if got := cfg.PollInterval().Seconds(); got != 5 {
		t.Fatalf("PollInterval=%v", cfg.PollInterval())
	}
}
