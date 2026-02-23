package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pbsladek/capacity-planning-operator/internal/civerify"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  ci-verify crosscheck-growth [flags]\n")
	fmt.Fprintf(os.Stderr, "  ci-verify verify-alerts [flags]\n")
	fmt.Fprintf(os.Stderr, "  ci-verify report [flags]\n")
	fmt.Fprintf(os.Stderr, "  ci-verify summarize-diagnostics [flags]\n")
	fmt.Fprintf(os.Stderr, "\nFlags for crosscheck-growth:\n")
	fmt.Fprintf(os.Stderr, "  --plan-name string (default ci-plan)\n")
	fmt.Fprintf(os.Stderr, "  --prom-url string (default http://127.0.0.1:19090)\n")
	fmt.Fprintf(os.Stderr, "  --namespace string (default default)\n")
	fmt.Fprintf(os.Stderr, "  --window-seconds int (default 240)\n")
	fmt.Fprintf(os.Stderr, "  --rel-tol float (default 0.65)\n")
	fmt.Fprintf(os.Stderr, "  --abs-tol-bytes-per-day float (default 15000000000)\n")
	fmt.Fprintf(os.Stderr, "  --min-comparable int (default 4)\n")
	fmt.Fprintf(os.Stderr, "  --min-matching int (default 4)\n")
	fmt.Fprintf(os.Stderr, "  --request-timeout duration (default 5s)\n")
	fmt.Fprintf(os.Stderr, "\nFlags for verify-alerts:\n")
	fmt.Fprintf(os.Stderr, "  --prom-url string (default http://127.0.0.1:19090)\n")
	fmt.Fprintf(os.Stderr, "  --alertmanager-url string (default http://127.0.0.1:19093)\n")
	fmt.Fprintf(os.Stderr, "  --capacity-already-seen bool (default false)\n")
	fmt.Fprintf(os.Stderr, "  --workloads csv (default cpo-ci-steady,cpo-ci-bursty,cpo-ci-trickle,cpo-ci-churn,cpo-ci-delayed)\n")
	fmt.Fprintf(os.Stderr, "  --poll-interval duration (default 5s)\n")
	fmt.Fprintf(os.Stderr, "  --timeout duration (default 15m)\n")
	fmt.Fprintf(os.Stderr, "  --request-timeout duration (default 5s)\n")
	fmt.Fprintf(os.Stderr, "\nFlags for report:\n")
	fmt.Fprintf(os.Stderr, "  --context string\n")
	fmt.Fprintf(os.Stderr, "  --prometheus-endpoint string\n")
	fmt.Fprintf(os.Stderr, "  --manager-rollout string\n")
	fmt.Fprintf(os.Stderr, "  --plan-reconcile string\n")
	fmt.Fprintf(os.Stderr, "  --trend-signal string\n")
	fmt.Fprintf(os.Stderr, "  --growth-math-crosscheck string\n")
	fmt.Fprintf(os.Stderr, "  --prom-rule-content string\n")
	fmt.Fprintf(os.Stderr, "  --manager-metrics string\n")
	fmt.Fprintf(os.Stderr, "  --prometheus-capacity-alerts string\n")
	fmt.Fprintf(os.Stderr, "  --workload-budget-alerts string\n")
	fmt.Fprintf(os.Stderr, "  --alertmanager-capacity-alerts string\n")
	fmt.Fprintf(os.Stderr, "  --snapshots int\n")
	fmt.Fprintf(os.Stderr, "  --peak-growing-pvcs int\n")
	fmt.Fprintf(os.Stderr, "  --trend-seconds int\n")
	fmt.Fprintf(os.Stderr, "  --total-seconds int\n")
	fmt.Fprintf(os.Stderr, "  --output-json path (default /tmp/cpo-ci-validation-report.json)\n")
	fmt.Fprintf(os.Stderr, "\nFlags for summarize-diagnostics:\n")
	fmt.Fprintf(os.Stderr, "  --out-dir path (default /tmp/cpo-ci-diagnostics)\n")
	fmt.Fprintf(os.Stderr, "  --plan-name string (default ci-plan)\n")
}

