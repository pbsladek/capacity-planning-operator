package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	pkgmetrics "github.com/pbsladek/capacity-planning-operator/internal/metrics"
)

func TestBackfillAllPVCs_Success(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	pvcA := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "a",
			UID:       types.UID("uid-a"),
		},
	}
	pvcB := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "b",
			UID:       types.UID("uid-b"),
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pvcA, pvcB).Build()
	mock := pkgmetrics.NewMockPVCMetricsClient()
	now := time.Now()
	mock.RangeData["default/a"] = []pkgmetrics.RangePoint{
		{Timestamp: now.Add(-10 * time.Minute), UsedBytes: 10},
		{Timestamp: now.Add(-5 * time.Minute), UsedBytes: 20},
	}
	mock.RangeData["default/b"] = []pkgmetrics.RangePoint{
		{Timestamp: now.Add(-10 * time.Minute), UsedBytes: 30},
	}

	w := NewPVCWatcherReconciler(k8sClient, mock, 16)
	n, err := w.BackfillAllPVCs(context.Background(), now.Add(-30*time.Minute), now, 5*time.Minute)
	if err != nil {
		t.Fatalf("unexpected backfill error: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 successful backfills, got %d", n)
	}
	if got := len(w.GetSnapshot("default/a")); got != 2 {
		t.Fatalf("expected 2 samples for a, got %d", got)
	}
	if got := len(w.GetSnapshot("default/b")); got != 1 {
		t.Fatalf("expected 1 sample for b, got %d", got)
	}
}

func TestBackfillAllPVCs_AggregatesErrors(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "a",
			UID:       types.UID("uid-a"),
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
	mock := pkgmetrics.NewMockPVCMetricsClient()
	mock.Err = errors.New("backend down")

	w := NewPVCWatcherReconciler(k8sClient, mock, 16)
	n, err := w.BackfillAllPVCs(context.Background(), time.Now().Add(-time.Hour), time.Now(), 5*time.Minute)
	if err == nil {
		t.Fatalf("expected aggregated error")
	}
	if n != 0 {
		t.Fatalf("expected 0 successful backfills, got %d", n)
	}
}
