package waveop_test

// Ported from
//   wave/.../model/operation/wave/OperationEqualityTest.java
//   wave/.../model/operation/core/CoreWaveletOperationEqualsTest.java
//
// Both Java suites build a set of distinct operations and assert the full
// pairwise equality matrix: op[i].equals(op[j]) iff i == j. We express
// operation equality with waveop.EqualOps (the package's payload comparison,
// which ignores Context — Java's WaveletOperation.equals likewise compares only
// the operation payload, not the context). Each op is wrapped in a one-element
// slice so EqualOps applies.
//
// Adaptation notes:
//   - SubmitBlip and VersionUpdateOp (OperationEqualityTest rows 8-10) are not
//     present in our op set; those rows are dropped. See skipped[] in the report.
//   - The Java "core" op family (CoreNoOp / CoreAddParticipant /
//     CoreRemoveParticipant / CoreWaveletDocumentOperation) maps onto our single
//     op set: NoOp, AddParticipant, RemoveParticipant, and a document mutation
//     carried by WaveletBlipOperation{BlipContentOperation}. The behavioral
//     intent — distinct types are unequal, same type distinguishes its payload —
//     is preserved.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

// checkEqualityMatrix asserts EqualOps([ops[i]], [ops[j]]) == (i == j) for all
// pairs. Mirrors the nested loops in both Java equality suites.
func checkEqualityMatrix(t *testing.T, ops []waveop.Operation) {
	t.Helper()
	for i := range ops {
		for j := range ops {
			got := waveop.EqualOps([]waveop.Operation{ops[i]}, []waveop.Operation{ops[j]})
			want := i == j
			if got != want {
				t.Errorf("EqualOps(ops[%d]=%v, ops[%d]=%v) = %v, want %v",
					i, ops[i], j, ops[j], got, want)
			}
		}
	}
}

func TestConformanceOperationEquality(t *testing.T) {
	fred := pid(t, "fred@example.com")
	jane := pid(t, "jane@example.com")
	c := waveop.Context{Creator: fred, Timestamp: 175, VersionIncrement: 1}
	// Java compares bare BlipContentOperations; we wrap each in a
	// WaveletBlipOperation with the same blip id so the doc-op payload is what
	// distinguishes them.
	bc := func(text string) waveop.Operation {
		return waveop.WaveletBlipOperation{
			BlipID: "b",
			BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: text}})},
		}
	}
	ops := []waveop.Operation{
		waveop.AddParticipant{Ctx: c, Participant: fred},
		waveop.AddParticipant{Ctx: c, Participant: jane},
		bc("Hello"),
		bc("World"),
		waveop.NoOp{Ctx: c},
		waveop.RemoveParticipant{Ctx: c, Participant: fred},
		waveop.RemoveParticipant{Ctx: c, Participant: jane},
	}
	checkEqualityMatrix(t, ops)
}

// TestConformanceCoreWaveletOperationEqualsTypes ports
// CoreWaveletOperationEqualsTest.testTypes: distinct op TYPES are pairwise
// unequal, each equal only to itself.
func TestConformanceCoreWaveletOperationEqualsTypes(t *testing.T) {
	empty := pid(t, "x@example.com")
	c := ctx(empty)
	a := waveop.NoOp{Ctx: c}
	b := waveop.AddParticipant{Ctx: c, Participant: empty}
	cc := waveop.RemoveParticipant{Ctx: c, Participant: empty}
	d := waveop.WaveletBlipOperation{
		BlipID: "",
		BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: op.EmptyDoc()},
	}
	checkEqualityMatrix(t, []waveop.Operation{a, b, cc, d})
}

// TestConformanceCoreWaveletOperationEqualsAddParticipant ports
// CoreWaveletOperationEqualsTest.testAddParticipant: AddParticipant distinguishes
// by participant, ignores context.
func TestConformanceCoreWaveletOperationEqualsAddParticipant(t *testing.T) {
	a := pid(t, "a@example.com")
	b := pid(t, "b@example.com")
	a1 := waveop.AddParticipant{Ctx: ctx(a), Participant: a}
	a2 := waveop.AddParticipant{Ctx: ctx(b), Participant: a} // same participant, different ctx
	b1 := waveop.AddParticipant{Ctx: ctx(a), Participant: b}
	if !waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{a2}) {
		t.Error("AddParticipant(a) should equal AddParticipant(a) regardless of context")
	}
	if waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{b1}) {
		t.Error("AddParticipant(a) should not equal AddParticipant(b)")
	}
}

// TestConformanceCoreWaveletOperationEqualsRemoveParticipant ports
// CoreWaveletOperationEqualsTest.testRemoveParticipant.
func TestConformanceCoreWaveletOperationEqualsRemoveParticipant(t *testing.T) {
	a := pid(t, "a@example.com")
	b := pid(t, "b@example.com")
	a1 := waveop.RemoveParticipant{Ctx: ctx(a), Participant: a}
	a2 := waveop.RemoveParticipant{Ctx: ctx(b), Participant: a}
	b1 := waveop.RemoveParticipant{Ctx: ctx(a), Participant: b}
	if !waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{a2}) {
		t.Error("RemoveParticipant(a) should equal RemoveParticipant(a) regardless of context")
	}
	if waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{b1}) {
		t.Error("RemoveParticipant(a) should not equal RemoveParticipant(b)")
	}
}

// TestConformanceCoreWaveletOperationEqualsDocumentId ports
// CoreWaveletOperationEqualsTest.testWaveletDocumentOperationDocumentId: a doc
// mutation distinguishes by blip (document) id for identical content.
func TestConformanceCoreWaveletOperationEqualsDocumentId(t *testing.T) {
	c := ctx(pid(t, "a@example.com"))
	d := op.NewDocOp([]op.Component{op.Characters{Text: "a"}})
	docOp := func(blipID string) waveop.Operation {
		return waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: d}}
	}
	a1, a2 := docOp("a"), docOp("a")
	b1 := docOp("b")
	if !waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{a2}) {
		t.Error("same blip id + content should be equal")
	}
	if waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{b1}) {
		t.Error("different blip id should not be equal")
	}
}

// TestConformanceCoreWaveletOperationEqualsDocOp ports
// CoreWaveletOperationEqualsTest.testWaveletDocumentOperationDocOp: a doc
// mutation distinguishes by content op for identical blip id.
func TestConformanceCoreWaveletOperationEqualsDocOp(t *testing.T) {
	c := ctx(pid(t, "a@example.com"))
	da := op.NewDocOp([]op.Component{op.Characters{Text: "a"}})
	db := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "a"}})
	docOp := func(content op.DocOp) waveop.Operation {
		return waveop.WaveletBlipOperation{BlipID: "a", BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: content}}
	}
	a1, a2 := docOp(da), docOp(da)
	b1 := docOp(db)
	if !waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{a2}) {
		t.Error("same content op should be equal")
	}
	if waveop.EqualOps([]waveop.Operation{a1}, []waveop.Operation{b1}) {
		t.Error("different content op should not be equal")
	}
}
