/*
Copyright 2024 pbsladek.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package analysis

import (
	"sync"
	"time"
)

// Sample is a single point-in-time measurement for one PVC.
type Sample struct {
	Timestamp time.Time
	UsedBytes int64
}

// RingBuffer is a thread-safe, fixed-capacity circular buffer of Samples.
// When the buffer is full, the oldest sample is overwritten.
// The zero value is not usable; use NewRingBuffer.
type RingBuffer struct {
	mu       sync.RWMutex
	buf      []Sample
	head     int // index of next write slot
	count    int // number of valid entries [0..capacity]
	capacity int
}

// NewRingBuffer creates a RingBuffer with the given capacity.
// capacity must be >= 1.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &RingBuffer{
		buf:      make([]Sample, capacity),
		capacity: capacity,
	}
}

// Push adds a sample to the buffer. If the buffer is full, the oldest
// entry is overwritten.
func (r *RingBuffer) Push(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = s
	r.head = (r.head + 1) % r.capacity
	if r.count < r.capacity {
		r.count++
	}
}

// Snapshot returns all valid samples in chronological order (oldest first).
// The returned slice is a copy; callers may freely mutate it.
// Returns nil if the buffer is empty.
func (r *RingBuffer) Snapshot() []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.count == 0 {
		return nil
	}
	out := make([]Sample, r.count)
	// The oldest entry is at (head - count + capacity) % capacity.
	start := (r.head - r.count + r.capacity) % r.capacity
	for i := 0; i < r.count; i++ {
		out[i] = r.buf[(start+i)%r.capacity]
	}
	return out
}

// Len returns the number of valid samples currently stored.
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Reset clears all samples from the buffer without releasing memory.
// Used when a PVC is deleted and recreated (UID change) to avoid
// contaminating the new PVC's growth data with old history.
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.head = 0
	r.count = 0
}