func runCrosscheckGrowth(args []string) error {
	fs := flag.NewFlagSet("crosscheck-growth", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	planName := fs.String("plan-name", "ci-plan", "CapacityPlan name")
	promURL := fs.String("prom-url", "http://127.0.0.1:19090", "Prometheus base URL")
	namespace := fs.String("namespace", "default", "Namespace containing test PVCs")
	windowSeconds := fs.Int("window-seconds", 240, "Prometheus deriv lookback window in seconds")
	relTol := fs.Float64("rel-tol", 0.65, "Relative tolerance for growth comparison")
	absTol := fs.Float64("abs-tol-bytes-per-day", 15000000000, "Absolute tolerance for growth comparison (bytes/day)")
	minComparable := fs.Int("min-comparable", 4, "Minimum comparable PVCs required")
	minMatching := fs.Int("min-matching", 4, "Minimum matching PVCs required")
	requestTimeout := fs.Duration("request-timeout", 5*time.Second, "Per-request timeout to Prometheus")

	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	k8sClient, err := civerify.NewKubeClient()
	if err != nil {
		return fmt.Errorf("creating Kubernetes client: %w", err)
	}

	pvcGrowth, err := civerify.LoadCapacityPlanPVCGrowth(ctx, k8sClient, *planName)
	if err != nil {
		return err
	}

	promClient := civerify.NewPrometheusClient(*promURL, *requestTimeout)
	opts := civerify.CompareOptions{
		RelativeTolerance:    *relTol,
		AbsToleranceBytesDay: *absTol,
		MinComparablePVCs:    *minComparable,
		MinMatchingPVCs:      *minMatching,
	}

	summary, compareErr := civerify.CompareGrowth(ctx, pvcGrowth, func(ctx context.Context, pvcName string) (float64, bool, error) {
		query := civerify.BuildPVCGrowthDerivQuery(*namespace, pvcName, *windowSeconds)
		return promClient.QueryInstantScalar(ctx, query)
	}, opts)

	civerify.PrintGrowthSummary(os.Stdout, summary, *windowSeconds)
	if compareErr != nil {
		return compareErr
	}
	fmt.Printf("RESULT matched=%d comparable=%d\n", summary.Matched, summary.Comparable)
	return nil
}

func parseWorkloadsCSV(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func runVerifyAlerts(args []string) error {
	fs := flag.NewFlagSet("verify-alerts", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	promURL := fs.String("prom-url", "http://127.0.0.1:19090", "Prometheus base URL")
	alertmanagerURL := fs.String("alertmanager-url", "http://127.0.0.1:19093", "Alertmanager base URL")
	capacityAlreadySeen := fs.Bool("capacity-already-seen", false, "Skip waiting for Prometheus capacity alerts because they were already observed")
	workloadsCSV := fs.String("workloads", "cpo-ci-steady,cpo-ci-bursty,cpo-ci-trickle,cpo-ci-churn,cpo-ci-delayed", "Comma-separated workload names")
	pollInterval := fs.Duration("poll-interval", 5*time.Second, "Poll interval for alert checks")
	timeout := fs.Duration("timeout", 15*time.Minute, "Overall timeout for each wait stage")
	requestTimeout := fs.Duration("request-timeout", 5*time.Second, "Per-request timeout to Prometheus/Alertmanager")

	if err := fs.Parse(args); err != nil {
		return err
	}
	workloads := parseWorkloadsCSV(*workloadsCSV)
	if len(workloads) == 0 {
		return fmt.Errorf("no workloads were provided")
	}

	ctx := context.Background()
	promClient := civerify.NewPrometheusClient(*promURL, *requestTimeout)
	verifier := civerify.NewAlertVerifier(promClient, *alertmanagerURL, *requestTimeout)

	promStatus := "pass (after wait)"
	if *capacityAlreadySeen {
		promStatus = "pass (seen during trend observation)"
	} else {
		if err := civerify.WaitUntil(ctx, *timeout, *pollInterval, verifier.PrometheusHasCapacityAlerts); err != nil {
			return fmt.Errorf("timed out waiting for capacity alerts in Prometheus ALERTS metric after %s: %w", timeout.String(), err)
		}
	}

	if err := civerify.WaitUntil(ctx, *timeout, *pollInterval, func(ctx context.Context) (bool, error) {
		return verifier.PrometheusHasAllWorkloadBudgetAlerts(ctx, workloads)
	}); err != nil {
		return fmt.Errorf("timed out waiting for WorkloadBudgetBreachSoon alerts for all workloads after %s: %w", timeout.String(), err)
	}
	for _, workload := range workloads {
		ok, err := verifier.PrometheusHasWorkloadBudgetAlert(ctx, workload)
		if err != nil {
			return fmt.Errorf("querying workload alert for %s: %w", workload, err)
		}
		if !ok {
			return fmt.Errorf("missing WorkloadBudgetBreachSoon for %s after aggregate wait", workload)
		}
	}

	if err := civerify.WaitUntil(ctx, *timeout, *pollInterval, verifier.AlertmanagerHasCapacityAlerts); err != nil {
		return fmt.Errorf("timed out waiting for capacity alerts in Alertmanager API after %s: %w", timeout.String(), err)
	}

	fmt.Println("Alert verification summary")
	fmt.Printf("  prometheus_capacity_alerts: %s\n", promStatus)
	fmt.Printf("  workload_budget_alerts: pass (%d workloads)\n", len(workloads))
	fmt.Println("  alertmanager_capacity_alerts: pass")
	return nil
}

func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	contextStatus := fs.String("context", "pending", "Context check status")
	promEndpoint := fs.String("prometheus-endpoint", "pending", "Prometheus endpoint status")
	managerRollout := fs.String("manager-rollout", "pending", "Manager rollout status")
	planReconcile := fs.String("plan-reconcile", "pending", "CapacityPlan reconcile status")
	trendSignal := fs.String("trend-signal", "pending", "Trend signal status")
	growthCrosscheck := fs.String("growth-math-crosscheck", "pending", "Growth cross-check status")
	promRuleContent := fs.String("prom-rule-content", "pending", "PrometheusRule content status")
	managerMetrics := fs.String("manager-metrics", "pending", "Manager metrics status")
	promCapacityAlerts := fs.String("prometheus-capacity-alerts", "pending", "Prometheus capacity alerts status")
	workloadBudgetAlerts := fs.String("workload-budget-alerts", "pending", "Workload budget alerts status")
	alertmanagerCapacityAlerts := fs.String("alertmanager-capacity-alerts", "pending", "Alertmanager capacity alerts status")
	snapshots := fs.Int("snapshots", 0, "Trend observation snapshots")
	peakGrowingPVCs := fs.Int("peak-growing-pvcs", 0, "Peak growing PVC count")
	trendSeconds := fs.Int64("trend-seconds", 0, "Trend observation duration in seconds")
	totalSeconds := fs.Int64("total-seconds", 0, "Total run duration in seconds")
	outputJSON := fs.String("output-json", "/tmp/cpo-ci-validation-report.json", "Path to write JSON report")

	if err := fs.Parse(args); err != nil {
		return err
	}

	report := civerify.ValidationReport{
		Context:                    *contextStatus,
		PrometheusEndpoint:         *promEndpoint,
		ManagerRollout:             *managerRollout,
		PlanReconcile:              *planReconcile,
		TrendSignal:                *trendSignal,
		GrowthMathCrosscheck:       *growthCrosscheck,
		PromRuleContent:            *promRuleContent,
		ManagerMetrics:             *managerMetrics,
		PrometheusCapacityAlerts:   *promCapacityAlerts,
		WorkloadBudgetAlerts:       *workloadBudgetAlerts,
		AlertmanagerCapacityAlerts: *alertmanagerCapacityAlerts,
		Snapshots:                  *snapshots,
		PeakGrowingPVCs:            *peakGrowingPVCs,
		TrendSeconds:               *trendSeconds,
		TotalSeconds:               *totalSeconds,
	}

	civerify.PrintValidationReport(os.Stdout, report)
	if *outputJSON != "" {
		if err := civerify.WriteValidationReportJSON(*outputJSON, report); err != nil {
			return err
		}
		fmt.Printf("REPORT_JSON %s\n", *outputJSON)
	}
	return nil
}

func runSummarizeDiagnostics(args []string) error {
	fs := flag.NewFlagSet("summarize-diagnostics", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	outDir := fs.String("out-dir", "/tmp/cpo-ci-diagnostics", "Diagnostics output directory")
	planName := fs.String("plan-name", "ci-plan", "CapacityPlan name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	summaryPath, err := civerify.WriteDiagnosticsSummary(*outDir, *planName)
	if err != nil {
		return err
	}
	fmt.Printf("SUMMARY_TXT %s\n", summaryPath)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "crosscheck-growth":
		err = runCrosscheckGrowth(os.Args[2:])
	case "verify-alerts":
		err = runVerifyAlerts(os.Args[2:])
	case "report":
		err = runReport(os.Args[2:])
	case "summarize-diagnostics":
		err = runSummarizeDiagnostics(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}
