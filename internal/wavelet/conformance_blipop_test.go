package wavelet_test

// Forward-apply conformance port of
//   wave/.../model/operation/wave/BlipOperationTest.java
//
// Java tests the shared BlipOperation.apply() metadata path against a BlipData:
// timestamp/last-modified-version updates and the three contributor methods
// (ADD / REMOVE / NONE). Java applies the op directly to a BlipData created by
// waveletData.createDocument("blipid", fred, noParticipants,
// ModelTestUtils.createContent(""), 0, 0) — i.e. author=fred, EMPTY contributor
// set, content "<body><line></line></body>", on a wavelet at version 0.
//
// Our Go model applies blip ops inside a wavelet delta and otherwise births a
// blip from its first content op (author=creator, contributors=[creator]); to
// reproduce Java's pre-existing blip with an EMPTY contributor set we seed it via
// the public snapshot API (FromState).
//
// Skipped Java cases (out of scope, see conformance_skipped_test.go):
//   - testGetContext, testApplyInvokesSubclassDoApply, testReverseAddContributor*
//     test the FakeBlipOperation harness / op INVERSION, not forward-apply
//     state. getContext is already ported in waveop/conformance_blipop_test.go.

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// OperationTestBase constants.
const (
	creationTimestamp     int64 = 100
	lastModifiedTimestamp int64 = creationTimestamp + 10    // 110
	opContextTimestamp    int64 = lastModifiedTimestamp + 5 // 115 (CONTEXT_TIMESTAMP)
)

// lineContent builds "<body><line></line></body>", the document
// ModelTestUtils.createContent("") yields, as an insertion-only DocOp.
func lineContent(t *testing.T) op.DocOp {
	t.Helper()
	empty, err := op.NewAttributes(nil)
	if err != nil {
		t.Fatal(err)
	}
	d := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body", Attributes: empty},
		op.ElementStart{Type: "line", Attributes: empty},
		op.ElementEnd{},
		op.ElementEnd{},
	})
	if !d.IsInitialization() {
		t.Fatal("lineContent should be an insertion-only initialization")
	}
	return d
}

// sampleContentOp builds BlipOperationTest.createSampleContentOperation's op:
// a worthy change that replaces the inner <line/> element but leaves the
// document content unchanged. It composes against "<body><line></line></body>":
// retain <body>, delete <line></line>, insert a fresh <line></line>, retain
// </body>.
func sampleContentOp(t *testing.T) op.DocOp {
	t.Helper()
	empty, err := op.NewAttributes(nil)
	if err != nil {
		t.Fatal(err)
	}
	o := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.DeleteElementStart{Type: "line", Attributes: empty},
		op.DeleteElementEnd{},
		op.ElementStart{Type: "line", Attributes: empty},
		op.ElementEnd{},
		op.Retain{Count: 1},
	})
	if !waveop.IsWorthyChange(o) {
		t.Fatal("sample content op should be a worthy change")
	}
	return o
}

// seededLineBlip builds a wavelet at version 0 with one pre-existing blip
// "blipid" authored by fred, with an EMPTY contributor set, content
// "<body><line></line></body>", and lmt/lmv = 0 — Java's createBlipData fixture.
func seededLineBlip(t *testing.T) *wavelet.Data {
	t.Helper()
	base := mkWavelet(t)
	s := base.State()
	s.Blips = []wavelet.BlipSnapshot{{
		ID:                  "blipid",
		Author:              "fred@example.com",
		Contributors:        nil,
		LastModifiedTime:    0,
		LastModifiedVersion: 0,
		Content:             lineContent(t),
	}}
	w, err := wavelet.FromState(s)
	if err != nil {
		t.Fatalf("FromState: %v", err)
	}
	return w
}

func applySample(t *testing.T, w *wavelet.Data, author id.ParticipantID, timestamp int64, method waveop.UpdateContributorMethod, bytesID string) {
	t.Helper()
	c := waveop.Context{Creator: author, Timestamp: timestamp, VersionIncrement: 1}
	d := waveop.NewWaveletDelta(author, w.HashedVersion(), []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "blipid", BlipOp: waveop.BlipContentOperation{
			Ctx: c, ContentOp: sampleContentOp(t), Method: method}},
	})
	if err := w.ApplyDelta(d, []byte(bytesID)); err != nil {
		t.Fatalf("ApplyDelta %s: %v", bytesID, err)
	}
}

