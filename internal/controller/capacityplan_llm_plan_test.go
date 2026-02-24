package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/llm"
)

type insightOnlyGenerator struct{}

func (insightOnlyGenerator) GenerateInsight(_ context.Context, _ llm.PVCContext) (string, error) {
	return "insight", nil
}

func TestComputeBudgetForecastHashStableOrdering(t *testing.T) {
	t.Parallel()

	day := 2.0
	a := []capacityv1.StorageBudgetForecast{
		{
			Scope:             "namespace",
			Namespace:         "default",
			BudgetBytes:       1600 * 1024 * 1024,
			UsedBytes:         1200 * 1024 * 1024,
			UsageRatio:        0.75,
			GrowthBytesPerDay: 12,
			DaysUntilBreach:   &day,
		},
		{
			Scope:             "namespace",
			Namespace:         "kube-system",
			BudgetBytes:       800 * 1024 * 1024,
			UsedBytes:         400 * 1024 * 1024,
			UsageRatio:        0.50,
			GrowthBytesPerDay: 8,
			DaysUntilBreach:   &day,
		},
	}
	b := []capacityv1.StorageBudgetForecast{
		{
			Scope:             "workload",
			Namespace:         "default",
			Kind:              "Pod",
			Name:              "pod-a",
			BudgetBytes:       160 * 1024 * 1024,
			UsedBytes:         120 * 1024 * 1024,
			UsageRatio:        0.75,
			GrowthBytesPerDay: 6,
			DaysUntilBreach:   &day,
		},
	}
	reversed := []capacityv1.StorageBudgetForecast{a[1], a[0]}
	hashAB := computeBudgetForecastHash(a, b)
	hashBA := computeBudgetForecastHash(reversed, append([]capacityv1.StorageBudgetForecast(nil), b...))
	if hashAB == "" {
		t.Fatalf("expected non-empty hash")
	}
	if hashAB != hashBA {
		t.Fatalf("expected stable hash, got %q vs %q", hashAB, hashBA)
	}
}

func TestRefreshPlanRiskChangeInsightFallbackWithoutPromptSupport(t *testing.T) {
	t.Parallel()

	reconciler := &CapacityPlanReconciler{}
	text, last := reconciler.refreshPlanRiskChangeInsight(
		context.Background(),
		"ci-plan",
		llmClientState{
			client:    insightOnlyGenerator{},
			provider:  "test",
			model:     "test",
			interval:  time.Minute,
			now:       time.Now(),
			prevHash:  "a",
			currHash:  "b",
			prevText:  "",
			prevTime:  nil,
			isEnabled: true,
		},
		"Risk changes: new=1 escalated=0 recovered=0",
		[]capacityv1.PVCRiskChange{
			{
				Type:                           "new",
				Namespace:                      "default",
				Name:                           "pvc-a",
				CurrentWeeklyGrowthBytesPerDay: 10,
				Time:                           metav1.Now(),
			},
		},
		nil,
	)
	if strings.TrimSpace(text) == "" {
		t.Fatalf("expected fallback text from deterministic summary")
	}
	if last != nil {
		t.Fatalf("expected no LLM timestamp update when prompt execution is unsupported")
	}
}

func TestRefreshPlanBudgetRecommendationsFallbackWithoutPromptSupport(t *testing.T) {
	t.Parallel()

	reconciler := &CapacityPlanReconciler{}
	text, last := reconciler.refreshPlanBudgetRecommendations(
		context.Background(),
		"ci-plan",
		llmClientState{
			client:    insightOnlyGenerator{},
			provider:  "test",
			model:     "test",
			interval:  time.Minute,
			now:       time.Now(),
			prevHash:  "a",
			currHash:  "b",
			prevText:  "",
			prevTime:  nil,
			isEnabled: true,
		},
		[]capacityv1.StorageBudgetForecast{
			{
				Scope:             "namespace",
				Namespace:         "default",
				BudgetBytes:       1600 * 1024 * 1024,
				UsedBytes:         1400 * 1024 * 1024,
				UsageRatio:        0.875,
				GrowthBytesPerDay: 10,
			},
		},
		[]capacityv1.StorageBudgetForecast{
			{
				Scope:             "workload",
				Namespace:         "default",
				Kind:              "Pod",
				Name:              "cpo-ci-steady",
				BudgetBytes:       160 * 1024 * 1024,
				UsedBytes:         150 * 1024 * 1024,
				UsageRatio:        0.9375,
				GrowthBytesPerDay: 6,
			},
		},
		nil,
	)
	if !strings.Contains(text, "Review workload") {
		t.Fatalf("expected fallback recommendations, got %q", text)
	}
	if last != nil {
		t.Fatalf("expected no LLM timestamp update when prompt execution is unsupported")
	}
}
