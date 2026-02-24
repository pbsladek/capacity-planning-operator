package civerify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func promQueryResponse(hasResult bool) string {
	if hasResult {
		return `{"status":"success","data":{"result":[{"value":[1730000000,"1"]}]}}`
	}
	return `{"status":"success","data":{"result":[]}}`
}

func TestPrometheusAlertChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "NamespaceBudgetBreachSoon"):
			_, _ = w.Write([]byte(promQueryResponse(true)))
		case strings.Contains(q, `workload="w1"`):
			_, _ = w.Write([]byte(promQueryResponse(true)))
		case strings.Contains(q, `workload="w2"`):
			_, _ = w.Write([]byte(promQueryResponse(true)))
		default:
			_, _ = w.Write([]byte(promQueryResponse(false)))
		}
	}))
	t.Cleanup(srv.Close)

	promClient := NewPrometheusClient(srv.URL, 2*time.Second)
	verifier := NewAlertVerifier(promClient, "http://127.0.0.1:19093", 2*time.Second)

	ok, err := verifier.PrometheusHasCapacityAlerts(context.Background())
	if err != nil {
		t.Fatalf("PrometheusHasCapacityAlerts error: %v", err)
	}
	if !ok {
		t.Fatal("expected capacity alerts to be present")
	}

	ok, err = verifier.PrometheusHasAllWorkloadBudgetAlerts(context.Background(), []string{"w1", "w2"})
	if err != nil {
		t.Fatalf("PrometheusHasAllWorkloadBudgetAlerts error: %v", err)
	}
	if !ok {
		t.Fatal("expected all workload alerts to be present")
	}
}

func TestAlertmanagerHasCapacityAlerts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
{"labels":{"alertname":"Watchdog"}},
{"labels":{"alertname":"PVCUsageHigh","namespace":"default","pvc":"example-pvc","severity":"warning"},"annotations":{"summary":"PVC usage high on default/example-pvc"},"status":{"state":"active"}}
]`))
	}))
	t.Cleanup(srv.Close)

	promClient := NewPrometheusClient("http://127.0.0.1:19090", 2*time.Second)
	verifier := NewAlertVerifier(promClient, srv.URL, 2*time.Second)

	ok, err := verifier.AlertmanagerHasCapacityAlerts(context.Background())
	if err != nil {
		t.Fatalf("AlertmanagerHasCapacityAlerts error: %v", err)
	}
	if !ok {
		t.Fatal("expected capacity alert to be present")
	}

	details, err := verifier.AlertmanagerCapacityAlertDetails(context.Background())
	if err != nil {
		t.Fatalf("AlertmanagerCapacityAlertDetails error: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("expected 1 capacity alert detail, got %d", len(details))
	}
	if details[0].AlertName != "PVCUsageHigh" {
		t.Fatalf("unexpected alert name: %s", details[0].AlertName)
	}
	if details[0].PVC != "example-pvc" {
		t.Fatalf("unexpected pvc label: %s", details[0].PVC)
	}
}

func TestWaitUntilTimeout(t *testing.T) {
	start := time.Now()
	err := WaitUntil(context.Background(), 50*time.Millisecond, 10*time.Millisecond, func(context.Context) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatalf("wait returned too quickly: %s", time.Since(start))
	}
}
