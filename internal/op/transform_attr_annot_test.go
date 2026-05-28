package op_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

func mkAttrs(t *testing.T, m map[string]string) op.Attributes {
	t.Helper()
	a, err := op.NewAttributes(m)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// --- Finding 1: attribute×attribute and attribute×delete-element transitions ---

func TestTransformReplaceVsReplaceSameKey(t *testing.T) {
	doc := elementWithAttr(t) // <x a="1">m</x>
	client := op.NewDocOp([]op.Component{
		op.ReplaceAttributes{OldAttributes: mkAttrs(t, map[string]string{"a": "1"}), NewAttributes: mkAttrs(t, map[string]string{"a": "2"})},
		op.Retain{Count: 2},
	})
	server := op.NewDocOp([]op.Component{
		op.ReplaceAttributes{OldAttributes: mkAttrs(t, map[string]string{"a": "1"}), NewAttributes: mkAttrs(t, map[string]string{"a": "3"})},
		op.Retain{Count: 2},
	})
	got := tp1(t, doc, client, server)
	if v, _ := elemAttr(got); v != "2" {
		t.Errorf("replace×replace converged a=%q, want 2 (client wins)", v)
	}
}

func TestTransformReplaceVsUpdate(t *testing.T) {
	doc := elementWithAttr(t)
	client := op.NewDocOp([]op.Component{
		op.ReplaceAttributes{OldAttributes: mkAttrs(t, map[string]string{"a": "1"}), NewAttributes: mkAttrs(t, map[string]string{"a": "2"})},
		op.Retain{Count: 2},
	})
	server := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdate(t, "a", "1", "9")}, op.Retain{Count: 2}})
	got := tp1(t, doc, client, server)
	if v, _ := elemAttr(got); v != "2" {
		t.Errorf("replace(client)×update(server) converged a=%q, want 2 (client replace wins)", v)
	}
}

func TestTransformUpdateVsReplace(t *testing.T) {
	doc := elementWithAttr(t)
	client := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdate(t, "a", "1", "2")}, op.Retain{Count: 2}})
	server := op.NewDocOp([]op.Component{
		op.ReplaceAttributes{OldAttributes: mkAttrs(t, map[string]string{"a": "1"}), NewAttributes: mkAttrs(t, map[string]string{"a": "9"})},
		op.Retain{Count: 2},
	})
	got := tp1(t, doc, client, server)
	if v, _ := elemAttr(got); v != "9" {
		t.Errorf("update(client)×replace(server) converged a=%q, want 9 (server replace wins)", v)
	}
}

// Mirror of TestTransformAttributeUpdateVsDeleteElement: the DELETING side is the
// client, exercising updateAttributesCache.resolveDeleteElementStart and the
// element-start delete absorbing the concurrent attribute change.
func TestTransformDeleteElementVsUpdate(t *testing.T) {
	doc := elementWithAttr(t)
	client := op.NewDocOp([]op.Component{
		op.DeleteElementStart{Type: "x", Attributes: mkAttrs(t, map[string]string{"a": "1"})},
		op.DeleteCharacters{Text: "m"},
		op.DeleteElementEnd{},
	})
	server := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdate(t, "a", "1", "2")}, op.Retain{Count: 2}})
	got := tp1(t, doc, client, server)
	if got.Normalize().Size() != 0 {
		t.Errorf("delete-element(client)×update(server) converged to %v, want empty (delete wins)", got.Components())
	}
}

func TestTransformDeleteElementVsReplace(t *testing.T) {
	doc := elementWithAttr(t)
	client := op.NewDocOp([]op.Component{
		op.DeleteElementStart{Type: "x", Attributes: mkAttrs(t, map[string]string{"a": "1"})},
		op.DeleteCharacters{Text: "m"},
		op.DeleteElementEnd{},
	})
	server := op.NewDocOp([]op.Component{
		op.ReplaceAttributes{OldAttributes: mkAttrs(t, map[string]string{"a": "1"}), NewAttributes: mkAttrs(t, map[string]string{"a": "2"})},
		op.Retain{Count: 2},
	})
	got := tp1(t, doc, client, server)
	if got.Normalize().Size() != 0 {
		t.Errorf("delete-element(client)×replace(server) converged to %v, want empty", got.Components())
	}
}

