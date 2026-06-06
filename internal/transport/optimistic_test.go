package transport_test

import (
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

// pipeDial returns a dial func that connects a fresh net.Pipe to a Serve goroutine
// sharing wm — a new server session per (re)connect.
func pipeDial(wm *server.WaveMap) func() (io.ReadWriteCloser, error) {
	return func() (io.ReadWriteCloser, error) {
		cConn, sConn := net.Pipe()
		go func() { _ = transport.Serve(sConn, wm) }()
		return cConn, nil
	}
}

// dropDialer hands out net.Pipe connections to a Serve goroutine sharing wm and
// can drop the current one (close it) to simulate a network failure, forcing the
// client's supervisor to reconnect and resync.
type dropDialer struct {
	wm      *server.WaveMap
	mu      sync.Mutex
	current net.Conn
}

func (d *dropDialer) dial() (io.ReadWriteCloser, error) {
	cConn, sConn := net.Pipe()
	go func() { _ = transport.Serve(sConn, d.wm) }()
	d.mu.Lock()
	d.current = cConn
	d.mu.Unlock()
	return cConn, nil
}

func (d *dropDialer) drop() {
	d.mu.Lock()
	c := d.current
	d.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// TestOptimisticSnapshotOpen: against a snapshot-enabled server, a joining
// optimistic client's open response is a current-state snapshot (not delta
// history), exercising the LoadSnapshot path.
func TestOptimisticSnapshotOpen(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer store.Close()
	clk := clock.NewFixed(time.UnixMilli(1000))
	wm := server.NewWaveMap(store, clk, server.WithSnapshots(store, 100))
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	a := transport.NewOptimisticClient(pipeDial(wm), name, alice)
	defer a.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("alice open: %v", err)
	}
	if err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.WaitServerVersion(1); err != nil {
		t.Fatalf("alice v1: %v", err)
	}
	if err := a.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
		cur, _ := blip("b")
		return insertAt(alice, "b", cur.DocumentLength(), cur.DocumentLength(), "!")
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := a.WaitServerVersion(2); err != nil {
		t.Fatalf("alice v2: %v", err)
	}

	// Bob joins: the snapshot-enabled server sends a current-state snapshot.
	b := transport.NewOptimisticClient(pipeDial(wm), name, bob)
	defer b.Close()
	if err := b.Open(); err != nil {
		t.Fatalf("bob open: %v", err)
	}
	if err := b.WaitServerVersion(2); err != nil {
		t.Fatalf("bob v2: %v", err)
	}
	if got, _ := b.BlipContent("b"); !got.Equal(chars("hi!")) {
		t.Errorf("bob (snapshot open) content = %v, want hi!", got.Components())
	}
}

// restartDialer serves from a swappable WaveMap so a test can simulate a server
// restart: point it at a fresh WaveMap over the same (still-open) store and drop
// the current connection — the client then reconnects to a server holding only the
// persisted state (its in-memory containers reloaded from disk).
type restartDialer struct {
	mu      sync.Mutex
	wm      *server.WaveMap
	current net.Conn
}

func (d *restartDialer) dial() (io.ReadWriteCloser, error) {
	d.mu.Lock()
	wm := d.wm
	d.mu.Unlock()
	cConn, sConn := net.Pipe()
	go func() { _ = transport.Serve(sConn, wm) }()
	d.mu.Lock()
	d.current = cConn
	d.mu.Unlock()
	return cConn, nil
}

func (d *restartDialer) restart(wm *server.WaveMap) {
	d.mu.Lock()
	c := d.current
	d.wm = wm
	d.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// TestOptimisticServerRestartRecovery: after the server "restarts" (a fresh WaveMap
// over the same store, in-memory containers gone), the client reconnects, resyncs
// from the persisted log, finds its document intact, and keeps editing.
func TestOptimisticServerRestartRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer store.Close()
		clk := clock.NewFixed(time.UnixMilli(1000))
		name := waveletName(t)
		alice := pid(t, "alice@example.com")

		dd := &restartDialer{wm: server.NewWaveMap(store, clk)}
		a := transport.NewOptimisticClient(dd.dial, name, alice)
		defer a.Close()
		if err := a.Open(); err != nil {
			t.Fatalf("open: %v", err)
		}

		if err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := a.WaitServerVersion(1); err != nil {
			t.Fatalf("v1: %v", err)
		}
		if err := a.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
			cur, _ := blip("b")
			return insertAt(alice, "b", cur.DocumentLength(), cur.DocumentLength(), "!")
		}); err != nil {
			t.Fatalf("edit: %v", err)
		}
		if err := a.WaitServerVersion(2); err != nil {
			t.Fatalf("v2: %v", err)
		}

		// Restart the server (fresh WaveMap over the same store) and drop the conn.
		dd.restart(server.NewWaveMap(store, clk))

		// Reconnect + resync recovers; alice keeps editing and converges.
		if err := a.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
			cur, _ := blip("b")
			return insertAt(alice, "b", cur.DocumentLength(), 0, "A")
		}); err != nil {
			t.Fatalf("post-restart edit: %v", err)
		}
		if err := a.WaitServerVersion(3); err != nil {
			t.Fatalf("v3 after restart: %v", err)
		}
		if got, _ := a.BlipContent("b"); !got.Equal(chars("Ahi!")) {
			t.Errorf("after restart = %v, want Ahi!", got.Components())
		}
	})
}

