package cirunner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseDurationOr(t *testing.T) {
	if got := parseDurationOr("15s", 3); got != 15*time.Second {
		t.Fatalf("got %v", got)
	}
	if got := parseDurationOr("bad", 3); got != 3*time.Second {
		t.Fatalf("got %v", got)
	}
	if got := parseDurationOr("", 0); got != 1*time.Second {
		t.Fatalf("got %v", got)
	}
}

func TestFormatDuration(t *testing.T) {
	if got := formatDuration(12); got != "12s" {
		t.Fatalf("got %q", got)
	}
	if got := formatDuration(121); got != "2m01s" {
		t.Fatalf("got %q", got)
	}
}

func TestWaitUntilSuccessAndTimeout(t *testing.T) {
	ctx := context.Background()
	count := 0
	err := waitUntil(ctx, 300*time.Millisecond, 10*time.Millisecond, "eventual", func(context.Context) (bool, error) {
		count++
		return count >= 3, nil
	})
	if err != nil {
		t.Fatalf("waitUntil unexpected err: %v", err)
	}

	err = waitUntil(ctx, 50*time.Millisecond, 10*time.Millisecond, "never", func(context.Context) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
}

func TestWaitUntilPropagatesCheckError(t *testing.T) {
	want := errors.New("boom")
	err := waitUntil(context.Background(), 200*time.Millisecond, 10*time.Millisecond, "check", func(context.Context) (bool, error) {
		return false, want
	})
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("expected wrapped boom error, got %v", err)
	}
}
