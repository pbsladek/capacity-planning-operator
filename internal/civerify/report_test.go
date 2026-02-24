package civerify

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintValidationReport(t *testing.T) {
	var buf bytes.Buffer
	PrintValidationReport(&buf, ValidationReport{
		Context:                    "pass",
		PrometheusEndpoint:         "pass",
		ManagerRollout:             "pass",
		PlanReconcile:              "pass",
		TrendSignal:                "pass",
		GrowthMathCrosscheck:       "pass",
		PromRuleContent:            "pass",
		ManagerMetrics:             "pass",
		PrometheusCapacityAlerts:   "pass",
		WorkloadBudgetAlerts:       "pass",
		AlertmanagerCapacityAlerts: "pass",
		Snapshots:                  2,
		PeakGrowingPVCs:            5,
		TrendSeconds:               362,
		TotalSeconds:               443,
	})
	out := buf.String()
	if !strings.Contains(out, "Validation report") {
		t.Fatalf("missing header: %s", out)
	}
	if !strings.Contains(out, "timings: trend=6m02s total=7m23s") {
		t.Fatalf("unexpected timing formatting: %s", out)
	}
}

func TestWriteValidationReportJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	err := WriteValidationReportJSON(path, ValidationReport{
		Context: "pass",
		PVCTrendDetails: []PVCTrendDetail{
			{Name: "pvc-a", UsedMiB: 42.5},
		},
		WorkloadBudgetDetails: []WorkloadBudgetDetail{
			{Target: "default/Pod/workload-a", BudgetMiB: 160},
		},
		AlertmanagerNotifications: []AlertNotificationDetail{
			{AlertName: "PVCUsageHigh", Target: "default/pvc/pvc-a"},
		},
	})
	if err != nil {
		t.Fatalf("WriteValidationReportJSON: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	if !strings.Contains(string(raw), `"context": "pass"`) {
		t.Fatalf("missing context field: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"generatedAtUTC"`) {
		t.Fatalf("missing generatedAtUTC: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"pvcTrendDetails"`) {
		t.Fatalf("missing pvcTrendDetails: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"workloadBudgetDetails"`) {
		t.Fatalf("missing workloadBudgetDetails: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"alertmanagerNotifications"`) {
		t.Fatalf("missing alertmanagerNotifications: %s", string(raw))
	}
}
