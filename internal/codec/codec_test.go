package codec_test

import (
	"bytes"
	"testing"

	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

func sp(s string) *string { return &s }

func attrs(t *testing.T, m map[string]string) op.Attributes {
	t.Helper()
	a, err := op.NewAttributes(m)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// allComponents builds a DocOp exercising every component type (not necessarily
// well-formed — codec doesn't validate structure, only round-trips it).
func allComponents(t *testing.T) op.DocOp {
	t.Helper()
	up, err := op.NewAttributesUpdate([]op.AttributeChange{{Name: "d", OldValue: sp("5"), NewValue: sp("6")}})
	if err != nil {
		t.Fatal(err)
	}
	ann, err := op.NewAnnotationBoundaryMap([]string{"k1"}, []op.AnnotationChange{{Key: "k2", OldValue: sp("a"), NewValue: sp("b")}})
	if err != nil {
		t.Fatal(err)
	}
	return op.NewDocOp([]op.Component{
		op.Retain{Count: 3},
		op.Characters{Text: "hi"},
		op.ElementStart{Type: "x", Attributes: attrs(t, map[string]string{"a": "1"})},
		op.ElementEnd{},
		op.DeleteCharacters{Text: "bye"},
		op.DeleteElementStart{Type: "y", Attributes: attrs(t, map[string]string{"b": "2"})},
		op.DeleteElementEnd{},
		op.ReplaceAttributes{OldAttributes: attrs(t, map[string]string{"c": "3"}), NewAttributes: attrs(t, map[string]string{"c": "4"})},
		op.UpdateAttributes{Update: up},
		op.AnnotationBoundary{Boundary: ann},
	})
}

func TestDocOpRoundTrip(t *testing.T) {
	d := allComponents(t)
	enc := codec.EncodeDocOp(d)

	got, err := codec.DecodeDocOp(enc)
	if err != nil {
		t.Fatalf("DecodeDocOp: %v", err)
	}
	if !got.Equal(d) {
		t.Errorf("decoded DocOp != original\n got %v\n want %v", got.Components(), d.Components())
	}
	// Re-encoding the decoded op must reproduce identical bytes (faithful round-trip).
	if !bytes.Equal(enc, codec.EncodeDocOp(got)) {
		t.Error("re-encode after decode produced different bytes")
	}
}

func TestEncodingDeterministic(t *testing.T) {
	d := allComponents(t)
	if !bytes.Equal(codec.EncodeDocOp(d), codec.EncodeDocOp(d)) {
		t.Error("encoding the same DocOp twice produced different bytes")
	}
}

func TestStoredDeltaRoundTrip(t *testing.T) {
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	ctx := func(c id.ParticipantID) waveop.Context {
		return waveop.Context{Creator: c, Timestamp: 1234, VersionIncrement: 1}
	}
	ops := []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{
			Ctx: ctx(alice), ContentOp: allComponents(t), Method: waveop.ContributorAdd}},
		waveop.AddParticipant{Ctx: ctx(alice), Participant: bob},
		waveop.RemoveParticipant{Ctx: ctx(alice), Participant: bob},
		waveop.NoOp{Ctx: ctx(alice)},
	}
	sd := codec.StoredDelta{
		Author:           alice,
		ResultingVersion: version.NewHashedVersion(7, []byte("hashbytes")),
		Timestamp:        9999,
		Ops:              ops,
	}
	enc := codec.EncodeStoredDelta(sd)
	got, err := codec.DecodeStoredDelta(enc)
	if err != nil {
		t.Fatalf("DecodeStoredDelta: %v", err)
	}
	if got.Author != alice {
		t.Errorf("author = %v, want alice", got.Author)
	}
	if got.ResultingVersion.Compare(sd.ResultingVersion) != 0 {
		t.Errorf("resulting version mismatch")
	}
	if got.Timestamp != 9999 {
		t.Errorf("timestamp = %d, want 9999", got.Timestamp)
	}
	if len(got.Ops) != 4 {
		t.Fatalf("op count = %d, want 4", len(got.Ops))
	}
	// Spot-check the blip op and that re-encoding is byte-identical.
	wb, ok := got.Ops[0].(waveop.WaveletBlipOperation)
	if !ok || wb.BlipID != "b+1" {
		t.Errorf("op[0] = %T (%v), want WaveletBlipOperation b+1", got.Ops[0], got.Ops[0])
	}
	if !bytes.Equal(enc, codec.EncodeStoredDelta(got)) {
		t.Error("re-encode after decode produced different bytes")
	}
}

