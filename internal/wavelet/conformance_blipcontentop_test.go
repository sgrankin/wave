package wavelet_test

// Forward-apply conformance port of
//   wave/.../model/operation/wave/BlipContentOperationTest.java
//
// Java testApply: a BlipContentOperation(context, characters("Hello")) applied
// to a blip authored by jane with an empty contributor set and EMPTY_DOCUMENT
// content (1) reaches the document — the blip's content becomes "Hello" — and
// (2) makes the op creator (fred) a contributor (the default contributor method
// is ADD).
//
// Java applies the op directly to a BlipData; our Go model applies it inside a
// wavelet delta via Data.ApplyDelta. To reproduce the Java fixture's blip
// (author=jane, empty contributors) — which our model otherwise can only birth
// from a content op (auto-creating the blip with author=creator,
// contributors=[creator]) — we seed the blip through the public snapshot API
// (FromState), then apply the tested op.
//
// testReverseRestoresContent is op INVERSION (applyAndReturnReverse) and is out
// of scope — see conformance_skipped_test.go.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// seededBlipWavelet builds a wavelet at version 0 holding one pre-existing blip
// (id, author, empty contributors, EMPTY_DOCUMENT content, lmt/lmv = 0) via the
// public snapshot round-trip. This is the closest faithful analogue of Java's
// waveletData.createDocument(id, author, noParticipants, EMPTY_DOCUMENT, 0, 0).
func seededBlipWavelet(t *testing.T, blipID, author string) *wavelet.Data {
	t.Helper()
	base := mkWavelet(t)
	s := base.State()
	s.Blips = []wavelet.BlipSnapshot{{
		ID:                  blipID,
		Author:              author,
		Contributors:        nil, // noParticipants
		LastModifiedTime:    0,
		LastModifiedVersion: 0,
		Content:             op.EmptyDoc(),
	}}
	w, err := wavelet.FromState(s)
	if err != nil {
		t.Fatalf("FromState: %v", err)
	}
	return w
}

// TestConformanceBlipContentOperationApply ports BlipContentOperationTest.testApply.
func TestConformanceBlipContentOperationApply(t *testing.T) {
	fred := pid(t, "fred@example.com")
	jane := pid(t, "jane@example.com")
	w := seededBlipWavelet(t, "root", jane.Address())

	// context = new WaveletOperationContext(fred, CONTEXT_TIMESTAMP, 1L).
	const contextTimestamp int64 = 115 // LAST_MODIFIED_TIMESTAMP + 5 in OperationTestBase
	c := waveop.Context{Creator: fred, Timestamp: contextTimestamp, VersionIncrement: 1}
	docOp := op.NewDocOp([]op.Component{op.Characters{Text: "Hello"}})

	d := waveop.NewWaveletDelta(fred, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "root", BlipOp: waveop.BlipContentOperation{
			Ctx: c, ContentOp: docOp, Method: waveop.ContributorAdd}},
	})
	if err := w.ApplyDelta(d, []byte("d")); err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}

	blip, ok := w.Blip("root")
	if !ok {
		t.Fatal("blip root should still exist")
	}
	// (1) the op reached the document: content is now "Hello".
	if !blip.Content().Equal(docOp) {
		t.Errorf("content = %v, want \"Hello\"", blip.Content().Components())
	}
	// (2) editing makes the op creator (fred) a contributor — exactly {fred}.
	got := blip.Contributors()
	if len(got) != 1 || got[0] != fred {
		t.Errorf("contributors = %v, want exactly {fred}", got)
	}
}
