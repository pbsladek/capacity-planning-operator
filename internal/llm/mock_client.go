/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package llm

import (
	"context"
	"sync"
)

// MockInsightGenerator records calls for test assertions, particularly for
// verifying rate-limiting behavior in CapacityPlanReconciler.
type MockInsightGenerator struct {
	mu sync.Mutex

	// Response controls what the mock returns.
	Response string
	Err      error

	// CallCount is incremented on each GenerateInsight call.
	CallCount int

	// Calls records each PVCContext passed to GenerateInsight, in order.
	Calls []PVCContext
}

// GenerateInsight records the call and returns the configured Response/Err.
func (m *MockInsightGenerator) GenerateInsight(_ context.Context, pvc PVCContext) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CallCount++
	m.Calls = append(m.Calls, pvc)
	if m.Err != nil {
		return "", m.Err
	}
	if m.Response != "" {
		return m.Response, nil
	}
	return "mock insight for " + pvc.Namespace + "/" + pvc.Name, nil
}

// SetErr sets the error that GenerateInsight will return. Safe for concurrent use.
func (m *MockInsightGenerator) SetErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Err = err
}

// GetCallCount returns the number of times GenerateInsight has been called.
// Safe for concurrent use.
func (m *MockInsightGenerator) GetCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.CallCount
}

// Reset clears recorded calls and resets counters. Call between test cases.
func (m *MockInsightGenerator) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CallCount = 0
	m.Calls = nil
	m.Response = ""
	m.Err = nil
}
