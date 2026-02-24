package civerify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

var capacityAlertNames = []string{
	"PVCUsageHigh",
	"PVCUsageCritical",
	"NamespaceBudgetBreachSoon",
	"WorkloadBudgetBreachSoon",
}

// BuildPrometheusCapacityAlertsQuery returns the PromQL expression used to detect capacity alerts.
func BuildPrometheusCapacityAlertsQuery() string {
	return `ALERTS{alertname=~"PVCUsageHigh|PVCUsageCritical|NamespaceBudgetBreachSoon|WorkloadBudgetBreachSoon",alertstate=~"pending|firing"}`
}

// BuildPrometheusWorkloadAlertQuery returns the PromQL expression used to detect workload alert state.
func BuildPrometheusWorkloadAlertQuery(workload string) string {
	return fmt.Sprintf(`ALERTS{alertname="WorkloadBudgetBreachSoon",workload=%q,alertstate=~"pending|firing"}`, workload)
}

// WaitUntil polls check until it returns true or the timeout is reached.
func WaitUntil(ctx context.Context, timeout, interval time.Duration, check func(context.Context) (bool, error)) error {
	if timeout <= 0 {
		timeout = 1 * time.Second
	}
	if interval <= 0 {
		interval = 1 * time.Second
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		ok, err := check(deadlineCtx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}

		select {
		case <-deadlineCtx.Done():
			return deadlineCtx.Err()
		case <-time.After(interval):
		}
	}
}

// AlertVerifier verifies alert presence in Prometheus and Alertmanager.
type AlertVerifier struct {
	promClient         *PrometheusClient
	alertmanagerURL    string
	alertmanagerClient *http.Client
}

// NewAlertVerifier creates an AlertVerifier.
func NewAlertVerifier(promClient *PrometheusClient, alertmanagerURL string, timeout time.Duration) *AlertVerifier {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &AlertVerifier{
		promClient:      promClient,
		alertmanagerURL: strings.TrimSuffix(alertmanagerURL, "/"),
		alertmanagerClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// PrometheusHasCapacityAlerts checks if any capacity alert is pending/firing in Prometheus ALERTS.
func (v *AlertVerifier) PrometheusHasCapacityAlerts(ctx context.Context) (bool, error) {
	return v.promClient.QueryInstantHasResults(ctx, BuildPrometheusCapacityAlertsQuery())
}

// PrometheusHasWorkloadBudgetAlert checks for a workload-specific WorkloadBudgetBreachSoon alert.
func (v *AlertVerifier) PrometheusHasWorkloadBudgetAlert(ctx context.Context, workload string) (bool, error) {
	return v.promClient.QueryInstantHasResults(ctx, BuildPrometheusWorkloadAlertQuery(workload))
}

// PrometheusHasAllWorkloadBudgetAlerts checks if all workloads have pending/firing alerts.
func (v *AlertVerifier) PrometheusHasAllWorkloadBudgetAlerts(ctx context.Context, workloads []string) (bool, error) {
	for _, workload := range workloads {
		hasAlert, err := v.PrometheusHasWorkloadBudgetAlert(ctx, workload)
		if err != nil {
			return false, err
		}
		if !hasAlert {
			return false, nil
		}
	}
	return true, nil
}

type alertmanagerAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Status      struct {
		State string `json:"state"`
	} `json:"status"`
	StartsAt  string `json:"startsAt"`
	UpdatedAt string `json:"updatedAt"`
}

// AlertDetail is a compact view of an alert instance for reporting.
type AlertDetail struct {
	AlertName   string
	State       string
	Severity    string
	Namespace   string
	PVC         string
	Kind        string
	Workload    string
	Summary     string
	Description string
	StartsAt    string
	UpdatedAt   string
}

func isCapacityAlertName(name string) bool {
	return slices.Contains(capacityAlertNames, name)
}

func toAlertDetail(a alertmanagerAlert) AlertDetail {
	return AlertDetail{
		AlertName:   strings.TrimSpace(a.Labels["alertname"]),
		State:       strings.TrimSpace(a.Status.State),
		Severity:    strings.TrimSpace(a.Labels["severity"]),
		Namespace:   strings.TrimSpace(a.Labels["namespace"]),
		PVC:         strings.TrimSpace(a.Labels["pvc"]),
		Kind:        strings.TrimSpace(a.Labels["kind"]),
		Workload:    strings.TrimSpace(a.Labels["workload"]),
		Summary:     strings.TrimSpace(a.Annotations["summary"]),
		Description: strings.TrimSpace(a.Annotations["description"]),
		StartsAt:    strings.TrimSpace(a.StartsAt),
		UpdatedAt:   strings.TrimSpace(a.UpdatedAt),
	}
}

// AlertmanagerHasCapacityAlerts checks if Alertmanager currently has at least one capacity alert.
func (v *AlertVerifier) AlertmanagerHasCapacityAlerts(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.alertmanagerURL+"/api/v2/alerts", nil)
	if err != nil {
		return false, fmt.Errorf("building alertmanager request: %w", err)
	}
	resp, err := v.alertmanagerClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("calling alertmanager API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("reading alertmanager response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("alertmanager HTTP %d: %s", resp.StatusCode, string(body))
	}

	var alerts []alertmanagerAlert
	if err := json.Unmarshal(body, &alerts); err != nil {
		return false, fmt.Errorf("decoding alertmanager response: %w", err)
	}
	for _, alert := range alerts {
		name := alert.Labels["alertname"]
		if isCapacityAlertName(name) {
			return true, nil
		}
	}
	return false, nil
}

// AlertmanagerCapacityAlertDetails returns active capacity alert details from Alertmanager.
func (v *AlertVerifier) AlertmanagerCapacityAlertDetails(ctx context.Context) ([]AlertDetail, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.alertmanagerURL+"/api/v2/alerts", nil)
	if err != nil {
		return nil, fmt.Errorf("building alertmanager request: %w", err)
	}
	resp, err := v.alertmanagerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling alertmanager API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading alertmanager response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("alertmanager HTTP %d: %s", resp.StatusCode, string(body))
	}

	var alerts []alertmanagerAlert
	if err := json.Unmarshal(body, &alerts); err != nil {
		return nil, fmt.Errorf("decoding alertmanager response: %w", err)
	}
	out := make([]AlertDetail, 0, len(alerts))
	for _, alert := range alerts {
		if !isCapacityAlertName(alert.Labels["alertname"]) {
			continue
		}
		out = append(out, toAlertDetail(alert))
	}
	return out, nil
}
