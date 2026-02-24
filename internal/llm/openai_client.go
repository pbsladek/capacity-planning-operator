package llm

import (
	"context"
	"errors"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type OpenAIInsightGenerator struct {
	client      openai.Client
	model       string
	maxTokens   int
	temperature *float64
}

func NewOpenAIInsightGenerator(cfg ProviderConfig) (InsightGenerator, error) {
	if strings.TrimSpace(cfg.OpenAI.APIKey) == "" {
		return nil, errors.New("openai api key is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "gpt-4.1-mini"
	}

	opts := []option.RequestOption{
		option.WithAPIKey(cfg.OpenAI.APIKey),
		option.WithRequestTimeout(cfg.Timeout),
	}
	if strings.TrimSpace(cfg.OpenAI.BaseURL) != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimSpace(cfg.OpenAI.BaseURL)))
	}

	return &OpenAIInsightGenerator{
		client:      openai.NewClient(opts...),
		model:       model,
		maxTokens:   cfg.MaxTokens,
		temperature: cfg.Temperature,
	}, nil
}

func (g *OpenAIInsightGenerator) GenerateInsight(ctx context.Context, pvc PVCContext) (string, error) {
	return g.GenerateFromPrompt(ctx, BuildPromptParts(pvc))
}

func (g *OpenAIInsightGenerator) GenerateFromPrompt(ctx context.Context, parts PromptParts) (string, error) {
	params := responses.ResponseNewParams{
		Model:        shared.ResponsesModel(g.model),
		Instructions: openai.String(parts.System),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(parts.User),
		},
		MaxOutputTokens: openai.Int(int64(g.maxTokens)),
	}
	if g.temperature != nil {
		params.Temperature = openai.Float(*g.temperature)
	}

	resp, err := g.client.Responses.New(ctx, params)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(resp.OutputText())
	if text == "" {
		return "", errors.New("openai returned empty insight")
	}
	return text, nil
}
