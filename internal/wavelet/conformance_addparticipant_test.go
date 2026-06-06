package wavelet_test

// Forward-apply conformance port of
//   wave/.../model/operation/wave/AddParticipantTest.java
//
// Java applies AddParticipant/RemoveParticipant directly to a WaveletData via
// op.apply(wavelet). Our Go model applies them inside a delta via
// Data.ApplyDelta; the participant-set side effects (success, duplicate-add
// error, remove-non-participant error) are the same. The Java suite's
// reverse-half cases (testReverseOfAddParticipantIsRemoveParticipant,
// testReverseOfRemoveParticipantIsAddParticipantWithPosition) test op INVERSION
// (applyAndReturnReverse), which is out of scope — see conformance_skipped_test.go.
//
// Java fixture: a wavelet at version 0 with an empty participant set. We build
// the equivalent via mkWavelet (see apply_test.go). Each AddParticipant /
// RemoveParticipant is delivered as a one-op delta authored by CREATOR.

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// addCtx mirrors AddParticipantTest.createContext():
// new WaveletOperationContext(CREATOR, -1L /*no timestamp*/, 1L).
func addCtx(creator id.ParticipantID) waveop.Context {
	return waveop.Context{Creator: creator, Timestamp: waveop.NoTimestamp, VersionIncrement: 1}
}

func applyAdd(t *testing.T, w *wavelet.Data, author, p id.ParticipantID) error {
	t.Helper()
	d := waveop.NewWaveletDelta(author, w.HashedVersion(), []waveop.Operation{
		waveop.AddParticipant{Ctx: addCtx(author), Participant: p},
	})
	return w.ApplyDelta(d, []byte("add"))
}

func applyRemove(t *testing.T, w *wavelet.Data, author, p id.ParticipantID) error {
	t.Helper()
	d := waveop.NewWaveletDelta(author, w.HashedVersion(), []waveop.Operation{
		waveop.RemoveParticipant{Ctx: addCtx(author), Participant: p},
	})
	return w.ApplyDelta(d, []byte("remove"))
}

func participantSet(w *wavelet.Data) map[id.ParticipantID]bool {
	m := make(map[id.ParticipantID]bool)
	for _, p := range w.Participants() {
		m[p] = true
	}
	return m
}

// TestConformanceAddManyParticipants ports AddParticipantTest.testAddManyParticipants:
// adding the creator then ten further distinct participants populates the set
// with exactly those, with no duplicates.
func TestConformanceAddManyParticipants(t *testing.T) {
	w := mkWavelet(t)
	creator := w.Creator()

	if len(w.Participants()) != 0 {
		t.Fatalf("new wavelet should start with no participants, got %d", len(w.Participants()))
	}

	want := map[id.ParticipantID]bool{}

	if err := applyAdd(t, w, creator, creator); err != nil {
		t.Fatalf("add creator: %v", err)
	}
	want[creator] = true
	if got := participantSet(w); !sameParticipantSet(got, want) {
		t.Fatalf("after adding creator: participants = %v, want %v", got, want)
	}

	for i := 0; i < 10; i++ {
		p := pid(t, "abc"+string(rune('0'+i))+"@example.com")
		if err := applyAdd(t, w, creator, p); err != nil {
			t.Fatalf("add participant %d: %v", i, err)
		}
		want[p] = true
	}
	if got := participantSet(w); !sameParticipantSet(got, want) {
		t.Errorf("after adding 10 participants: participants = %v, want %v", got, want)
	}
}

// TestConformanceCannotAddSameParticipantTwice ports
// AddParticipantTest.testCannotAddSameParticipantTwice: a second add of the same
// participant errors (Java throws OperationException), but adding a different
// participant afterwards still succeeds.
func TestConformanceCannotAddSameParticipantTwice(t *testing.T) {
	w := mkWavelet(t)
	creator := w.Creator()
	another := pid(t, "def@example.com")

	if err := applyAdd(t, w, creator, creator); err != nil {
		t.Fatalf("first add of creator: %v", err)
	}
	if !w.HasParticipant(creator) {
		t.Fatal("creator should be a participant after add")
	}

	if err := applyAdd(t, w, creator, creator); err == nil {
		t.Error("adding the same participant twice should error")
	}

	// Adding another participant after the rejected duplicate is still fine.
	if err := applyAdd(t, w, creator, another); err != nil {
		t.Errorf("adding a different participant should succeed: %v", err)
	}
	if !w.HasParticipant(another) {
		t.Error("the other participant should have been added")
	}
}

// TestConformanceCannotRemoveNonParticipant ports
// AddParticipantTest.testCannotRemoveNonParticipant: removing a participant who
// is absent errors; after adding then removing, a second remove errors again.
func TestConformanceCannotRemoveNonParticipant(t *testing.T) {
	w := mkWavelet(t)
	creator := w.Creator()

	// Remove with an empty participant set: error.
	if err := applyRemove(t, w, creator, creator); err == nil {
		t.Error("removing a participant from an empty set should error")
	}

	// Add then remove: ok, set is empty again.
	if err := applyAdd(t, w, creator, creator); err != nil {
		t.Fatalf("add creator: %v", err)
	}
	if err := applyRemove(t, w, creator, creator); err != nil {
		t.Fatalf("remove creator: %v", err)
	}
	if len(w.Participants()) != 0 {
		t.Errorf("participant set should be empty after add+remove, got %v", w.Participants())
	}

	// Remove twice in a row: error.
	if err := applyRemove(t, w, creator, creator); err == nil {
		t.Error("removing the same participant twice in a row should error")
	}
}

func sameParticipantSet(a, b map[id.ParticipantID]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
