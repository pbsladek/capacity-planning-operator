package cirunner

import (
	"testing"

	"github.com/pbsladek/capacity-planning-operator/internal/civerify"
)

func TestParseCounterSumWithLabels(t *testing.T) {
	t.Parallel()

	body := `
# HELP alertmanager_notifications_total Number of notifications sent.
alertmanager_notifications_total{integration="webhook",receiver="ci-webhook"} 4
alertmanager_notifications_total{integration="email",receiver="ops-email"} 3
alertmanager_notifications_failed_total{integration="webhook",receiver="ci-webhook"} 1
`
	got := parseCounterSumWithLabels(body, "alertmanager_notifications_total", map[string]string{
		"integration": "webhook",
		"receiver":    "ci-webhook",
	})
	if got != 4 {
		t.Fatalf("notifications_total sum=%v", got)
	}
	failed := parseCounterSumWithLabels(body, "alertmanager_notifications_failed_total", map[string]string{
		"integration": "webhook",
		"receiver":    "ci-webhook",
	})
	if failed != 1 {
		t.Fatalf("notifications_failed_total sum=%v", failed)
	}
}

func TestResolveAlertmanagerNotificationCounters(t *testing.T) {
	t.Parallel()

	body := `
# HELP alertmanager_notifications_total Number of notifications sent.
alertmanager_notifications_total{integration="webhook",receiver="ci-webhook"} 4
alertmanager_notifications_total{integration="webhook[0]",receiver="ci-webhook"} 3
alertmanager_notifications_failed_total{integration="webhook",receiver="ci-webhook"} 1
alertmanager_notifications_failed_total{integration="webhook[0]",receiver="ci-webhook"} 2
`
	exact := resolveAlertmanagerNotificationCounters(body, "ci-webhook", "webhook")
	if exact.Basis != "exact" || exact.Sent != 4 || exact.Failed != 1 {
		t.Fatalf("exact counters mismatch: %+v", exact)
	}

	receiverOnlyBody := `
alertmanager_notifications_total{integration="webhook[0]",receiver="ci-webhook"} 3
alertmanager_notifications_failed_total{integration="webhook[0]",receiver="ci-webhook"} 2
`
	receiverOnly := resolveAlertmanagerNotificationCounters(receiverOnlyBody, "ci-webhook", "webhook")
	if receiverOnly.Basis != "receiver_only" || receiverOnly.Sent != 3 || receiverOnly.Failed != 2 {
		t.Fatalf("receiver-only counters mismatch: %+v", receiverOnly)
	}
}

func TestValidateAlertmanagerRouteConfig(t *testing.T) {
	t.Parallel()

	statusBody := `{
  "config": {
    "original": "route:\n  receiver: ci-webhook\nreceivers:\n- name: ci-webhook\n  webhook_configs:\n  - url: http://alert-receiver.monitoring.svc.cluster.local:8080/\n"
  }
}`
	if err := validateAlertmanagerRouteConfig(statusBody, "ci-webhook", "webhook"); err != nil {
		t.Fatalf("validateAlertmanagerRouteConfig error: %v", err)
	}
}

func TestValidateAlertMetadata(t *testing.T) {
	t.Parallel()

	details := []civerify.AlertDetail{
		{
			AlertName: "WorkloadBudgetBreachSoon",
			Namespace: "default",
			Kind:      "Pod",
			Workload:  "cpo-ci-steady",
			Summary:   "workload budget breach soon",
		},
		{
			AlertName: "WorkloadBudgetBreachSoon",
			Namespace: "default",
			Kind:      "Pod",
			Workload:  "cpo-ci-bursty",
			Summary:   "workload budget breach soon",
		},
		{
			AlertName: "NamespaceBudgetBreachSoon",
			Namespace: "default",
			Summary:   "namespace budget breach soon",
		},
	}
	if err := validateAlertMetadata(details, []string{"cpo-ci-steady", "cpo-ci-bursty"}); err != nil {
		t.Fatalf("validateAlertMetadata error: %v", err)
	}
}

func TestParseAlertReceiverRecords(t *testing.T) {
	t.Parallel()

	body := `{"count":1,"records":["{\"receiver\":\"ci-webhook\",\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"WorkloadBudgetBreachSoon\",\"namespace\":\"default\",\"kind\":\"Pod\",\"workload\":\"cpo-ci-steady\"},\"annotations\":{\"summary\":\"steady workload budget breach soon\"}}]}"]}`
	records, err := parseAlertReceiverRecords(body)
	if err != nil {
		t.Fatalf("parseAlertReceiverRecords error: %v", err)
	}
	if records.Count != 1 {
		t.Fatalf("records.Count=%d", records.Count)
	}
	if len(records.Records) != 1 {
		t.Fatalf("len(records.Records)=%d", len(records.Records))
	}
}

func TestParseAlertReceiverDetails(t *testing.T) {
	t.Parallel()

	records := alertReceiverRecords{
		Count: 2,
		Records: []string{
			`{"receiver":"ci-webhook","alerts":[{"status":"firing","labels":{"alertname":"WorkloadBudgetBreachSoon","namespace":"default","kind":"Pod","workload":"cpo-ci-steady","severity":"warning"},"annotations":{"summary":"steady breach soon"}}]}`,
			`{"receiver":"ci-webhook","alerts":[{"status":"firing","labels":{"alertname":"WorkloadBudgetBreachSoon","namespace":"default","kind":"Pod","workload":"cpo-ci-bursty","severity":"warning"},"annotations":{"summary":"bursty breach soon"}},{"status":"firing","labels":{"alertname":"NamespaceBudgetBreachSoon","namespace":"default","severity":"warning"},"annotations":{"summary":"namespace breach soon"}}]}`,
		},
	}
	details, err := parseAlertReceiverDetails(records, "ci-webhook")
	if err != nil {
		t.Fatalf("parseAlertReceiverDetails error: %v", err)
	}
	if len(details) != 3 {
		t.Fatalf("len(details)=%d", len(details))
	}
	if err := validateAlertMetadata(details, []string{"cpo-ci-steady", "cpo-ci-bursty"}); err != nil {
		t.Fatalf("validateAlertMetadata(details) error: %v", err)
	}
}
