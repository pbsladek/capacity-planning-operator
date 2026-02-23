package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
