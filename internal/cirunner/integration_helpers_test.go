package cirunner

import (
	"testing"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
)

func TestPVCNames(t *testing.T) {
	got := pvcNames([]string{"a", "", " b "})
	want := []string{"a-pvc", "b-pvc"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx=%d got=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestTrendHelpers(t *testing.T) {
	r := &IntegrationRunner{cfg: Config{MinGrowthBytesPerMin: 100, UsageRatioSanityMax: 0.9}}
	cp := &capacityv1.CapacityPlan{}
	cp.Status.PVCs = []capacityv1.PVCSummary{
		{Name: "a", UsedBytes: 1, GrowthBytesPerDay: 200 * 1440, UsageRatio: 0.5, CapacityBytes: 10},
		{Name: "b", UsedBytes: 0, GrowthBytesPerDay: 50 * 1440, UsageRatio: 0.95, CapacityBytes: 10},
	}

	if got := r.countGrowingPVCs(cp); got != 1 {
		t.Fatalf("countGrowingPVCs=%d", got)
	}
	if !r.hasNonzeroUsage(cp) {
		t.Fatalf("expected non-zero usage")
	}
	if !r.hasInvalidUsageRatio(cp) {
		t.Fatalf("expected invalid usage ratio")
	}

	r.cfg.UsageRatioSanityMax = 0
	if r.hasInvalidUsageRatio(cp) {
		t.Fatalf("did not expect invalid ratio when sanity max disabled")
	}
}
