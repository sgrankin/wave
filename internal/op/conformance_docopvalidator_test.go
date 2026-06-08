package op_test

// Port of wave/src/test/java/org/waveprotocol/wave/model/document/operation/impl/
// DocOpValidatorTest.java (16 test methods), one Go func per Java method.
//
// The Go op.Validate is the faithful port of DocOpValidator run with
// NO_SCHEMA_CONSTRAINTS (the box server uses SchemaCollection.empty()), collapsing
// the four-level ValidationResult lattice into accept (nil) / reject (error). It
// reports the FIRST violation in stream order, where Java's ViolationCollector
// reports the most-severe across the whole op; on a single-violation op the two
// agree, which is all these cases exercise.
//
// CONFORMANCE DIVERGENCE: cases whose expected result depends on the
// INVALID_SCHEMA axis (TEST_CONSTRAINTS: permitsChild / permittedCharacters /
// required-initial-children) are t.Skip'd — that axis is authoritatively dropped
// (docs/architecture/01 §8). Cases that rely on constructing malformed value
// objects the Go constructors forbid (unsorted attributes/annotation keys, a key
// in both end and change sets) are untranslatable-by-construction and noted as
// such; the malformed-construction path is covered at the NewAttributes /
// NewAnnotationBoundaryMap level elsewhere.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// conformAccept asserts the op is accepted (Java expected == true).
func conformAccept(t *testing.T, doc, o op.DocOp) {
	t.Helper()
	if err := op.Validate(doc, o); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

// conformReject asserts the op is rejected (Java expected == false).
func conformReject(t *testing.T, doc, o op.DocOp) {
	t.Helper()
	if err := op.Validate(doc, o); err == nil {
		t.Errorf("expected invalid, but Validate accepted the op")
	}
}

// DocOpValidatorTest.test1: empty op on empty doc is valid.
func TestConformanceValidator_test1(t *testing.T) {
	conformAccept(t, op.EmptyDoc(), op.NewDocOp(nil))
}

// DocOpValidatorTest.test2: an element whose type is "<" is not a valid XML name.
func TestConformanceValidator_test2(t *testing.T) {
	o := op.NewDocOp([]op.Component{op.ElementStart{Type: "<"}, op.ElementEnd{}})
	conformReject(t, op.EmptyDoc(), o)
}

// DocOpValidatorTest.test3: inserting <blip></blip> is valid under no schema.
func TestConformanceValidator_test3(t *testing.T) {
	o := op.NewDocOp([]op.Component{op.ElementStart{Type: "blip"}, op.ElementEnd{}})
	conformAccept(t, op.EmptyDoc(), o)
}

// DocOpValidatorTest.test4: inserting <p></p> is rejected under TEST_CONSTRAINTS
// (p is not a permitted top-level child). SCHEMA axis — dropped here.
func TestConformanceValidator_test4(t *testing.T) {
	t.Skip("CONFORMANCE DIVERGENCE: INVALID_SCHEMA (permitsChild) is dropped; server uses SchemaCollection.empty()")
}

// DocOpValidatorTest.test5: inserting <blip></blip> is valid.
func TestConformanceValidator_test5(t *testing.T) {
	o := op.NewDocOp([]op.Component{op.ElementStart{Type: "blip"}, op.ElementEnd{}})
	conformAccept(t, op.EmptyDoc(), o)
}

// DocOpValidatorTest.testMaxSkipDistanceDoesntAssert: over-retaining past the
// document end (doc <blip></blip> has length 2; retain 3 then 1) must not crash and
// is rejected.
func TestConformanceValidator_testMaxSkipDistanceDoesntAssert(t *testing.T) {
	doc := op.NewDocOp([]op.Component{op.ElementStart{Type: "blip"}, op.ElementEnd{}})
	o := op.NewDocOp([]op.Component{op.Retain{Count: 3}, op.Retain{Count: 1}})
	conformReject(t, doc, o)
}

// DocOpValidatorTest.testUnsortedAttributes: untranslatable by construction.
func TestConformanceValidator_testUnsortedAttributes(t *testing.T) {
	t.Skip("UNTRANSLATABLE BY CONSTRUCTION: op.NewAttributes always sorts; an unsorted " +
		"Attributes is unrepresentable. The sorted (positive) case is covered by other tests; " +
		"malformed construction is checked at the NewAttributes level in op_test.go.")
}

// DocOpValidatorTest.testDuplicateAnnotationKeys: positive case (distinct keys
// opened/ended in sequence over a 2-char doc) is valid. The negative case injects a
// boundary with a key in both end and change sets, which NewAnnotationBoundaryMap
// forbids — untranslatable by construction.
func TestConformanceValidator_testDuplicateAnnotationKeys(t *testing.T) {
	doc := textDoc("ab")
	openA := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
	endAopenB := mustAnnBoundary(t, []string{"a"}, []op.AnnotationChange{{Key: "b", NewValue: sp("2")}})
	endB := mustAnnBoundary(t, []string{"b"}, nil)
	o := op.NewDocOp([]op.Component{
		op.AnnotationBoundary{Boundary: openA},
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: endAopenB},
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: endB},
	})
	conformAccept(t, doc, o)
	// Negative case (key in both end and change) is untranslatable: NewAnnotationBoundaryMap
	// rejects it at construction.
}

