// Package clock provides an injectable time source. Production code depends on
// Clock instead of calling time.Now directly, so that time-dependent behavior
// (wavelet delta application timestamps, session expiry, snapshot cadence) is
// deterministic under test.
//
// This is a foundational dependency: the original Java carried a TODO noting
// that System.currentTimeMillis() should have been injected
// (LocalWaveletContainerImpl); we do it from day one.
package clock

import (
	"sync"
	"time"
)

// Clock is a source of the current time.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// System is the real, wall-clock Clock. The zero value is ready to use.
type System struct{}

// Now returns the current wall-clock time.
func (System) Now() time.Time { return time.Now() }

// Fixed is a deterministic Clock for tests. It is safe for concurrent use; the
// time advances only via Set or Advance, never on its own.
type Fixed struct {
	mu sync.Mutex
	t  time.Time
}

// NewFixed returns a Fixed clock started at t.
func NewFixed(t time.Time) *Fixed { return &Fixed{t: t} }

// Now returns the current fixed time.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Advance moves the clock forward by d.
func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// Set sets the clock to t.
func (f *Fixed) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}

// Compile-time assertions that both implementations satisfy Clock.
var (
	_ Clock = System{}
	_ Clock = (*Fixed)(nil)
)
