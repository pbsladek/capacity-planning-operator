package llm

import (
	"context"
	"errors"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AnthropicInsightGenerator struct {
	client      anthropic.Client
	model       anthropic.Model
	maxTokens   int
	temperature *float64
}

func NewAnthropicInsightGenerator(cfg ProviderConfig) (InsightGenerator, error) {
	if strings.TrimSpace(cfg.Anthropic.APIKey) == "" {
		return nil, errors.New("anthropic api key is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "claude-3-5-haiku-latest"
	}

	opts := []option.RequestOption{
		option.WithAPIKey(cfg.Anthropic.APIKey),
		option.WithRequestTimeout(cfg.Timeout),
	}
	if strings.TrimSpace(cfg.Anthropic.BaseURL) != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimSpace(cfg.Anthropic.BaseURL)))
	}

	return &AnthropicInsightGenerator{
		client:      anthropic.NewClient(opts...),
		model:       anthropic.Model(model),
		maxTokens:   cfg.MaxTokens,
		temperature: cfg.Temperature,
	}, nil
}

func (g *AnthropicInsightGenerator) GenerateInsight(ctx context.Context, pvc PVCContext) (string, error) {
	req := anthropic.MessageNewParams{
		Model:     g.model,
		MaxTokens: int64(g.maxTokens),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(BuildPrompt(pvc))),
		},
	}
	if g.temperature != nil {
		req.Temperature = anthropic.Float(*g.temperature)
	}

	resp, err := g.client.Messages.New(ctx, req)
	if err != nil {
		return "", err
	}

	parts := make([]string, 0, len(resp.Content))
	for _, block := range resp.Content {
		if strings.TrimSpace(block.Type) == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return "", errors.New("anthropic returned empty insight")
	}
	return text, nil
}
