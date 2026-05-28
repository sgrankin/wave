package op_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

func strPtr(s string) *string { return &s }

func TestAttributesSortsAndLooksUp(t *testing.T) {
	a, err := op.NewAttributes(map[string]string{"z": "1", "a": "2", "m": "3"})
	if err != nil {
		t.Fatalf("NewAttributes: %v", err)
	}
	if a.Len() != 3 {
		t.Fatalf("Len = %d, want 3", a.Len())
	}
	// All is sorted by name.
	all := a.All()
	if all[0].Name != "a" || all[1].Name != "m" || all[2].Name != "z" {
		t.Errorf("All not sorted: %+v", all)
	}
	if v, ok := a.Get("m"); !ok || v != "3" {
		t.Errorf("Get(m) = %q,%v want 3,true", v, ok)
	}
	if _, ok := a.Get("missing"); ok {
		t.Error("Get(missing) = true, want false")
	}
}

func TestAttributesEqual(t *testing.T) {
	a, _ := op.NewAttributes(map[string]string{"a": "1", "b": "2"})
	b, _ := op.NewAttributes(map[string]string{"b": "2", "a": "1"}) // different insertion order
	c, _ := op.NewAttributes(map[string]string{"a": "1", "b": "3"})
	if !a.Equal(b) {
		t.Error("a.Equal(b) = false, want true (order-independent)")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) = true, want false")
	}
	if empty := (op.Attributes{}); !empty.Equal(op.Attributes{}) {
		t.Error("empty attributes should be equal")
	}
}

func TestAttributesInvalid(t *testing.T) {
	if _, err := op.NewAttributes(map[string]string{"": "v"}); err == nil {
		t.Error("empty attribute name accepted")
	}
	if _, err := op.NewAttributes(map[string]string{"k": "\xff\xfe"}); err == nil {
		t.Error("invalid UTF-8 value accepted")
	}
}

func TestAttributesUpdate(t *testing.T) {
	u, err := op.NewAttributesUpdate([]op.AttributeChange{
		{Name: "z", OldValue: nil, NewValue: strPtr("new")},
		{Name: "a", OldValue: strPtr("old"), NewValue: nil},
	})
	if err != nil {
		t.Fatalf("NewAttributesUpdate: %v", err)
	}
	all := u.All()
	if all[0].Name != "a" || all[1].Name != "z" {
		t.Errorf("update not sorted: %+v", all)
	}
	// duplicate name rejected
	if _, err := op.NewAttributesUpdate([]op.AttributeChange{
		{Name: "a", NewValue: strPtr("1")},
		{Name: "a", NewValue: strPtr("2")},
	}); err == nil {
		t.Error("duplicate attribute name in update accepted")
	}
}

func TestAttributesUpdateEqualNullSemantics(t *testing.T) {
	withNil, _ := op.NewAttributesUpdate([]op.AttributeChange{{Name: "a", NewValue: nil}})
	withEmpty, _ := op.NewAttributesUpdate([]op.AttributeChange{{Name: "a", NewValue: strPtr("")}})
	if withNil.Equal(withEmpty) {
		t.Error("null NewValue should differ from empty-string NewValue")
	}
}

func TestAnnotationBoundaryMap(t *testing.T) {
	m, err := op.NewAnnotationBoundaryMap(
		[]string{"style/color", "style/bold"},
		[]op.AnnotationChange{{Key: "link", OldValue: nil, NewValue: strPtr("http://x")}},
	)
	if err != nil {
		t.Fatalf("NewAnnotationBoundaryMap: %v", err)
	}
	ends := m.EndKeys()
	if ends[0] != "style/bold" || ends[1] != "style/color" {
		t.Errorf("end keys not sorted: %v", ends)
	}
	if m.Empty() {
		t.Error("map should not be empty")
	}
}

func TestAnnotationBoundaryMapInvalid(t *testing.T) {
	// duplicate end key
	if _, err := op.NewAnnotationBoundaryMap([]string{"a", "a"}, nil); err == nil {
		t.Error("duplicate end key accepted")
	}
	// key in both end and change sets
	if _, err := op.NewAnnotationBoundaryMap([]string{"k"}, []op.AnnotationChange{{Key: "k", NewValue: strPtr("v")}}); err == nil {
		t.Error("key in both end and change sets accepted")
	}
	// '?' / '@' forbidden in keys
	if _, err := op.NewAnnotationBoundaryMap([]string{"a?b"}, nil); err == nil {
		t.Error("'?' in annotation key accepted")
	}
	if _, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: "a@b", NewValue: strPtr("v")}}); err == nil {
		t.Error("'@' in annotation key accepted")
	}
}

func TestDocOpIsCopied(t *testing.T) {
	comps := []op.Component{op.Retain{Count: 3}, op.Characters{Text: "hi"}}
	d := op.NewDocOp(comps)
	comps[0] = op.ElementEnd{} // mutate caller's slice
	if _, ok := d.Components()[0].(op.Retain); !ok {
		t.Error("NewDocOp did not copy its input; external mutation leaked in")
	}
	if d.Size() != 2 {
		t.Errorf("Size = %d, want 2", d.Size())
	}
}
