package llm

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
)

const insightPromptTemplateVersion = "insight-v1"

const insightSystemPromptTemplate = `
You are a Kubernetes storage capacity planning assistant for SRE teams.
PromptVersion: {{.PromptVersion}}

Goal:
Write one short operational insight for this PVC. Use only the PVC data provided in the user message.

Output contract (strict):
- Return plain text only (no markdown, no bullets, no JSON).
- 2-4 sentences total.
- Include "Risk: <low|medium|high>" exactly once.
- Include one concrete next action with a timeframe.
- If ConfidenceR2 < 0.50, explicitly mention low confidence.
- If GrowthBytesPerDay <= 0, do not recommend expansion; recommend monitoring/investigation.
`

const insightUserPromptTemplate = `
PVC Data:
Namespace={{.Namespace}}
Name={{.Name}}
UsedBytes={{.UsedBytes}}
CapacityBytes={{.CapacityBytes}}
UsagePercent={{printf "%.2f" .UsagePercent}}
UsageRatio={{printf "%.4f" .UsageRatio}}
GrowthBytesPerDay={{printf "%.2f" .GrowthBytesPerDay}}
GrowthMiBPerMin={{printf "%.2f" .GrowthMiBPerMin}}
ConfidenceR2={{printf "%.3f" .ConfidenceR2}}
DaysUntilFull={{.DaysUntilFull}}
AlertFiring={{.AlertFiring}}
SamplesCount={{.SamplesCount}}
SampleWindowMinutes={{.SampleWindowMinutes}}
SampleDeltaBytes={{.SampleDeltaBytes}}
SampleDeltaMiB={{printf "%.2f" .SampleDeltaMiB}}

Now produce the insight.
`

var compiledInsightSystemPromptTemplate = template.Must(template.New("insightSystemPrompt").Parse(strings.TrimSpace(insightSystemPromptTemplate)))
var compiledInsightUserPromptTemplate = template.Must(template.New("insightUserPrompt").Parse(strings.TrimSpace(insightUserPromptTemplate)))

// PromptParts is a provider-agnostic split prompt with explicit system and user components.
type PromptParts struct {
	Version string
	System  string
	User    string
}

type insightPromptData struct {
	PromptVersion       string
	Namespace           string
	Name                string
	UsedBytes           int64
	CapacityBytes       int64
	UsagePercent        float64
	UsageRatio          float64
	GrowthBytesPerDay   float64
	GrowthMiBPerMin     float64
	ConfidenceR2        float64
	DaysUntilFull       string
	AlertFiring         bool
	SamplesCount        int
	SampleWindowMinutes int64
	SampleDeltaBytes    int64
	SampleDeltaMiB      float64
}

func buildInsightPromptData(pvc PVCContext) insightPromptData {
	usagePct := 0.0
	usageRatio := 0.0
	if pvc.CapacityBytes > 0 {
		usageRatio = float64(pvc.UsedBytes) / float64(pvc.CapacityBytes)
		usagePct = usageRatio * 100.0
	}

	days := "unknown"
	if pvc.Growth.DaysUntilFull != nil {
		days = fmt.Sprintf("%.2f", *pvc.Growth.DaysUntilFull)
	}

	windowMinutes, deltaBytes, deltaMiB := sampleWindowDetails(pvc.Samples)
	return insightPromptData{
		PromptVersion:       insightPromptTemplateVersion,
		Namespace:           pvc.Namespace,
		Name:                pvc.Name,
		UsedBytes:           pvc.UsedBytes,
		CapacityBytes:       pvc.CapacityBytes,
		UsagePercent:        usagePct,
		UsageRatio:          usageRatio,
		GrowthBytesPerDay:   pvc.Growth.GrowthBytesPerDay,
		GrowthMiBPerMin:     pvc.Growth.GrowthBytesPerDay / 1440.0 / (1024.0 * 1024.0),
		ConfidenceR2:        pvc.Growth.ConfidenceR2,
		DaysUntilFull:       days,
		AlertFiring:         pvc.AlertFiring,
		SamplesCount:        len(pvc.Samples),
		SampleWindowMinutes: windowMinutes,
		SampleDeltaBytes:    deltaBytes,
		SampleDeltaMiB:      deltaMiB,
	}
}

func sampleWindowDetails(samples []analysis.Sample) (windowMinutes int64, deltaBytes int64, deltaMiB float64) {
	if len(samples) < 2 {
		return 0, 0, 0
	}
	first := samples[0]
	last := samples[len(samples)-1]
	windowMinutes = int64(last.Timestamp.Sub(first.Timestamp).Minutes())
	deltaBytes = last.UsedBytes - first.UsedBytes
	deltaMiB = float64(deltaBytes) / (1024.0 * 1024.0)
	return windowMinutes, deltaBytes, deltaMiB
}

func executePromptTemplate(tpl *template.Template, data insightPromptData) (string, error) {
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// BuildPromptParts creates provider-agnostic system and user prompt components.
func BuildPromptParts(pvc PVCContext) PromptParts {
	data := buildInsightPromptData(pvc)
	system, systemErr := executePromptTemplate(compiledInsightSystemPromptTemplate, data)
	user, userErr := executePromptTemplate(compiledInsightUserPromptTemplate, data)
	if systemErr == nil && userErr == nil {
		return PromptParts{
			Version: data.PromptVersion,
			System:  system,
			User:    user,
		}
	}

	// Fallback to deterministic minimal prompts when template execution fails.
	fallback := strings.TrimSpace(fmt.Sprintf(
		"PVC %s/%s UsedBytes=%d CapacityBytes=%d GrowthBytesPerDay=%.2f ConfidenceR2=%.3f DaysUntilFull=%s AlertFiring=%t",
		pvc.Namespace, pvc.Name, pvc.UsedBytes, pvc.CapacityBytes, pvc.Growth.GrowthBytesPerDay, pvc.Growth.ConfidenceR2, data.DaysUntilFull, pvc.AlertFiring,
	))
	return PromptParts{
		Version: data.PromptVersion,
		System:  "You are a Kubernetes storage capacity planning assistant.",
		User:    fallback,
	}
}

// BuildPrompt creates a single combined prompt for providers that accept one prompt string.
func BuildPrompt(pvc PVCContext) string {
	parts := BuildPromptParts(pvc)
	return strings.TrimSpace(fmt.Sprintf("System:\n%s\n\nUser:\n%s", parts.System, parts.User))
}
