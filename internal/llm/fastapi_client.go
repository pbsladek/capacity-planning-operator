package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type FastAPIInsightGenerator struct {
	url       string
	authToken string
	client    *http.Client
}

type fastAPIInsightRequest struct {
	Namespace         string   `json:"namespace"`
	Name              string   `json:"name"`
	UsedBytes         int64    `json:"usedBytes"`
	CapacityBytes     int64    `json:"capacityBytes"`
	GrowthBytesPerDay float64  `json:"growthBytesPerDay"`
	ConfidenceR2      float64  `json:"confidenceR2"`
	AlertFiring       bool     `json:"alertFiring"`
	SamplesCount      int      `json:"samplesCount"`
	DaysUntilFull     *float64 `json:"daysUntilFull,omitempty"`
}

type fastAPIInsightResponse struct {
	Insight string `json:"insight"`
	Text    string `json:"text"`
	Output  string `json:"output"`
}

func NewFastAPIInsightGenerator(cfg ProviderConfig) (InsightGenerator, error) {
	url := strings.TrimSpace(cfg.FastAPI.URL)
	if url == "" {
		return nil, errors.New("fastapi url is required")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.FastAPI.TLSSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &FastAPIInsightGenerator{
		url:       url,
		authToken: strings.TrimSpace(cfg.FastAPI.AuthToken),
		client: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
	}, nil
}

func (g *FastAPIInsightGenerator) GenerateInsight(ctx context.Context, pvc PVCContext) (string, error) {
	payload, err := json.Marshal(fastAPIInsightRequest{
		Namespace:         pvc.Namespace,
		Name:              pvc.Name,
		UsedBytes:         pvc.UsedBytes,
		CapacityBytes:     pvc.CapacityBytes,
		GrowthBytesPerDay: pvc.Growth.GrowthBytesPerDay,
		ConfidenceR2:      pvc.Growth.ConfidenceR2,
		DaysUntilFull:     pvc.Growth.DaysUntilFull,
		AlertFiring:       pvc.AlertFiring,
		SamplesCount:      len(pvc.Samples),
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.authToken)
	}

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
		return "", fmt.Errorf("fastapi returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out fastAPIInsightResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}

	text := strings.TrimSpace(out.Insight)
	if text == "" {
		text = strings.TrimSpace(out.Text)
	}
	if text == "" {
		text = strings.TrimSpace(out.Output)
	}
	if text == "" {
		return "", errors.New("fastapi returned empty insight")
	}
	return text, nil
}
