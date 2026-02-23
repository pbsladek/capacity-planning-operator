package cirunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func logStep(msg string) {
	fmt.Printf("\n==> %s\n", msg)
}

func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

func parseDurationOr(raw string, fallbackSeconds int) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err == nil && d > 0 {
		return d
	}
	if fallbackSeconds <= 0 {
		fallbackSeconds = 1
	}
	return time.Duration(fallbackSeconds) * time.Second
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func writeFile(path, content string) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func waitUntil(ctx context.Context, timeout, interval time.Duration, description string, check func(context.Context) (bool, error)) error {
	if timeout <= 0 {
		timeout = 1 * time.Second
	}
	if interval <= 0 {
		interval = 1 * time.Second
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		ok, err := check(deadlineCtx)
		if err != nil {
			return fmt.Errorf("%s: %w", description, err)
		}
		if ok {
			return nil
		}

		select {
		case <-deadlineCtx.Done():
			if errors.Is(deadlineCtx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("timed out waiting for %s after %s", description, timeout)
			}
			return fmt.Errorf("waiting for %s cancelled: %w", description, deadlineCtx.Err())
		case <-tick.C:
		}
	}
}
