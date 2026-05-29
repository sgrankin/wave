package transport_test

import (
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

func newWaveMap(t *testing.T) *server.WaveMap {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
}

func waveletName(t *testing.T) id.WaveletName {
	t.Helper()
	w, _ := id.NewWaveID("example.com", "w+abc")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	return id.NewWaveletName(w, wl)
}

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// connect dials a fresh in-process connection to a Serve goroutine sharing wm,
// then opens the wavelet. Each client gets its own connection (one wavelet
// channel per connection), but all connections share the same WaveMap, so they
// see each other's deltas.
func connect(t *testing.T, wm *server.WaveMap, name id.WaveletName, author id.ParticipantID) *transport.Client {
	t.Helper()
	cConn, sConn := net.Pipe()
	go func() { _ = transport.Serve(sConn, wm) }()
	cl := transport.NewClient(cConn, name, author)
	t.Cleanup(func() { cl.Close() })
	if err := cl.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	return cl
}

func chars(s string) op.DocOp { return op.NewDocOp([]op.Component{op.Characters{Text: s}}) }

func appendText(retain int, s string) op.DocOp {
	return op.NewDocOp([]op.Component{op.Retain{Count: retain}, op.Characters{Text: s}})
}

// writeBlip is a single blip-content operation (a flat-text blip, for the demo).
func writeBlip(author id.ParticipantID, blipID string, content op.DocOp) []waveop.Operation {
	c := waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}
	return []waveop.Operation{waveop.WaveletBlipOperation{
		BlipID: blipID,
		BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: content},
	}}
}

// TestClientCreateEditReload exercises the round trip end to end: a client
// creates a blip and edits it over the wire, then a second client joins and
// sees the full history.
func TestClientCreateEditReload(t *testing.T) {
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	a := connect(t, wm, name, alice)
	if _, err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err)
	}
	v, err := a.Submit(writeBlip(alice, "b", appendText(2, "!")))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if v.Version() != 2 {
		t.Errorf("resulting version v%d, want v2", v.Version())
	}
	if got, _ := a.BlipContent("b"); !got.Equal(chars("hi!")) {
		t.Errorf("author content = %v, want hi!", got.Components())
	}

	// A second client joins and must see the full history.
	b := connect(t, wm, name, pid(t, "bob@example.com"))
	if err := b.WaitVersion(2); err != nil {
		t.Fatalf("joiner wait: %v", err)
	}
	if got, _ := b.BlipContent("b"); !got.Equal(chars("hi!")) {
		t.Errorf("joiner content = %v, want hi!", got.Components())
	}
}

// TestTwoClientsConverge: two clients on one wave, editing in turn, each sees
// the other's edits and they converge.
func TestTwoClientsConverge(t *testing.T) {
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	a := connect(t, wm, name, alice)
	b := connect(t, wm, name, bob)

	// Alice creates the blip; Bob waits to see it.
	if _, err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
		t.Fatalf("alice create: %v", err)
	}
	if err := b.WaitVersion(1); err != nil {
		t.Fatalf("bob wait v1: %v", err)
	}

	// Bob appends; Alice waits to see it.
	if _, err := b.Submit(writeBlip(bob, "b", appendText(2, " there"))); err != nil {
		t.Fatalf("bob edit: %v", err)
	}
	if err := a.WaitVersion(2); err != nil {
		t.Fatalf("alice wait v2: %v", err)
	}

	ca, _ := a.BlipContent("b")
	cb, _ := b.BlipContent("b")
	if !ca.Equal(cb) {
		t.Errorf("divergence: alice %v vs bob %v", ca.Components(), cb.Components())
	}
	if !ca.Equal(chars("hi there")) {
		t.Errorf("converged content = %v, want 'hi there'", ca.Components())
	}
}

// TestConcurrentClientsConverge: two clients submit edits to the same blip
// position concurrently; the server serializes + transforms, and both replicas
// converge to the same document (TP1).
func TestConcurrentClientsConverge(t *testing.T) {
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	a := connect(t, wm, name, alice)
	b := connect(t, wm, name, bob)

	// Seed a shared blip "ab" (2 chars) so both can append at position 2.
	if _, err := a.Submit(writeBlip(alice, "b", chars("ab"))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := b.WaitVersion(1); err != nil {
		t.Fatalf("bob wait seed: %v", err)
	}

	// Concurrent single-character appends at the same position.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.Submit(writeBlip(alice, "b", appendText(2, "X"))) }()
	go func() { defer wg.Done(); b.Submit(writeBlip(bob, "b", appendText(2, "Y"))) }()
	wg.Wait()

	// Both deltas applied → version 3. Wait for both replicas to catch up.
	if err := a.WaitVersion(3); err != nil {
		t.Fatalf("alice wait v3: %v", err)
	}
	if err := b.WaitVersion(3); err != nil {
		t.Fatalf("bob wait v3: %v", err)
	}

	ca, _ := a.BlipContent("b")
	cb, _ := b.BlipContent("b")
	if !ca.Equal(cb) {
		t.Errorf("divergence: alice %v vs bob %v", ca.Components(), cb.Components())
	}
	if ca.DocumentLength() != 4 {
		t.Errorf("converged length = %d, want 4 ('ab' + two appends)", ca.DocumentLength())
	}
}
