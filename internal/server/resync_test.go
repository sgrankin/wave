package server_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/version"
)

// TestSubmitFromSuppressesOrigin: SubmitFrom excludes the submitter's own
// subscription from fan-out (self-suppression), while a plain Submit reaches
// everyone.
func TestSubmitFromSuppressesOrigin(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")

	s1 := c.Subscribe()
	defer s1.Close()
	s2 := c.Subscribe()
	defer s2.Close()

	// Submit excluding s1: s2 sees the delta, s1 does not.
	if _, err := c.SubmitFrom(creationDelta(alice, version.Zero(name), "b", chars("hi")), s1); err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case <-s2.Updates():
	default:
		t.Error("s2 (not excluded) received no update")
	}
	select {
	case u := <-s1.Updates():
		t.Errorf("s1 (excluded) received its own delta: v%d", u.ResultingVersion.Version())
	default:
	}

	// A plain Submit (no exclusion) reaches both.
	if _, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(2, "!"))); err != nil {
		t.Fatalf("submit2: %v", err)
	}
	for i, sub := range []*server.Subscription{s1, s2} {
		select {
		case <-sub.Updates():
		default:
			t.Errorf("sub %d received no update for the un-excluded submit", i)
		}
	}
}

// TestOpenAtResyncTail: OpenAt at a known interior version returns the deltas
// after it and a live subscription that continues from there; at head it returns
// an empty tail.
func TestOpenAtResyncTail(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")

	// The creation delta carries two ops (AddParticipant + blip), so versions are
	// not 1,2,3; compare against the actual resulting versions instead.
	r1, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi")))
	if err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	r2, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(2, "!")))
	if err != nil {
		t.Fatalf("submit 2: %v", err)
	}
	r3, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(3, "?")))
	if err != nil {
		t.Fatalf("submit 3: %v", err)
	}

	tail, sub, ok := c.OpenAt(r1.ResultingVersion.Version(), r1.ResultingVersion.HistoryHash())
	if !ok {
		t.Fatal("OpenAt(r1) returned reset; want tail")
	}
	defer sub.Close()
	if len(tail) != 2 ||
		tail[0].ResultingVersion.Compare(r2.ResultingVersion) != 0 ||
		tail[1].ResultingVersion.Compare(r3.ResultingVersion) != 0 {
		t.Fatalf("tail = %d deltas; want exactly the r2,r3 deltas", len(tail))
	}

	// Live continuation: a subsequent submit is delivered on the resync subscription.
	r4, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(4, ".")))
	if err != nil {
		t.Fatalf("submit 4: %v", err)
	}
	select {
	case u := <-sub.Updates():
		if u.ResultingVersion.Compare(r4.ResultingVersion) != 0 {
			t.Errorf("live update v%d, want v%d", u.ResultingVersion.Version(), r4.ResultingVersion.Version())
		}
	default:
		t.Error("resync subscription received no live update")
	}

	// Resync from the current head: empty tail, working subscription.
	head := c.Version()
	tailH, subH, ok := c.OpenAt(head.Version(), head.HistoryHash())
	if !ok {
		t.Fatal("OpenAt(head) returned reset; want tail")
	}
	subH.Close()
	if len(tailH) != 0 {
		t.Errorf("head resync tail = %d deltas, want 0", len(tailH))
	}
}

// TestOpenAtResyncReset: a fork (right version, wrong hash) or an unknown version
// cannot be resynced incrementally and must reset.
func TestOpenAtResyncReset(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")

	r1, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Fork: matching version number, divergent history hash.
	forked := append([]byte(nil), r1.ResultingVersion.HistoryHash()...)
	forked[0] ^= 0xFF
	if _, _, ok := c.OpenAt(r1.ResultingVersion.Version(), forked); ok {
		t.Error("OpenAt with a forked hash returned tail; want reset")
	}

	// Unknown / future version.
	if _, _, ok := c.OpenAt(999, r1.ResultingVersion.HistoryHash()); ok {
		t.Error("OpenAt at an unknown version returned tail; want reset")
	}
}
