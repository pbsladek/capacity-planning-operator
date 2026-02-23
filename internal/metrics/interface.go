/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

// Package metrics provides the interface and implementations for querying
// PVC storage usage from an external source (Prometheus).
package metrics

import (
	"context"
	"time"
)

// PVCKey uniquely identifies a PersistentVolumeClaim.
type PVCKey struct {
	Namespace string
	Name      string
}

// PVCUsage holds the result of a single PVC usage query.
type PVCUsage struct {
	// UsedBytes is the observed used storage in bytes.
	UsedBytes int64
	// CapacityBytes is the total capacity in bytes. May be 0 if the source
	// does not provide capacity (use pvc.spec.resources.requests in that case).
	CapacityBytes int64
}

// RangePoint is a single data point from a Prometheus range query,
// used to backfill the ring buffer on operator startup.
type RangePoint struct {
	Timestamp time.Time
	UsedBytes int64
}

// PVCMetricsClient is the interface for querying PVC storage usage data.
// All implementations must be safe for concurrent use.
type PVCMetricsClient interface {
	// GetUsage returns the current byte usage for a single PVC.
	// Returns an error if the data source is unreachable or the PVC has no data.
	GetUsage(ctx context.Context, key PVCKey) (PVCUsage, error)

	// GetUsageRange returns historical usage data points for a PVC between
	// start and end, sampled at the given step interval.
	// Used to backfill the ring buffer on operator restart.
	// Returns an empty slice (not an error) if no data is found.
	GetUsageRange(ctx context.Context, key PVCKey, start, end time.Time, step time.Duration) ([]RangePoint, error)
}
