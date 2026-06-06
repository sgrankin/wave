package op_test

// Port of wave/.../model/document/operation/util/ImmutableStateMapTest.java.
//
// Java's ImmutableStateMap is the immutable, sorted attribute-name->value map
// behind AttributesImpl. Our equivalent is op.Attributes. The Java suite maps:
//
//   testVarargsConstructor  -> TestConformanceStateMapConstructorSortsByName
//   testCheckAttributesSorted (duplicate-rejection)
//                           -> TestConformanceStateMapRejectsDuplicateNames
//   testRemovalInUpdateWith -> TestConformanceStateMapUpdateRemovesAttribute
//   testUpdate              -> TestConformanceStateMapUpdateAddsAttribute
//
// SKIPPED sub-cases of testCheckAttributesSorted: the "unsorted input" and
// "null element in the list" cases do not map. op.NewAttributes takes a Go
// map[string]string, which cannot carry duplicate keys, cannot be "unsorted"
// (it is sorted at construction), and cannot hold a nil entry; there is no
// nullable Attribute value type. updateWith itself is unexported, so the update
// cases are exercised through the public Compose/Apply path on an element's
// attributes.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// ImmutableStateMapTest.testVarargsConstructor: building attributes from a
// name->value map yields a name-sorted set independent of insertion order, equal
// to the same contents built any other way.
func TestConformanceStateMapConstructorSortsByName(t *testing.T) {
	// Java: new AttributesImpl("c", "0", "a", "1", "b", "2") equals the HashMap
	// built {a:1, b:2, c:0}.
	a := mkAttrs(t, map[string]string{"c": "0", "a": "1", "b": "2"})
	b := mkAttrs(t, map[string]string{"a": "1", "b": "2", "c": "0"})
	if !a.Equal(b) {
		t.Errorf("attributes equal regardless of construction; got %v vs %v", a.All(), b.All())
	}
	all := a.All()
	if len(all) != 3 || all[0].Name != "a" || all[1].Name != "b" || all[2].Name != "c" {
		t.Errorf("attributes not name-sorted: %v", all)
	}
	if v, _ := a.Get("c"); v != "0" {
		t.Errorf("Get(c) = %q, want 0", v)
	}
}

// ImmutableStateMapTest.testCheckAttributesSorted (duplicate-rejection half):
// constructing attribute *changes* with duplicate names is rejected. (For
// op.Attributes proper, duplicate names are structurally impossible because the
// constructor takes a Go map; the list-based duplicate check lives on
// AttributesUpdate, which is what an attribute change list maps to.)
func TestConformanceStateMapRejectsDuplicateNames(t *testing.T) {
	if _, err := op.NewAttributesUpdate([]op.AttributeChange{
		{Name: "a", NewValue: sp("1")},
		{Name: "a", NewValue: sp("1")},
	}); err == nil {
		t.Error("duplicate attribute name in a change list must be rejected")
	}
}

// ImmutableStateMapTest.testRemovalInUpdateWith: applying an update whose new
// value is null removes the attribute. Java:
//
//	new AttributesImpl("a", "1").updateWith(new AttributesUpdateImpl("a", "1", null))
//
// has no key "a". updateWith is unexported, so we exercise the same semantics via
// Compose+Apply: insert <x a="1"> then update a:1->null, and read back the
// element's attributes.
func TestConformanceStateMapUpdateRemovesAttribute(t *testing.T) {
	got := applyAttrUpdate(t,
		map[string]string{"a": "1"},
		op.UpdateAttributes{Update: mustUpdate(t, "a", sp("1"), nil)})
	if _, ok := got.Get("a"); ok {
		t.Errorf("attribute a should have been removed; got %v", got.All())
	}
	if got.Len() != 0 {
		t.Errorf("expected empty attributes after removal, got %v", got.All())
	}
}

// ImmutableStateMapTest.testUpdate: an update adds a new key while leaving
// existing keys intact. Java starts from {a:0} and applies update(b: null->1),
// yielding {a:0, b:1}. Exercised via Compose+Apply.
func TestConformanceStateMapUpdateAddsAttribute(t *testing.T) {
	got := applyAttrUpdate(t,
		map[string]string{"a": "0"},
		op.UpdateAttributes{Update: mustUpdate(t, "b", nil, sp("1"))})
	if v, ok := got.Get("a"); !ok || v != "0" {
		t.Errorf("existing attribute a should remain 0; got %v ok=%v", v, ok)
	}
	if v, ok := got.Get("b"); !ok || v != "1" {
		t.Errorf("attribute b should be added as 1; got %v ok=%v", v, ok)
	}
	if got.Len() != 2 {
		t.Errorf("expected 2 attributes after add, got %v", got.All())
	}
}

// applyAttrUpdate inserts <x ...initial> then applies the given attribute-mutating
// component to that element start, and returns the resulting element's
// attributes. It is the public-API stand-in for ImmutableStateMap.updateWith.
func applyAttrUpdate(t *testing.T, initial map[string]string, mutate op.Component) op.Attributes {
	t.Helper()
	doc := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "x", Attributes: mkAttrs(t, initial)},
		op.ElementEnd{},
	})
	mut := op.NewDocOp([]op.Component{mutate, op.Retain{Count: 1}})
	result, err := op.Apply(doc, mut)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, c := range result.Components() {
		if es, ok := c.(op.ElementStart); ok {
			return es.Attributes
		}
	}
	t.Fatalf("no element start in result: %v", result.Components())
	return op.Attributes{}
}
