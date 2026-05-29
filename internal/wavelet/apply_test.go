package wavelet_test

import (
	"bytes"
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

func mkWavelet(t *testing.T) *wavelet.Data {
	t.Helper()
	waveID, err := id.NewWaveID("example.com", "w+abc")
	if err != nil {
		t.Fatal(err)
	}
	waveletID, err := id.NewWaveletID("example.com", "conv+root")
	if err != nil {
		t.Fatal(err)
	}
	name := id.NewWaveletName(waveID, waveletID)
	return wavelet.New(waveID, waveletID, pid(t, "alice@example.com"), 1000, version.Zero(name))
}

func ctx(creator id.ParticipantID) waveop.Context {
	return waveop.Context{Creator: creator, Timestamp: 2000, VersionIncrement: 1}
}

func TestNewWavelet(t *testing.T) {
	w := mkWavelet(t)
	if w.Version() != 0 {
		t.Errorf("version = %d, want 0", w.Version())
	}
	if !w.HasParticipant(pid(t, "alice@example.com")) {
		t.Error("creator should be a participant")
	}
	if len(w.BlipIDs()) != 0 {
		t.Error("new wavelet should have no blips")
	}
}

func TestApplyAddParticipant(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	bob := pid(t, "bob@example.com")
	before := w.HashedVersion()
	delta := waveop.NewWaveletDelta(alice, before, []waveop.Operation{
		waveop.AddParticipant{Ctx: ctx(alice), Participant: bob},
	})
	if err := w.ApplyDelta(delta, []byte("d1")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if !w.HasParticipant(bob) {
		t.Error("bob should be a participant after add")
	}
	if w.Version() != 1 {
		t.Errorf("version = %d, want 1", w.Version())
	}
	if bytes.Equal(w.HashedVersion().HistoryHash(), before.HistoryHash()) {
		t.Error("history hash should change after applying a delta")
	}
	// Re-adding an existing participant is rejected (Java AddParticipant.doApply
	// throws), and the wavelet is left unchanged.
	if err := w.ApplyDelta(waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.AddParticipant{Ctx: ctx(alice), Participant: bob},
	}), []byte("d2")); err == nil {
		t.Error("re-adding an existing participant should error")
	}
	if w.Version() != 1 {
		t.Errorf("version = %d after rejected add, want 1 (unchanged)", w.Version())
	}
}

func TestApplyRemoveNonexistentErrors(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	delta := waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.RemoveParticipant{Ctx: ctx(alice), Participant: pid(t, "ghost@example.com")},
	})
	if err := w.ApplyDelta(delta, []byte("d")); err == nil {
		t.Error("removing a non-participant should error")
	}
	if w.Version() != 0 {
		t.Errorf("version = %d after rejected remove, want 0", w.Version())
	}
}

func TestApplyComposeFailureErrors(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	// Create blip with "hi".
	w.ApplyDelta(waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{
			Ctx: ctx(alice), ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "hi"}})}},
	}), []byte("d1"))

	// An op whose input length (5) does not match the content length (2) must
	// surface as an error, leaving content and version unchanged.
	bad := op.NewDocOp([]op.Component{op.Retain{Count: 5}})
	err := w.ApplyDelta(waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{Ctx: ctx(alice), ContentOp: bad}},
	}), []byte("d2"))
	if err == nil {
		t.Fatal("applying a length-mismatched op should error")
	}
	blip, _ := w.Blip("b+1")
	if !blip.Content().Equal(op.NewDocOp([]op.Component{op.Characters{Text: "hi"}})) {
		t.Errorf("content changed despite error: %v", blip.Content().Components())
	}
	if w.Version() != 1 {
		t.Errorf("version = %d after failed apply, want 1 (unchanged)", w.Version())
	}
}

func TestApplyUnworthyEditDoesNotUpdateMetadata(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	bob := pid(t, "bob@example.com")
	// alice creates blip "b+1" with "hi" (worthy) at version 1.
	w.ApplyDelta(waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{
			Ctx: ctx(alice), ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "hi"}})}},
	}), []byte("d1"))

	// bob applies an unworthy edit (a presence annotation only) — metadata must
	// not change: bob is not added as a contributor and lastModifiedVersion stays 1.
	uval := "online"
	presence, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: "user/r/bob", NewValue: &uval}})
	if err != nil {
		t.Fatal(err)
	}
	endPresence, _ := op.NewAnnotationBoundaryMap([]string{"user/r/bob"}, nil)
	unworthy := op.NewDocOp([]op.Component{
		op.AnnotationBoundary{Boundary: presence},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: endPresence},
	})
	if err := w.ApplyDelta(waveop.NewWaveletDelta(bob, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{Ctx: ctx(bob), ContentOp: unworthy}},
	}), []byte("d2")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	blip, _ := w.Blip("b+1")
	for _, c := range blip.Contributors() {
		if c == bob {
			t.Error("bob made an unworthy edit and should not be a contributor")
		}
	}
	if blip.LastModifiedVersion() != 1 {
		t.Errorf("lastModifiedVersion = %d after unworthy edit, want 1 (unchanged)", blip.LastModifiedVersion())
	}
	if w.Version() != 2 {
		t.Errorf("wavelet version = %d, want 2 (the op still advances the wavelet)", w.Version())
	}
}

