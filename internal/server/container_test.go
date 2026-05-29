package server_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func waveletName(t *testing.T) id.WaveletName {
	t.Helper()
	w, _ := id.NewWaveID("example.com", "w+abc")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	return id.NewWaveletName(w, wl)
}

// newContainer creates a fresh on-disk store and an empty container for it.
func newContainer(t *testing.T) (*server.WaveletContainer, storage.DeltasAccess, id.WaveletName) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	name := waveletName(t)
	access, err := store.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFixed(time.UnixMilli(1000))
	c, err := server.Load(name, access, clk)
	if err != nil {
		t.Fatal(err)
	}
	return c, access, name
}

func blipDelta(author id.ParticipantID, target version.HashedVersion, blipID string, content op.DocOp) waveop.WaveletDelta {
	o := waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{
		Ctx: waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}, ContentOp: content}}
	return waveop.NewWaveletDelta(author, target, []waveop.Operation{o})
}

func chars(s string) op.DocOp { return op.NewDocOp([]op.Component{op.Characters{Text: s}}) }

// appendText retains `retain` items then inserts s (an edit that appends to an
// existing document of length `retain`).
func appendText(retain int, s string) op.DocOp {
	return op.NewDocOp([]op.Component{op.Retain{Count: retain}, op.Characters{Text: s}})
}

// creationDelta is a realistic wave-creation first delta: it adds the author as a
// participant and edits a blip — exercising that the creator is added by the
// delta (not pre-added), so no duplicate-add occurs.
func creationDelta(author id.ParticipantID, target version.HashedVersion, blipID string, content op.DocOp) waveop.WaveletDelta {
	c := waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}
	return waveop.NewWaveletDelta(author, target, []waveop.Operation{
		waveop.AddParticipant{Ctx: c, Participant: author},
		waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: content}},
	})
}

func TestSubmitCreatesAndApplies(t *testing.T) {
	c, access, name := newContainer(t)
	alice := pid(t, "alice@example.com")

	// A first delta that adds the creator AND edits a blip (the normal shape).
	res, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.OpsApplied != 2 || res.ResultingVersion.Version() != 2 {
		t.Errorf("result = %+v, want 2 ops @ v2", res)
	}
	w := c.Wavelet()
	if w == nil || !w.HasParticipant(alice) {
		t.Error("wavelet should exist with alice added (by the delta) as a participant")
	}
	blip, ok := w.Blip("b")
	if !ok || !blip.Content().Equal(chars("hi")) {
		t.Errorf("blip content not 'hi'")
	}
	// Persisted: one record in storage.
	all, _ := access.ReadAll()
	if len(all) != 1 {
		t.Errorf("storage has %d records, want 1", len(all))
	}
}

// A delta whose op carries VersionIncrement != 1 must advance the wavelet by op
// count (1), not by the increment — otherwise the version diverges from the
// history's op-count basis (which previously panicked on append).
func TestVersionIncrementUsesOpCount(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	o := waveop.WaveletBlipOperation{BlipID: "b", BlipOp: waveop.BlipContentOperation{
		Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 3}, ContentOp: chars("hi")}}
	res, err := c.Submit(waveop.NewWaveletDelta(alice, version.Zero(name), []waveop.Operation{o}))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.OpsApplied != 1 || res.ResultingVersion.Version() != 1 {
		t.Errorf("result = %+v, want 1 op @ v1 (op count, not VersionIncrement 3)", res)
	}
}

// An empty first delta is a no-op that must NOT materialize a phantom wavelet
// (which would disagree with a reloaded, empty container).
func TestEmptyFirstDeltaNoPhantom(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	res, err := c.Submit(waveop.NewWaveletDelta(alice, version.Zero(name), nil))
	if err != nil {
		t.Fatalf("submit empty first delta: %v", err)
	}
	if res.OpsApplied != 0 {
		t.Errorf("ops applied = %d, want 0", res.OpsApplied)
	}
	if c.Wavelet() != nil {
		t.Error("an empty first delta must not create a wavelet")
	}
	if c.Version().Compare(version.Zero(name)) != 0 {
		t.Error("version should remain at zero")
	}
}