// DocOpValidatorTest.testUnsortedAnnotationKeys: positive case valid; the negative
// cases inject unsorted end/change keys via DumbAnnotationBoundaryMap, which
// NewAnnotationBoundaryMap forbids — untranslatable by construction.
func TestConformanceValidator_testUnsortedAnnotationKeys(t *testing.T) {
	doc := textDoc("ab")
	openAB := mustAnnBoundary(t, nil, []op.AnnotationChange{
		{Key: "a", NewValue: sp("1")}, {Key: "b", NewValue: sp("2")},
	})
	endAopenBC := mustAnnBoundary(t, []string{"a"}, []op.AnnotationChange{
		{Key: "b", NewValue: sp("2")}, {Key: "c", NewValue: sp("3")},
	})
	endBC := mustAnnBoundary(t, []string{"b", "c"}, nil)
	o := op.NewDocOp([]op.Component{
		op.AnnotationBoundary{Boundary: openAB},
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: endAopenBC},
		op.Retain{Count: 1},
		op.AnnotationBoundary{Boundary: endBC},
	})
	conformAccept(t, doc, o)
	// Negative (unsorted keys) untranslatable: NewAnnotationBoundaryMap sorts/validates.
}

// DocOpValidatorTest.testDeletionAnnotationsAreRelative: deleting an item whose
// annotations match the inherited (left) annotations needs no reset; specifying a
// redundant reset is also fine.
func TestConformanceValidator_testDeletionAnnotationsAreRelative(t *testing.T) {
	// annotations are not needed if previous character has the same annotations:
	// doc = a("a","1") b("a","1"); retain 'a', delete 'b' (both carry a=1, so the
	// inherited target for 'b' already has a=1).
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "a"},
			op.Characters{Text: "b"},
			op.AnnotationBoundary{Boundary: end},
		})
		o := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.DeleteCharacters{Text: "b"}})
		conformAccept(t, doc, o)
	}
	// but may be specified (redundant a:1->1 reset):
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "a"},
			op.Characters{Text: "b"},
			op.AnnotationBoundary{Boundary: end},
		})
		o := op.NewDocOp([]op.Component{
			op.Retain{Count: 1},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", OldValue: sp("1"), NewValue: sp("1")}})},
			op.DeleteCharacters{Text: "b"},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"a"}, nil)},
		})
		conformAccept(t, doc, o)
	}
	// even for null values: doc has no annotations; deleting 'b' with a redundant
	// a:null->null over the deletion is fine.
	{
		doc := op.NewDocOp([]op.Component{op.Characters{Text: "a"}, op.Characters{Text: "b"}})
		o := op.NewDocOp([]op.Component{
			op.Retain{Count: 1},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: nil}})},
			op.DeleteCharacters{Text: "b"},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"a"}, nil)},
		})
		conformAccept(t, doc, o)
	}
}

