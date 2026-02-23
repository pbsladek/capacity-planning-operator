package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
)

func TestPickActivePlan_Empty(t *testing.T) {
	t.Parallel()

	if got := pickActivePlan(nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestPickActivePlan_OldestWins(t *testing.T) {
	t.Parallel()

	now := time.Now()
	plans := []capacityv1.CapacityPlan{
		{ObjectMeta: metav1.ObjectMeta{Name: "newer", CreationTimestamp: metav1.NewTime(now)}},
		{ObjectMeta: metav1.ObjectMeta{Name: "older", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}},
	}

	got := pickActivePlan(plans)
	if got == nil {
		t.Fatalf("expected active plan, got nil")
	}
	if got.Name != "older" {
		t.Fatalf("expected older plan, got %q", got.Name)
	}
}

func TestPickActivePlan_NameTieBreak(t *testing.T) {
	t.Parallel()

	ts := metav1.NewTime(time.Now())
	plans := []capacityv1.CapacityPlan{
		{ObjectMeta: metav1.ObjectMeta{Name: "zeta", CreationTimestamp: ts}},
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha", CreationTimestamp: ts}},
	}

	got := pickActivePlan(plans)
	if got == nil {
		t.Fatalf("expected active plan, got nil")
	}
	if got.Name != "alpha" {
		t.Fatalf("expected lexicographically first plan, got %q", got.Name)
	}
}
