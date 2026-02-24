package llm

import (
	"strings"
	"testing"
)

func TestBuildRiskChangePromptParts(t *testing.T) {
	t.Parallel()

	parts := BuildRiskChangePromptParts(PlanRiskChangeContext{
		PlanName:          "ci-plan",
		RiskChangeSummary: "Risk changes: new=1 escalated=1 recovered=0",
		Changes: []PlanRiskChange{
			{
				Type:                    "escalated",
				Namespace:               "default",
				Name:                    "data-pvc",
				PreviousWeeklyGiBPerDay: 1.2,
				CurrentWeeklyGiBPerDay:  2.4,
				PreviousProjectedFullAt: "2026-03-01T00:00:00Z",
				CurrentProjectedFullAt:  "2026-02-27T00:00:00Z",
				Message:                 "Projected full date moved earlier",
			},
		},
		TopRisks: []PlanTopRisk{
			{
				Namespace:       "default",
				Name:            "data-pvc",
				WeeklyGiBPerDay: 2.4,
				UsageRatio:      0.82,
				DaysUntilFull:   "3.50",
				AlertFiring:     true,
			},
		},
	})

	if parts.Version != riskChangePromptTemplateVersion {
		t.Fatalf("version=%q", parts.Version)
	}
	if !strings.Contains(parts.System, "Most likely driver:") {
		t.Fatalf("system prompt missing contract hint: %s", parts.System)
	}
	if !strings.Contains(parts.User, "type=escalated pvc=default/data-pvc") {
		t.Fatalf("user prompt missing risk change row: %s", parts.User)
	}
	if !strings.Contains(parts.User, "CurrentTopRisks:") {
		t.Fatalf("user prompt missing top risks section: %s", parts.User)
	}
}

func TestBuildBudgetRecommendationPromptParts(t *testing.T) {
	t.Parallel()

	parts := BuildBudgetRecommendationPromptParts(PlanBudgetRecommendationsContext{
		PlanName: "ci-plan",
		NamespaceForecasts: []PlanBudgetForecast{
			{
				Namespace:       "default",
				BudgetMiB:       1600,
				UsedMiB:         1400,
				UsageRatio:      0.875,
				GrowthMiBPerMin: 22.5,
				DaysUntilBreach: "2.10",
			},
		},
		WorkloadForecasts: []PlanBudgetForecast{
			{
				Namespace:       "default",
				Kind:            "Pod",
				Name:            "cpo-ci-churn",
				BudgetMiB:       160,
				UsedMiB:         150,
				UsageRatio:      0.937,
				GrowthMiBPerMin: 12.2,
				DaysUntilBreach: "0.80",
			},
		},
		TopRisks: []PlanTopRisk{
			{
				Namespace:       "default",
				Name:            "cpo-ci-churn-pvc",
				WeeklyGiBPerDay: 3.8,
				UsageRatio:      0.93,
				DaysUntilFull:   "0.75",
			},
		},
	})

	if parts.Version != budgetRecommendationPromptTemplateVersion {
		t.Fatalf("version=%q", parts.Version)
	}
	if !strings.Contains(parts.System, "Exactly 3 numbered lines") {
		t.Fatalf("system prompt missing numbering contract: %s", parts.System)
	}
	if !strings.Contains(parts.User, "workload=default/Pod/cpo-ci-churn") {
		t.Fatalf("user prompt missing workload forecast row: %s", parts.User)
	}
	if !strings.Contains(parts.User, "TopRisks:") {
		t.Fatalf("user prompt missing top risks section: %s", parts.User)
	}
}
