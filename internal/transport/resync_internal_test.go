package transport

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/waveop"
)

// appendBlipOp builds a blip content op that retains `retain` items then inserts
// text — valid onto existing content, unlike the insert-only blipOp.
func appendBlipOp(author id.ParticipantID, blipID string, retain int, text string) waveop.Operation {
	c := waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}
	return waveop.WaveletBlipOperation{
		BlipID: blipID,
		BlipOp: waveop.BlipContentOperation{
			Ctx:       c,
			ContentOp: op.NewDocOp([]op.Component{op.Retain{Count: retain}, op.Characters{Text: text}}),
		},
	}
}

// resyncFixture spins up a server over a shared WaveMap and returns it plus a
// wavelet name and an author.
func resyncFixture(t *testing.T) (*Server, id.WaveletName, id.ParticipantID) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
	srv := &Server{WaveMap: wm}

	w, _ := id.NewWaveID("example.com", "w+abc")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	alice, _ := id.NewParticipantID("alice@example.com")
	return srv, id.NewWaveletName(w, wl), alice
}

// serveConn wires a client end of a pipe to srv and returns the client conn.
func serveConn(t *testing.T, srv *Server) net.Conn {
	t.Helper()
	cConn, sConn := net.Pipe()
	go func() { _ = srv.ServeConn(sConn) }()
	t.Cleanup(func() { cConn.Close() })
	return cConn
}

// TestResyncTailOverWire: a fresh connection resyncing at the current head gets a
// tail-mode response with an empty tail, then receives subsequent deltas live.
func TestResyncTailOverWire(t *testing.T) {
	srv, name, alice := resyncFixture(t)

	// Build the wavelet to v1 with a pessimistic client (it gets its own echo).
	cl := NewClient(serveConn(t, srv), name, alice)
	if err := cl.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := cl.Submit([]waveop.Operation{blipOp(alice, "b", "hi")}); err != nil {
		t.Fatalf("submit v1: %v", err)
	}
	head := cl.Version()

	// New connection resyncs at head (suppressEcho=true, optimistic-style).
	rc := serveConn(t, srv)
	mustWrite(t, rc, encodeResync(name.Serialize(), head.Version(), head.HistoryHash()))
	kind, raw, err := messageKind(mustRead(t, rc))
	if err != nil || kind != mResyncResponse {
		t.Fatalf("resync response kind=%d err=%v", kind, err)
	}
	mode, tail, snap, hist, err := decodeResyncResponse(raw)
	if err != nil {
		t.Fatalf("decode resync response: %v", err)
	}
	if mode != resyncTail || len(tail) != 0 || len(snap) != 0 || len(hist) != 0 {
		t.Fatalf("resync at head = mode %d, tail %d, snap %d, hist %d; want tail mode, all empty",
			mode, len(tail), len(snap), len(hist))
	}

	// A delta from the other connection is delivered live on the resync stream.
	// (Append onto the existing "hi" content — retain 2, then insert.)
	if _, err := cl.Submit([]waveop.Operation{appendBlipOp(alice, "b", 2, "!")}); err != nil {
		t.Fatalf("submit v2: %v", err)
	}
	kind, _, err = messageKind(mustRead(t, rc))
	if err != nil || kind != mUpdate {
		t.Fatalf("expected live update after resync, got kind=%d err=%v", kind, err)
	}
}

// TestResyncResetOverWire: resyncing at a forked (wrong-hash) version yields a
// reset-mode response carrying the full history.
func TestResyncResetOverWire(t *testing.T) {
	srv, name, alice := resyncFixture(t)

	cl := NewClient(serveConn(t, srv), name, alice)
	if err := cl.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := cl.Submit([]waveop.Operation{blipOp(alice, "b", "hi")}); err != nil {
		t.Fatalf("submit v1: %v", err)
	}
	head := cl.Version()

	forked := append([]byte(nil), head.HistoryHash()...)
	forked[0] ^= 0xFF

	rc := serveConn(t, srv)
	mustWrite(t, rc, encodeResync(name.Serialize(), head.Version(), forked))
	kind, raw, err := messageKind(mustRead(t, rc))
	if err != nil || kind != mResyncResponse {
		t.Fatalf("resync response kind=%d err=%v", kind, err)
	}
	mode, tail, _, hist, err := decodeResyncResponse(raw)
	if err != nil {
		t.Fatalf("decode resync response: %v", err)
	}
	if mode != resyncReset {
		t.Fatalf("forked resync mode = %d, want reset", mode)
	}
	if len(tail) != 0 {
		t.Errorf("reset response carried %d tail deltas, want 0", len(tail))
	}
	if len(hist) != 1 {
		t.Errorf("reset response history = %d deltas, want 1 (full view)", len(hist))
	}
}
