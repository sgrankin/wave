package agent

import (
	"reflect"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/id"
)

// These are white-box, zero-real-time unit tests for the pure coalescer: every
// `now` is hand-fed, so all deterministic correctness claims live here (the
// integration tests only prove the Run wiring).

func edited(blip string, version uint64, text string) Event {
	return Event{Kind: BlipEdited, BlipID: blip, Version: version, Text: text}
}

// ids extracts the (BlipID, Version, Text) of a slice of blip.edited events for
// concise comparison.
func editTriples(evs []Event) [][3]any {
	out := make([][3]any, len(evs))
	for i, ev := range evs {
		out[i] = [3]any{ev.BlipID, ev.Version, ev.Text}
	}
	return out
}

func wantEdits(t *testing.T, got []Event, want ...[3]any) {
	t.Helper()
	if want == nil {
		want = [][3]any{}
	}
	gt := editTriples(got)
	if len(gt) == 0 {
		gt = [][3]any{}
	}
	if !reflect.DeepEqual(gt, want) {
		t.Fatalf("edits = %v, want %v", gt, want)
	}
}

func newTestCoalescer() *coalescer {
	return newCoalescer(400*time.Millisecond, 2000*time.Millisecond)
}

var t0 = time.UnixMilli(0)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

// T1: a single edit buffers, sets a window-from-firstSeen deadline, and flushes
// exactly at the window boundary (not a millisecond before).
func TestCoalesceSingleEditWindowBoundary(t *testing.T) {
	c := newTestCoalescer()
	if out := c.add(t0, []Event{edited("A", 5, "a1")}); len(out) != 0 {
		t.Fatalf("add returned %v, want empty (buffered)", out)
	}
	dl, ok := c.nextDeadline()
	if !ok || !dl.Equal(at(400)) {
		t.Fatalf("nextDeadline = (%v, %v), want (%v, true)", dl, ok, at(400))
	}
	if out := c.flushDue(at(399)); len(out) != 0 {
		t.Fatalf("flushDue(399) = %v, want empty (A still pending)", out)
	}
	wantEdits(t, c.flushDue(at(400)), [3]any{"A", uint64(5), "a1"})
	if _, ok := c.nextDeadline(); ok {
		t.Fatal("nextDeadline ok after flush, want empty")
	}
}

// T2: a burst to one blip keeps a single pending entry, latest-wins on Version AND
// Text, with the deadline tracking lastSeen+window.
func TestCoalesceBurstLatestWins(t *testing.T) {
	c := newTestCoalescer()
	for i, e := range []struct {
		ms   int
		ver  uint64
		text string
	}{{0, 5, "a1"}, {100, 6, "a2"}, {200, 7, "a3"}} {
		if out := c.add(at(e.ms), []Event{edited("A", e.ver, e.text)}); len(out) != 0 {
			t.Fatalf("add #%d returned %v, want empty", i, out)
		}
	}
	if len(c.pending) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(c.pending))
	}
	dl, ok := c.nextDeadline()
	if !ok || !dl.Equal(at(600)) {
		t.Fatalf("nextDeadline = (%v, %v), want (%v, true)", dl, ok, at(600))
	}
	if out := c.flushDue(at(599)); len(out) != 0 {
		t.Fatalf("flushDue(599) = %v, want empty", out)
	}
	wantEdits(t, c.flushDue(at(600)), [3]any{"A", uint64(7), "a3"})
	wantEdits(t, c.flushDue(at(1000)))
}

// T3: flushAll emits in first-seen order regardless of which blip was edited last.
func TestCoalesceInsertionOrderFlush(t *testing.T) {
	c := newTestCoalescer()
	c.add(at(0), []Event{edited("A", 1, "a1")})
	c.add(at(10), []Event{edited("B", 2, "b1")})
	c.add(at(20), []Event{edited("A", 3, "a2")}) // re-add A: keeps its first-seen slot
	wantEdits(t, c.flushAll(),
		[3]any{"A", uint64(3), "a2"},
		[3]any{"B", uint64(2), "b1"},
	)
	if len(c.pending) != 0 || len(c.order) != 0 {
		t.Fatalf("buffer not empty after flushAll: pending=%d order=%d", len(c.pending), len(c.order))
	}
}

