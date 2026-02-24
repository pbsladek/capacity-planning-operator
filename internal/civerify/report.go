package civerify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
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
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  pvc\tstatusMiBPerMin\tpromMiBPerMin\tstatusBytesPerDay\tpromDerivBytesPerDay\tabsDiffBytesPerDay\trelDiffPct\tallowedDiffBytesPerDay\tbasis\tmatch\treason")
	for _, row := range summary.Rows {
		if !row.HasPromData {
			fmt.Fprintf(
				tw,
				"  %s\t%.2f\tn/a\t%s\tn/a\tn/a\tn/a\tn/a\tn/a\tno\t%s\n",
				row.Name,
				row.StatusBytesPerDay/(1024.0*1024.0*1440.0),
				formatFloat(row.StatusBytesPerDay),
				row.Reason,
			)
			continue
		}
		match := "no"
		if row.Matched {
			match = "yes"
		}
		fmt.Fprintf(
			tw,
			"  %s\t%.2f\t%.2f\t%s\t%s\t%.12g\t%.2f\t%.12g\t%s\t%s\t%s\n",
			row.Name,
			row.StatusBytesPerDay/(1024.0*1024.0*1440.0),
			row.PromBytesPerDay/(1024.0*1024.0*1440.0),
			formatFloat(row.StatusBytesPerDay),
			formatFloat(row.PromBytesPerDay),
			row.AbsDiff,
			row.RelDiffPct,
			row.AllowedDiff,
			row.ToleranceBasis,
			match,
			row.Reason,
		)
	}
	_ = tw.Flush()
}

// ValidationReport is the final integration validation summary.
type ValidationReport struct {
	GeneratedAtUTC             string                    `json:"generatedAtUTC"`
	Context                    string                    `json:"context"`
	PrometheusEndpoint         string                    `json:"prometheusEndpoint"`
	ManagerRollout             string                    `json:"managerRollout"`
	PlanReconcile              string                    `json:"planReconcile"`
	TrendSignal                string                    `json:"trendSignal"`
	GrowthMathCrosscheck       string                    `json:"growthMathCrosscheck"`
	PromRuleContent            string                    `json:"promRuleContent"`
	ManagerMetrics             string                    `json:"managerMetrics"`
	PrometheusCapacityAlerts   string                    `json:"prometheusCapacityAlerts"`
	WorkloadBudgetAlerts       string                    `json:"workloadBudgetAlerts"`
	AlertmanagerCapacityAlerts string                    `json:"alertmanagerCapacityAlerts"`
	TrendSeconds               int64                     `json:"trendSeconds"`
	TotalSeconds               int64                     `json:"totalSeconds"`
	Snapshots                  int                       `json:"snapshots"`
	PeakGrowingPVCs            int                       `json:"peakGrowingPVCs"`
	PVCTrendDetails            []PVCTrendDetail          `json:"pvcTrendDetails,omitempty"`
	WorkloadBudgetDetails      []WorkloadBudgetDetail    `json:"workloadBudgetDetails,omitempty"`
	AlertmanagerNotifications  []AlertNotificationDetail `json:"alertmanagerNotifications,omitempty"`
}

// PVCTrendDetail captures per-PVC trend metrics for report artifacts.
type PVCTrendDetail struct {
	Namespace         string   `json:"namespace"`
	Name              string   `json:"name"`
	UsedBytes         int64    `json:"usedBytes"`
	UsedMiB           float64  `json:"usedMiB"`
	UsageRatio        float64  `json:"usageRatio"`
	GrowthBytesPerDay float64  `json:"growthBytesPerDay"`
	GrowthMiBPerMin   float64  `json:"growthMiBPerMin"`
	SamplesCount      int      `json:"samplesCount"`
	AlertFiring       bool     `json:"alertFiring"`
	DaysUntilFull     *float64 `json:"daysUntilFull,omitempty"`
}

// WorkloadBudgetDetail captures per-workload budget forecast metrics.
type WorkloadBudgetDetail struct {
	Namespace         string   `json:"namespace"`
	Kind              string   `json:"kind"`
	Name              string   `json:"name"`
	Target            string   `json:"target"`
	BudgetBytes       int64    `json:"budgetBytes"`
	BudgetMiB         float64  `json:"budgetMiB"`
	UsedBytes         int64    `json:"usedBytes"`
	UsedMiB           float64  `json:"usedMiB"`
	UsageRatio        float64  `json:"usageRatio"`
	GrowthBytesPerDay float64  `json:"growthBytesPerDay"`
	GrowthMiBPerMin   float64  `json:"growthMiBPerMin"`
	DaysUntilBreach   *float64 `json:"daysUntilBreach,omitempty"`
	ProjectedBreachAt string   `json:"projectedBreachAt,omitempty"`
}

// AlertNotificationDetail captures active Alertmanager alert instances.
type AlertNotificationDetail struct {
	AlertName   string `json:"alertName"`
	State       string `json:"state"`
	Severity    string `json:"severity"`
	Namespace   string `json:"namespace,omitempty"`
	PVC         string `json:"pvc,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Workload    string `json:"workload,omitempty"`
	Target      string `json:"target"`
	Why         string `json:"why"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	StartsAt    string `json:"startsAt,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
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
