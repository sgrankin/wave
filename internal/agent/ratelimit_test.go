package agent

import (
	"testing"
	"time"
)

// settableClock is a clock.Clock whose time the test advances by hand.
type settableClock struct{ t time.Time }

func (c *settableClock) Now() time.Time { return c.t }

func TestRateLimiterBurstThenRefill(t *testing.T) {
	clk := &settableClock{t: time.UnixMilli(0)}
	rl := newRateLimiter(clk, 3, 2) // burst 3, refill 2/sec

	// The initial burst of 3 is allowed; the 4th is denied (bucket empty, no time passed).
	for i := 0; i < 3; i++ {
		if !rl.allow() {
			t.Fatalf("allow #%d should pass within the burst", i)
		}
	}
	if rl.allow() {
		t.Fatal("4th allow should be denied (bucket empty)")
	}

	// Advance 1s → +2 tokens, so exactly two more are allowed, then denied.
	clk.t = clk.t.Add(time.Second)
	if !rl.allow() || !rl.allow() {
		t.Fatal("two refilled tokens should be allowed after 1s")
	}
	if rl.allow() {
		t.Fatal("third after a 1s refill should be denied (only 2/sec)")
	}

	// Tokens never exceed the burst cap, even after a long idle.
	clk.t = clk.t.Add(time.Hour)
	allowed := 0
	for i := 0; i < 10; i++ {
		if rl.allow() {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("after a long idle, %d allowed, want exactly the burst cap 3", allowed)
	}
}
