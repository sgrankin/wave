package op_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

func sp(s string) *string { return &s }

// elementDoc builds <x a="1"></x> as a DocInitialization (length 2).
func elementDoc(t *testing.T) op.DocOp {
	t.Helper()
	attrs, err := op.NewAttributes(map[string]string{"a": "1"})
	if err != nil {
		t.Fatal(err)
	}
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "x", Attributes: attrs},
		op.ElementEnd{},
	})
}

func mustUpdate(t *testing.T, name string, old, new *string) op.AttributesUpdate {
	t.Helper()
	u, err := op.NewAttributesUpdate([]op.AttributeChange{{Name: name, OldValue: old, NewValue: new}})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// elementStart ∘ updateAttributes: op1 inserts the element, op2 updates its attrs.
func TestComposeElementStartWithUpdateAttributes(t *testing.T) {
	insertAttrs, _ := op.NewAttributes(map[string]string{"a": "1"})
	a := op.NewDocOp([]op.Component{ // insert <x a=1></x>
		op.ElementStart{Type: "x", Attributes: insertAttrs},
		op.ElementEnd{},
	})
	b := op.NewDocOp([]op.Component{ // update a: 1 -> 2, retain </x>
		op.UpdateAttributes{Update: mustUpdate(t, "a", sp("1"), sp("2"))},
		op.Retain{Count: 1},
	})
	ab, err := op.Compose(a, b)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	// Composed result applied to the empty doc must equal sequential application.
	got, err := op.Apply(op.EmptyDoc(), ab)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	afterA, _ := op.Apply(op.EmptyDoc(), a)
	want, _ := op.Apply(afterA, b)
	if !got.Equal(want) {
		t.Errorf("compose(elementStart, updateAttributes) != sequential apply")
	}
}

// updateAttributes ∘ updateAttributes collapses to (first old, second new).
func TestComposeUpdateAttributesPair(t *testing.T) {
	doc := elementDoc(t) // <x a=1></x>
	a := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mustUpdate(t, "a", sp("1"), sp("2"))}, op.Retain{Count: 1}})
	b := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mustUpdate(t, "a", sp("2"), sp("3"))}, op.Retain{Count: 1}})

	ab, err := op.Compose(a, b)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	got, err := op.Apply(doc, ab)
	if err != nil {
		t.Fatalf("apply compose: %v", err)
	}
	afterA, _ := op.Apply(doc, a)
	want, _ := op.Apply(afterA, b)
	if !got.Equal(want) {
		t.Errorf("compose(update 1->2, update 2->3) != sequential (should net a: 1->3)")
	}
}

// A mismatched old value in composed updateAttributes must be rejected.
func TestComposeUpdateAttributesOldValueMismatch(t *testing.T) {
	a := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mustUpdate(t, "a", sp("1"), sp("2"))}, op.Retain{Count: 1}})
	// b expects old value "9", but a left it at "2".
	b := op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mustUpdate(t, "a", sp("9"), sp("3"))}, op.Retain{Count: 1}})
	if _, err := op.Compose(a, b); err == nil {
		t.Error("Compose should reject updateAttributes with a mismatched old value")
	}
}

// Partial cancel: insert shorter than the following delete leaves a delete remainder.
func TestComposePartialCancelInsertShorterThanDelete(t *testing.T) {
	doc, _ := op.Apply(op.EmptyDoc(), op.NewDocOp([]op.Component{op.Characters{Text: "Z"}})) // "Z"
	a := op.NewDocOp([]op.Component{op.Characters{Text: "ab"}, op.Retain{Count: 1}})         // insert "ab" before Z
	b := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "abZ"}})                       // delete "abZ"
	ab, err := op.Compose(a, b)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	got, err := op.Apply(doc, ab)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	afterA, _ := op.Apply(doc, a)
	want, _ := op.Apply(afterA, b)
	if !got.Equal(want) {
		t.Errorf("partial-cancel (insert shorter than delete) diverged from sequential apply")
	}
	if got.Normalize().Size() != 0 {
		t.Errorf("expected empty document after deleting all content, got %d components", got.Normalize().Size())
	}
}

// Partial cancel: insert longer than the following delete leaves an insert remainder.
func TestComposePartialCancelInsertLongerThanDelete(t *testing.T) {
	doc := op.EmptyDoc()
	a := op.NewDocOp([]op.Component{op.Characters{Text: "abc"}})                           // insert "abc"
	b := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "ab"}, op.Retain{Count: 1}}) // delete "ab", keep "c"
	ab, err := op.Compose(a, b)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	got, err := op.Apply(doc, ab)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	afterA, _ := op.Apply(doc, a)
	want, _ := op.Apply(afterA, b)
	if !got.Equal(want) {
		t.Errorf("partial-cancel (insert longer than delete) diverged from sequential apply")
	}
}

// An illegal composition (insert characters then delete an element start) errors.
func TestComposeIllegalCompositionErrors(t *testing.T) {
	a := op.NewDocOp([]op.Component{op.Characters{Text: "a"}}) // output: 1 char
	emptyAttrs, _ := op.NewAttributes(nil)
	b := op.NewDocOp([]op.Component{op.DeleteElementStart{Type: "x", Attributes: emptyAttrs}}) // input: 1 item
	if _, err := op.Compose(a, b); err == nil {
		t.Error("Compose should reject deleting an element start over inserted characters")
	}
}
