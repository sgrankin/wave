package op_test

// Port of wave/.../model/operation/OpComparatorsLargeTest.java.
//
// Java's OpComparators.SYNTACTIC_IDENTITY is an OpEquator that decides whether
// two DocOps are syntactically identical (after normalization). Our equivalent
// is DocOp.Equal, which normalizes both operands and compares component-by-
// component. The negative-focused Java suite maps as follows:
//
//   testNullable               -> SKIPPED: DocOp is a Go value type; there is no
//                                 nullable DocOp, no equalNullable, and no NPE
//                                 path to exercise.
//   testDocOp                  -> TestConformanceOpComparatorsDocOp
//   testEqualHandlesSpaces...  -> TestConformanceOpComparatorsSpacesInAnnotationKeys
//   testEqualHandlesQuotes...  -> TestConformanceOpComparatorsQuotesInAnnotationKeys
//   testRandomDocOps           -> TestConformanceOpComparatorsRandomReflexive
//                                 (the assertFalse(eq.equal(a,b)) half is dropped:
//                                 it depends on Java's exact seeded RNG, which we
//                                 cannot reproduce; we keep the reflexive half.)
//
// The Java suite's secondary assertions over OpComparators.equalDocuments +
// DocOpUtil.toXmlString are dropped: we have no XML serializer (see notes).

import (
	"math/rand"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// OpComparatorsLargeTest.testDocOp: two independently built characters("a") ops
// are equal; a characters op is never equal to a deleteCharacters op of the same
// text.
func TestConformanceOpComparatorsDocOp(t *testing.T) {
	a1 := op.NewDocOp([]op.Component{op.Characters{Text: "a"}})
	a2 := op.NewDocOp([]op.Component{op.Characters{Text: "a"}})
	b1 := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "a"}})
	b2 := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "a"}})

	for _, p := range [][2]op.DocOp{{a1, a1}, {a1, a2}, {a2, a1}, {a2, a2}} {
		if !p[0].Equal(p[1]) {
			t.Errorf("expected equal characters ops: %v vs %v", p[0].Components(), p[1].Components())
		}
	}
	for _, p := range [][2]op.DocOp{{b1, b1}, {b1, b2}, {b2, b1}, {b2, b2}} {
		if !p[0].Equal(p[1]) {
			t.Errorf("expected equal deleteCharacters ops: %v vs %v", p[0].Components(), p[1].Components())
		}
	}
	for _, p := range [][2]op.DocOp{{a1, b1}, {a1, b2}, {a2, b1}, {a2, b2}} {
		if p[0].Equal(p[1]) {
			t.Errorf("characters must not equal deleteCharacters: %v vs %v", p[0].Components(), p[1].Components())
		}
	}
}

// OpComparatorsLargeTest.testEqualHandlesSpacesInAnnotationKeys: ending the
// single annotation "x y" must be distinguishable from ending the two
// annotations "x" and "y". The two documents below differ only in which boundary
// ends "x y" vs {"x","y"}, and must compare unequal.
func TestConformanceOpComparatorsSpacesInAnnotationKeys(t *testing.T) {
	assertAnnotationKeyAmbiguityRejected(t, "x y")
}

// OpComparatorsLargeTest.testEqualHandlesQuotesInAnnotationKeys: annotation keys
// containing double quotes must not introduce ambiguity in equality checks.
func TestConformanceOpComparatorsQuotesInAnnotationKeys(t *testing.T) {
	assertAnnotationKeyAmbiguityRejected(t, `x" "y`)
}

// assertAnnotationKeyAmbiguityRejected builds the two documents from the Java
// space/quote tests parameterized by the "compound" key (e.g. "x y" or `x" "y`),
// and asserts they are NOT equal. doc1 ends {"x","y"} at the first boundary and
// {compound} at the second; doc2 swaps them. With ambiguity-free key handling the
// two documents are distinct.
func assertAnnotationKeyAmbiguityRejected(t *testing.T, compound string) {
	t.Helper()
	open := newAnnBoundary(t, nil, []op.AnnotationChange{
		annChangeOldNil(t, "x", "1"),
		annChangeOldNil(t, compound, "3"),
		annChangeOldNil(t, "y", "2"),
	})
	endCompound := newAnnBoundary(t, []string{compound}, nil)
	endXY := newAnnBoundary(t, []string{"x", "y"}, nil)

	doc1 := op.NewDocOp([]op.Component{
		op.AnnotationBoundary{Boundary: open},
		op.Characters{Text: "m"},
		op.AnnotationBoundary{Boundary: endXY},
		op.Characters{Text: "n"},
		op.AnnotationBoundary{Boundary: endCompound},
	})
	doc2 := op.NewDocOp([]op.Component{
		op.AnnotationBoundary{Boundary: open},
		op.Characters{Text: "m"},
		op.AnnotationBoundary{Boundary: endCompound},
		op.Characters{Text: "n"},
		op.AnnotationBoundary{Boundary: endXY},
	})
	if doc1.Equal(doc2) {
		t.Errorf("documents differing only in compound-key %q vs split keys must be unequal\ndoc1=%v\ndoc2=%v",
			compound, doc1.Components(), doc2.Components())
	}
}

// annChangeOldNil builds an AnnotationChange with a nil (absent) old value and a
// concrete new value, matching Java updateValues(key, null, new).
func annChangeOldNil(t *testing.T, key, new string) op.AnnotationChange {
	t.Helper()
	n := new
	return op.AnnotationChange{Key: key, OldValue: nil, NewValue: &n}
}

// OpComparatorsLargeTest.testRandomDocOps: a randomly generated DocOp equals a
// structurally-identical copy under SYNTACTIC_IDENTITY, and is distinguished from
// a structurally-different op. The Java suite asserts inequality between ops from
// adjacent seeds; that relies on Java's exact seeded RNG and is not reproducible,
// so we substitute a deterministic structural mutation that Equal must reject.
func TestConformanceOpComparatorsRandomReflexive(t *testing.T) {
	rng := rand.New(rand.NewSource(0xC0FFEE))
	for i := 0; i < 200; i++ {
		nodes, _ := randStructuredDoc(rng, 3)
		a := randStructuredOp(rng, nodes)
		// Reflexivity via a distinct equal-valued copy (comparing the value to
		// itself would be a tautology; a clone forces Equal to walk both lists).
		clone := op.NewDocOp(append([]op.Component(nil), a.Components()...))
		if !a.Equal(clone) {
			t.Fatalf("iteration %d: op not equal to its clone: %v", i, a.Components())
		}
		// Distinctness: appending a balanced element insertion is real content that
		// normalization cannot elide, so Equal must report inequality.
		mutated := op.NewDocOp(append(append([]op.Component(nil), a.Components()...),
			op.ElementStart{Type: "x"}, op.ElementEnd{}))
		if a.Equal(mutated) {
			t.Fatalf("iteration %d: op reported equal to a structurally-extended version", i)
		}
	}
}