// T4: under sustained typing the deadline is capped at firstSeen+maxAge, the entry
// flushes at the cap, and a fresh edit afterward re-bases firstSeen (no starvation).
func TestCoalesceMaxAgeCap(t *testing.T) {
	c := newTestCoalescer()
	for ms := 0; ms <= 1900; ms += 100 {
		c.add(at(ms), []Event{edited("A", uint64(ms), "v")})
		dl, ok := c.nextDeadline()
		if !ok {
			t.Fatalf("nextDeadline empty at ms=%d", ms)
		}
		if dl.After(at(2000)) {
			t.Fatalf("nextDeadline %v at ms=%d exceeds the cap %v", dl, ms, at(2000))
		}
	}
	wantEdits(t, c.flushDue(at(2000)), [3]any{"A", uint64(1900), "v"})
	if _, ok := c.nextDeadline(); ok {
		t.Fatal("entry not removed at cap")
	}
	// A fresh add re-bases firstSeen so continuous typing emits about every maxAge.
	c.add(at(2100), []Event{edited("A", 9999, "fresh")})
	dl, ok := c.nextDeadline()
	if !ok || !dl.Equal(at(2500)) { // window from the fresh lastSeen, < cap (2100+2000)
		t.Fatalf("after fresh add nextDeadline = (%v, %v), want (%v, true)", dl, ok, at(2500))
	}
}

// T5: two independent blips keep independent deadlines; flushing one leaves the
// other pending.
func TestCoalesceIndependentDeadlines(t *testing.T) {
	c := newTestCoalescer()
	c.add(at(0), []Event{edited("A", 1, "a")})
	c.add(at(300), []Event{edited("B", 2, "b")})
	dl, ok := c.nextDeadline()
	if !ok || !dl.Equal(at(400)) {
		t.Fatalf("nextDeadline = (%v, %v), want (%v, true)", dl, ok, at(400))
	}
	wantEdits(t, c.flushDue(at(400)), [3]any{"A", uint64(1), "a"})
	dl, ok = c.nextDeadline()
	if !ok || !dl.Equal(at(700)) {
		t.Fatalf("after flushing A nextDeadline = (%v, %v), want (%v, true)", dl, ok, at(700))
	}
}

// T6: global flush-before-immediate within one delta — a same-delta mention drains
// the edit buffered ahead of it, so the harness sees blip.edited THEN mention.
func TestCoalesceGlobalFlushBeforeImmediate(t *testing.T) {
	c := newTestCoalescer()
	out := c.add(t0, []Event{
		edited("X", 10, "hi @bob"),
		{Kind: Mention, BlipID: "X", Version: 10, Target: "bob"},
	})
	if len(out) != 2 {
		t.Fatalf("add returned %d events, want 2", len(out))
	}
	if out[0].Kind != BlipEdited || out[0].BlipID != "X" || out[0].Text != "hi @bob" {
		t.Fatalf("out[0] = %+v, want blip.edited X 'hi @bob'", out[0])
	}
	if out[1].Kind != Mention || out[1].Target != "bob" {
		t.Fatalf("out[1] = %+v, want mention bob", out[1])
	}
	if len(c.pending) != 0 {
		t.Fatalf("buffer not empty after mention flush: %d pending", len(c.pending))
	}
}

// T7: blip.added (immediate) flushes a previously-buffered edit ahead of it.
func TestCoalesceBlipAddedFlushesPending(t *testing.T) {
	c := newTestCoalescer()
	c.add(t0, []Event{edited("A", 1, "a")})
	out := c.add(at(10), []Event{{Kind: BlipAdded, BlipID: "B", Version: 2, Text: "b"}})
	if len(out) != 2 {
		t.Fatalf("add returned %d events, want 2", len(out))
	}
	if out[0].Kind != BlipEdited || out[0].BlipID != "A" {
		t.Fatalf("out[0] = %+v, want flushed blip.edited A", out[0])
	}
	if out[1].Kind != BlipAdded || out[1].BlipID != "B" {
		t.Fatalf("out[1] = %+v, want blip.added B", out[1])
	}
}

