package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

// removeParticipant is a single RemoveParticipant op.
func removeParticipant(author, p id.ParticipantID) []waveop.Operation {
	return []waveop.Operation{waveop.RemoveParticipant{
		Ctx: waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}, Participant: p}}
}

// editRootBlip inserts text into the seeded root blip (<body><line/></body>):
// retain past the body+line markers, insert, retain the closing </body>.
func editRootBlip(author id.ParticipantID, text string) []waveop.Operation {
	edit := op.NewDocOp([]op.Component{op.Retain{Count: 3}, op.Characters{Text: text}, op.Retain{Count: 1}})
	return []waveop.Operation{waveop.WaveletBlipOperation{BlipID: conv.RootBlipID, BlipOp: waveop.BlipContentOperation{
		Ctx: waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}, ContentOp: edit}}}
}

// TestWebSocketRemovedParticipantStreamCutOff is the access-control guard: once a
// participant is removed, edits made AFTER the removal must never reach them. Their
// live stream is cut at the removal boundary and a reconnect+resync is denied by the
// membership check, so a post-removal edit is unreachable. (Whether the removal delta
// itself lands before the cut is best-effort and not asserted here.)
func TestWebSocketRemovedParticipantStreamCutOff(t *testing.T) {
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
		t.Fatalf("seed settle: %v", err)
	}
	if err := a.Submit(addParticipant(alice, bob)); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	if err := a.WaitServerVersion(4); err != nil {
		t.Fatalf("settle add: %v", err)
	}

	// Bob joins as a member and converges to v4.
	b := transport.NewOptimisticClient(wsDialAs(ctx, hs.URL, bob), name, bob)
	defer b.Close()
	if err := b.Open(); err != nil {
		t.Fatalf("bob open: %v", err)
	}
	if err := b.WaitServerVersion(4); err != nil {
		t.Fatalf("bob settle: %v", err)
	}

	// Alice removes bob (v5), then edits the root blip (v6). Bob's stream is cut at
	// the removal, so the v6 edit must never reach him.
	if err := a.Submit(removeParticipant(alice, bob)); err != nil {
		t.Fatalf("remove bob: %v", err)
	}
	if err := a.WaitServerVersion(5); err != nil {
		t.Fatalf("settle remove: %v", err)
	}
	if err := a.Submit(editRootBlip(alice, "secret")); err != nil {
		t.Fatalf("post-removal edit: %v", err)
	}
	if err := a.WaitServerVersion(6); err != nil {
		t.Fatalf("settle edit: %v", err)
	}

	// Across several reconnect+resync cycles (reconnectDelay is 100ms), all denied by
	// the membership check, bob must never advance to v6 (the post-removal edit).
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.Version().Version() >= 6 {
			t.Fatalf("removed participant received a post-removal edit (v%d) — stream not cut off", b.Version().Version())
		}
		time.Sleep(25 * time.Millisecond)
	}
}
