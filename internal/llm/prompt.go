package llm

import (
	"fmt"
	"strings"
)

// BuildPrompt creates a provider-agnostic prompt for a single PVC summary.
func BuildPrompt(pvc PVCContext) string {
	usagePct := 0.0
	if pvc.CapacityBytes > 0 {
		usagePct = (float64(pvc.UsedBytes) / float64(pvc.CapacityBytes)) * 100.0
	}

	days := "unknown"
	if pvc.Growth.DaysUntilFull != nil {
		days = fmt.Sprintf("%.2f", *pvc.Growth.DaysUntilFull)
	}

	return strings.TrimSpace(fmt.Sprintf(`
You are a Kubernetes storage capacity planning assistant.
Generate a concise operational insight for this PVC in plain English.

PVC:
- Namespace: %s
- Name: %s
- UsedBytes: %d
- CapacityBytes: %d
- UsagePercent: %.2f
- GrowthBytesPerDay: %.2f
- ConfidenceR2: %.3f
- DaysUntilFull: %s
- AlertFiring: %t
- SamplesCount: %d

Output requirements:
- 2-4 short sentences.
- Include risk level (low/medium/high).
- Include one concrete next action.
- No markdown, no bullets.
`, pvc.Namespace, pvc.Name, pvc.UsedBytes, pvc.CapacityBytes, usagePct,
		pvc.Growth.GrowthBytesPerDay, pvc.Growth.ConfidenceR2, days, pvc.AlertFiring, len(pvc.Samples)))
}
