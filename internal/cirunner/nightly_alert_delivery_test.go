package cirunner

import "testing"

func TestContainsSyntheticAlert(t *testing.T) {
	t.Parallel()

	records := []string{
		`{"receiver":"ci-webhook","alerts":[{"labels":{"alertname":"WorkloadBudgetBreachSoon"}}]}`,
		`{"receiver":"ci-webhook","alerts":[{"labels":{"alertname":"CIAlwaysFiring"}}]}`,
	}
	if !containsSyntheticAlert(records, "CIAlwaysFiring") {
		t.Fatalf("expected synthetic alert to be detected")
	}
	if containsSyntheticAlert(records, "MissingAlert") {
		t.Fatalf("did not expect missing alert to be detected")
	}
}
