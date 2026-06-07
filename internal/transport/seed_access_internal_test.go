package transport

import (
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// authServeConn wires a client pipe to srv on an authenticated session bound to
// participant (the WebSocket path's authParticipant, reproduced over a pipe).
func authServeConn(t *testing.T, srv *Server, participant id.ParticipantID) net.Conn {
	t.Helper()
	cConn, sConn := net.Pipe()
	go func() { _ = srv.serveConn(sConn, &participant) }()
	t.Cleanup(func() { cConn.Close() })
	return cConn
}

// TestResyncAccessDenied: a non-member must not bypass the Open membership gate by
// reconnecting via Resync. handleResync must enforce access just like handleOpen.
func TestResyncAccessDenied(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))

	w, _ := id.NewWaveID("example.com", "w+abc")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(w, wl)
	alice, _ := id.NewParticipantID("alice@example.com")
	bob, _ := id.NewParticipantID("bob@example.com")

	// Seed the wavelet with alice as its only member.
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := conv.SeedConversation(alice, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeedIfEmpty(alice, ops); err != nil {
		t.Fatal(err)
	}

	srv := &Server{WaveMap: wm, Access: MembershipChecker{WaveMap: wm}}

	// Bob (a non-member) resyncs at version zero — the reconnect path. It must be
	// denied with an error frame, not served the wavelet.
	rc := authServeConn(t, srv, bob)
	mustWrite(t, rc, encodeResync(name.Serialize(), 0, version.Zero(name).HistoryHash()))
	kind, raw, err := messageKind(mustRead(t, rc))
	if err != nil {
		t.Fatalf("read resync response: %v", err)
	}
	if kind != mError {
		t.Fatalf("resync by non-member returned kind %d, want an error frame (%d)", kind, mError)
	}
	msg, _ := decodeError(raw)
	if !strings.Contains(msg, "access denied") {
		t.Errorf("error = %q, want one mentioning access denied", msg)
	}
}

// TestMembershipCheckerRaceWithSubmit: CanAccess must read the participant set
// under the container lock, so it does not race a concurrent Submit mutating it.
// Run with -race; the membership read and the participant-set write must not be
// flagged. (Regression for the off-lock c.Wavelet() read in MembershipChecker.)
func TestMembershipCheckerRaceWithSubmit(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))

	w, _ := id.NewWaveID("example.com", "w+abc")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(w, wl)
	alice, _ := id.NewParticipantID("alice@example.com")

	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	// Create the wavelet at v1 (alice authors a blip; she becomes creator).
	blip := waveop.WaveletBlipOperation{BlipID: "b", BlipOp: waveop.BlipContentOperation{
		Ctx:       waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1},
		ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "hi"}}),
	}}
	if _, err := c.Submit(waveop.NewWaveletDelta(alice, version.Zero(name), []waveop.Operation{blip})); err != nil {
		t.Fatal(err)
	}

	checker := MembershipChecker{WaveMap: wm}
	var wg sync.WaitGroup
	wg.Add(2)
	// Writer: append edits (mutates wavelet/blip state under the container lock).
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			ed := waveop.WaveletBlipOperation{BlipID: "b", BlipOp: waveop.BlipContentOperation{
				Ctx:       waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1},
				ContentOp: op.NewDocOp([]op.Component{op.Retain{Count: 2 + i}, op.Characters{Text: "x"}}),
			}}
			_, _ = c.Submit(waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{ed}))
		}
	}()
	// Reader: membership checks concurrent with the writes.
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if _, err := checker.CanAccess(alice, name); err != nil {
				t.Errorf("CanAccess: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// TestResyncAllowedForMember: a member resyncs successfully (the access check
// does not block legitimate reconnects).
func TestResyncAllowedForMember(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))

	w, _ := id.NewWaveID("example.com", "w+abc")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(w, wl)
	alice, _ := id.NewParticipantID("alice@example.com")

	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := conv.SeedConversation(alice, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeedIfEmpty(alice, ops); err != nil {
		t.Fatal(err)
	}
	head := c.Version()

	srv := &Server{WaveMap: wm, Access: MembershipChecker{WaveMap: wm}}
	rc := authServeConn(t, srv, alice)
	mustWrite(t, rc, encodeResync(name.Serialize(), head.Version(), head.HistoryHash()))
	kind, _, err := messageKind(mustRead(t, rc))
	if err != nil {
		t.Fatalf("read resync response: %v", err)
	}
	if kind != mResyncResponse {
		t.Fatalf("member resync returned kind %d, want a resync response (%d)", kind, mResyncResponse)
	}
}
