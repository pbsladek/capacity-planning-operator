package controller

import (
	"testing"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/metrics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPrometheusReadyStatus(t *testing.T) {
	t.Parallel()

	r := &CapacityPlanReconciler{
		DefaultMetricsClient: &metrics.MockPVCMetricsClient{},
	}
	status, reason, _ := r.prometheusReadyStatus(capacityv1.CapacityPlanSpec{})
	if status != metav1.ConditionFalse || reason != "Disabled" {
		t.Fatalf("expected disabled status, got status=%s reason=%s", status, reason)
	}

	status, reason, _ = r.prometheusReadyStatus(capacityv1.CapacityPlanSpec{PrometheusURL: "http://prometheus:9090"})
	if status != metav1.ConditionTrue || reason != "Configured" {
		t.Fatalf("expected configured status from spec URL, got status=%s reason=%s", status, reason)
	}

	r.DefaultMetricsClient = metrics.NewPrometheusClient("http://prometheus:9090")
	status, reason, _ = r.prometheusReadyStatus(capacityv1.CapacityPlanSpec{})
	if status != metav1.ConditionTrue || reason != "Configured" {
		t.Fatalf("expected configured status from default client, got status=%s reason=%s", status, reason)
	}
}

func TestBackfillReadyStatus(t *testing.T) {
	t.Parallel()

	r := &CapacityPlanReconciler{}
	status, reason, _ := r.backfillReadyStatus()
	if status != metav1.ConditionFalse || reason != "NotConfigured" {
		t.Fatalf("expected not configured status, got status=%s reason=%s", status, reason)
	}

	r.StartupBackfillConfigured = true
	r.StartupBackfillSuccessfulPVCs = 4
	status, reason, _ = r.backfillReadyStatus()
	if status != metav1.ConditionTrue || reason != "Succeeded" {
		t.Fatalf("expected succeeded status, got status=%s reason=%s", status, reason)
	}

	r.StartupBackfillError = "partial errors"
	status, reason, _ = r.backfillReadyStatus()
	if status != metav1.ConditionFalse || reason != "PartialFailure" {
		t.Fatalf("expected partial failure status, got status=%s reason=%s", status, reason)
	}
}
