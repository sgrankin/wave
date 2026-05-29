package cc

import (
	"errors"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
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

func newWavelet(t *testing.T) *wavelet.Data {
	t.Helper()
	waveID, _ := id.NewWaveID("example.com", "w+abc")
	waveletID, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(waveID, waveletID)
	return wavelet.New(waveID, waveletID, pid(t, "alice@example.com"), 0, version.Zero(name))
}

// blipEdit builds a WaveletBlipOperation editing blipID with contentOp.
func blipEdit(author id.ParticipantID, blipID string, contentOp op.DocOp) waveop.Operation {
	c := waveop.Context{Creator: author, Timestamp: 1, VersionIncrement: 1}
	return waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: contentOp}}
}

// applyOps applies ops to w as a delta targeting w's current version.
func applyOps(t *testing.T, w *wavelet.Data, author id.ParticipantID, ops []waveop.Operation, bytesID string) {
	t.Helper()
	d := waveop.NewWaveletDelta(author, w.HashedVersion(), ops)
	if err := w.ApplyDelta(d, []byte(bytesID)); err != nil {
		t.Fatalf("ApplyDelta(%s): %v", bytesID, err)
	}
}

// TestTransformOpListsConvergence checks delta-level TP1: with (b', a') =
// transform(B-client, A-server), applying A then b' converges with B then a'.
func TestTransformOpListsConvergence(t *testing.T) {
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	// Concurrent edits to blip "b" (auto-created): A inserts "X", B inserts "Y".
	aOps := []waveop.Operation{blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Characters{Text: "X"}}))}
	bOps := []waveop.Operation{blipEdit(bob, "b", op.NewDocOp([]op.Component{op.Characters{Text: "Y"}}))}

	bPrime, aPrime, err := transformOpLists(bOps, aOps) // B is client, A is server
	if err != nil {
		t.Fatalf("transformOpLists: %v", err)
	}

	server := newWavelet(t)
	applyOps(t, server, alice, aOps, "A")
	applyOps(t, server, bob, bPrime, "Bp")

	ref := newWavelet(t)
	applyOps(t, ref, bob, bOps, "B")
	applyOps(t, ref, alice, aPrime, "Ap")

	sb, _ := server.Blip("b")
	rb, _ := ref.Blip("b")
	if !sb.Content().Equal(rb.Content()) {
		t.Errorf("delta-level TP1 violated:\n server %v\n ref    %v", sb.Content().Components(), rb.Content().Components())
	}
	// Client-first tie-break: B (client) before A (server) => "YX".
	if !sb.Content().Equal(op.NewDocOp([]op.Component{op.Characters{Text: "YX"}})) {
		t.Errorf("converged content = %v, want YX", sb.Content().Components())
	}
}

// TestTransformToHead runs the server flow: apply A, then transform a concurrent
// B (targeting the old version) to head and apply it; the result converges.
func TestTransformToHead(t *testing.T) {
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	w := newWavelet(t)
	h := NewMemoryHistory(w.HashedVersion()) // zero signature

	v0 := w.HashedVersion()
	aOps := []waveop.Operation{blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Characters{Text: "X"}}))}
	applyOps(t, w, alice, aOps, "A")
	h.Append(TransformedWaveletDelta{Author: alice, ResultingVersion: w.HashedVersion(), Ops: aOps})

	// B was authored against v0, concurrent with A.
	bDelta := waveop.NewWaveletDelta(bob, v0, []waveop.Operation{
		blipEdit(bob, "b", op.NewDocOp([]op.Component{op.Characters{Text: "Y"}})),
	})
	transformed, err := TransformToHead(h, bDelta)
	if err != nil {
		t.Fatalf("TransformToHead: %v", err)
	}
	if transformed.TargetVersion().Compare(w.HashedVersion()) != 0 {
		t.Errorf("transformed delta targets %d, want current %d", transformed.TargetVersion().Version(), w.Version())
	}
	applyOps(t, w, bob, transformed.Ops(), "Bp")

	blip, _ := w.Blip("b")
	if !blip.Content().Equal(op.NewDocOp([]op.Component{op.Characters{Text: "YX"}})) {
		t.Errorf("server content after transform-to-head = %v, want YX", blip.Content().Components())
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2", w.Version())
	}
}

// A delta already targeting the current head needs no transform.
func TestTransformToHeadNoTransform(t *testing.T) {
	alice := pid(t, "alice@example.com")
	w := newWavelet(t)
	h := NewMemoryHistory(w.HashedVersion())
	d := waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Characters{Text: "Z"}})),
	})
	got, err := TransformToHead(h, d)
	if err != nil {
		t.Fatalf("TransformToHead: %v", err)
	}
	if len(got.Ops()) != 1 {
		t.Errorf("op count = %d, want 1 (unchanged)", len(got.Ops()))
	}
}