func blipContains(b *wavelet.BlipData, p id.ParticipantID) bool {
	for _, c := range b.Contributors() {
		if c == p {
			return true
		}
	}
	return false
}

// TestConformanceBlipApplyUpdatesTimestamp ports BlipOperationTest.testApplyUpdatesTimestamp:
// applying a worthy content op sets the blip's last-modified time to the op's
// context timestamp and its last-modified version to waveletVersion + 1.
func TestConformanceBlipApplyUpdatesTimestamp(t *testing.T) {
	fred := pid(t, "fred@example.com")
	w := seededLineBlip(t)

	blip, _ := w.Blip("blipid")
	if blip.LastModifiedTime() == opContextTimestamp {
		t.Fatal("precondition: blip lmt should differ from the op timestamp")
	}
	oldWaveletVersion := w.Version() // 0

	applySample(t, w, fred, opContextTimestamp, waveop.ContributorAdd, "1")

	blip, _ = w.Blip("blipid")
	if blip.LastModifiedTime() != opContextTimestamp {
		t.Errorf("blip lmt = %d, want %d (context timestamp)", blip.LastModifiedTime(), opContextTimestamp)
	}
	// Java: data.getLastModifiedVersion() == waveletData.getVersion() + 1.
	// Our op-count basis gives the same value for a single op from version 0.
	if blip.LastModifiedVersion() != oldWaveletVersion+1 {
		t.Errorf("blip lmv = %d, want %d (wavelet version + 1)", blip.LastModifiedVersion(), oldWaveletVersion+1)
	}
}

// TestConformanceBlipNoneContributorMethodLeavesContributors ports
// BlipOperationTest.testNoneContributorMethodLeavesContributors: a worthy edit
// with method NONE does not alter the contributor list (stays empty).
func TestConformanceBlipNoneContributorMethodLeavesContributors(t *testing.T) {
	fred := pid(t, "fred@example.com")
	w := seededLineBlip(t)

	applySample(t, w, fred, opContextTimestamp, waveop.ContributorNone, "1")

	blip, _ := w.Blip("blipid")
	if len(blip.Contributors()) != 0 {
		t.Errorf("contributors = %v, want empty (NONE method)", blip.Contributors())
	}
}

// TestConformanceBlipAddContributorMethodAddsNewContributor ports
// BlipOperationTest.testAddContributorMethodAddsNewContributor: an ADD edit by
// fred makes fred a contributor; a subsequent ADD edit by jane adds jane while
// keeping fred.
func TestConformanceBlipAddContributorMethodAddsNewContributor(t *testing.T) {
	fred := pid(t, "fred@example.com")
	jane := pid(t, "jane@example.com")
	w := seededLineBlip(t)

	applySample(t, w, fred, opContextTimestamp, waveop.ContributorAdd, "1")
	blip, _ := w.Blip("blipid")
	if !blipContains(blip, fred) {
		t.Error("fred should be a contributor after his ADD edit")
	}

	// createJaneContext() = new WaveletOperationContext(jane, 42L, 1L).
	applySample(t, w, jane, 42, waveop.ContributorAdd, "2")
	blip, _ = w.Blip("blipid")
	if !blipContains(blip, fred) {
		t.Error("fred should remain a contributor after jane's edit")
	}
	if !blipContains(blip, jane) {
		t.Error("jane should be a contributor after her ADD edit")
	}
}

// TestConformanceBlipAddContributorMethodDoesntDuplicateContributors ports
// BlipOperationTest.testAddContributorMethodDoesntDuplicateContributors: two ADD
// edits by fred leave exactly {fred}.
func TestConformanceBlipAddContributorMethodDoesntDuplicateContributors(t *testing.T) {
	fred := pid(t, "fred@example.com")
	w := seededLineBlip(t)

	applySample(t, w, fred, opContextTimestamp, waveop.ContributorAdd, "1")
	applySample(t, w, fred, opContextTimestamp, waveop.ContributorAdd, "2")

	blip, _ := w.Blip("blipid")
	got := blip.Contributors()
	if len(got) != 1 || got[0] != fred {
		t.Errorf("contributors = %v, want exactly {fred}", got)
	}
}
