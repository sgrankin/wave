package transport_test

import (
	"net"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

// connectOptimistic dials a fresh connection to a Serve goroutine sharing wm and
// opens the wavelet with an optimistic client (self-suppressed stream).
func connectOptimistic(t *testing.T, wm *server.WaveMap, name id.WaveletName, author id.ParticipantID) *transport.OptimisticClient {
	t.Helper()
	cConn, sConn := net.Pipe()
	go func() { _ = transport.Serve(sConn, wm) }()
	cl := transport.NewOptimisticClient(cConn, name, author)
	t.Cleanup(func() { cl.Close() })
	if err := cl.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return cl
}

// insertAt builds a single blip-content op inserting text at pos in a document of
// the given length.
func insertAt(author id.ParticipantID, blipID string, length, pos int, text string) []waveop.Operation {
	var comps []op.Component
	if pos > 0 {
		comps = append(comps, op.Retain{Count: pos})
	}
	comps = append(comps, op.Characters{Text: text})
	if length-pos > 0 {
		comps = append(comps, op.Retain{Count: length - pos})
	}
	return writeBlip(author, blipID, op.NewDocOp(comps))
}

// TestOptimisticRoundTrip: an optimistic client creates and edits a blip; its local
// replica reflects the edit immediately, and a second optimistic client observes it.
func TestOptimisticRoundTrip(t *testing.T) {
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	a := connectOptimistic(t, wm, name, alice)

	// Create the blip; the optimistic replica shows it before the ack settles.
	if err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, _ := a.BlipContent("b"); !got.Equal(chars("hi")) {
		t.Errorf("optimistic content = %v, want hi (immediate)", got.Components())
	}
	if err := a.WaitServerVersion(1); err != nil {
		t.Fatalf("alice settle v1: %v", err)
	}

	if err := a.Submit(writeBlip(alice, "b", appendText(2, "!"))); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := a.WaitServerVersion(2); err != nil {
		t.Fatalf("alice settle v2: %v", err)
	}
	if got, _ := a.BlipContent("b"); !got.Equal(chars("hi!")) {
		t.Errorf("alice content = %v, want hi!", got.Components())
	}

	// A second optimistic client joins and sees the full history.
	b := connectOptimistic(t, wm, name, pid(t, "bob@example.com"))
	if err := b.WaitServerVersion(2); err != nil {
		t.Fatalf("bob wait v2: %v", err)
	}
	if got, _ := b.BlipContent("b"); !got.Equal(chars("hi!")) {
		t.Errorf("bob content = %v, want hi!", got.Components())
	}
}

// TestOptimisticConcurrentConverge: two optimistic clients edit the same blip
// concurrently (both targeting the same version, before seeing each other), and
// converge to the same document.
func TestOptimisticConcurrentConverge(t *testing.T) {
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	a := connectOptimistic(t, wm, name, alice)
	b := connectOptimistic(t, wm, name, bob)

	// Alice creates the blip "hi"; both reach v1.
	if err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.WaitServerVersion(1); err != nil {
		t.Fatalf("alice v1: %v", err)
	}
	if err := b.WaitServerVersion(1); err != nil {
		t.Fatalf("bob v1: %v", err)
	}

	// Both edit concurrently against v1 without waiting: Alice prepends "A", Bob
	// appends "B". Each applies its own edit optimistically first.
	if err := a.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
		cur, _ := blip("b")
		return insertAt(alice, "b", cur.DocumentLength(), 0, "A")
	}); err != nil {
		t.Fatalf("alice edit: %v", err)
	}
	if err := b.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
		cur, _ := blip("b")
		return insertAt(bob, "b", cur.DocumentLength(), cur.DocumentLength(), "B")
	}); err != nil {
		t.Fatalf("bob edit: %v", err)
	}

	// The two concurrent deltas serialize to v3; both clients converge there.
	if err := a.WaitServerVersion(3); err != nil {
		t.Fatalf("alice v3: %v", err)
	}
	if err := b.WaitServerVersion(3); err != nil {
		t.Fatalf("bob v3: %v", err)
	}

	ca, _ := a.BlipContent("b")
	cb, _ := b.BlipContent("b")
	if !ca.Equal(cb) {
		t.Fatalf("divergence: alice %v vs bob %v", ca.Components(), cb.Components())
	}
	if !ca.Equal(chars("AhiB")) {
		t.Errorf("converged content = %v, want AhiB", ca.Components())
	}
}
