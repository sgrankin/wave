package wavelet_test

// Forward-apply conformance port of
//   wave/.../model/operation/wave/WaveletOperationTest.java
//
// Java tests WaveletOperation.apply()'s metadata side effects against a
// WaveletData using a NoOp carrying various contexts: timestamp update (iff the
// context has a timestamp), version advance, and hashed-version ("signature")
// update. Our Go model delivers the NoOp inside a one-op delta via
// Data.ApplyDelta.
//
// VERSION BASIS DIFFERENCE: Java's WaveletOperation.update() advances the
// wavelet version by context.getVersionIncrement() (and only when it is
// non-zero). Our Go ApplyDelta advances by OP COUNT (one version per op),
// ignoring Context.VersionIncrement (see internal/wavelet/apply.go). The two
// agree for the common case (one op, increment 1), which testOpUpdatesVersion
// exercises; they disagree only for increment 0 / increment != op-count, which
// the Java suite does not assert version on (testOpWithoutTimestampDoesntUpdate*
// only checks timestamp).
//
// Skipped Java cases (CONFORMANCE DIVERGENCE / out of scope) are recorded as
// t.Skip below.

import (
	"bytes"
	"testing"

	"github.com/sgrankin/wave/internal/waveop"
)

// TestConformanceOpUpdatesTimestamp ports WaveletOperationTest.testOpUpdatesTimestamp:
// a NoOp whose context carries a timestamp updates the wavelet's last-modified
// time to that timestamp.
func TestConformanceOpUpdatesTimestamp(t *testing.T) {
	w := mkWavelet(t)
	creator := w.Creator()
	// new WaveletOperationContext(creator, CONTEXT_TIMESTAMP, 0).
	c := waveop.Context{Creator: creator, Timestamp: opContextTimestamp, VersionIncrement: 0}
	d := waveop.NewWaveletDelta(creator, w.HashedVersion(), []waveop.Operation{waveop.NoOp{Ctx: c}})
	if err := w.ApplyDelta(d, []byte("d")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if w.LastModifiedTime() != opContextTimestamp {
		t.Errorf("last-modified time = %d, want %d", w.LastModifiedTime(), opContextTimestamp)
	}
}

// TestConformanceOpWithoutTimestampDoesntUpdateTimestamp ports
// WaveletOperationTest.testOpWithoutTimestampDoesntUpdateTimestamp: a NoOp whose
// context has NO_TIMESTAMP leaves the last-modified time unchanged.
func TestConformanceOpWithoutTimestampDoesntUpdateTimestamp(t *testing.T) {
	w := mkWavelet(t)
	creator := w.Creator()
	oldTimestamp := w.LastModifiedTime()
	// new WaveletOperationContext(creator, NO_TIMESTAMP, 0).
	c := waveop.Context{Creator: creator, Timestamp: waveop.NoTimestamp, VersionIncrement: 0}
	d := waveop.NewWaveletDelta(creator, w.HashedVersion(), []waveop.Operation{waveop.NoOp{Ctx: c}})
	if err := w.ApplyDelta(d, []byte("d")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if w.LastModifiedTime() != oldTimestamp {
		t.Errorf("last-modified time = %d, want %d (unchanged)", w.LastModifiedTime(), oldTimestamp)
	}
}

// TestConformanceOpUpdatesVersion ports WaveletOperationTest.testOpUpdatesVersion:
// applying an op advances the wavelet version by one. (Java uses
// versionIncrement=1; our op-count basis yields the same +1 for one op.)
func TestConformanceOpUpdatesVersion(t *testing.T) {
	w := mkWavelet(t)
	creator := w.Creator()
	oldVersion := w.Version()
	// new WaveletOperationContext(creator, NO_TIMESTAMP, 1).
	c := waveop.Context{Creator: creator, Timestamp: waveop.NoTimestamp, VersionIncrement: 1}
	d := waveop.NewWaveletDelta(creator, w.HashedVersion(), []waveop.Operation{waveop.NoOp{Ctx: c}})
	if err := w.ApplyDelta(d, []byte("d")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if w.Version() != oldVersion+1 {
		t.Errorf("version = %d, want %d (old + 1)", w.Version(), oldVersion+1)
	}
}

// TestConformanceOpAdvancesHashedVersion is the Go analogue of
// WaveletOperationTest.testOpUpdatesSignature. The Java assertion
// (waveletData.getHashedVersion() == context.getHashedVersion()) is a
// CONFORMANCE DIVERGENCE — see the skip below — but the underlying invariant
// that applying a delta advances the hashed version is real and worth asserting.
func TestConformanceOpAdvancesHashedVersion(t *testing.T) {
	w := mkWavelet(t)
	creator := w.Creator()
	before := w.HashedVersion()
	c := waveop.Context{Creator: creator, Timestamp: waveop.NoTimestamp, VersionIncrement: 1}
	d := waveop.NewWaveletDelta(creator, before, []waveop.Operation{waveop.NoOp{Ctx: c}})
	if err := w.ApplyDelta(d, []byte("sig-bytes")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if w.HashedVersion().Version() != before.Version()+1 {
		t.Errorf("hashed version = %d, want %d", w.HashedVersion().Version(), before.Version()+1)
	}
	if bytes.Equal(w.HashedVersion().HistoryHash(), before.HistoryHash()) {
		t.Error("history hash should advance after applying a delta")
	}
}

// TestConformanceOpUpdatesSignature: CONFORMANCE DIVERGENCE.
//
// Java testOpUpdatesSignature asserts the wavelet adopts the hashed version
// CARRIED IN the operation's context (context.getHashedVersion()). Our Go model
// does not honor Context.HashedVersion at all: ApplyDelta always COMPUTES the
// next hashed version from the serialized delta bytes via version.Apply (the
// hashed version is authoritative and derived, never set from wire metadata).
// Honoring a context-supplied hashed version would let a delta dictate the
// wavelet's history hash, which our design forbids. There is no way to make the
// resulting hashed version equal CONTEXT_HASHED_VERSION without weakening the
// production invariant, so this case is skipped, not ported.
func TestConformanceOpUpdatesSignature(t *testing.T) {
	t.Skip("CONFORMANCE DIVERGENCE: Java sets the wavelet's hashed version from " +
		"context.getHashedVersion(); our ApplyDelta always computes it from the " +
		"serialized delta via version.Apply and ignores Context.HashedVersion. The " +
		"derived-hash invariant is intentional — see internal/wavelet/apply.go.")
}

// TestConformanceOpWithoutSignatureDoesntUpdateSignature: CONFORMANCE DIVERGENCE.
//
// Java testOpWithoutSignatureDoesntUpdateSignature asserts the hashed version is
// UNCHANGED when the op's context carries no hashed version. Our Go ApplyDelta
// ALWAYS advances the hashed version (via version.Apply over the delta bytes),
// regardless of any context metadata — the hash chain is unconditional. So this
// case cannot hold without weakening the production invariant.
func TestConformanceOpWithoutSignatureDoesntUpdateSignature(t *testing.T) {
	t.Skip("CONFORMANCE DIVERGENCE: Java leaves the hashed version unchanged when " +
		"the context has none; our ApplyDelta always advances the hashed version " +
		"over the serialized delta bytes (unconditional hash chain). See " +
		"TestConformanceOpAdvancesHashedVersion for the Go invariant.")
}

// TestConformanceCreateReverseContextReversesContext: out of scope.
//
// WaveletOperationTest.testCreateReverseContextReversesContext exercises op
// INVERSION (createReverseContext), which is not ported — see the task's SKIP
// list and waveop/conformance_skipped_test.go.
func TestConformanceCreateReverseContextReversesContext(t *testing.T) {
	t.Skip("OUT OF SCOPE: createReverseContext is op inversion (reverse/undo half), " +
		"which is not ported. Only forward-apply semantics are in scope.")
}
