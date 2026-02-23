/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package llm

import (
	"context"
	"fmt"
)

// StubInsightGenerator returns deterministic placeholder text without making
// any network calls. Used in production until a real LLM backend is configured,
// and in tests that don't need to verify LLM behavior specifically.
type StubInsightGenerator struct{}

// GenerateInsight returns a formatted placeholder insight string.
func (s *StubInsightGenerator) GenerateInsight(_ context.Context, pvc PVCContext) (string, error) {
	usagePercent := 0.0
	if pvc.CapacityBytes > 0 {
		usagePercent = float64(pvc.UsedBytes) / float64(pvc.CapacityBytes) * 100.0
	}

	daysStr := "N/A"
	if pvc.Growth.DaysUntilFull != nil {
		daysStr = fmt.Sprintf("%.1f days", *pvc.Growth.DaysUntilFull)
	}

	alertStr := ""
	if pvc.AlertFiring {
		alertStr = " [ALERT FIRING]"
	}

	return fmt.Sprintf(
		"PVC %s/%s: %.1f%% used (%.0f bytes / %.0f bytes capacity). "+
			"Growth rate: %.0f bytes/day (R²=%.2f). "+
			"Predicted time to full: %s.%s "+
			"[stub insight — configure LLM endpoint for AI-generated analysis]",
		pvc.Namespace, pvc.Name,
		usagePercent,
		float64(pvc.UsedBytes), float64(pvc.CapacityBytes),
		pvc.Growth.GrowthBytesPerDay, pvc.Growth.ConfidenceR2,
		daysStr,
		alertStr,
	), nil
}
