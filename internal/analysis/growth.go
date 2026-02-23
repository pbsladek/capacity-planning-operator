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

package analysis

const secondsPerDay = 86400.0

// GrowthResult holds the output of OLS linear regression on a sample set.
type GrowthResult struct {
	// GrowthBytesPerDay is the linear regression slope in bytes per day.
	// Negative means the volume is shrinking.
	GrowthBytesPerDay float64

	// DaysUntilFull is the predicted days remaining before the PVC reaches 100% capacity.
	// Nil when:
	//   - growth rate is <= 0 (shrinking or flat)
	//   - capacityBytes is 0 (unknown)
	//   - fewer than 2 samples (regression not possible)
	DaysUntilFull *float64

	// ConfidenceR2 is the coefficient of determination (R²) for the regression.
	// Range [0.0, 1.0]. Values below 0.5 indicate an unreliable trend.
	ConfidenceR2 float64
}

// CalculateGrowth performs ordinary least-squares (OLS) linear regression on
// the provided samples and returns the growth rate and predicted time to fill.
//
// Algorithm:
//
//	Given n points (x_i, y_i) where:
//	  x_i = (sample[i].Timestamp.Unix() - t0) / 86400.0  (days since first sample)
//	  y_i = float64(sample[i].UsedBytes)
//	  t0  = samples[0].Timestamp.Unix()  (anchor; prevents float64 precision loss)
//
//	slope     = (n·Σxy - Σx·Σy) / (n·Σx² - (Σx)²)
//	intercept = (Σy - slope·Σx) / n
//	R²        = 1 - SS_res / SS_tot
//
// Why OLS over a simple first/last delta:
//   - Robust to one-time spikes (backup jobs, log bursts, vacuums).
//   - A single large write at the window boundary would dominate a simple delta.
//   - OLS over many points smooths transient anomalies.
//
// capacityBytes of 0 means unknown; DaysUntilFull will be nil.
// Returns zero-value GrowthResult if fewer than 2 samples are provided.
func CalculateGrowth(samples []Sample, capacityBytes int64) GrowthResult {
	n := len(samples)
	if n < 2 {
		return GrowthResult{}
	}

	// Anchor at first sample to preserve float64 precision.
	t0 := samples[0].Timestamp.Unix()

	var sumX, sumY, sumXY, sumX2 float64
	for _, s := range samples {
		x := float64(s.Timestamp.Unix()-t0) / secondsPerDay
		y := float64(s.UsedBytes)
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}

	fn := float64(n)
	denom := fn*sumX2 - sumX*sumX
	if denom == 0 {
		// All samples at the same timestamp — no time has elapsed.
		return GrowthResult{}
	}

	slope := (fn*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / fn

	// R² — coefficient of determination.
	meanY := sumY / fn
	var ssTot, ssRes float64
	for _, s := range samples {
		x := float64(s.Timestamp.Unix()-t0) / secondsPerDay
		yHat := slope*x + intercept
		y := float64(s.UsedBytes)
		ssTot += (y - meanY) * (y - meanY)
		ssRes += (y - yHat) * (y - yHat)
	}

	var r2 float64
	if ssTot > 0 {
		r2 = 1.0 - ssRes/ssTot
	}

	result := GrowthResult{
		GrowthBytesPerDay: slope,
		ConfidenceR2:      r2,
	}

	// Predict days until full only when growth is positive and capacity is known.
	if slope > 0 && capacityBytes > 0 {
		lastUsed := float64(samples[n-1].UsedBytes)
		remaining := float64(capacityBytes) - lastUsed
		if remaining <= 0 {
			// Already at or over capacity.
			zero := 0.0
			result.DaysUntilFull = &zero
		} else {
			days := remaining / slope
			result.DaysUntilFull = &days
		}
	}

	return result
}