// TestOptimisticReconnectResync drops an optimistic client's connection mid-session
// and verifies its supervisor reconnects, resyncs to catch up on deltas it missed,
// and keeps converging — all under synctest (fake time, deterministic).
func TestOptimisticReconnectResync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wm := newWaveMap(t)
		name := waveletName(t)
		alice := pid(t, "alice@example.com")
		bob := pid(t, "bob@example.com")

		dd := &dropDialer{wm: wm}
		a := transport.NewOptimisticClient(dd.dial, name, alice)
		defer a.Close()
		if err := a.Open(); err != nil {
			t.Fatalf("open: %v", err)
		}
		b := connectOptimistic(t, wm, name, bob)

		// Create + converge to "hi" at v1.
		if err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := b.WaitServerVersion(1); err != nil {
			t.Fatalf("bob v1: %v", err)
		}

		// Drop alice's connection. While she reconnects, bob appends "!" (v2).
		dd.drop()
		if err := b.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
			cur, _ := blip("b")
			return insertAt(bob, "b", cur.DocumentLength(), cur.DocumentLength(), "!")
		}); err != nil {
			t.Fatalf("bob edit: %v", err)
		}

		// Alice must reconnect, resync, and see bob's edit (reaching v2).
		if err := a.WaitServerVersion(2); err != nil {
			t.Fatalf("alice v2 after reconnect: %v", err)
		}
		if got, _ := a.BlipContent("b"); !got.Equal(chars("hi!")) {
			t.Errorf("alice after resync = %v, want hi!", got.Components())
		}

		// Alice edits post-reconnect; both converge.
		if err := a.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
			cur, _ := blip("b")
			return insertAt(alice, "b", cur.DocumentLength(), 0, "A")
		}); err != nil {
			t.Fatalf("alice edit: %v", err)
		}
		if err := b.WaitServerVersion(3); err != nil {
			t.Fatalf("bob v3: %v", err)
		}

		ca, _ := a.BlipContent("b")
		cb, _ := b.BlipContent("b")
		if !ca.Equal(cb) {
			t.Fatalf("divergence after reconnect: alice %v vs bob %v", ca.Components(), cb.Components())
		}
		if !ca.Equal(chars("Ahi!")) {
			t.Errorf("converged = %v, want Ahi!", ca.Components())
		}
	})
}

// connectOptimistic opens an optimistic client (self-suppressed stream) against wm.
func connectOptimistic(t *testing.T, wm *server.WaveMap, name id.WaveletName, author id.ParticipantID) *transport.OptimisticClient {
	t.Helper()
	cl := transport.NewOptimisticClient(pipeDial(wm), name, author)
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