func TestTransformToHeadVersionErrors(t *testing.T) {
	alice := pid(t, "alice@example.com")
	w := newWavelet(t)
	h := NewMemoryHistory(w.HashedVersion())

	// Future version: targets v5 when head is v0.
	future := waveop.NewWaveletDelta(alice, version.NewHashedVersion(5, []byte("x")), nil)
	if _, err := TransformToHead(h, future); !isVersionError(err) {
		t.Errorf("future version: got %v, want VersionError", err)
	}

	// Same version number, wrong hash.
	wrongHash := waveop.NewWaveletDelta(alice, version.NewHashedVersion(0, []byte("not-the-zero-hash")), nil)
	if _, err := TransformToHead(h, wrongHash); !isVersionError(err) {
		t.Errorf("wrong hash: got %v, want VersionError", err)
	}
}

func isVersionError(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == VersionError
}

func errorCode(err error) ResponseCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return OK
}

// A target version whose delta has been pruned from history (but whose
// signature is still known) is TOO_OLD (recoverable), not VERSION_ERROR.
func TestTransformToHeadTooOld(t *testing.T) {
	alice := pid(t, "alice@example.com")
	w := newWavelet(t)
	h := NewMemoryHistory(w.HashedVersion())
	v0 := w.HashedVersion()

	// Apply two deltas so head is v2 and v0/v1/v2 are known signatures.
	op1 := []waveop.Operation{blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Characters{Text: "a"}}))}
	applyOps(t, w, alice, op1, "1")
	h.Append(TransformedWaveletDelta{Author: alice, ResultingVersion: w.HashedVersion(), Ops: op1})
	op2 := []waveop.Operation{blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "b"}}))}
	applyOps(t, w, alice, op2, "2")
	h.Append(TransformedWaveletDelta{Author: alice, ResultingVersion: w.HashedVersion(), Ops: op2})

	// Simulate a pruned prefix: the v0 signature is still known, but its delta
	// has been GC'd from the log.
	delete(h.deltas, 0)

	stale := waveop.NewWaveletDelta(alice, v0, []waveop.Operation{
		blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Characters{Text: "Z"}})),
	})
	_, err := TransformToHead(h, stale)
	if got := errorCode(err); got != TooOld {
		t.Errorf("pruned-prefix target: code = %v, want TooOld", got)
	}
}

// A client delta transformed across a multi-delta history chain converges with
// the equivalent client-side reconciliation.
func TestTransformToHeadMultiDeltaChain(t *testing.T) {
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	w := newWavelet(t)
	h := NewMemoryHistory(w.HashedVersion())
	v0 := w.HashedVersion()

	// Two server deltas land first: insert "a", then append "b" => "ab".
	s1 := []waveop.Operation{blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Characters{Text: "a"}}))}
	applyOps(t, w, alice, s1, "s1")
	h.Append(TransformedWaveletDelta{Author: alice, ResultingVersion: w.HashedVersion(), Ops: s1})
	s2 := []waveop.Operation{blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "b"}}))}
	applyOps(t, w, alice, s2, "s2")
	h.Append(TransformedWaveletDelta{Author: alice, ResultingVersion: w.HashedVersion(), Ops: s2})

	// Bob, still at v0, inserts "Z" at the front. Transform across both deltas.
	bob0 := waveop.NewWaveletDelta(bob, v0, []waveop.Operation{
		blipEdit(bob, "b", op.NewDocOp([]op.Component{op.Characters{Text: "Z"}})),
	})
	transformed, err := TransformToHead(h, bob0)
	if err != nil {
		t.Fatalf("TransformToHead: %v", err)
	}
	if transformed.TargetVersion().Version() != 2 {
		t.Fatalf("transformed targets v%d, want v2", transformed.TargetVersion().Version())
	}
	applyOps(t, w, bob, transformed.Ops(), "bp")

	// Bob is the client (tie-break first) at v0, so "Z" precedes the server "ab".
	blip, _ := w.Blip("b")
	if !blip.Content().Equal(op.NewDocOp([]op.Component{op.Characters{Text: "Zab"}})) {
		t.Errorf("converged content = %v, want Zab", blip.Content().Components())
	}
	if w.Version() != 3 {
		t.Errorf("version = %d, want 3", w.Version())
	}
}

// Append must reject a non-contiguous delta (silent corruption guard).
func TestMemoryHistoryNonContiguousAppendPanics(t *testing.T) {
	alice := pid(t, "alice@example.com")
	w := newWavelet(t)
	h := NewMemoryHistory(w.HashedVersion())
	defer func() {
		if recover() == nil {
			t.Error("Append should panic on a non-contiguous delta")
		}
	}()
	// A delta claiming to apply at v5 when current is v0.
	h.Append(TransformedWaveletDelta{
		Author:           alice,
		ResultingVersion: version.NewHashedVersion(6, []byte("h")),
		Ops:              []waveop.Operation{blipEdit(alice, "b", op.NewDocOp([]op.Component{op.Characters{Text: "x"}}))},
	})
}
