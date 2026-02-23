package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
	"github.com/pbsladek/capacity-planning-operator/internal/llm"
	"github.com/pbsladek/capacity-planning-operator/internal/metrics"
)

func TestBuildSummary_OnlyAlertingPVCsSkipsNonAlerting(t *testing.T) {
	t.Parallel()

	r := &CapacityPlanReconciler{
		Watcher: NewPVCWatcherReconciler(nil, &metrics.MockPVCMetricsClient{}, 10),
	}
	mockLLM := &llm.MockInsightGenerator{Response: "new insight"}
	pvc := &corev1.PersistentVolumeClaim{}
	pvc.Name = "data"
	pvc.Namespace = "ns"
	pvc.UID = types.UID("uid-1")
	pvc.Spec.Resources.Requests = corev1.ResourceList{
		corev1.ResourceStorage: resource.MustParse("10Gi"),
	}

	// Low usage and no positive near-term fill-up trend keeps AlertFiring=false.
	key := "ns/data"
	state := r.Watcher.ensureState(key, pvc.UID)
	state.Buffer.Push(analysis.Sample{
		Timestamp: time.Now(),
		UsedBytes: 1 * 1024 * 1024 * 1024,
	})

	prev := capacityv1.PVCSummary{
		LLMInsight: "previous insight",
	}
	got := r.buildSummary(context.Background(), pvc, key, state.Buffer.Snapshot(), 0.85, 7, time.Hour, mockLLM, "mock", "mock", prev, true)
	if got.AlertFiring {
		t.Fatalf("expected AlertFiring=false")
	}
	if mockLLM.GetCallCount() != 0 {
		t.Fatalf("expected no LLM calls, got %d", mockLLM.GetCallCount())
	}
	if got.LLMInsight != "previous insight" {
		t.Fatalf("expected previous insight to be preserved, got %q", got.LLMInsight)
	}
}

func TestBuildSummary_OnlyAlertingPVCsCallsLLMForAlerting(t *testing.T) {
	t.Parallel()

	r := &CapacityPlanReconciler{
		Watcher: NewPVCWatcherReconciler(nil, &metrics.MockPVCMetricsClient{}, 10),
	}
	mockLLM := &llm.MockInsightGenerator{Response: "new insight"}
	pvc := &corev1.PersistentVolumeClaim{}
	pvc.Name = "data"
	pvc.Namespace = "ns"
	pvc.UID = types.UID("uid-2")
	pvc.Spec.Resources.Requests = corev1.ResourceList{
		corev1.ResourceStorage: resource.MustParse("10Gi"),
	}

	key := "ns/data"
	state := r.Watcher.ensureState(key, pvc.UID)
	state.Buffer.Push(analysis.Sample{
		Timestamp: time.Now(),
		UsedBytes: 9 * 1024 * 1024 * 1024,
	})

	got := r.buildSummary(context.Background(), pvc, key, state.Buffer.Snapshot(), 0.85, 7, time.Hour, mockLLM, "mock", "mock", capacityv1.PVCSummary{}, true)
	if !got.AlertFiring {
		t.Fatalf("expected AlertFiring=true")
	}
	if mockLLM.GetCallCount() != 1 {
		t.Fatalf("expected one LLM call, got %d", mockLLM.GetCallCount())
	}
	if got.LLMInsight != "new insight" {
		t.Fatalf("expected refreshed insight, got %q", got.LLMInsight)
	}
}