// An incompatible pairing (deleteCharacters vs deleteElementStart at the same
// position) must surface as an error, not a panic or wrong result.
func TestTransformIncompatiblePairing(t *testing.T) {
	client := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "m"}})
	server := op.NewDocOp([]op.Component{op.DeleteElementStart{Type: "x", Attributes: mkAttrs(t, nil)}})
	if _, _, err := op.Transform(client, server); err == nil {
		t.Error("Transform should reject deleteCharacters vs deleteElementStart at the same position")
	}
}

// --- Finding 2: annotations carried across a concurrent deletion ---
// These drive commenceDeletion/concludeDeletion with non-empty changes: one side
// holds an annotation that the other side's deletion must carry and restore.

// paragraph4 builds <p>abcd</p> (length 6).
func paragraph4(t *testing.T) op.DocOp {
	t.Helper()
	pa := mkAttrs(t, nil)
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: pa},
		op.Characters{Text: "abcd"},
		op.ElementEnd{},
	})
}

// annotateRange bolds [start,start+n) of a length-6 <p>abcd</p>: retain `start`,
// open bold, retain n, end bold, retain rest.
func annotateBold(t *testing.T, retainBefore, n, retainAfter int) op.DocOp {
	t.Helper()
	tval := "true"
	return op.NewDocOp([]op.Component{
		op.Retain{Count: retainBefore},
		op.AnnotationBoundary{Boundary: boldChange(t, &tval)},
		op.Retain{Count: n},
		op.AnnotationBoundary{Boundary: endBold(t)},
		op.Retain{Count: retainAfter},
	})
}

func TestTransformAnnotationPartlyDeleted(t *testing.T) {
	doc := paragraph4(t) // <p>abcd</p>
	// client bolds "bc" (retain 2 = <p>,a; bold over b,c; retain 2 = d,</p>).
	client := annotateBold(t, 2, 2, 2)
	// server deletes "b" (retain 2, delete "b", retain 3).
	server := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.DeleteCharacters{Text: "b"}, op.Retain{Count: 3}})
	tp1(t, doc, client, server) // bold must survive on remaining "c"; convergence is the assertion
}

func TestTransformAnnotationFullyDeleted(t *testing.T) {
	doc := paragraph4(t)
	client := annotateBold(t, 2, 2, 2)                                                                               // bold "bc"
	server := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.DeleteCharacters{Text: "bc"}, op.Retain{Count: 2}}) // delete "bc"
	tp1(t, doc, client, server)
}

// Mirror: the annotating side is the SERVER, exercising the serverProcess
// deletion path.
func TestTransformDeleteVsAnnotationMirror(t *testing.T) {
	doc := paragraph4(t)
	client := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.DeleteCharacters{Text: "bc"}, op.Retain{Count: 2}})
	server := annotateBold(t, 2, 2, 2)
	tp1(t, doc, client, server)
}

func TestTransformAnnotationValueChangeAcrossDelete(t *testing.T) {
	doc := paragraph4(t)
	tval, fval := "true", "false"
	// client: bold=true over "ab", then bold=false over "cd" (adjacent regions,
	// value change at the boundary).
	client := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: boldChange(t, &tval)},
		op.Retain{Count: 2}, // a,b
		op.AnnotationBoundary{Boundary: boldChange(t, &fval)},
		op.Retain{Count: 2}, // c,d
		op.AnnotationBoundary{Boundary: endBold(t)},
		op.Retain{Count: 1},
	})
	// server deletes "bc" — straddles the annotation value-change boundary.
	server := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.DeleteCharacters{Text: "bc"}, op.Retain{Count: 2}})
	tp1(t, doc, client, server)
}
