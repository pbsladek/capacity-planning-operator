package civerify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// PrintGrowthSummary emits human-readable growth cross-check rows.
func PrintGrowthSummary(w io.Writer, summary ComparisonSummary, windowSeconds int) {
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	fmt.Fprintf(w, "Growth math cross-check (status vs Prometheus deriv over %ds)\n", windowSeconds)
	fmt.Fprintln(w, "  pvc statusBytesPerDay promDerivBytesPerDay absDiff relDiffPct match")
	for _, row := range summary.Rows {
		if !row.HasPromData {
			fmt.Fprintf(w, "  %s %s n/a n/a n/a no-data\n", row.Name, formatFloat(row.StatusBytesPerDay))
			continue
		}
		match := "no"
		if row.Matched {
			match = "yes"
		}
		fmt.Fprintf(
			w,
			"  %s %s %s %.12g %.2f %s\n",
			row.Name,
			formatFloat(row.StatusBytesPerDay),
			formatFloat(row.PromBytesPerDay),
			row.AbsDiff,
			row.RelDiffPct,
			match,
		)
	}
}

// ValidationReport is the final integration validation summary.
type ValidationReport struct {
	GeneratedAtUTC             string `json:"generatedAtUTC"`
	Context                    string `json:"context"`
	PrometheusEndpoint         string `json:"prometheusEndpoint"`
	ManagerRollout             string `json:"managerRollout"`
	PlanReconcile              string `json:"planReconcile"`
	TrendSignal                string `json:"trendSignal"`
	GrowthMathCrosscheck       string `json:"growthMathCrosscheck"`
	PromRuleContent            string `json:"promRuleContent"`
	ManagerMetrics             string `json:"managerMetrics"`
	PrometheusCapacityAlerts   string `json:"prometheusCapacityAlerts"`
	WorkloadBudgetAlerts       string `json:"workloadBudgetAlerts"`
	AlertmanagerCapacityAlerts string `json:"alertmanagerCapacityAlerts"`
	TrendSeconds               int64  `json:"trendSeconds"`
	TotalSeconds               int64  `json:"totalSeconds"`
	Snapshots                  int    `json:"snapshots"`
	PeakGrowingPVCs            int    `json:"peakGrowingPVCs"`
}

func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

// PrintValidationReport prints a human-readable validation report.
func PrintValidationReport(w io.Writer, r ValidationReport) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Validation report")
	fmt.Fprintf(w, "  context: %s\n", r.Context)
	fmt.Fprintf(w, "  prometheus_endpoint: %s\n", r.PrometheusEndpoint)
	fmt.Fprintf(w, "  manager_rollout: %s\n", r.ManagerRollout)
	fmt.Fprintf(w, "  plan_reconcile: %s\n", r.PlanReconcile)
	fmt.Fprintf(w, "  trend_signal: %s (snapshots=%d, peakGrowingPVCs=%d)\n", r.TrendSignal, r.Snapshots, r.PeakGrowingPVCs)
	fmt.Fprintf(w, "  growth_math_crosscheck: %s\n", r.GrowthMathCrosscheck)
	fmt.Fprintf(w, "  prom_rule_content: %s\n", r.PromRuleContent)
	fmt.Fprintf(w, "  manager_metrics: %s\n", r.ManagerMetrics)
	fmt.Fprintf(w, "  prometheus_capacity_alerts: %s\n", r.PrometheusCapacityAlerts)
	fmt.Fprintf(w, "  workload_budget_alerts: %s\n", r.WorkloadBudgetAlerts)
	fmt.Fprintf(w, "  alertmanager_capacity_alerts: %s\n", r.AlertmanagerCapacityAlerts)
	fmt.Fprintf(w, "  timings: trend=%s total=%s\n", formatDuration(r.TrendSeconds), formatDuration(r.TotalSeconds))
}

// WriteValidationReportJSON writes the validation report as pretty JSON.
func WriteValidationReportJSON(path string, r ValidationReport) error {
	if r.GeneratedAtUTC == "" {
		r.GeneratedAtUTC = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding report JSON: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing report JSON: %w", err)
	}
	return nil
}
