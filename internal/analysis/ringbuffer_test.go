/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package analysis_test

import (
	"sync"
	"testing"
	"time"

	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
)

func TestRingBuffer_EmptySnapshot(t *testing.T) {
	rb := analysis.NewRingBuffer(10)
	snp := rb.Snapshot()
	if snp != nil {
		t.Errorf("expected nil snapshot for empty buffer, got %v", snp)
	}
}

func TestRingBuffer_Len(t *testing.T) {
	rb := analysis.NewRingBuffer(5)
	if rb.Len() != 0 {
		t.Errorf("expected Len=0, got %d", rb.Len())
	}
	now := time.Now()
	rb.Push(analysis.Sample{Timestamp: now, UsedBytes: 1})
	rb.Push(analysis.Sample{Timestamp: now.Add(time.Second), UsedBytes: 2})
	if rb.Len() != 2 {
		t.Errorf("expected Len=2, got %d", rb.Len())
	}
	// Fill past capacity.
	for i := 0; i < 10; i++ {
		rb.Push(analysis.Sample{Timestamp: now.Add(time.Duration(i+2) * time.Second), UsedBytes: int64(i + 3)})
	}
	if rb.Len() != 5 {
		t.Errorf("expected Len=5 (capacity), got %d", rb.Len())
	}
}

func TestRingBuffer_CapacityNotExceeded(t *testing.T) {
	rb := analysis.NewRingBuffer(3)
	now := time.Now()
	// Push 4 items into a capacity-3 buffer; oldest should be evicted.
	rb.Push(analysis.Sample{Timestamp: now.Add(0), UsedBytes: 10})
	rb.Push(analysis.Sample{Timestamp: now.Add(time.Second), UsedBytes: 20})
	rb.Push(analysis.Sample{Timestamp: now.Add(2 * time.Second), UsedBytes: 30})
	rb.Push(analysis.Sample{Timestamp: now.Add(3 * time.Second), UsedBytes: 40}) // evicts 10

	snp := rb.Snapshot()
	if len(snp) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(snp))
	}
	// Oldest retained should be the second push (UsedBytes=20).
	if snp[0].UsedBytes != 20 {
		t.Errorf("expected oldest UsedBytes=20, got %d", snp[0].UsedBytes)
	}
	// Newest should be the last push (UsedBytes=40).
	if snp[2].UsedBytes != 40 {
		t.Errorf("expected newest UsedBytes=40, got %d", snp[2].UsedBytes)
	}
}

func TestRingBuffer_ChronologicalOrder(t *testing.T) {
	rb := analysis.NewRingBuffer(5)
	now := time.Now()
	// Push 7 items (overflows 5-capacity buffer by 2).
	for i := 0; i < 7; i++ {
		rb.Push(analysis.Sample{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			UsedBytes: int64(i * 100),
		})
	}
	snp := rb.Snapshot()
	if len(snp) != 5 {
		t.Fatalf("expected 5 samples, got %d", len(snp))
	}
	for i := 1; i < len(snp); i++ {
		if !snp[i].Timestamp.After(snp[i-1].Timestamp) {
			t.Errorf("snapshot not in chronological order at index %d: %v <= %v",
				i, snp[i].Timestamp, snp[i-1].Timestamp)
		}
	}
}

func TestRingBuffer_SnapshotIsCopy(t *testing.T) {
	rb := analysis.NewRingBuffer(5)
	now := time.Now()
	rb.Push(analysis.Sample{Timestamp: now, UsedBytes: 100})

	snp := rb.Snapshot()
	// Mutate the snapshot.
	snp[0].UsedBytes = 9999

	// Original buffer should be unaffected.
	snp2 := rb.Snapshot()
	if snp2[0].UsedBytes == 9999 {
		t.Error("snapshot mutation affected the ring buffer internal state")
	}
}

func TestRingBuffer_Reset(t *testing.T) {
	rb := analysis.NewRingBuffer(5)
	now := time.Now()
	for i := 0; i < 5; i++ {
		rb.Push(analysis.Sample{Timestamp: now.Add(time.Duration(i)), UsedBytes: int64(i)})
	}
	if rb.Len() != 5 {
		t.Fatalf("expected 5 before reset, got %d", rb.Len())
	}
	rb.Reset()
	if rb.Len() != 0 {
		t.Errorf("expected 0 after reset, got %d", rb.Len())
	}
	if rb.Snapshot() != nil {
		t.Error("expected nil snapshot after reset")
	}
	// Buffer should still be usable after reset.
	rb.Push(analysis.Sample{Timestamp: now, UsedBytes: 42})
	snp := rb.Snapshot()
	if len(snp) != 1 || snp[0].UsedBytes != 42 {
		t.Errorf("buffer not usable after reset, got %v", snp)
	}
}

func TestRingBuffer_ConcurrentPushSnapshot(t *testing.T) {
	rb := analysis.NewRingBuffer(100)
	var wg sync.WaitGroup
	now := time.Now()

	// 10 goroutines each push 50 samples.
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				rb.Push(analysis.Sample{
					Timestamp: now.Add(time.Duration(g*50+i) * time.Millisecond),
					UsedBytes: int64(g*50 + i),
				})
			}
		}(g)
	}

	// 5 goroutines concurrently snapshot.
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = rb.Snapshot()
			}
		}()
	}

	wg.Wait()
	// Just verify we haven't panicked and the buffer has a sane length.
	if rb.Len() > 100 {
		t.Errorf("Len %d exceeds capacity 100", rb.Len())
	}
}
