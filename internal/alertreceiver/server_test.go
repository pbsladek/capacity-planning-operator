package alertreceiver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostAndReadRecordsRoundTrip(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "alerts.log"))
	handler := NewHandler(store, 1024*1024)

	raw := "{\n  \"receiver\":\"ci-webhook\",\n  \"message\":\"hello\"\n}"
	postReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(raw))
	postReq.Header.Set("Content-Type", "application/json")
	postResp := httptest.NewRecorder()
	handler.ServeHTTP(postResp, postReq)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST / status=%d", postResp.Code)
	}

	recordsReq := httptest.NewRequest(http.MethodGet, "/records", nil)
	recordsResp := httptest.NewRecorder()
	handler.ServeHTTP(recordsResp, recordsReq)
	if recordsResp.Code != http.StatusOK {
		t.Fatalf("GET /records status=%d", recordsResp.Code)
	}

	var payload recordsResponse
	if err := json.NewDecoder(recordsResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /records payload: %v", err)
	}
	if payload.Count != 1 {
		t.Fatalf("payload.Count=%d", payload.Count)
	}
	if len(payload.Records) != 1 {
		t.Fatalf("len(payload.Records)=%d", len(payload.Records))
	}
	if payload.Records[0] != raw {
		t.Fatalf("payload.Records[0]=%q want=%q", payload.Records[0], raw)
	}
}

func TestPayloadTooLarge(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "alerts.log"))
	handler := NewHandler(store, 10)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 11)))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestInvalidContentLength(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "alerts.log"))
	handler := NewHandler(store, 1024)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("abc"))
	req.Header.Set("Content-Length", "invalid")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestNotFoundAndMethodHandling(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "alerts.log"))
	handler := NewHandler(store, 1024)

	rr1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusNotFound {
		t.Fatalf("GET /unknown status=%d", rr1.Code)
	}

	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET / status=%d", rr2.Code)
	}
}
