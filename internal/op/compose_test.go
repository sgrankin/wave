package op_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// doc builds the sample document <p>ab</p> as a DocInitialization.
func sampleDoc(t *testing.T) op.DocOp {
	t.Helper()
	p, err := op.NewAttributes(nil)
	if err != nil {
		t.Fatal(err)
	}
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: p},
		op.Characters{Text: "ab"},
		op.ElementEnd{},
	})
}

func TestComposeMatchesSequentialApply(t *testing.T) {
	doc := sampleDoc(t) // <p>ab</p>, length 4

	// a inserts "X" after <p>: retain 1, chars "X", retain 3.
	a := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.Characters{Text: "X"},
		op.Retain{Count: 3},
	})
	// b deletes the "X": retain 1, delete "X", retain 3.
	b := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.DeleteCharacters{Text: "X"},
		op.Retain{Count: 3},
	})

	// Sequential: doc -> a -> b.
	afterA, err := op.Apply(doc, a)
	if err != nil {
		t.Fatalf("apply a: %v", err)
	}
	seq, err := op.Apply(afterA, b)
	if err != nil {
		t.Fatalf("apply b: %v", err)
	}

	// Composed: doc -> compose(a,b).
	ab, err := op.Compose(a, b)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	composed, err := op.Apply(doc, ab)
	if err != nil {
		t.Fatalf("apply compose: %v", err)
	}

	if !seq.Equal(composed) {
		t.Errorf("apply(apply(doc,a),b) != apply(doc,compose(a,b))")
	}
	// a then b cancel, so the result is the original document.
	if !seq.Equal(doc) {
		t.Errorf("insert-then-delete did not restore the document")
	}
}

func TestComposeInsertions(t *testing.T) {
	doc := sampleDoc(t)
	// a: retain all (4) — identity.
	a := op.NewDocOp([]op.Component{op.Retain{Count: 4}})
	// b: insert "Z" at end before </p>: retain 3, chars "Z", retain 1.
	b := op.NewDocOp([]op.Component{
		op.Retain{Count: 3},
		op.Characters{Text: "Z"},
		op.Retain{Count: 1},
	})
	ab, err := op.Compose(a, b)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	got, err := op.Apply(doc, ab)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want, _ := op.Apply(doc, b)
	if !got.Equal(want) {
		t.Errorf("compose(identity, b) should behave as b")
	}
	if !got.IsInitialization() {
		t.Errorf("applying an op to a document should yield a document (initialization)")
	}
}

func TestComposeLengthMismatch(t *testing.T) {
	a := op.NewDocOp([]op.Component{op.Characters{Text: "ab"}}) // output length 2
	b := op.NewDocOp([]op.Component{op.Retain{Count: 5}})       // input length 5
	if _, err := op.Compose(a, b); err == nil {
		t.Error("Compose should reject mismatched op1-output/op2-input lengths")
	}
}

func TestInvertRoundTrip(t *testing.T) {
	doc := sampleDoc(t)
	// op: insert "X" after <p> and delete "a".
	mut := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.Characters{Text: "X"},
		op.DeleteCharacters{Text: "a"},
		op.Retain{Count: 2}, // "b" + </p>
	})
	after, err := op.Apply(doc, mut)
	if err != nil {
		t.Fatalf("apply mut: %v", err)
	}
	inv := op.Invert(mut)
	restored, err := op.Apply(after, inv)
	if err != nil {
		t.Fatalf("apply invert: %v", err)
	}
	if !restored.Equal(doc) {
		t.Errorf("apply(apply(doc, op), invert(op)) != doc")
	}
}

func TestComposeAnnotations(t *testing.T) {
	doc := sampleDoc(t)
	bold := func(v *string) op.AnnotationBoundaryMap {
		m, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: "style/bold", NewValue: v}})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	endBold, err := op.NewAnnotationBoundaryMap([]string{"style/bold"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	yes := "true"
	// a: bold "ab".
	a := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: bold(&yes)},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: endBold},
		op.Retain{Count: 1},
	})
	// b: identity.
	b := op.NewDocOp([]op.Component{op.Retain{Count: 4}})

	ab, err := op.Compose(a, b)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	got, err := op.Apply(doc, ab)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want, err := op.Apply(doc, a)
	if err != nil {
		t.Fatalf("apply a: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("compose with identity changed the annotated result")
	}
}
