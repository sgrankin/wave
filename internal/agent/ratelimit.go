package agent

import (
	"math"
	"sync"
	"time"

	"github.com/sgrankin/wave/internal/clock"
)

// Default agent submit rate — a generous burst with a sustained cap. This is
// defense-in-depth that BOUNDS a runaway reaction loop (self-suppression already
// prevents an agent reacting to its OWN writes; this caps the rate at which any
// reaction — including a multi-agent or harness-induced storm — can hit the
// wavelet's single writer). It does not by itself prevent higher-level loops.
const (
	defaultRateBurst  = 16
	defaultRatePerSec = 8.0
)

// rateLimiter is a token bucket over a Clock. allow reports whether one more
// action is permitted now, consuming a token if so. It is safe for concurrent use.
type rateLimiter struct {
	clk    clock.Clock
	mu     sync.Mutex
	cap    float64
	perSec float64
	tokens float64
	last   time.Time
}

func newRateLimiter(clk clock.Clock, burst int, perSec float64) *rateLimiter {
	return &rateLimiter{clk: clk, cap: float64(burst), perSec: perSec, tokens: float64(burst), last: clk.Now()}
}

func (r *rateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clk.Now()
	if elapsed := now.Sub(r.last).Seconds(); elapsed > 0 {
		r.tokens = math.Min(r.cap, r.tokens+elapsed*r.perSec)
		r.last = now
	}
	if r.tokens >= 1 {
		r.tokens--
		return true
	}
	return false
}
