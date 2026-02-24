package alertreceiver

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	// DefaultMaxBodyBytes limits Alertmanager payload size accepted by POST /.
	DefaultMaxBodyBytes int64 = 2 * 1024 * 1024
)

type recordsResponse struct {
	Count   int      `json:"count"`
	Records []string `json:"records"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

// Store persists raw webhook payloads as newline-delimited JSON strings.
// Each line is a JSON-encoded string to preserve multiline payloads.
type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: strings.TrimSpace(path)}
}

func (s *Store) Append(raw string) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		return fmt.Errorf("write payload newline: %w", err)
	}
	return nil
}

func (s *Store) Records() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()

	records := make([]string, 0, 16)
	scanner := bufio.NewScanner(file)
	// Max body is 2 MiB; JSON-escaped line can be larger, keep headroom.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var decoded string
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			// Backward-compatible fallback for legacy plaintext lines.
			records = append(records, line)
			continue
		}
		records = append(records, decoded)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log file: %w", err)
	}
	return records, nil
}

type handler struct {
	store        *Store
	maxBodyBytes int64
}

func NewHandler(store *Store, maxBodyBytes int64) http.Handler {
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	h := &handler{
		store:        store,
		maxBodyBytes: maxBodyBytes,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealth)
	mux.HandleFunc("/records", h.handleRecords)
	mux.HandleFunc("/", h.handleRoot)
	return mux
}

func (h *handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func (h *handler) handleRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	records, err := h.store.Records()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read records"})
		return
	}
	writeJSON(w, http.StatusOK, recordsResponse{
		Count:   len(records),
		Records: records,
	})
}

func (h *handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	raw, err := h.readBody(r)
	if err != nil {
		switch {
		case errors.Is(err, errInvalidContentLength):
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid content-length"})
		case errors.Is(err, errPayloadTooLarge):
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "payload too large"})
		default:
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		}
		return
	}
	if err := h.store.Append(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist payload"})
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

var (
	errInvalidContentLength = errors.New("invalid content-length")
	errPayloadTooLarge      = errors.New("payload too large")
)

func (h *handler) readBody(r *http.Request) (string, error) {
	if raw := strings.TrimSpace(r.Header.Get("Content-Length")); raw != "" {
		length, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || length < 0 {
			return "", errInvalidContentLength
		}
		if length > h.maxBodyBytes {
			return "", errPayloadTooLarge
		}
	}
	defer r.Body.Close()

	limited := io.LimitReader(r.Body, h.maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > h.maxBodyBytes {
		return "", errPayloadTooLarge
	}
	return string(body), nil
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
		statusCode = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}
