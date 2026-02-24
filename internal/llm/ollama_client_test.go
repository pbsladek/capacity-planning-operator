package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOllamaGenerateInsight_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("expected /api/generate, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"response": "PVC growth suggests scaling next week.",
		})
	}))
	defer srv.Close()

	genRaw, err := NewOllamaInsightGenerator(ProviderConfig{
		Timeout:   3 * time.Second,
		MaxTokens: 200,
		Model:     "mistral:7b",
		Ollama: OllamaConfig{
			URL: srv.URL,
		},
	})
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}

	out, err := genRaw.GenerateInsight(context.Background(), PVCContext{Name: "pvc-a"})
	if err != nil {
		t.Fatalf("unexpected GenerateInsight error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty insight")
	}
}

func TestOllamaGenerateInsight_HTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	genRaw, err := NewOllamaInsightGenerator(ProviderConfig{
		Timeout: 2 * time.Second,
		Model:   "missing-model",
		Ollama: OllamaConfig{
			URL: srv.URL,
		},
	})
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	if _, err := genRaw.GenerateInsight(context.Background(), PVCContext{Name: "pvc-a"}); err == nil {
		t.Fatalf("expected non-2xx error")
	}
}

func TestResolveOllamaGenerateURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{
			in:   "http://ollama.llm.svc.cluster.local:11434",
			want: "http://ollama.llm.svc.cluster.local:11434/api/generate",
		},
		{
			in:   "http://example:11434/custom/path",
			want: "http://example:11434/custom/path/api/generate",
		},
		{
			in:   "http://example:11434/api/generate",
			want: "http://example:11434/api/generate",
		},
		{
			in:      "example:11434",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := resolveOllamaGenerateURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveOllamaGenerateURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
