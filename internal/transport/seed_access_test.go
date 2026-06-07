package transport_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

// newWSServer starts an httptest WebSocket server for srv, resolving identity
// from the test header (see headerIdentify), and registers its teardown.
func newWSServer(t *testing.T, srv *transport.Server) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(srv.WebSocketHandler(headerIdentify))
	t.Cleanup(hs.Close)
	return hs
}

// seedFn returns the conversation-seed function for a Server (fixed timestamp).
func seedFn(opener id.ParticipantID) ([]waveop.Operation, error) {
	return conv.SeedConversation(opener, 1000)
}

// addParticipant is a single AddParticipant op (the invite path in strict mode).
func addParticipant(author, p id.ParticipantID) []waveop.Operation {
	return []waveop.Operation{waveop.AddParticipant{
		Ctx: waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}, Participant: p}}
}

// TestWebSocketSeedsConversation: opening a brand-new wavelet on an authenticated
// connection seeds the conversation server-side (manifest + root blip), and the
// opener becomes the first participant — the client sees a ready conversation with
// no client-side bootstrap.
func TestWebSocketSeedsConversation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	srv := &transport.Server{WaveMap: wm, Seed: seedFn}
	hs := newWSServer(t, srv)

	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, alice)
	defer a.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := a.WaitServerVersion(3); err != nil {
		t.Fatalf("seed settle v3: %v", err)
	}

	man, ok := a.BlipContent(conv.ManifestDocumentID)
	if !ok {
		t.Fatal("conversation manifest was not seeded")
	}
	m, err := conv.ReadManifest(man)
	if err != nil {
		t.Fatalf("read seeded manifest: %v", err)
	}
	if len(m.RootThread.Blips) != 1 || m.RootThread.Blips[0].ID != conv.RootBlipID {
		t.Errorf("root thread = %+v, want exactly one blip %q", m.RootThread.Blips, conv.RootBlipID)
	}
	if _, ok := a.BlipContent(conv.RootBlipID); !ok {
		t.Error("root blip was not seeded")
	}

	c, err := wm.Container(name)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	if w := c.Wavelet(); w == nil || !w.HasParticipant(alice) {
		t.Errorf("opener %s is not the seeded wavelet's participant", alice)
	}
}

// TestWebSocketSeedNoDoubleSeed: two authenticated clients racing the first Open
// of the same empty wavelet must not both seed — exactly one seed delta is
// applied (version 3, one root blip), proving SeedIfEmpty's atomic check.
func TestWebSocketSeedNoDoubleSeed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	srv := &transport.Server{WaveMap: wm, Seed: seedFn} // no access checker: dev-permissive
	hs := newWSServer(t, srv)

	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, alice)
	defer a.Close()
	b := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, bob), name, bob)
	defer b.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a.Open() }()
	go func() { defer wg.Done(); _ = b.Open() }()
	wg.Wait()

	if err := a.WaitServerVersion(3); err != nil {
		t.Fatalf("alice settle v3: %v", err)
	}
	if err := b.WaitServerVersion(3); err != nil {
		t.Fatalf("bob settle v3: %v", err)
	}
	c, err := wm.Container(name)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	if got := c.Version().Version(); got != 3 {
		t.Errorf("wavelet version = %d, want 3 (a second seed would make it 6)", got)
	}
	man, ok := a.BlipContent(conv.ManifestDocumentID)
	if !ok {
		t.Fatal("manifest missing after concurrent open")
	}
	m, _ := conv.ReadManifest(man)
	if len(m.RootThread.Blips) != 1 {
		t.Errorf("root thread has %d blips, want 1 (double seed?)", len(m.RootThread.Blips))
	}
}

// TestWebSocketStrictAccessDenied: with a strict AccessChecker, a non-member may
// not open an existing wavelet — the Open fails with an access-denied error
// surfaced through the client (no hang).
func TestWebSocketStrictAccessDenied(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	srv := &transport.Server{WaveMap: wm, Seed: seedFn, Access: transport.MembershipChecker{WaveMap: wm}}
	hs := newWSServer(t, srv)

	// Alice opens the fresh wavelet: open-or-create seeds it and makes her a member.
	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, alice)
	defer a.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("alice open: %v", err)
	}
	if err := a.WaitServerVersion(3); err != nil {
		t.Fatalf("alice seed: %v", err)
	}

	// Bob is not a participant: his open must be denied.
	b := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, bob), name, bob)
	defer b.Close()
	err := b.Open()
	if err == nil {
		t.Fatal("expected bob's open to be denied")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error = %v, want one mentioning access denied", err)
	}
}

// TestWebSocketStrictAccessAfterAdd: once a member adds a participant, that
// participant may open the wavelet (the invite path under strict access).
func TestWebSocketStrictAccessAfterAdd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	srv := &transport.Server{WaveMap: wm, Seed: seedFn, Access: transport.MembershipChecker{WaveMap: wm}}
	hs := newWSServer(t, srv)

	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, alice)
	defer a.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("alice open: %v", err)
	}
	if err := a.WaitServerVersion(3); err != nil {
		t.Fatalf("alice seed: %v", err)
	}
	// Alice invites bob.
	if err := a.Submit(addParticipant(alice, bob)); err != nil {
		t.Fatalf("add participant: %v", err)
	}
	if err := a.WaitServerVersion(4); err != nil {
		t.Fatalf("settle add: %v", err)
	}

	// Bob is now a member: his open succeeds and he converges.
	b := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, bob), name, bob)
	defer b.Close()
	if err := b.Open(); err != nil {
		t.Fatalf("bob open after add: %v", err)
	}
	if err := b.WaitServerVersion(4); err != nil {
		t.Fatalf("bob settle: %v", err)
	}
}

// TestWebSocketStrictOpenOrCreateNoSeed: with strict access but seeding disabled,
// the first opener of a never-created wavelet is still allowed (open-or-create via
// the empty-wavelet branch), and the wavelet stays empty until a client submits.
func TestWebSocketStrictOpenOrCreateNoSeed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wm := newWaveMap(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	srv := &transport.Server{WaveMap: wm, Access: transport.MembershipChecker{WaveMap: wm}} // no seed
	hs := newWSServer(t, srv)

	a := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, alice), name, alice)
	defer a.Close()
	if err := a.Open(); err != nil {
		t.Fatalf("first open of empty wavelet should be allowed (open-or-create): %v", err)
	}
	c, err := wm.Container(name)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	if c.Wavelet() != nil {
		t.Error("wavelet should not exist until a delta is submitted (no seed)")
	}
}
