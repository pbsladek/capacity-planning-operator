package civerify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDiagnosticsSummary(t *testing.T) {
	outDir := t.TempDir()
	dirs := []string{
		filepath.Join(outDir, "cluster"),
		filepath.Join(outDir, "prometheus"),
		filepath.Join(outDir, "monitoring"),
		filepath.Join(outDir, "operator"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	cpYAML := `status:
  conditions:
  - type: Ready
    status: "True"
  - type: PrometheusReady
    status: "True"
  - type: BackfillReady
    status: "False"
  lastReconcileTime: "2026-02-23T10:54:05Z"
`
	if err := os.WriteFile(filepath.Join(outDir, "cluster", "capacityplan-ci-plan.yaml"), []byte(cpYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "prometheus", "query_capacity_alerts.json"), []byte(`{"data":{"result":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "prometheus", "query_capacity_metrics.json"), []byte(`{"data":{"result":[{"value":[1,"1"]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "prometheus", "query_up_controller_manager.json"), []byte(`{"data":{"result":[{"value":[1,"1"]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "prometheus", "targets.json"), []byte(`{"data":{"activeTargets":[{"health":"up"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "monitoring", "prometheusrules-all.yaml"), []byte(`name: capacityplan-ci-plan`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "monitoring", "servicemonitors-all.yaml"), []byte(`name: k8s-operator-controller-manager-metrics-monitor`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "operator", "servicemonitors.yaml"), []byte(``), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := WriteDiagnosticsSummary(outDir, "ci-plan")
	if err != nil {
		t.Fatalf("WriteDiagnosticsSummary: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading summary: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, "Ready: \"True\"") {
		t.Fatalf("summary missing ready status: %s", s)
	}
	if !strings.Contains(s, "query.capacity_metrics: non-empty") {
		t.Fatalf("summary missing capacity metric state: %s", s)
	}
	if !strings.Contains(s, "prometheusrule.capacityplan-ci-plan: present") {
		t.Fatalf("summary missing rule state: %s", s)
	}
}
