package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pbsladek/capacity-planning-operator/internal/alertreceiver"
)

func getenvDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func getenvInt64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func main() {
	addr := getenvDefault("ALERT_RECEIVER_ADDR", ":8080")
	logPath := getenvDefault("ALERT_RECEIVER_LOG_PATH", "/tmp/alerts.log")
	maxBodyBytes := getenvInt64("ALERT_RECEIVER_MAX_BODY_BYTES", alertreceiver.DefaultMaxBodyBytes)

	store := alertreceiver.NewStore(logPath)
	handler := alertreceiver.NewHandler(store, maxBodyBytes)

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "alert-receiver server failed: %v\n", err)
		os.Exit(1)
	}
}
