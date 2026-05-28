package op

import "testing"

// applyOrFail applies op to doc, failing the test on error.
func applyOrFail(t *testing.T, doc, op DocOp) DocOp {
	t.Helper()
	got, err := Apply(doc, op)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return got
}

func TestDecomposeRecomposes(t *testing.T) {
	// An op with inserts, deletes, and a retain. decompose then compose must
	// reproduce the original.
	a, _ := NewAttributes(map[string]string{"k": "v"})
	op := NewDocOp([]Component{
		Retain{Count: 1},
		Characters{Text: "hi"},
		DeleteElementStart{Type: "x", Attributes: a},
		DeleteElementEnd{},
		Retain{Count: 2},
	})
	ins, non := decompose(op)
	// The insertion part is "insertion-form": only retain/characters/element
	// start/end (no deletes, attribute ops, or annotations). Note this is NOT a
	// DocInitialization, which has no retains.
	for _, c := range ins.Components() {
		switch c.(type) {
		case Retain, Characters, ElementStart, ElementEnd:
		default:
			t.Errorf("insertion part has non-insertion component %T", c)
		}
	}
	recomposed, err := Compose(ins, non)
	if err != nil {
		t.Fatalf("compose(ins, non): %v", err)
	}
	if !recomposed.Equal(op) {
		t.Errorf("compose(decompose(op)) != op")
	}
}

// insertionTP1 checks the TP1 convergence property for two insertion-only ops.
func insertionTP1(t *testing.T, doc, client, server DocOp) DocOp {
	t.Helper()
	cPrime, sPrime, err := insertionTransform(client, server)
	if err != nil {
		t.Fatalf("insertionTransform: %v", err)
	}
	// apply(apply(doc, client), sPrime) == apply(apply(doc, server), cPrime)
	left := applyOrFail(t, applyOrFail(t, doc, client), sPrime)
	right := applyOrFail(t, applyOrFail(t, doc, server), cPrime)
	if !left.Equal(right) {
		t.Errorf("TP1 violated:\n  client-then-server' = %v\n  server-then-client' = %v",
			left.Components(), right.Components())
	}
	return left
}

func TestInsertionTransformDisjointPositions(t *testing.T) {
	doc := NewDocOp([]Component{Characters{Text: "ab"}})                                       // "ab"
	client := NewDocOp([]Component{Retain{Count: 1}, Characters{Text: "X"}, Retain{Count: 1}}) // a X b
	server := NewDocOp([]Component{Retain{Count: 2}, Characters{Text: "Y"}})                   // ab Y
	got := insertionTP1(t, doc, client, server)
	want := NewDocOp([]Component{Characters{Text: "aXbY"}})
	if !got.Equal(want) {
		t.Errorf("converged doc = %v, want aXbY", got.Components())
	}
}

func TestInsertionTransformClientFirstTieBreak(t *testing.T) {
	doc := NewDocOp([]Component{Characters{Text: "ab"}})
	// Both insert at the same position (after "a"): client "X", server "Y".
	client := NewDocOp([]Component{Retain{Count: 1}, Characters{Text: "X"}, Retain{Count: 1}})
	server := NewDocOp([]Component{Retain{Count: 1}, Characters{Text: "Y"}, Retain{Count: 1}})
	got := insertionTP1(t, doc, client, server)
	// Client goes first: "aXYb", not "aYXb".
	want := NewDocOp([]Component{Characters{Text: "aXYb"}})
	if !got.Equal(want) {
		t.Errorf("tie-break converged to %v, want aXYb (client first)", got.Components())
	}
}

func TestInsertionTransformAtDocumentEnds(t *testing.T) {
	doc := NewDocOp([]Component{Characters{Text: "m"}})
	// client inserts at start, server inserts at end.
	client := NewDocOp([]Component{Characters{Text: "S"}, Retain{Count: 1}})
	server := NewDocOp([]Component{Retain{Count: 1}, Characters{Text: "E"}})
	got := insertionTP1(t, doc, client, server)
	want := NewDocOp([]Component{Characters{Text: "SmE"}})
	if !got.Equal(want) {
		t.Errorf("converged doc = %v, want SmE", got.Components())
	}
}
