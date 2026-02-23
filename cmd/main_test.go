package main

import (
	"testing"
	"time"
)

func TestBackfillWindow_UsesProvidedValues(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	start, end := backfillWindow(12, 10*time.Minute, now)

	if !end.Equal(now) {
		t.Fatalf("expected end=%v, got %v", now, end)
	}
	wantStart := now.Add(-120 * time.Minute)
	if !start.Equal(wantStart) {
		t.Fatalf("expected start=%v, got %v", wantStart, start)
	}
}

func TestBackfillWindow_UsesDefaults(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	start, end := backfillWindow(0, 0, now)

	if !end.Equal(now) {
		t.Fatalf("expected end=%v, got %v", now, end)
	}
	wantStart := now.Add(-time.Duration(defaultSampleRetention) * defaultBackfillStep)
	if !start.Equal(wantStart) {
		t.Fatalf("expected start=%v, got %v", wantStart, start)
	}
}
