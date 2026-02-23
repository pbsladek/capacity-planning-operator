package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
)

func TestCapacityPlanNotificationReconcile_DryRun(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add kube scheme: %v", err)
	}
	if err := capacityv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add capacity scheme: %v", err)
	}

	now := metav1.NewTime(time.Unix(1_700_000_000, 0))
	plan := &capacityv1.CapacityPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: capacityv1.CapacityPlanStatus{
			RiskDigest:       "risk digest",
			RiskSnapshotHash: "hash-1",
			TopRisks: []capacityv1.PVCRiskSummary{
				{Namespace: "ns", Name: "pvc", WeeklyGrowthBytesPerDay: 1},
			},
			LastReconcileTime: &now,
		},
	}
	notif := &capacityv1.CapacityPlanNotification{
		ObjectMeta: metav1.ObjectMeta{Name: "digest", Namespace: "default"},
		Spec: capacityv1.CapacityPlanNotificationSpec{
			PlanRefName:  "cluster",
			OnChangeOnly: true,
			DryRun:       true,
			Slack: capacityv1.SlackNotificationSpec{
				Enabled: true,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&capacityv1.CapacityPlanNotification{}).
		WithObjects(plan, notif).
		Build()
	r := &CapacityPlanNotificationReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "digest", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got capacityv1.CapacityPlanNotification
	if err := c.Get(context.Background(), types.NamespacedName{Name: "digest", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get notification: %v", err)
	}
	if got.Status.LastDigestHash != "hash-1" {
		t.Fatalf("expected digest hash recorded, got %q", got.Status.LastDigestHash)
	}
	if got.Status.LastSentTime == nil {
		t.Fatalf("expected LastSentTime to be set")
	}
	if len(got.Status.LastSentChannels) != 1 || got.Status.LastSentChannels[0] != "slack" {
		t.Fatalf("expected slack channel in status, got %#v", got.Status.LastSentChannels)
	}
}

func TestCapacityPlanNotificationReconcile_NoChannels(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add kube scheme: %v", err)
	}
	if err := capacityv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add capacity scheme: %v", err)
	}

	plan := &capacityv1.CapacityPlan{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
	notif := &capacityv1.CapacityPlanNotification{
		ObjectMeta: metav1.ObjectMeta{Name: "digest", Namespace: "default"},
		Spec: capacityv1.CapacityPlanNotificationSpec{
			PlanRefName: "cluster",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&capacityv1.CapacityPlanNotification{}).
		WithObjects(plan, notif).
		Build()
	r := &CapacityPlanNotificationReconciler{Client: c, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "digest", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got capacityv1.CapacityPlanNotification
	if err := c.Get(context.Background(), types.NamespacedName{Name: "digest", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get notification: %v", err)
	}
	if got.Status.LastMessage == "" {
		t.Fatalf("expected status message for no channels")
	}
}
