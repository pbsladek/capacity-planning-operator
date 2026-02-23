/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

// Package llm provides the interface and implementations for generating
// natural-language capacity planning insights using a language model.
package llm

import (
	"context"

	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
)

// PVCContext is the input to the LLM insight generator for a single PVC.
type PVCContext struct {
	// Namespace of the PVC.
	Namespace string
	// Name of the PVC.
	Name string
	// Growth holds the OLS regression result for this PVC.
	Growth analysis.GrowthResult
	// UsedBytes is the most recently observed used storage in bytes.
	UsedBytes int64
	// CapacityBytes is the total PVC capacity in bytes.
	CapacityBytes int64
	// AlertFiring is true when the PVC has crossed a configured threshold.
	AlertFiring bool
	// Samples are the recent data points available for trend description.
	Samples []analysis.Sample
}

// InsightGenerator generates natural-language insights about PVC usage trends.
// Implementations must be safe for concurrent use.
//
// Rate limiting is the caller's responsibility: the CapacityPlanReconciler
// checks PVCSummary.LastLLMTime against spec.LLMInsightsInterval before
// calling GenerateInsight, keeping this interface simple and focused.
type InsightGenerator interface {
	// GenerateInsight returns a human-readable analysis of the PVC's growth trend.
	// On error, callers should preserve the previous insight and log a warning
	// rather than failing the entire reconcile.
	GenerateInsight(ctx context.Context, pvc PVCContext) (string, error)
}