// T8: the monotonic guard keeps the newer Version's text when an older Version
// arrives late, while lastSeen still advances (extending the window).
func TestCoalesceMonotonicGuard(t *testing.T) {
	c := newTestCoalescer()
	c.add(t0, []Event{edited("A", 7, "v7")})
	c.add(at(50), []Event{edited("A", 5, "v5-stale")})
	p := c.pending["A"]
	if p.ev.Version != 7 || p.ev.Text != "v7" {
		t.Fatalf("pending after stale add = (v%d %q), want (v7 \"v7\")", p.ev.Version, p.ev.Text)
	}
	if !p.lastSeen.Equal(at(50)) {
		t.Fatalf("lastSeen = %v, want %v (window still extended)", p.lastSeen, at(50))
	}
}

// T8b: latest-wins carries the LATEST delta's Author too, not just Version/Text.
// put() replaces the whole Event, so a burst from two authors to the same blip
// surfaces the last author — the dimension of latest-wins editTriples can't see.
func TestCoalesceLatestWinsAuthor(t *testing.T) {
	c := newTestCoalescer()
	mustPID := func(addr string) id.ParticipantID {
		p, err := id.NewParticipantID(addr)
		if err != nil {
			t.Fatalf("parse %q: %v", addr, err)
		}
		return p
	}
	alice, bob := mustPID("alice@example.com"), mustPID("bob@example.com")
	c.add(t0, []Event{{Kind: BlipEdited, BlipID: "A", Version: 5, Text: "a1", Author: alice}})
	c.add(at(100), []Event{{Kind: BlipEdited, BlipID: "A", Version: 6, Text: "a2", Author: bob}})
	out := c.flushAll()
	if len(out) != 1 {
		t.Fatalf("flushAll = %d events, want 1", len(out))
	}
	if out[0].Author != bob {
		t.Fatalf("coalesced Author = %v, want the latest delta's author %v", out[0].Author, bob)
	}
	if out[0].Version != 6 || out[0].Text != "a2" {
		t.Fatalf("coalesced = (v%d %q), want (v6 \"a2\")", out[0].Version, out[0].Text)
	}
}

// T9: a partial flushDue removes only the due blip and rebuilds order intact.
func TestCoalesceFlushDuePartial(t *testing.T) {
	c := newTestCoalescer()
	c.add(at(0), []Event{edited("A", 1, "a")})
	c.add(at(200), []Event{edited("B", 2, "b")})
	wantEdits(t, c.flushDue(at(400)), [3]any{"A", uint64(1), "a"})
	if !reflect.DeepEqual(c.order, []string{"B"}) {
		t.Fatalf("order = %v, want [B]", c.order)
	}
	if _, ok := c.pending["A"]; ok {
		t.Fatal("A still pending after due flush")
	}
	if _, ok := c.pending["B"]; !ok {
		t.Fatal("B dropped by partial flush")
	}
}

// Empty add and empty flush are no-ops (Events() returns nil for self-authored
// deltas, so add(now, nil) must be harmless).
func TestCoalesceEmptyIsNoOp(t *testing.T) {
	c := newTestCoalescer()
	if out := c.add(t0, nil); out != nil {
		t.Fatalf("add(nil) = %v, want nil", out)
	}
	if out := c.flushAll(); out != nil {
		t.Fatalf("flushAll on empty = %v, want nil", out)
	}
	if out := c.flushDue(at(1000)); out != nil {
		t.Fatalf("flushDue on empty = %v, want nil", out)
	}
	if _, ok := c.nextDeadline(); ok {
		t.Fatal("nextDeadline ok on empty")
	}
}
