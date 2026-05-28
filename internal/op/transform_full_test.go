package op_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// tp1 asserts the TP1 convergence property for the full Transform:
//
//	apply(apply(D, server), client') == apply(apply(D, client), server')
//
// and returns the converged document.
func tp1(t *testing.T, doc, client, server op.DocOp) op.DocOp {
	t.Helper()
	cPrime, sPrime, err := op.Transform(client, server)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	afterClient, err := op.Apply(doc, client)
	if err != nil {
		t.Fatalf("apply client: %v", err)
	}
	afterServer, err := op.Apply(doc, server)
	if err != nil {
		t.Fatalf("apply server: %v", err)
	}
	left, err := op.Apply(afterClient, sPrime)
	if err != nil {
		t.Fatalf("apply server' after client: %v", err)
	}
	right, err := op.Apply(afterServer, cPrime)
	if err != nil {
		t.Fatalf("apply client' after server: %v", err)
	}
	if !sameDocument(left, right) {
		t.Errorf("TP1 violated:\n  client then server' = %v\n  server then client' = %v",
			left.Components(), right.Components())
	}
	return left
}

// paragraph builds <p>abc</p> (length 5).
func paragraph(t *testing.T) op.DocOp {
	t.Helper()
	pa, _ := op.NewAttributes(nil)
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: pa},
		op.Characters{Text: "abc"},
		op.ElementEnd{},
	})
}

func TestTransformConcurrentInsertsDisjoint(t *testing.T) {
	doc := paragraph(t)
	client := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 4}})
	server := op.NewDocOp([]op.Component{op.Retain{Count: 4}, op.Characters{Text: "Y"}, op.Retain{Count: 1}})
	got := tp1(t, doc, client, server)
	pa, _ := op.NewAttributes(nil)
	want := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: pa},
		op.Characters{Text: "XabcY"},
		op.ElementEnd{},
	})
	if !got.Equal(want) {
		t.Errorf("converged to %v, want <p>XabcY</p>", got.Components())
	}
}

func TestTransformConcurrentInsertsSamePosition(t *testing.T) {
	doc := paragraph(t)
	client := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 4}})
	server := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "Y"}, op.Retain{Count: 4}})
	got := tp1(t, doc, client, server)
	pa, _ := op.NewAttributes(nil)
	want := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: pa},
		op.Characters{Text: "XYabc"}, // client first
		op.ElementEnd{},
	})
	if !got.Equal(want) {
		t.Errorf("converged to %v, want <p>XYabc</p> (client-first)", got.Components())
	}
}

func TestTransformInsertVsDelete(t *testing.T) {
	doc := paragraph(t)
	client := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 4}})
	server := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.DeleteCharacters{Text: "a"}, op.Retain{Count: 3}})
	got := tp1(t, doc, client, server)
	pa, _ := op.NewAttributes(nil)
	want := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: pa},
		op.Characters{Text: "Xbc"},
		op.ElementEnd{},
	})
	if !got.Equal(want) {
		t.Errorf("converged to %v, want <p>Xbc</p>", got.Components())
	}
}

func TestTransformOverlappingDeletes(t *testing.T) {
	doc := paragraph(t)
	// client deletes "ab", server deletes "bc"; union is "abc".
	client := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.DeleteCharacters{Text: "ab"}, op.Retain{Count: 2}})
	server := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.DeleteCharacters{Text: "bc"}, op.Retain{Count: 1}})
	got := tp1(t, doc, client, server)
	pa, _ := op.NewAttributes(nil)
	want := op.NewDocOp([]op.Component{op.ElementStart{Type: "p", Attributes: pa}, op.ElementEnd{}})
	if !got.Equal(want) {
		t.Errorf("converged to %v, want empty <p></p>", got.Components())
	}
}

func TestTransformIdenticalDeletes(t *testing.T) {
	doc := paragraph(t)
	del := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.DeleteCharacters{Text: "abc"}, op.Retain{Count: 1}})
	got := tp1(t, doc, del, del)
	pa, _ := op.NewAttributes(nil)
	want := op.NewDocOp([]op.Component{op.ElementStart{Type: "p", Attributes: pa}, op.ElementEnd{}})
	if !got.Equal(want) {
		t.Errorf("converged to %v, want empty <p></p>", got.Components())
	}
}

// elementWithAttr builds <x a="1">m</x> (length 3).
func elementWithAttr(t *testing.T) op.DocOp {
	t.Helper()
	a, _ := op.NewAttributes(map[string]string{"a": "1"})
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "x", Attributes: a},
		op.Characters{Text: "m"},
		op.ElementEnd{},
	})
}

