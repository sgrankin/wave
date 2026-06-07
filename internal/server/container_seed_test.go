package server_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/version"
)

// TestContainerSeedIfEmpty: SeedIfEmpty creates a brand-new wavelet from the
// conversation seed ops, makes the author a participant, and is idempotent — a
// second call is a no-op (the atomic guard that prevents a double-seed).
func TestContainerSeedIfEmpty(t *testing.T) {
	c, _, _ := newContainer(t)
	alice := pid(t, "alice@example.com")
	ops, err := conv.SeedConversation(alice, 1000)
	if err != nil {
		t.Fatalf("build seed: %v", err)
	}

	seeded, err := c.SeedIfEmpty(alice, ops)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !seeded {
		t.Error("expected seeded=true on an empty container")
	}
	if got := c.Version().Version(); got != 3 {
		t.Errorf("version = %d, want 3 (the 3 seed ops)", got)
	}
	w := c.Wavelet()
	if w == nil || !w.HasParticipant(alice) {
		t.Error("author should be the seeded wavelet's first participant")
	}

	// Idempotent: a second seed must not re-apply (no double manifest / version 6).
	seeded2, err := c.SeedIfEmpty(alice, ops)
	if err != nil {
		t.Fatalf("seed again: %v", err)
	}
	if seeded2 {
		t.Error("expected seeded=false on an already-seeded container")
	}
	if got := c.Version().Version(); got != 3 {
		t.Errorf("version = %d after no-op seed, want 3", got)
	}
}

// TestContainerSeedIfEmptyAfterSubmit: SeedIfEmpty is a no-op once the wavelet
// has been created by a normal submit (an existing wavelet is never reseeded).
func TestContainerSeedIfEmptyAfterSubmit(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	if _, err := c.Submit(blipDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatalf("submit: %v", err)
	}
	ops, err := conv.SeedConversation(alice, 1000)
	if err != nil {
		t.Fatalf("build seed: %v", err)
	}
	seeded, err := c.SeedIfEmpty(alice, ops)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if seeded {
		t.Error("seed should be a no-op once the wavelet exists")
	}
}
