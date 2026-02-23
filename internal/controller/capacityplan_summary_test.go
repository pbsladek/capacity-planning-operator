package controller

import (
	"testing"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
)

func TestBuildStatusSummary(t *testing.T) {
	t.Parallel()

	d3 := 3.0
	d1 := 1.0
	d5 := 5.0
	items := []capacityv1.PVCSummary{
		{Namespace: "ns", Name: "a", UsageRatio: 0.70, DaysUntilFull: &d3, AlertFiring: false, GrowthBytesPerDay: 4},
		{Namespace: "ns", Name: "b", UsageRatio: 0.95, DaysUntilFull: &d1, AlertFiring: true, GrowthBytesPerDay: 9},
		{Namespace: "ns", Name: "c", UsageRatio: 0.80, DaysUntilFull: nil, AlertFiring: false, GrowthBytesPerDay: -2},
		{Namespace: "ns", Name: "d", UsageRatio: 0.85, DaysUntilFull: &d5, AlertFiring: true, GrowthBytesPerDay: 7},
	}

	s := buildStatusSummary(items, 2)
	if s.TotalPVCs != 4 {
		t.Fatalf("expected TotalPVCs=4, got %d", s.TotalPVCs)
	}
	if s.AlertingPVCs != 2 {
		t.Fatalf("expected AlertingPVCs=2, got %d", s.AlertingPVCs)
	}

	if len(s.TopByUsage) != 2 {
		t.Fatalf("expected 2 TopByUsage entries, got %d", len(s.TopByUsage))
	}
	if s.TopByUsage[0].Name != "b" || s.TopByUsage[1].Name != "d" {
		t.Fatalf("unexpected TopByUsage order: %#v", s.TopByUsage)
	}

	if len(s.TopBySoonestToFull) != 2 {
		t.Fatalf("expected 2 TopBySoonestToFull entries, got %d", len(s.TopBySoonestToFull))
	}
	if s.TopBySoonestToFull[0].Name != "b" || s.TopBySoonestToFull[1].Name != "a" {
		t.Fatalf("unexpected TopBySoonestToFull order: %#v", s.TopBySoonestToFull)
	}

	if len(s.TopByGrowth) != 2 {
		t.Fatalf("expected 2 TopByGrowth entries, got %d", len(s.TopByGrowth))
	}
	if s.TopByGrowth[0].Name != "b" || s.TopByGrowth[1].Name != "d" {
		t.Fatalf("unexpected TopByGrowth order: %#v", s.TopByGrowth)
	}
}
