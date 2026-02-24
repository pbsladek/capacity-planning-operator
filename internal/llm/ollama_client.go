package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type OllamaInsightGenerator struct {
	endpoint    string
	model       string
	maxTokens   int
	temperature *float64
	client      *http.Client
}

type ollamaGenerateOptions struct {
	NumPredict  int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type ollamaGenerateRequest struct {
	Model   string                `json:"model"`
	Prompt  string                `json:"prompt"`
	Stream  bool                  `json:"stream"`
	Options ollamaGenerateOptions `json:"options,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error"`
}

func NewOllamaInsightGenerator(cfg ProviderConfig) (InsightGenerator, error) {
	baseURL := strings.TrimSpace(cfg.Ollama.URL)
	if baseURL == "" {
		return nil, errors.New("ollama url is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, errors.New("ollama model is required")
	}

	endpoint, err := resolveOllamaGenerateURL(baseURL)
	if err != nil {
		return nil, err
	}

	return &OllamaInsightGenerator{
		endpoint:    endpoint,
		model:       model,
		maxTokens:   cfg.MaxTokens,
		temperature: cfg.Temperature,
		client:      &http.Client{Timeout: cfg.Timeout},
	}, nil
}

func resolveOllamaGenerateURL(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid ollama url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("ollama url must include scheme and host")
	}

	path := strings.TrimSpace(u.Path)
	switch {
	case path == "", path == "/":
		u.Path = "/api/generate"
	case strings.HasSuffix(path, "/api/generate"):
		// Use as provided.
	default:
		u.Path = strings.TrimRight(path, "/") + "/api/generate"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (g *OllamaInsightGenerator) GenerateInsight(ctx context.Context, pvc PVCContext) (string, error) {
	return g.GenerateFromPrompt(ctx, BuildPromptParts(pvc))
}

func (g *OllamaInsightGenerator) GenerateFromPrompt(ctx context.Context, parts PromptParts) (string, error) {
	reqBody := ollamaGenerateRequest{
		Model:  g.model,
		Prompt: strings.TrimSpace(fmt.Sprintf("System:\n%s\n\nUser:\n%s", parts.System, parts.User)),
		Stream: false,
		Options: ollamaGenerateOptions{
			NumPredict:  g.maxTokens,
			Temperature: g.temperature,
		},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out ollamaGenerateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Error) != "" {
		return "", errors.New(strings.TrimSpace(out.Error))
	}
	text := strings.TrimSpace(out.Response)
	if text == "" {
		return "", errors.New("ollama returned empty insight")
	}
	return text, nil
}
