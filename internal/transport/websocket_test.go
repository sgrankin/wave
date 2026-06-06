package transport_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

// wsURL converts an httptest http(s):// base URL to a ws(s):// URL.
func wsURL(httpURL string) string { return "ws" + strings.TrimPrefix(httpURL, "http") }

// headerIdentify resolves the participant from a test header, mirroring how a
// real deployment resolves it from a verified session cookie. An absent/invalid
// header is "unauthorized" (ok=false), which the handler rejects before Accept.
func headerIdentify(r *http.Request) (id.ParticipantID, bool) {
	addr := r.Header.Get("X-Test-User")
	if addr == "" {
		return id.ParticipantID{}, false
	}
	p, err := id.NewParticipantID(addr)
	if err != nil {
		return id.ParticipantID{}, false
	}
	return p, true
}

// wsDialAs returns a reconnecting dial func for a client authenticating (and
// authoring) as who, over the WebSocket endpoint at base (an httptest URL).
func wsDialAs(ctx context.Context, base string, who id.ParticipantID) func() (io.ReadWriteCloser, error) {
	return transport.WebSocketDialer(ctx, wsURL(base), func() *websocket.DialOptions {
		return &websocket.DialOptions{HTTPHeader: http.Header{"X-Test-User": {who.Address()}}}
	})
}

// TestWebSocketRoundTrip: an optimistic client over a real WebSocket creates and
// edits a blip; its replica reflects the edit and the server advances — the whole
// framed-CBOR session protocol rides the WebSocket transport unchanged.
func TestWebSocketRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	srv := &transport.Server{WaveMap: wm}
	hs := httptest.NewServer(srv.WebSocketHandler(headerIdentify))
	defer hs.Close()

	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, alice)
	defer a.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, _ := a.BlipContent("b"); !got.Equal(chars("hi")) {
		t.Errorf("optimistic content = %v, want hi (immediate)", got.Components())
	}
	if err := a.WaitServerVersion(1); err != nil {
		t.Fatalf("settle v1: %v", err)
	}
	if err := a.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
		cur, _ := blip("b")
		return insertAt(alice, "b", cur.DocumentLength(), cur.DocumentLength(), "!")
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := a.WaitServerVersion(2); err != nil {
		t.Fatalf("settle v2: %v", err)
	}
	if got, _ := a.BlipContent("b"); !got.Equal(chars("hi!")) {
		t.Errorf("content = %v, want hi!", got.Components())
	}
}

// TestWebSocketTwoClientConverge: two optimistic clients over WebSockets, each
// authenticated as a distinct participant, edit the same blip concurrently and
// converge — exercising fan-out and self-suppression across the real transport.
func TestWebSocketTwoClientConverge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	srv := &transport.Server{WaveMap: wm}
	hs := httptest.NewServer(srv.WebSocketHandler(headerIdentify))
	defer hs.Close()

	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, alice)
	defer a.Close()
	b := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, bob), name, bob)
	defer b.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("alice open: %v", err)
	}
	if err := b.Open(); err != nil {
		t.Fatalf("bob open: %v", err)
	}

	// Alice creates "hi"; both reach v1.
	if err := a.Submit(writeBlip(alice, "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.WaitServerVersion(1); err != nil {
		t.Fatalf("alice v1: %v", err)
	}
	if err := b.WaitServerVersion(1); err != nil {
		t.Fatalf("bob v1: %v", err)
	}

	// Both edit concurrently against v1: alice prepends "A", bob appends "B".
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
		t.Errorf("converged = %v, want AhiB", ca.Components())
	}
}

// TestWebSocketUnauthorizedRejected: an upgrade with no authenticated identity is
// rejected before the WebSocket handshake (the dial fails).
func TestWebSocketUnauthorizedRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv := &transport.Server{WaveMap: newWaveMap(t)}
	hs := httptest.NewServer(srv.WebSocketHandler(headerIdentify))
	defer hs.Close()

	// No X-Test-User header → identify returns ok=false → 401, never upgraded.
	if _, err := transport.DialWebSocket(ctx, wsURL(hs.URL), nil); err == nil {
		t.Fatal("expected dial to fail for an unauthenticated request")
	}
}

// TestWebSocketAuthorMismatchNacked: a connection authenticated as one participant
// may not submit deltas authored by another — the server nacks, and the optimistic
// client surfaces it as a fatal error.
func TestWebSocketAuthorMismatchNacked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	srv := &transport.Server{WaveMap: wm}
	hs := httptest.NewServer(srv.WebSocketHandler(headerIdentify))
	defer hs.Close()

	// Authenticate the connection as alice, but author deltas as bob.
	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, bob)
	defer a.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := a.Submit(writeBlip(bob, "b", chars("hi"))); err != nil {
		t.Fatalf("submit (optimistic apply): %v", err)
	}
	// The nack arrives asynchronously; the client goes fatal rather than reaching v1.
	err := a.WaitServerVersion(1)
	if err == nil {
		t.Fatal("expected a fatal error from the author-mismatch nack")
	}
	if !strings.Contains(err.Error(), "author") {
		t.Errorf("error = %v, want one mentioning the author mismatch", err)
	}
}

// TestWebSocketServerShutdownDrains: Shutdown cancels a live WebSocket session and
// returns once it has drained.
func TestWebSocketServerShutdownDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	srv := &transport.Server{WaveMap: wm}
	hs := httptest.NewServer(srv.WebSocketHandler(headerIdentify))
	defer hs.Close()

	// A pessimistic Client does not reconnect, so the session stays single and
	// Shutdown's drain is unambiguous.
	conn, err := transport.DialWebSocket(ctx, wsURL(hs.URL),
		&websocket.DialOptions{HTTPHeader: http.Header{"X-Test-User": {alice.Address()}}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cl := transport.NewClient(conn, name, alice)
	defer cl.Close()
	if err := cl.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if srv.Metrics().ActiveSessions.Load() != 1 {
		t.Fatalf("active sessions = %d, want 1", srv.Metrics().ActiveSessions.Load())
	}

	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	if err := srv.Shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := srv.Metrics().ActiveSessions.Load(); got != 0 {
		t.Errorf("active sessions after shutdown = %d, want 0", got)
	}
}

// TestWebSocketShutdownRacesDials hammers Shutdown concurrently with a storm of
// dialing clients to exercise the wsWG-Add / registerWS / Shutdown-Wait ordering.
// Run with -race; the point is no WaitGroup-misuse panic and a clean final drain
// regardless of how dials and Shutdowns interleave.
func TestWebSocketShutdownRacesDials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	srv := &transport.Server{WaveMap: wm}
	hs := httptest.NewServer(srv.WebSocketHandler(headerIdentify))
	defer hs.Close()

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := transport.DialWebSocket(ctx, wsURL(hs.URL),
				&websocket.DialOptions{HTTPHeader: http.Header{"X-Test-User": {alice.Address()}}})
			if err != nil {
				return // a dial that lost the race to a Shutdown is fine
			}
			cl := transport.NewClient(conn, name, alice)
			_ = cl.Open()
			_ = cl.Close()
		}()
	}
	// Drain repeatedly while the dial storm is in flight.
	for range 4 {
		sctx, sc := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(sctx)
		sc()
	}
	wg.Wait()

	// After the storm, a final drain must settle everything.
	sctx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := srv.Shutdown(sctx); err != nil {
		t.Fatalf("final shutdown: %v", err)
	}
	if got := srv.Metrics().ActiveSessions.Load(); got != 0 {
		t.Errorf("active sessions after final shutdown = %d, want 0", got)
	}
}
