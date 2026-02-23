package civerify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func readFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func extractConditionStatusFromYAML(yamlContent, conditionType string) string {
	lines := strings.Split(yamlContent, "\n")
	hit := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		typeVal := ""
		switch {
		case strings.HasPrefix(trimmed, "type: "):
			typeVal = strings.TrimSpace(strings.TrimPrefix(trimmed, "type: "))
		case strings.HasPrefix(trimmed, "- type: "):
			typeVal = strings.TrimSpace(strings.TrimPrefix(trimmed, "- type: "))
		}
		if typeVal == conditionType {
			hit = true
			continue
		}
		if hit && strings.HasPrefix(trimmed, "status: ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "status: "))
		}
	}
	return ""
}

func firstLineValue(yamlContent, prefix string) string {
	lines := strings.Split(yamlContent, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return ""
}

func queryHasResults(jsonContent string) bool {
	if jsonContent == "" {
		return false
	}
	var payload struct {
		Data struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonContent), &payload); err != nil {
		return false
	}
	return len(payload.Data.Result) > 0
}

func stateString(has bool) string {
	if has {
		return "non-empty"
	}
	return "empty"
}

// WriteDiagnosticsSummary generates summary.txt from collected diagnostics files.
func WriteDiagnosticsSummary(outDir, planName string) (string, error) {
	if outDir == "" {
		outDir = "/tmp/cpo-ci-diagnostics"
	}
	if planName == "" {
		planName = "ci-plan"
	}
	summaryPath := filepath.Join(outDir, "summary.txt")

	cpYAML := readFile(filepath.Join(outDir, "cluster", fmt.Sprintf("capacityplan-%s.yaml", planName)))
	promAlertsQ := readFile(filepath.Join(outDir, "prometheus", "query_capacity_alerts.json"))
	promMetricsQ := readFile(filepath.Join(outDir, "prometheus", "query_capacity_metrics.json"))
	promUpQ := readFile(filepath.Join(outDir, "prometheus", "query_up_controller_manager.json"))
	promTargets := readFile(filepath.Join(outDir, "prometheus", "targets.json"))
	allRules := readFile(filepath.Join(outDir, "monitoring", "prometheusrules-all.yaml"))
	allSMs := readFile(filepath.Join(outDir, "monitoring", "servicemonitors-all.yaml"))
	opSMs := readFile(filepath.Join(outDir, "operator", "servicemonitors.yaml"))

	readyStatus := extractConditionStatusFromYAML(cpYAML, "Ready")
	promReadyStatus := extractConditionStatusFromYAML(cpYAML, "PrometheusReady")
	backfillStatus := extractConditionStatusFromYAML(cpYAML, "BackfillReady")
	lastReconcile := firstLineValue(cpYAML, "lastReconcileTime:")

	targetUpCount := strings.Count(promTargets, `"health":"up"`)
	targetDownCount := strings.Count(promTargets, `"health":"down"`)
	cmMentions := strings.Count(promTargets, "controller-manager")

	capacityAlertState := stateString(queryHasResults(promAlertsQ))
	capacityMetricState := stateString(queryHasResults(promMetricsQ))
	controllerUpState := stateString(queryHasResults(promUpQ))

	ruleSelected := "missing"
	if strings.Contains(allRules, "name: capacityplan-"+planName) {
		ruleSelected = "present"
	}
	smSelected := "missing"
	if strings.Contains(allSMs, "k8s-operator-controller-manager-metrics-monitor") {
		smSelected = "present"
	} else if strings.Contains(opSMs, "k8s-operator-controller-manager-metrics-monitor") {
		smSelected = "present (operator namespace only)"
	}

	var b strings.Builder
	fmt.Fprintln(&b, "Capacity Planning CI Diagnostics Summary")
	fmt.Fprintf(&b, "GeneratedAtUTC: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "PlanName: %s\n", planName)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "[CapacityPlan]")
	fmt.Fprintf(&b, "lastReconcileTime: %s\n", defaultString(lastReconcile, "unknown"))
	fmt.Fprintf(&b, "Ready: %s\n", defaultString(readyStatus, "unknown"))
	fmt.Fprintf(&b, "PrometheusReady: %s\n", defaultString(promReadyStatus, "unknown"))
	fmt.Fprintf(&b, "BackfillReady: %s\n", defaultString(backfillStatus, "unknown"))
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "[Prometheus]")
	fmt.Fprintf(&b, "targets.up: %d\n", targetUpCount)
	fmt.Fprintf(&b, "targets.down: %d\n", targetDownCount)
	fmt.Fprintf(&b, "targets.controller-manager-mentions: %d\n", cmMentions)
	fmt.Fprintf(&b, "query.capacity_metrics: %s\n", capacityMetricState)
	fmt.Fprintf(&b, "query.controller_manager_up: %s\n", controllerUpState)
	fmt.Fprintf(&b, "query.capacity_alerts: %s\n", capacityAlertState)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "[Resources]")
	fmt.Fprintf(&b, "prometheusrule.capacityplan-%s: %s\n", planName, ruleSelected)
	fmt.Fprintf(&b, "servicemonitor.controller-manager-metrics: %s\n", smSelected)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "[Likely Cause Hints]")
	if capacityMetricState == "empty" && controllerUpState == "empty" {
		fmt.Fprintln(&b, "- Operator metrics are likely not being scraped by Prometheus (check ServiceMonitor selector/labels and targets).")
	}
	if capacityMetricState == "non-empty" && capacityAlertState == "empty" {
		fmt.Fprintln(&b, "- Capacity metrics exist but ALERTS query is empty (check PrometheusRule selection labels, expressions, and 'for' duration windows).")
	}
	if ruleSelected == "missing" {
		fmt.Fprintln(&b, "- CapacityPlan PrometheusRule resource was not found (check controller reconcile logs for PrometheusRule create/update errors).")
	}
	if smSelected == "missing" {
		fmt.Fprintln(&b, "- Controller metrics ServiceMonitor was not found (Prometheus cannot discover controller metrics).")
	}

	if err := os.WriteFile(summaryPath, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("writing summary file: %w", err)
	}
	return summaryPath, nil
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