// DocOpValidatorTest.testDeletionAnnotationsAreRelative2: deleting an item whose
// annotations differ from the inherited (left) annotations requires an explicit
// reset; otherwise the deletion is rejected.
func TestConformanceValidator_testDeletionAnnotationsAreRelative2(t *testing.T) {
	// annotations are needed if previous character has different annotations:
	// doc = a("a","1"); deleting 'a' with no reset is rejected (inherited target at
	// pos 0 is empty, but 'a' carries a=1).
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "a"},
			op.AnnotationBoundary{Boundary: end},
		})
		o := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "a"}})
		conformReject(t, doc, o)
	}
	// positive case: same doc, but the op explicitly resets a:1->null over the delete.
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "a"},
			op.AnnotationBoundary{Boundary: end},
		})
		o := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", OldValue: sp("1"), NewValue: nil}})},
			op.DeleteCharacters{Text: "a"},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"a"}, nil)},
		})
		conformAccept(t, doc, o)
	}
	// deleting multiple items: reset open, delete 'a', end annotation, then delete 'b'
	// (no longer reset) is rejected.
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "ab"},
			op.AnnotationBoundary{Boundary: end},
		})
		o := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", OldValue: sp("1"), NewValue: nil}})},
			op.DeleteCharacters{Text: "a"},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"a"}, nil)},
			op.DeleteCharacters{Text: "b"},
		})
		conformReject(t, doc, o)
	}
	// positive case: reset open, delete 'a' then 'b', then end the annotation.
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "ab"},
			op.AnnotationBoundary{Boundary: end},
		})
		o := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", OldValue: sp("1"), NewValue: nil}})},
			op.DeleteCharacters{Text: "a"},
			op.DeleteCharacters{Text: "b"},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"a"}, nil)},
		})
		conformAccept(t, doc, o)
	}
}

// DocOpValidatorTest.testDeletionAnnotationsAreRelative3: deleting an item whose
// inherited (left) annotations carry MORE than the item itself also requires an
// explicit reset.
func TestConformanceValidator_testDeletionAnnotationsAreRelative3(t *testing.T) {
	// doc = a("a","1") then b(no ann); deleting 'b' (inherited target from 'a' has
	// a=1, but 'b' has no a) with no reset is rejected.
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "a"},
			op.AnnotationBoundary{Boundary: end},
			op.Characters{Text: "b"},
		})
		o := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.DeleteCharacters{Text: "b"}})
		conformReject(t, doc, o)
	}
	// positive case: reset a:null->1 over the delete of 'b'.
	{
		set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", NewValue: sp("1")}})
		end := mustAnnBoundary(t, []string{"a"}, nil)
		doc := op.NewDocOp([]op.Component{
			op.AnnotationBoundary{Boundary: set},
			op.Characters{Text: "a"},
			op.AnnotationBoundary{Boundary: end},
			op.Characters{Text: "b"},
		})
		o := op.NewDocOp([]op.Component{
			op.Retain{Count: 1},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "a", OldValue: nil, NewValue: sp("1")}})},
			op.DeleteCharacters{Text: "b"},
			op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"a"}, nil)},
		})
		conformAccept(t, doc, o)
	}
}

// DocOpValidatorTest.testRequiredTag: SCHEMA axis (required-initial-children) — dropped.
func TestConformanceValidator_testRequiredTag(t *testing.T) {
	t.Skip("CONFORMANCE DIVERGENCE: INVALID_SCHEMA (getRequiredInitialChildren) is dropped")
}

// DocOpValidatorTest.testDeletingRequiredTag: SCHEMA axis — dropped.
func TestConformanceValidator_testDeletingRequiredTag(t *testing.T) {
	t.Skip("CONFORMANCE DIVERGENCE: INVALID_SCHEMA (attemptToDeleteRequiredChild) is dropped")
}

// DocOpValidatorTest.testInsertingAroundRequiredTag: SCHEMA axis — dropped.
func TestConformanceValidator_testInsertingAroundRequiredTag(t *testing.T) {
	t.Skip("CONFORMANCE DIVERGENCE: INVALID_SCHEMA (attemptToInsertBeforeRequiredChild) is dropped")
}

// DocOpValidatorTest.testCharAtPastEnd: retain(1) then deleteCharacters("ab") on an
// empty document must not crash and is rejected (retain past end).
func TestConformanceValidator_testCharAtPastEnd(t *testing.T) {
	o := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.DeleteCharacters{Text: "ab"}})
	conformReject(t, op.EmptyDoc(), o)
}
