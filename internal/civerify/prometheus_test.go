package civerify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueryInstantScalarSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1730000000,"123.5"]}]}}`))
	}))
	t.Cleanup(srv.Close)

	client := NewPrometheusClient(srv.URL, 2*time.Second)
	val, ok, err := client.QueryInstantScalar(context.Background(), "up")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != 123.5 {
		t.Fatalf("expected 123.5, got %v", val)
	}
}

func TestQueryInstantScalarNoData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	t.Cleanup(srv.Close)

	client := NewPrometheusClient(srv.URL, 2*time.Second)
	_, ok, err := client.QueryInstantScalar(context.Background(), "up")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
}

func TestQueryInstantScalarHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	client := NewPrometheusClient(srv.URL, 2*time.Second)
	_, _, err := client.QueryInstantScalar(context.Background(), "up")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
