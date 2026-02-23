package llm

import (
	"errors"
	"time"
)

const (
	ProviderDisabled  = "disabled"
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderFastAPI   = "fastapi"
)

const (
	defaultTimeout   = 15 * time.Second
	defaultMaxTokens = 256
)

// ProviderConfig contains normalized runtime settings for LLM providers.
type ProviderConfig struct {
	Provider    string
	Model       string
	Timeout     time.Duration
	MaxTokens   int
	Temperature *float64

	OpenAI    OpenAIConfig
	Anthropic AnthropicConfig
	FastAPI   FastAPIConfig
}

// OpenAIConfig holds OpenAI-specific runtime settings.
type OpenAIConfig struct {
	APIKey  string
	BaseURL string
}

// AnthropicConfig holds Anthropic-specific runtime settings.
type AnthropicConfig struct {
	APIKey  string
	BaseURL string
}

// FastAPIConfig holds FastAPI-specific runtime settings.
type FastAPIConfig struct {
	URL           string
	AuthToken     string
	TLSSkipVerify bool
}

func (c *ProviderConfig) normalize() {
	if c.Provider == "" {
		c.Provider = ProviderDisabled
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = defaultMaxTokens
	}
}

// NewInsightGenerator creates an InsightGenerator from the selected provider.
// Returns nil,nil when provider is disabled.
func NewInsightGenerator(cfg ProviderConfig) (InsightGenerator, error) {
	cfg.normalize()

	switch cfg.Provider {
	case ProviderDisabled:
		return nil, nil
	case ProviderOpenAI:
		return NewOpenAIInsightGenerator(cfg)
	case ProviderAnthropic:
		return NewAnthropicInsightGenerator(cfg)
	case ProviderFastAPI:
		return NewFastAPIInsightGenerator(cfg)
	default:
		return nil, errors.New("unsupported llm provider: " + cfg.Provider)
	}
}
