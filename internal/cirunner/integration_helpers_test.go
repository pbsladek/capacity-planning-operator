package cirunner

import (
	"testing"
	"time"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/civerify"
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

func TestEffectiveGrowthCompareWindowSeconds(t *testing.T) {
	now := time.Now()
	r := &IntegrationRunner{
		cfg: Config{
			GrowthCompareWindowSeconds: 240,
			PlanSampleRetention:        999,
			PlanSampleInterval:         "5s",
		},
		state: integrationState{
			obsStartedAt:  now,
			obsFinishedAt: now.Add(8 * time.Minute),
		},
	}
	if got := r.effectiveGrowthCompareWindowSeconds(); got != 480 {
		t.Fatalf("effective window=%d want=480", got)
	}

	r.cfg.GrowthCompareWindowSeconds = 900
	if got := r.effectiveGrowthCompareWindowSeconds(); got != 900 {
		t.Fatalf("effective window=%d want=900", got)
	}
}

func TestEffectiveGrowthCompareWindowCappedBySampleRetention(t *testing.T) {
	now := time.Now()
	r := &IntegrationRunner{
		cfg: Config{
			GrowthCompareWindowSeconds: 240,
			PlanSampleRetention:        32,
			PlanSampleInterval:         "5s",
		},
		state: integrationState{
			obsStartedAt:  now,
			obsFinishedAt: now.Add(8 * time.Minute),
		},
	}
	if got := r.sampleRetentionWindowSeconds(); got != 160 {
		t.Fatalf("sample retention window=%d want=160", got)
	}
	if got := r.effectiveGrowthCompareWindowSeconds(); got != 160 {
		t.Fatalf("effective window=%d want=160", got)
	}
}

func TestGrowthComparisonHint(t *testing.T) {
	summary := civerify.ComparisonSummary{
		Rows: []civerify.ComparisonRow{
			{Name: "a", StatusBytesPerDay: 10, HasPromData: true, PromBytesPerDay: 0},
			{Name: "b", StatusBytesPerDay: 20, HasPromData: true, PromBytesPerDay: 0},
			{Name: "c", StatusBytesPerDay: 30, HasPromData: true, PromBytesPerDay: 1},
		},
	}
	if hint := growthComparisonHint(summary); hint == "" {
		t.Fatalf("expected non-empty hint")
	}

	okSummary := civerify.ComparisonSummary{
		Rows: []civerify.ComparisonRow{
			{Name: "a", StatusBytesPerDay: 10, HasPromData: true, PromBytesPerDay: 8},
			{Name: "b", StatusBytesPerDay: 20, HasPromData: true, PromBytesPerDay: 19},
		},
	}
	if hint := growthComparisonHint(okSummary); hint != "" {
		t.Fatalf("unexpected hint: %q", hint)
	}
}