func mkUpdate(t *testing.T, name, old, new string) op.AttributesUpdate {
	t.Helper()
	o, n := old, new
	u, err := op.NewAttributesUpdate([]op.AttributeChange{{Name: name, OldValue: &o, NewValue: &n}})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTransformConcurrentAttributeUpdatesSameKey(t *testing.T) {
	doc := elementWithAttr(t)
	client := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdate(t, "a", "1", "2")}, op.Retain{Count: 2}})
	server := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdate(t, "a", "1", "3")}, op.Retain{Count: 2}})
	got := tp1(t, doc, client, server)
	// Client wins for the shared key: a = "2".
	if v, _ := elemAttr(got); v != "2" {
		t.Errorf("converged attribute a=%q, want 2 (client wins)", v)
	}
}

func TestTransformConcurrentAttributeUpdatesDifferentKeys(t *testing.T) {
	doc := elementWithAttr(t)
	client := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdateNew(t, "b", "x")}, op.Retain{Count: 2}})
	server := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdateNew(t, "c", "y")}, op.Retain{Count: 2}})
	tp1(t, doc, client, server) // both keys should survive; just assert convergence
}

func TestTransformAttributeUpdateVsDeleteElement(t *testing.T) {
	doc := elementWithAttr(t)
	// client updates the element's attribute; server deletes the whole element.
	client := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mkUpdate(t, "a", "1", "2")}, op.Retain{Count: 2}})
	a, _ := op.NewAttributes(map[string]string{"a": "1"})
	server := op.NewDocOp([]op.Component{
		op.DeleteElementStart{Type: "x", Attributes: a},
		op.DeleteCharacters{Text: "m"},
		op.DeleteElementEnd{},
	})
	got := tp1(t, doc, client, server)
	if got.Normalize().Size() != 0 {
		t.Errorf("converged to %v, want empty document (server delete wins)", got.Components())
	}
}

func TestTransformElementDeletionAbsorbsInsertion(t *testing.T) {
	doc := paragraph(t) // <p>abc</p>
	// client inserts X inside the paragraph; server deletes the whole paragraph.
	client := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 4}})
	pa, _ := op.NewAttributes(nil)
	server := op.NewDocOp([]op.Component{
		op.DeleteElementStart{Type: "p", Attributes: pa},
		op.DeleteCharacters{Text: "abc"},
		op.DeleteElementEnd{},
	})
	got := tp1(t, doc, client, server)
	if got.Normalize().Size() != 0 {
		t.Errorf("converged to %v, want empty document (insertion absorbed by deletion)", got.Components())
	}
}

func boldChange(t *testing.T, val *string) op.AnnotationBoundaryMap {
	t.Helper()
	m, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: "style/bold", NewValue: val}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func endBold(t *testing.T) op.AnnotationBoundaryMap {
	t.Helper()
	m, err := op.NewAnnotationBoundaryMap([]string{"style/bold"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTransformAnnotateVsInsert(t *testing.T) {
	doc := paragraph(t)
	tval := "true"
	client := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: boldChange(t, &tval)},
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: endBold(t)},
		op.Retain{Count: 1},
	})
	server := op.NewDocOp([]op.Component{op.Retain{Count: 4}, op.Characters{Text: "Y"}, op.Retain{Count: 1}})
	tp1(t, doc, client, server)
}

func TestTransformAnnotateVsDelete(t *testing.T) {
	doc := paragraph(t)
	tval := "true"
	client := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: boldChange(t, &tval)},
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: endBold(t)},
		op.Retain{Count: 1},
	})
	server := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.DeleteCharacters{Text: "abc"}, op.Retain{Count: 1}})
	tp1(t, doc, client, server)
}

func TestTransformConcurrentAnnotationsSameKey(t *testing.T) {
	doc := paragraph(t)
	tval, fval := "true", "false"
	client := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: boldChange(t, &tval)},
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: endBold(t)},
		op.Retain{Count: 1},
	})
	server := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: boldChange(t, &fval)},
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: endBold(t)},
		op.Retain{Count: 1},
	})
	tp1(t, doc, client, server) // must converge to one consistent value
}

// --- small helpers ---

func mkUpdateNew(t *testing.T, name, new string) op.AttributesUpdate {
	t.Helper()
	n := new
	u, err := op.NewAttributesUpdate([]op.AttributeChange{{Name: name, NewValue: &n}})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// elemAttr returns the value of attribute "a" on the first element start of doc.
func elemAttr(doc op.DocOp) (string, bool) {
	for _, c := range doc.Normalize().Components() {
		if es, ok := c.(op.ElementStart); ok {
			return es.Attributes.Get("a")
		}
	}
	return "", false
}