// Field-level round-trip of a Context carrying a HashedVersion (the
// HashedVersion-present branch is otherwise unexercised) plus BlipContentOperation.Method.
func TestContextAndMethodRoundTrip(t *testing.T) {
	alice := pid(t, "alice@example.com")
	hv := version.NewHashedVersion(42, []byte("twenty-byte-history!"))
	ctx := waveop.Context{Creator: alice, Timestamp: 555, VersionIncrement: 3, HashedVersion: &hv}
	sd := codec.StoredDelta{
		Author:           alice,
		ResultingVersion: version.NewHashedVersion(42, []byte("rh")),
		Timestamp:        1,
		Ops: []waveop.Operation{
			waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{
				Ctx: ctx, ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "z"}}), Method: waveop.ContributorRemove}},
		},
	}
	got, err := codec.DecodeStoredDelta(codec.EncodeStoredDelta(sd))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wb := got.Ops[0].(waveop.WaveletBlipOperation)
	bc := wb.BlipOp.(waveop.BlipContentOperation)
	if bc.Method != waveop.ContributorRemove {
		t.Errorf("Method = %v, want ContributorRemove", bc.Method)
	}
	gc := bc.Ctx
	if gc.Creator != alice {
		t.Errorf("Creator = %v, want alice", gc.Creator)
	}
	if gc.Timestamp != 555 || gc.VersionIncrement != 3 {
		t.Errorf("ctx scalars = (%d,%d), want (555,3)", gc.Timestamp, gc.VersionIncrement)
	}
	if gc.HashedVersion == nil {
		t.Fatal("HashedVersion dropped on round-trip")
	}
	if gc.HashedVersion.Compare(hv) != 0 {
		t.Errorf("HashedVersion mismatch: got %v", gc.HashedVersion)
	}
}

// Empty attributes and empty annotation end-keys must round-trip and encode
// identically across re-encode (single empty-collection representation).
func TestEmptyCollectionsRoundTrip(t *testing.T) {
	none := attrs(t, nil)
	// Annotation boundary with only a change (no end keys) and a nil-valued change.
	ann, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: "k", OldValue: nil, NewValue: sp("v")}})
	if err != nil {
		t.Fatal(err)
	}
	d := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: none}, // empty attributes
		op.AnnotationBoundary{Boundary: ann},         // empty end keys
		op.ElementEnd{},
	})
	enc := codec.EncodeDocOp(d)
	got, err := codec.DecodeDocOp(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Equal(d) {
		t.Errorf("empty-collection round-trip changed the op")
	}
	if !bytes.Equal(enc, codec.EncodeDocOp(got)) {
		t.Error("empty-collection re-encode not byte-stable")
	}
}

func TestDecodeRejectsTruncatedInput(t *testing.T) {
	// A one-component op whose component array carries only the kind (here
	// cReplaceAttributes) and no operands: must error, not panic.
	truncated := []byte{0x81, 0x81, 0x07} // [ [7] ] — kind 7 = replaceAttributes, missing operands
	if _, err := codec.DecodeDocOp(truncated); err == nil {
		t.Error("DecodeDocOp should error on a truncated component, not panic")
	}
	// Garbage bytes.
	if _, err := codec.DecodeStoredDelta([]byte{0xff, 0x00, 0x12}); err == nil {
		t.Error("DecodeStoredDelta should error on garbage input")
	}
}

func TestHashBytesDeterministicAndChains(t *testing.T) {
	alice := pid(t, "alice@example.com")
	ops := []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{
			Ctx: waveop.Context{Creator: alice, Timestamp: 1, VersionIncrement: 1}, ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "x"}})}},
	}
	b1 := codec.HashBytes(alice, 0, 1000, ops)
	b2 := codec.HashBytes(alice, 0, 1000, ops)
	if !bytes.Equal(b1, b2) {
		t.Fatal("HashBytes not deterministic")
	}
	// Different applied-at version => different bytes.
	if bytes.Equal(b1, codec.HashBytes(alice, 1, 1000, ops)) {
		t.Error("HashBytes ignored the applied-at version")
	}
	// Feeds the version chain deterministically.
	waveID, _ := id.NewWaveID("example.com", "w+a")
	waveletID, _ := id.NewWaveletID("example.com", "conv+root")
	zero := version.Zero(id.NewWaveletName(waveID, waveletID))
	h1 := version.Apply(zero, b1, uint64(len(ops)))
	h2 := version.Apply(zero, b2, uint64(len(ops)))
	if h1.Compare(h2) != 0 {
		t.Error("hash chain not deterministic over identical delta bytes")
	}
	if h1.Version() != 1 {
		t.Errorf("resulting version = %d, want 1", h1.Version())
	}
}
