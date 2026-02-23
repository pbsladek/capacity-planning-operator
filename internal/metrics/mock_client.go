/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package metrics

import (
	"context"
	"fmt"
	"time"
)

// MockPVCMetricsClient is a test double that returns pre-programmed values.
// It is safe for concurrent use.
type MockPVCMetricsClient struct {
	// Data maps "namespace/name" to PVCUsage. Looked up on GetUsage calls.
	Data map[string]PVCUsage

	// RangeData maps "namespace/name" to a slice of RangePoints for GetUsageRange.
	RangeData map[string][]RangePoint

	// Err, if non-nil, is returned by every call to GetUsage.
	Err error
}

// NewMockPVCMetricsClient returns a ready-to-use mock client.
func NewMockPVCMetricsClient() *MockPVCMetricsClient {
	return &MockPVCMetricsClient{
		Data:      make(map[string]PVCUsage),
		RangeData: make(map[string][]RangePoint),
	}
}

// GetUsage returns the pre-programmed PVCUsage for the given key, or an error
// if Err is set or no data is registered for the key.
func (m *MockPVCMetricsClient) GetUsage(_ context.Context, key PVCKey) (PVCUsage, error) {
	if m.Err != nil {
		return PVCUsage{}, m.Err
	}
	k := key.Namespace + "/" + key.Name
	usage, ok := m.Data[k]
	if !ok {
		return PVCUsage{}, fmt.Errorf("mock: no data for %s", k)
	}
	return usage, nil
}

// GetUsageRange returns the pre-programmed range data for the given key.
// Returns an empty slice if no data is registered.
func (m *MockPVCMetricsClient) GetUsageRange(_ context.Context, key PVCKey, _, _ time.Time, _ time.Duration) ([]RangePoint, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	k := key.Namespace + "/" + key.Name
	return m.RangeData[k], nil
}
