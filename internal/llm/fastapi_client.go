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
	"net/url"
	"strings"
	"sync"
	"time"
)

type FastAPIInsightGenerator struct {
	url              string
	healthURL        string
	authToken        string
	client           *http.Client
	failureThreshold int
	cooldown         time.Duration

	mu               sync.Mutex
	consecutiveFails int
	degradedUntil    time.Time
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

var errFastAPIDegraded = errors.New("fastapi is in degraded mode")

func NewFastAPIInsightGenerator(cfg ProviderConfig) (InsightGenerator, error) {
	url := strings.TrimSpace(cfg.FastAPI.URL)
	if url == "" {
		return nil, errors.New("fastapi url is required")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.FastAPI.TLSSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	failureThreshold := cfg.FastAPI.FailureThreshold
	if failureThreshold <= 0 {
		failureThreshold = defaultFailures
	}
	cooldown := cfg.FastAPI.Cooldown
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	healthURL := strings.TrimSpace(cfg.FastAPI.HealthURL)
	if healthURL == "" {
		healthURL = deriveFastAPIHealthURL(url)
	}

	return &FastAPIInsightGenerator{
		url:              url,
		healthURL:        healthURL,
		authToken:        strings.TrimSpace(cfg.FastAPI.AuthToken),
		failureThreshold: failureThreshold,
		cooldown:         cooldown,
		client: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
	}, nil
}

func (g *FastAPIInsightGenerator) GenerateInsight(ctx context.Context, pvc PVCContext) (string, error) {
	if ok := g.checkAvailability(ctx); !ok {
		return "", errFastAPIDegraded
	}

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
		g.recordFailure(time.Now())
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		g.recordFailure(time.Now())
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		g.recordFailure(time.Now())
		return "", fmt.Errorf("fastapi returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out fastAPIInsightResponse
	if err := json.Unmarshal(body, &out); err != nil {
		g.recordFailure(time.Now())
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
		g.recordFailure(time.Now())
		return "", errors.New("fastapi returned empty insight")
	}
	g.clearFailures()
	return text, nil
}

func (g *FastAPIInsightGenerator) checkAvailability(ctx context.Context) bool {
	now := time.Now()

	g.mu.Lock()
	failures := g.consecutiveFails
	cooldownUntil := g.degradedUntil
	g.mu.Unlock()

	if failures < g.failureThreshold {
		return true
	}
	if now.Before(cooldownUntil) {
		return false
	}

	healthy := g.probeHealth(ctx)
	if healthy {
		g.clearFailures()
		return true
	}
	g.recordFailure(now)
	return false
}

func (g *FastAPIInsightGenerator) probeHealth(ctx context.Context) bool {
	if strings.TrimSpace(g.healthURL) == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.healthURL, nil)
	if err != nil {
		return false
	}
	if g.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.authToken)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (g *FastAPIInsightGenerator) recordFailure(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.consecutiveFails++
	if g.consecutiveFails >= g.failureThreshold {
		g.degradedUntil = now.Add(g.cooldown)
	}
}

func (g *FastAPIInsightGenerator) clearFailures() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.consecutiveFails = 0
	g.degradedUntil = time.Time{}
}

func deriveFastAPIHealthURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Path = "/healthz"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