func TestApplyContributorMethods(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	bob := pid(t, "bob@example.com")
	carol := pid(t, "carol@example.com")
	edit := func(author id.ParticipantID, method waveop.UpdateContributorMethod, content op.DocOp, bytesID string) {
		t.Helper()
		c := waveop.Context{Creator: author, Timestamp: 2000, VersionIncrement: 1}
		d := waveop.NewWaveletDelta(author, w.HashedVersion(), []waveop.Operation{
			waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: content, Method: method}},
		})
		if err := w.ApplyDelta(d, []byte(bytesID)); err != nil {
			t.Fatalf("ApplyDelta %s: %v", bytesID, err)
		}
	}
	contains := func(set []id.ParticipantID, p id.ParticipantID) bool {
		for _, e := range set {
			if e == p {
				return true
			}
		}
		return false
	}

	edit(alice, waveop.ContributorAdd, op.NewDocOp([]op.Component{op.Characters{Text: "a"}}), "1")
	edit(bob, waveop.ContributorAdd, op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "b"}}), "2")
	edit(carol, waveop.ContributorNone, op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "c"}}), "3")
	edit(bob, waveop.ContributorRemove, op.NewDocOp([]op.Component{op.Retain{Count: 3}, op.Characters{Text: "d"}}), "4")

	blip, _ := w.Blip("b+1")
	c := blip.Contributors()
	if !contains(c, alice) {
		t.Error("alice (author + add) should be a contributor")
	}
	if contains(c, carol) {
		t.Error("carol used ContributorNone and should not be a contributor")
	}
	if contains(c, bob) {
		t.Error("bob was added then removed (ContributorRemove) and should not be a contributor")
	}
}

func TestApplyRemoveParticipant(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	bob := pid(t, "bob@example.com")
	w.ApplyDelta(waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.AddParticipant{Ctx: ctx(alice), Participant: bob},
	}), []byte("d1"))
	w.ApplyDelta(waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.RemoveParticipant{Ctx: ctx(alice), Participant: bob},
	}), []byte("d2"))
	if w.HasParticipant(bob) {
		t.Error("bob should be removed")
	}
	if w.HasParticipant(alice) == false {
		t.Error("alice should remain")
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2", w.Version())
	}
}

func TestApplyBlipOpCreatesAndComposes(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	// First edit: insert "hi" into the (auto-created, empty) blip.
	op1 := op.NewDocOp([]op.Component{op.Characters{Text: "hi"}})
	d1 := waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{Ctx: ctx(alice), ContentOp: op1}},
	})
	if err := w.ApplyDelta(d1, []byte("d1")); err != nil {
		t.Fatalf("ApplyDelta d1: %v", err)
	}
	blip, ok := w.Blip("b+1")
	if !ok {
		t.Fatal("blip b+1 should have been created")
	}
	if !blip.Content().Equal(op.NewDocOp([]op.Component{op.Characters{Text: "hi"}})) {
		t.Errorf("content = %v, want \"hi\"", blip.Content().Components())
	}
	if blip.Author() != alice {
		t.Errorf("author = %v, want alice", blip.Author())
	}
	if blip.LastModifiedVersion() != 1 {
		t.Errorf("lastModifiedVersion = %d, want 1", blip.LastModifiedVersion())
	}

	// Second edit: append "!" — composes onto "hi" to give "hi!".
	op2 := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "!"}})
	d2 := waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{Ctx: ctx(alice), ContentOp: op2}},
	})
	if err := w.ApplyDelta(d2, []byte("d2")); err != nil {
		t.Fatalf("ApplyDelta d2: %v", err)
	}
	blip, _ = w.Blip("b+1")
	if !blip.Content().Equal(op.NewDocOp([]op.Component{op.Characters{Text: "hi!"}})) {
		t.Errorf("content = %v, want \"hi!\"", blip.Content().Components())
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2", w.Version())
	}
	if blip.LastModifiedVersion() != 2 {
		t.Errorf("lastModifiedVersion = %d, want 2", blip.LastModifiedVersion())
	}
}

func TestApplyDeltaHashChainDeterministic(t *testing.T) {
	alice := pid(t, "alice@example.com")
	mk := func() *wavelet.Data { return mkWavelet(t) }
	delta := func(w *wavelet.Data) waveop.WaveletDelta {
		return waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
			waveop.AddParticipant{Ctx: ctx(alice), Participant: pid(t, "bob@example.com")},
		})
	}
	w1, w2 := mk(), mk()
	if err := w1.ApplyDelta(delta(w1), []byte("same-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := w2.ApplyDelta(delta(w2), []byte("same-bytes")); err != nil {
		t.Fatal(err)
	}
	// Same base, same bytes, same op count => identical hashed version.
	if w1.HashedVersion().Compare(w2.HashedVersion()) != 0 {
		t.Error("hash chain is not deterministic for identical inputs")
	}
	// Matches a direct version.Apply computation.
	base := mk()
	want := version.Apply(base.HashedVersion(), []byte("same-bytes"), 1)
	if w1.HashedVersion().Compare(want) != 0 {
		t.Error("hashed version does not match version.Apply")
	}
}

func TestApplyMultiOpDelta(t *testing.T) {
	w := mkWavelet(t)
	alice := w.Creator()
	bob := pid(t, "bob@example.com")
	contentOp := op.NewDocOp([]op.Component{op.Characters{Text: "x"}})
	delta := waveop.NewWaveletDelta(alice, w.HashedVersion(), []waveop.Operation{
		waveop.AddParticipant{Ctx: ctx(alice), Participant: bob},
		waveop.WaveletBlipOperation{BlipID: "b+1", BlipOp: waveop.BlipContentOperation{Ctx: ctx(alice), ContentOp: contentOp}},
	})
	if err := w.ApplyDelta(delta, []byte("d")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2 (two ops)", w.Version())
	}
	if !w.HasParticipant(bob) {
		t.Error("bob should be added")
	}
	if _, ok := w.Blip("b+1"); !ok {
		t.Error("b+1 should be created")
	}
	if got := w.HashedVersion().Version(); got != delta.ResultingVersion() {
		t.Errorf("wavelet version %d != delta.ResultingVersion %d", got, delta.ResultingVersion())
	}
}
