package llm

import (
	"fmt"
	"strings"
	"text/template"
)

const riskChangePromptTemplateVersion = "risk-change-v1"
const budgetRecommendationPromptTemplateVersion = "budget-recommendation-v1"

const riskChangeSystemPromptTemplate = `
You are a Kubernetes storage risk analyst.
PromptVersion: {{.PromptVersion}}

Goal:
Summarize what changed in storage risk between reconciles and what operators should verify next.

Output contract (strict):
- Plain text only (no markdown tables or JSON).
- 3-5 short sentences.
- Include one sentence that starts with "Most likely driver:".
- Include one sentence that starts with "Next check (24h):".
`

const riskChangeUserPromptTemplate = `
Plan={{.PlanName}}
ChangeSummary={{.RiskChangeSummary}}

RiskChanges:
{{- if .Changes}}
{{- range .Changes}}
- type={{.Type}} pvc={{.Namespace}}/{{.Name}} prevWeeklyGiBPerDay={{printf "%.2f" .PreviousWeeklyGiBPerDay}} currWeeklyGiBPerDay={{printf "%.2f" .CurrentWeeklyGiBPerDay}} prevProjectedFullAt={{.PreviousProjectedFullAt}} currProjectedFullAt={{.CurrentProjectedFullAt}} message={{.Message}}
{{- end}}
{{- else}}
- none
{{- end}}

CurrentTopRisks:
{{- if .TopRisks}}
{{- range .TopRisks}}
- pvc={{.Namespace}}/{{.Name}} weeklyGiBPerDay={{printf "%.2f" .WeeklyGiBPerDay}} usageRatio={{printf "%.3f" .UsageRatio}} daysUntilFull={{.DaysUntilFull}} alertFiring={{.AlertFiring}}
{{- end}}
{{- else}}
- none
{{- end}}
`

const budgetRecommendationSystemPromptTemplate = `
You are a Kubernetes storage budget planner.
PromptVersion: {{.PromptVersion}}

Goal:
Provide concrete budget/risk mitigation actions based on namespace and workload forecasts.

Output contract (strict):
- Plain text only.
- Exactly 3 numbered lines: "1) ...", "2) ...", "3) ...".
- Each line must include a target (namespace or workload) and a timeframe.
- Prefer actions that reduce forecasted budget breach risk first.
`

const budgetRecommendationUserPromptTemplate = `
Plan={{.PlanName}}

NamespaceForecasts:
{{- if .NamespaceForecasts}}
{{- range .NamespaceForecasts}}
- namespace={{.Namespace}} budgetMiB={{printf "%.2f" .BudgetMiB}} usedMiB={{printf "%.2f" .UsedMiB}} usageRatio={{printf "%.3f" .UsageRatio}} growthMiBPerMin={{printf "%.2f" .GrowthMiBPerMin}} daysUntilBreach={{.DaysUntilBreach}}
{{- end}}
{{- else}}
- none
{{- end}}

WorkloadForecasts:
{{- if .WorkloadForecasts}}
{{- range .WorkloadForecasts}}
- workload={{.Namespace}}/{{.Kind}}/{{.Name}} budgetMiB={{printf "%.2f" .BudgetMiB}} usedMiB={{printf "%.2f" .UsedMiB}} usageRatio={{printf "%.3f" .UsageRatio}} growthMiBPerMin={{printf "%.2f" .GrowthMiBPerMin}} daysUntilBreach={{.DaysUntilBreach}}
{{- end}}
{{- else}}
- none
{{- end}}

TopRisks:
{{- if .TopRisks}}
{{- range .TopRisks}}
- pvc={{.Namespace}}/{{.Name}} weeklyGiBPerDay={{printf "%.2f" .WeeklyGiBPerDay}} usageRatio={{printf "%.3f" .UsageRatio}} daysUntilFull={{.DaysUntilFull}}
{{- end}}
{{- else}}
- none
{{- end}}
`

var compiledRiskChangeSystemPromptTemplate = template.Must(template.New("riskChangeSystemPrompt").Parse(strings.TrimSpace(riskChangeSystemPromptTemplate)))
var compiledRiskChangeUserPromptTemplate = template.Must(template.New("riskChangeUserPrompt").Parse(strings.TrimSpace(riskChangeUserPromptTemplate)))
var compiledBudgetRecommendationSystemPromptTemplate = template.Must(template.New("budgetRecommendationSystemPrompt").Parse(strings.TrimSpace(budgetRecommendationSystemPromptTemplate)))
var compiledBudgetRecommendationUserPromptTemplate = template.Must(template.New("budgetRecommendationUserPrompt").Parse(strings.TrimSpace(budgetRecommendationUserPromptTemplate)))

