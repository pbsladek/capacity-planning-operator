package cirunner

import (
	"context"
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
	deadline := time.Now().Add(timeout)

	// Each poll gets its own bounded context instead of sharing one global deadline
	// context. This avoids client-go rate limiter waits failing near timeout.
	perAttemptTimeout := interval
	if perAttemptTimeout < 5*time.Second {
		perAttemptTimeout = 5 * time.Second
	}
	if perAttemptTimeout > 30*time.Second {
		perAttemptTimeout = 30 * time.Second
	}

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for %s cancelled: %w", description, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timed out waiting for %s after %s", description, timeout)
		}

		attemptTimeout := perAttemptTimeout
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		ok, err := check(attemptCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", description, err)
		}
		if ok {
			return nil
		}

		remaining = time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timed out waiting for %s after %s", description, timeout)
		}
		sleep := interval
		if remaining < sleep {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for %s cancelled: %w", description, ctx.Err())
		case <-timer.C:
		}
	}
}
