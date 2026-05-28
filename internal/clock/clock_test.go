package clock_test

import (
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
)

func TestSystemNow(t *testing.T) {
	before := time.Now()
	got := clock.System{}.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System.Now() = %v, want within [%v, %v]", got, before, after)
	}
}

func TestFixedNowIsStable(t *testing.T) {
	start := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	c := clock.NewFixed(start)

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}
	// Now must not drift on its own.
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("second Now() = %v, want stable %v", got, start)
	}
}

func TestFixedAdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	c := clock.NewFixed(start)

	c.Advance(90 * time.Minute)
	if want, got := start.Add(90*time.Minute), c.Now(); !got.Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", got, want)
	}

	c.Set(start)
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("after Set, Now() = %v, want %v", got, start)
	}
}