type PlanRiskChangeContext struct {
	PlanName          string
	RiskChangeSummary string
	Changes           []PlanRiskChange
	TopRisks          []PlanTopRisk
}

type PlanRiskChange struct {
	Type                    string
	Namespace               string
	Name                    string
	PreviousWeeklyGiBPerDay float64
	CurrentWeeklyGiBPerDay  float64
	PreviousProjectedFullAt string
	CurrentProjectedFullAt  string
	Message                 string
}

type PlanTopRisk struct {
	Namespace       string
	Name            string
	WeeklyGiBPerDay float64
	UsageRatio      float64
	DaysUntilFull   string
	AlertFiring     bool
}

type PlanBudgetRecommendationsContext struct {
	PlanName           string
	NamespaceForecasts []PlanBudgetForecast
	WorkloadForecasts  []PlanBudgetForecast
	TopRisks           []PlanTopRisk
}

type PlanBudgetForecast struct {
	Namespace       string
	Kind            string
	Name            string
	BudgetMiB       float64
	UsedMiB         float64
	UsageRatio      float64
	GrowthMiBPerMin float64
	DaysUntilBreach string
}

type riskChangePromptData struct {
	PromptVersion     string
	PlanName          string
	RiskChangeSummary string
	Changes           []PlanRiskChange
	TopRisks          []PlanTopRisk
}

type budgetRecommendationPromptData struct {
	PromptVersion      string
	PlanName           string
	NamespaceForecasts []PlanBudgetForecast
	WorkloadForecasts  []PlanBudgetForecast
	TopRisks           []PlanTopRisk
}

func buildRiskChangePromptData(ctx PlanRiskChangeContext) riskChangePromptData {
	return riskChangePromptData{
		PromptVersion:     riskChangePromptTemplateVersion,
		PlanName:          strings.TrimSpace(ctx.PlanName),
		RiskChangeSummary: strings.TrimSpace(ctx.RiskChangeSummary),
		Changes:           append([]PlanRiskChange(nil), ctx.Changes...),
		TopRisks:          append([]PlanTopRisk(nil), ctx.TopRisks...),
	}
}

func buildBudgetRecommendationPromptData(ctx PlanBudgetRecommendationsContext) budgetRecommendationPromptData {
	return budgetRecommendationPromptData{
		PromptVersion:      budgetRecommendationPromptTemplateVersion,
		PlanName:           strings.TrimSpace(ctx.PlanName),
		NamespaceForecasts: append([]PlanBudgetForecast(nil), ctx.NamespaceForecasts...),
		WorkloadForecasts:  append([]PlanBudgetForecast(nil), ctx.WorkloadForecasts...),
		TopRisks:           append([]PlanTopRisk(nil), ctx.TopRisks...),
	}
}

func BuildRiskChangePromptParts(ctx PlanRiskChangeContext) PromptParts {
	data := buildRiskChangePromptData(ctx)
	system, systemErr := executeRiskChangeTemplate(compiledRiskChangeSystemPromptTemplate, data)
	user, userErr := executeRiskChangeTemplate(compiledRiskChangeUserPromptTemplate, data)
	if systemErr == nil && userErr == nil {
		return PromptParts{
			Version: data.PromptVersion,
			System:  system,
			User:    user,
		}
	}
	return PromptParts{
		Version: data.PromptVersion,
		System:  "You are a Kubernetes storage risk analyst.",
		User:    fmt.Sprintf("Plan=%s RiskChangeSummary=%s", data.PlanName, data.RiskChangeSummary),
	}
}

func BuildBudgetRecommendationPromptParts(ctx PlanBudgetRecommendationsContext) PromptParts {
	data := buildBudgetRecommendationPromptData(ctx)
	system, systemErr := executeBudgetRecommendationTemplate(compiledBudgetRecommendationSystemPromptTemplate, data)
	user, userErr := executeBudgetRecommendationTemplate(compiledBudgetRecommendationUserPromptTemplate, data)
	if systemErr == nil && userErr == nil {
		return PromptParts{
			Version: data.PromptVersion,
			System:  system,
			User:    user,
		}
	}
	return PromptParts{
		Version: data.PromptVersion,
		System:  "You are a Kubernetes storage budget planner.",
		User:    fmt.Sprintf("Plan=%s Forecasts=%d/%d", data.PlanName, len(data.NamespaceForecasts), len(data.WorkloadForecasts)),
	}
}

func executeRiskChangeTemplate(tpl *template.Template, data riskChangePromptData) (string, error) {
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

func executeBudgetRecommendationTemplate(tpl *template.Template, data budgetRecommendationPromptData) (string, error) {
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}
