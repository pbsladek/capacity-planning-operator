package llm

import "testing"

func TestNewInsightGenerator_Disabled(t *testing.T) {
	gen, err := NewInsightGenerator(ProviderConfig{Provider: ProviderDisabled})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if gen != nil {
		t.Fatalf("expected nil generator when disabled")
	}
}

func TestNewInsightGenerator_Unsupported(t *testing.T) {
	gen, err := NewInsightGenerator(ProviderConfig{Provider: "unknown"})
	if err == nil {
		t.Fatalf("expected error for unsupported provider")
	}
	if gen != nil {
		t.Fatalf("expected nil generator on error")
	}
}

func TestNewInsightGenerator_OpenAIMissingKey(t *testing.T) {
	gen, err := NewInsightGenerator(ProviderConfig{
		Provider: ProviderOpenAI,
		Model:    "gpt-4.1-mini",
	})
	if err == nil {
		t.Fatalf("expected error for missing OpenAI API key")
	}
	if gen != nil {
		t.Fatalf("expected nil generator on error")
	}
}

func TestNewInsightGenerator_AnthropicMissingKey(t *testing.T) {
	gen, err := NewInsightGenerator(ProviderConfig{
		Provider: ProviderAnthropic,
		Model:    "claude-3-5-haiku-latest",
	})
	if err == nil {
		t.Fatalf("expected error for missing Anthropic API key")
	}
	if gen != nil {
		t.Fatalf("expected nil generator on error")
	}
}

func TestNewInsightGenerator_FastAPIMissingURL(t *testing.T) {
	gen, err := NewInsightGenerator(ProviderConfig{
		Provider: ProviderFastAPI,
	})
	if err == nil {
		t.Fatalf("expected error for missing FastAPI URL")
	}
	if gen != nil {
		t.Fatalf("expected nil generator on error")
	}
}