func TestConcurrentSubmitsConverge(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	zero := version.Zero(name)

	// Alice submits at v0 -> v1 (inserts "X").
	if _, err := c.Submit(blipDelta(alice, zero, "b", chars("X"))); err != nil {
		t.Fatalf("submit A: %v", err)
	}
	// Bob, still at v0 (stale), submits "Y" -> transformed to head -> v2.
	resB, err := c.Submit(blipDelta(bob, zero, "b", chars("Y")))
	if err != nil {
		t.Fatalf("submit B: %v", err)
	}
	if resB.ResultingVersion.Version() != 2 {
		t.Errorf("B resulting version = %d, want 2", resB.ResultingVersion.Version())
	}
	// Client-first tie-break: Bob (the submitting client) before Alice -> "YX".
	blip, _ := c.Wavelet().Blip("b")
	if !blip.Content().Equal(chars("YX")) {
		t.Errorf("converged content = %v, want YX", blip.Content().Components())
	}
}

func TestLoadReplaysState(t *testing.T) {
	c, access, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	zero := version.Zero(name)

	if _, err := c.Submit(blipDelta(alice, zero, "b", chars("hi"))); err != nil {
		t.Fatal(err)
	}
	v1 := c.Version()
	// Append a second edit.
	if _, err := c.Submit(blipDelta(alice, v1, "b", op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "!"}}))); err != nil {
		t.Fatal(err)
	}
	want := c.Version()

	// Reload from the same persisted log.
	c2, err := server.Load(name, access, clock.NewFixed(time.UnixMilli(5000)))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if c2.Version().Compare(want) != 0 {
		t.Errorf("reloaded version v%d != v%d", c2.Version().Version(), want.Version())
	}
	blip, ok := c2.Wavelet().Blip("b")
	if !ok || !blip.Content().Equal(chars("hi!")) {
		t.Errorf("reloaded content not 'hi!'")
	}
}

func TestNoOpSubmit(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	if _, err := c.Submit(blipDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatal(err)
	}
	before := c.Version()
	// A delta with no operations is a no-op: 0 ops applied, version unchanged.
	res, err := c.Submit(waveop.NewWaveletDelta(alice, before, nil))
	if err != nil {
		t.Fatalf("no-op submit: %v", err)
	}
	if res.OpsApplied != 0 {
		t.Errorf("ops applied = %d, want 0", res.OpsApplied)
	}
	if c.Version().Compare(before) != 0 {
		t.Error("no-op submit advanced the version")
	}
}

// A delta resent at the version its original was applied at (same author + ops)
// is a double-submit: it must return the original result idempotently, not be
// applied again.
func TestDoubleSubmitDedup(t *testing.T) {
	c, access, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	zero := version.Zero(name)

	first := blipDelta(alice, zero, "b", chars("hi"))
	r1, err := c.Submit(first)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// Resubmit the identical delta targeting the same (still-valid) version.
	r2, err := c.Submit(blipDelta(alice, zero, "b", chars("hi")))
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if r2.ResultingVersion.Compare(r1.ResultingVersion) != 0 {
		t.Errorf("resubmit version v%d, want the original v%d", r2.ResultingVersion.Version(), r1.ResultingVersion.Version())
	}
	if r2.OpsApplied != 0 {
		t.Errorf("resubmit applied %d ops, want 0 (idempotent)", r2.OpsApplied)
	}
	// History/storage must hold exactly one delta — the duplicate was not applied.
	if all, _ := access.ReadAll(); len(all) != 1 {
		t.Errorf("storage has %d deltas after a double-submit, want 1", len(all))
	}
	if c.Version().Compare(r1.ResultingVersion) != 0 {
		t.Errorf("version advanced past v%d on a double-submit", r1.ResultingVersion.Version())
	}
}

// A DIFFERENT delta targeting an old (superseded) version is NOT a duplicate —
// it must be transformed to head and applied (not deduped away).
func TestNonDuplicateAtOldVersionApplies(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	zero := version.Zero(name)

	if _, err := c.Submit(blipDelta(alice, zero, "b", chars("X"))); err != nil {
		t.Fatal(err)
	}
	// A different delta still targeting v0 (stale) → transformed to head, applied.
	r, err := c.Submit(blipDelta(alice, zero, "b", chars("Y")))
	if err != nil {
		t.Fatalf("stale distinct submit: %v", err)
	}
	if r.OpsApplied != 1 || r.ResultingVersion.Version() != 2 {
		t.Errorf("distinct stale delta = %+v, want 1 op @ v2 (applied, not deduped)", r)
	}
}

func TestVersionMismatchRejected(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")
	if _, err := c.Submit(blipDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatal(err)
	}
	// Submit targeting version 0 with the WRONG hash (unknown signature).
	bad := blipDelta(alice, version.NewHashedVersion(0, []byte("not-the-zero-hash")), "b", chars("z"))
	_, err := c.Submit(bad)
	var ce *cc.Error
	if !errors.As(err, &ce) || ce.Code != cc.VersionError {
		t.Errorf("got %v, want VersionError", err)
	}
}
