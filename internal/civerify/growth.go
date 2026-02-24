package civerify

import (
	"context"
	"fmt"
	"math"
)

// PVCGrowth captures the status-reported growth signal for a PVC.
type PVCGrowth struct {
	Name              string
	StatusBytesPerDay float64
}

// CompareOptions controls tolerance and pass/fail thresholds for growth checks.
type CompareOptions struct {
	RelativeTolerance    float64
	AbsToleranceBytesDay float64
	MinComparablePVCs    int
	MinMatchingPVCs      int
}

// GrowthFetcher returns Prometheus-derived growth bytes/day for a PVC.
// hasData=false means Prometheus had no usable sample for that PVC.
type GrowthFetcher func(ctx context.Context, pvcName string) (value float64, hasData bool, err error)

// ComparisonRow is one line in the growth cross-check report.
type ComparisonRow struct {
	Name              string
	StatusBytesPerDay float64
	PromBytesPerDay   float64
	HasPromData       bool
	AbsDiff           float64
	RelDiffPct        float64
	AllowedDiff       float64
	ToleranceBasis    string
	Matched           bool
	Reason            string
}

// ComparisonSummary captures the aggregate cross-check result.
type ComparisonSummary struct {
	Rows       []ComparisonRow
	Comparable int
	Matched    int
}

// CompareGrowth compares status growth values against Prometheus-derived growth values.
func CompareGrowth(ctx context.Context, status []PVCGrowth, fetch GrowthFetcher, opts CompareOptions) (ComparisonSummary, error) {
	summary := ComparisonSummary{Rows: make([]ComparisonRow, 0, len(status))}
	for _, pvc := range status {
		if pvc.Name == "" {
			continue
		}
		row := ComparisonRow{
			Name:              pvc.Name,
			StatusBytesPerDay: pvc.StatusBytesPerDay,
		}

		promGrowth, hasData, err := fetch(ctx, pvc.Name)
		if err != nil {
			return summary, fmt.Errorf("querying Prometheus growth for %s: %w", pvc.Name, err)
		}
		if !hasData || math.IsNaN(promGrowth) || math.IsInf(promGrowth, 0) {
			row.Reason = "no_prometheus_data"
			summary.Rows = append(summary.Rows, row)
			continue
		}

		row.HasPromData = true
		row.PromBytesPerDay = promGrowth
		row.AbsDiff = math.Abs(pvc.StatusBytesPerDay - promGrowth)
		scale := math.Max(1, math.Max(math.Abs(pvc.StatusBytesPerDay), math.Abs(promGrowth)))
		allowedAbs := opts.AbsToleranceBytesDay
		allowedRel := opts.RelativeTolerance * scale
		allowed := math.Max(allowedAbs, allowedRel)
		row.RelDiffPct = (row.AbsDiff / scale) * 100
		row.AllowedDiff = allowed
		if allowedAbs >= allowedRel {
			row.ToleranceBasis = "absolute"
		} else {
			row.ToleranceBasis = "relative"
		}
		row.Matched = row.AbsDiff <= allowed
		if row.Matched {
			row.Reason = "within_tolerance"
		} else {
			row.Reason = "exceeds_" + row.ToleranceBasis + "_tolerance"
		}

		summary.Comparable++
		if row.Matched {
			summary.Matched++
		}
		summary.Rows = append(summary.Rows, row)
	}

	if summary.Comparable < opts.MinComparablePVCs {
		return summary, fmt.Errorf(
			"growth cross-check had only %d comparable PVCs (required: %d)",
			summary.Comparable,
			opts.MinComparablePVCs,
		)
	}
	if summary.Matched < opts.MinMatchingPVCs {
		return summary, fmt.Errorf(
			"growth cross-check matched %d/%d PVCs (required matches: %d)",
			summary.Matched,
			summary.Comparable,
			opts.MinMatchingPVCs,
		)
	}
	return summary, nil
}
