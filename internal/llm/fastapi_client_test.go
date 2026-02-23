package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFastAPIGenerateInsight_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Fatalf("expected bearer token, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"insight": "PVC is trending up; expand in 3 days.",
		})
	}))
	defer srv.Close()

	gen, err := NewFastAPIInsightGenerator(ProviderConfig{
		Timeout: 5 * time.Second,
		FastAPI: FastAPIConfig{
			URL:       srv.URL,
			AuthToken: "token-123",
		},
	})
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}

	out, err := gen.GenerateInsight(context.Background(), PVCContext{
		Namespace:     "default",
		Name:          "data",
		UsedBytes:     100,
		CapacityBytes: 1000,
	})
	if err != nil {
		t.Fatalf("unexpected GenerateInsight error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty insight")
	}
}

func TestFastAPIGenerateInsight_HTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	gen, err := NewFastAPIInsightGenerator(ProviderConfig{
		FastAPI: FastAPIConfig{
			URL: srv.URL,
		},
	})
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}

	if _, err := gen.GenerateInsight(context.Background(), PVCContext{}); err == nil {
		t.Fatalf("expected error for non-2xx status")
	}
}

func TestFastAPIGenerateInsight_DegradedModeAndRecovery(t *testing.T) {
	t.Parallel()

	var healthy atomic.Bool
	var postCalls atomic.Int32
	var healthCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			healthCalls.Add(1)
			if healthy.Load() {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`ok`))
				return
			}
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		case "/v1/insights":
			postCalls.Add(1)
			if !healthy.Load() {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"insight": "recovered"})
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	genRaw, err := NewFastAPIInsightGenerator(ProviderConfig{
		Timeout: 2 * time.Second,
		FastAPI: FastAPIConfig{
			URL:              srv.URL + "/v1/insights",
			HealthURL:        srv.URL + "/healthz",
			FailureThreshold: 1,
			Cooldown:         40 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}

	// First failure should enter degraded mode (threshold=1).
	if _, err := genRaw.GenerateInsight(context.Background(), PVCContext{}); err == nil {
		t.Fatalf("expected initial fastapi error")
	}
	if got := postCalls.Load(); got != 1 {
		t.Fatalf("expected 1 POST call after first attempt, got %d", got)
	}

	// Immediate second call should short-circuit in degraded mode.
	if _, err := genRaw.GenerateInsight(context.Background(), PVCContext{}); err == nil {
		t.Fatalf("expected degraded mode error on immediate retry")
	}
	if got := postCalls.Load(); got != 1 {
		t.Fatalf("expected no extra POST call while degraded, got %d", got)
	}

	// After cooldown, health passes and request should recover.
	time.Sleep(60 * time.Millisecond)
	healthy.Store(true)

	out, err := genRaw.GenerateInsight(context.Background(), PVCContext{})
	if err != nil {
		t.Fatalf("expected recovery success, got error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty recovered insight")
	}
	if postCalls.Load() < 2 {
		t.Fatalf("expected POST to be retried after recovery")
	}
	if healthCalls.Load() < 1 {
		t.Fatalf("expected at least one health probe in degraded mode")
	}
}
