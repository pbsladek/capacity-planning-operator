/*
Copyright 2024 pbsladek.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package analysis_test

import (
	"math"
	"testing"
	"time"

	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
)

const (
	gb             = int64(1_000_000_000)
	toleranceBytes = float64(10_000_000) // 10 MB/day tolerance for float arithmetic
	toleranceDays  = 0.1                 // 0.1 day tolerance for DaysUntilFull
)

func TestCalculateGrowth_ZeroSamples(t *testing.T) {
	result := analysis.CalculateGrowth(nil, 10*gb)
	if result.GrowthBytesPerDay != 0 {
		t.Errorf("expected 0 growth, got %f", result.GrowthBytesPerDay)
	}
	if result.DaysUntilFull != nil {
		t.Error("expected nil DaysUntilFull with no samples")
	}
	if result.ConfidenceR2 != 0 {
		t.Errorf("expected 0 R², got %f", result.ConfidenceR2)
	}
}

func TestCalculateGrowth_OneSample(t *testing.T) {
	samples := []analysis.Sample{
		{Timestamp: time.Now(), UsedBytes: 5 * gb},
	}
	result := analysis.CalculateGrowth(samples, 10*gb)
	if result.GrowthBytesPerDay != 0 {
		t.Errorf("expected 0 growth for single sample, got %f", result.GrowthBytesPerDay)
	}
	if result.DaysUntilFull != nil {
		t.Error("expected nil DaysUntilFull for single sample")
	}
}

func TestCalculateGrowth_LinearGrowth_1GBperDay(t *testing.T) {
	// 7 daily samples at exactly 1 GB/day growth on a 10 GB PVC.
	// At the last sample, 7 GB used → 3 GB remaining → 3 days until full.
	now := time.Now().Truncate(24 * time.Hour)
	samples := make([]analysis.Sample, 7)
	for i := range samples {
		samples[i] = analysis.Sample{
			Timestamp: now.Add(time.Duration(i) * 24 * time.Hour),
			UsedBytes: int64(i+1) * gb,
		}
	}

	result := analysis.CalculateGrowth(samples, 10*gb)

	// Slope should be very close to 1 GB/day.
	if math.Abs(result.GrowthBytesPerDay-float64(gb)) > toleranceBytes {
		t.Errorf("expected slope ≈ 1GB/day, got %f", result.GrowthBytesPerDay)
	}

	// R² should be near-perfect for perfectly linear data.
	if result.ConfidenceR2 < 0.99 {
		t.Errorf("expected R² > 0.99 for linear data, got %f", result.ConfidenceR2)
	}

	// DaysUntilFull: 10GB capacity, 7GB used at last sample → 3 GB remaining / 1 GB/day = 3 days.
	if result.DaysUntilFull == nil {
		t.Fatal("expected non-nil DaysUntilFull")
	}
	if math.Abs(*result.DaysUntilFull-3.0) > toleranceDays {
		t.Errorf("expected DaysUntilFull ≈ 3.0, got %f", *result.DaysUntilFull)
	}
}

func TestCalculateGrowth_NegativeGrowth(t *testing.T) {
	// Volume is shrinking — DaysUntilFull should be nil.
	now := time.Now()
	samples := []analysis.Sample{
		{Timestamp: now.Add(-2 * 24 * time.Hour), UsedBytes: 5 * gb},
		{Timestamp: now.Add(-1 * 24 * time.Hour), UsedBytes: 4 * gb},
		{Timestamp: now, UsedBytes: 3 * gb},
	}
	result := analysis.CalculateGrowth(samples, 10*gb)

	if result.GrowthBytesPerDay >= 0 {
		t.Errorf("expected negative slope, got %f", result.GrowthBytesPerDay)
	}
	if result.DaysUntilFull != nil {
		t.Errorf("expected nil DaysUntilFull for shrinking volume, got %f", *result.DaysUntilFull)
	}
}

func TestCalculateGrowth_ZeroGrowth(t *testing.T) {
	// Flat usage — DaysUntilFull should be nil (not growing, will never fill at this rate).
	now := time.Now()
	samples := []analysis.Sample{
		{Timestamp: now.Add(-3 * 24 * time.Hour), UsedBytes: 5 * gb},
		{Timestamp: now.Add(-2 * 24 * time.Hour), UsedBytes: 5 * gb},
		{Timestamp: now.Add(-1 * 24 * time.Hour), UsedBytes: 5 * gb},
		{Timestamp: now, UsedBytes: 5 * gb},
	}
	result := analysis.CalculateGrowth(samples, 10*gb)

	// Slope should be ~0 (may have tiny floating point residual).
	if math.Abs(result.GrowthBytesPerDay) > toleranceBytes {
		t.Errorf("expected ~0 slope for flat data, got %f", result.GrowthBytesPerDay)
	}
	if result.DaysUntilFull != nil {
		t.Errorf("expected nil DaysUntilFull for zero growth, got %f", *result.DaysUntilFull)
	}
}

func TestCalculateGrowth_AlreadyFull(t *testing.T) {
	// Last sample is at or over capacity — DaysUntilFull should be &0.0.
	now := time.Now()
	samples := []analysis.Sample{
		{Timestamp: now.Add(-24 * time.Hour), UsedBytes: 9 * gb},
		{Timestamp: now, UsedBytes: 10*gb + 500_000_000}, // over 10 GB capacity
	}
	result := analysis.CalculateGrowth(samples, 10*gb)

	if result.DaysUntilFull == nil {
		t.Fatal("expected non-nil DaysUntilFull when already full")
	}
	if *result.DaysUntilFull != 0.0 {
		t.Errorf("expected DaysUntilFull=0.0 when over capacity, got %f", *result.DaysUntilFull)
	}
}

func TestCalculateGrowth_ZeroCapacity(t *testing.T) {
	// capacityBytes=0 means unknown — DaysUntilFull must be nil.
	now := time.Now()
	samples := []analysis.Sample{
		{Timestamp: now.Add(-24 * time.Hour), UsedBytes: 3 * gb},
		{Timestamp: now, UsedBytes: 4 * gb},
	}
	result := analysis.CalculateGrowth(samples, 0)

	if result.DaysUntilFull != nil {
		t.Errorf("expected nil DaysUntilFull with unknown capacity, got %f", *result.DaysUntilFull)
	}
	// Growth should still be calculated even without capacity.
	if result.GrowthBytesPerDay <= 0 {
		t.Errorf("expected positive slope, got %f", result.GrowthBytesPerDay)
	}
}

func TestCalculateGrowth_HighVariance(t *testing.T) {
	// Noisy, non-linear data — R² should be low.
	now := time.Now()
	samples := []analysis.Sample{
		{Timestamp: now.Add(-6 * 24 * time.Hour), UsedBytes: 1 * gb},
		{Timestamp: now.Add(-5 * 24 * time.Hour), UsedBytes: 9 * gb},
		{Timestamp: now.Add(-4 * 24 * time.Hour), UsedBytes: 2 * gb},
		{Timestamp: now.Add(-3 * 24 * time.Hour), UsedBytes: 8 * gb},
		{Timestamp: now.Add(-2 * 24 * time.Hour), UsedBytes: 1 * gb},
		{Timestamp: now.Add(-1 * 24 * time.Hour), UsedBytes: 9 * gb},
		{Timestamp: now, UsedBytes: 2 * gb},
	}
	result := analysis.CalculateGrowth(samples, 10*gb)

	if result.ConfidenceR2 >= 0.5 {
		t.Errorf("expected R² < 0.5 for noisy data, got %f", result.ConfidenceR2)
	}
}

func TestCalculateGrowth_AllSameTimestamp(t *testing.T) {
	// All samples at the same timestamp — denom=0, should return zero-value gracefully.
	now := time.Now()
	samples := []analysis.Sample{
		{Timestamp: now, UsedBytes: 1 * gb},
		{Timestamp: now, UsedBytes: 2 * gb},
		{Timestamp: now, UsedBytes: 3 * gb},
	}
	result := analysis.CalculateGrowth(samples, 10*gb)
	if result.GrowthBytesPerDay != 0 {
		t.Errorf("expected 0 growth for same-timestamp samples, got %f", result.GrowthBytesPerDay)
	}
}
