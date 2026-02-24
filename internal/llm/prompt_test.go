package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
)

func TestBuildPromptIncludesVersionAndContract(t *testing.T) {
	t.Parallel()

	days := 3.5
	now := time.Unix(1700000000, 0)
	prompt := BuildPrompt(PVCContext{
		Namespace:     "default",
		Name:          "data-pvc",
		UsedBytes:     300 * 1024 * 1024,
		CapacityBytes: 500 * 1024 * 1024,
		AlertFiring:   true,
		Growth: analysis.GrowthResult{
			GrowthBytesPerDay: 45_000_000_000,
			ConfidenceR2:      0.82,
			DaysUntilFull:     &days,
		},
		Samples: []analysis.Sample{
			{Timestamp: now.Add(-20 * time.Minute), UsedBytes: 250 * 1024 * 1024},
			{Timestamp: now, UsedBytes: 300 * 1024 * 1024},
		},
	})

	for _, want := range []string{
		"PromptVersion: insight-v1",
		`Include "Risk: <low|medium|high>" exactly once.`,
		"Namespace=default",
		"Name=data-pvc",
		"AlertFiring=true",
		"SamplesCount=2",
		"SampleWindowMinutes=20",
		"SampleDeltaBytes=52428800",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\nprompt:\n%s", want, prompt)
		}
	}
}

func TestBuildPromptUsesUnknownDaysWhenNil(t *testing.T) {
	t.Parallel()

	prompt := BuildPrompt(PVCContext{
		Namespace:     "default",
		Name:          "flat-pvc",
		UsedBytes:     10,
		CapacityBytes: 20,
		Growth: analysis.GrowthResult{
			GrowthBytesPerDay: 0,
			ConfidenceR2:      0.1,
			DaysUntilFull:     nil,
		},
	})

	if !strings.Contains(prompt, "DaysUntilFull=unknown") {
		t.Fatalf("expected unknown days marker, got:\n%s", prompt)
	}
}

func TestBuildPromptPartsSplitsSystemAndUser(t *testing.T) {
	t.Parallel()

	parts := BuildPromptParts(PVCContext{
		Namespace:     "default",
		Name:          "split-pvc",
		UsedBytes:     100,
		CapacityBytes: 200,
		Growth: analysis.GrowthResult{
			GrowthBytesPerDay: 12_345_678,
			ConfidenceR2:      0.66,
		},
	})

	if parts.Version != "insight-v1" {
		t.Fatalf("unexpected version %q", parts.Version)
	}
	if !strings.Contains(parts.System, "Output contract (strict):") {
		t.Fatalf("system prompt missing contract, got:\n%s", parts.System)
	}
	if strings.Contains(parts.User, "Output contract (strict):") {
		t.Fatalf("user prompt should not duplicate system contract")
	}
	if !strings.Contains(parts.User, "PVC Data:") || !strings.Contains(parts.User, "Name=split-pvc") {
		t.Fatalf("user prompt missing pvc data, got:\n%s", parts.User)
	}
}
