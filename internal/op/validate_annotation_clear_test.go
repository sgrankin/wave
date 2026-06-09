package op_test

import (
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// These tests pin the validator's treatment of annotation-CLEAR ops — the shape the
// TS editor's setAnnotationRange/clearStyleRange/clearLink emit when removing a
// character annotation (un-bold, clear color, remove link, and clear-all-formatting).
// A clear opens a change to a null value at the range start; it MUST close that change
// with an end-key at the range end, or the annotation is left open and the op is
// ill-formed. The client's permissive compose() accepts the unclosed form, but this
// (the server submit path) must reject it — and the fixed client must emit the closed
// form. Guards against a regression of the "clear leaves a dangling annotation" bug.

// boldDoc builds <body><line/>AB</body> with style/fontWeight="bold" over "AB".
// Item stream: body-start(0) line-start(1) line-end(2) A(3) B(4) body-end(5).
func boldDoc(t *testing.T) op.DocOp {
	t.Helper()
	bold := "bold"
	open := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "style/fontWeight", NewValue: &bold}})
	end := mustAnnBoundary(t, []string{"style/fontWeight"}, nil)
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body"},
		op.ElementStart{Type: "line"}, op.ElementEnd{},
		op.AnnotationBoundary{Boundary: open},
		op.Characters{Text: "AB"},
		op.AnnotationBoundary{Boundary: end},
		op.ElementEnd{},
	})
}

// TestValidateRejectsUnclosedAnnotationClear is the reproduction: an un-bold op that
// opens style/fontWeight→null at the text start but never ends it (the buggy
// setAnnotationRange close-at-b "no close" branch) is rejected as ill-formed.
func TestValidateRejectsUnclosedAnnotationClear(t *testing.T) {
	doc := boldDoc(t)
	bold := "bold"
	// retain 3 (body/line/line-end), open fontWeight bold→null, retain 2 (A,B),
	// retain 1 (body-end) — NO end-key. fontWeight is left open.
	bad := op.NewDocOp([]op.Component{
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil,
			[]op.AnnotationChange{{Key: "style/fontWeight", OldValue: &bold, NewValue: nil}})},
		op.Retain{Count: 2},
		op.Retain{Count: 1},
	})
	err := op.Validate(doc, bad)
	if err == nil {
		t.Fatal("expected the unclosed-annotation clear op to be REJECTED, got nil")
	}
	// Rejected either by the trailing-retain annotation-old-value check (the retain past
	// the cleared run still carries old="bold" while the document is null) or, when the
	// clear runs to the document end, by checkFinish ("unclosed annotation"). Both are
	// the dangling-annotation defect; accept either.
	if !strings.Contains(err.Error(), "annotation") {
		t.Fatalf("expected an annotation well-formedness error, got: %v", err)
	}
}

// TestValidateAcceptsClosedAnnotationClear is the fix: the SAME un-bold but with the
// end-key emitted at the text end validates cleanly.
func TestValidateAcceptsClosedAnnotationClear(t *testing.T) {
	doc := boldDoc(t)
	bold := "bold"
	good := op.NewDocOp([]op.Component{
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil,
			[]op.AnnotationChange{{Key: "style/fontWeight", OldValue: &bold, NewValue: nil}})},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"style/fontWeight"}, nil)},
		op.Retain{Count: 1},
	})
	if err := op.Validate(doc, good); err != nil {
		t.Fatalf("the closed clear op should validate, got: %v", err)
	}
}

// twoParaBoldDoc builds <body><line/>AB<line/>CD</body> with style/fontWeight="bold"
// over BOTH "AB" and "CD", leaving the line markers between them null. Item stream:
// body(0) line(1) line-end(2) A(3) B(4) line(5) line-end(6) C(7) D(8) body-end(9).
func twoParaBoldDoc(t *testing.T) op.DocOp {
	t.Helper()
	bold := "bold"
	open := func() op.AnnotationBoundaryMap {
		return mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "style/fontWeight", NewValue: &bold}})
	}
	end := func() op.AnnotationBoundaryMap { return mustAnnBoundary(t, []string{"style/fontWeight"}, nil) }
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body"},
		op.ElementStart{Type: "line"}, op.ElementEnd{},
		op.AnnotationBoundary{Boundary: open()}, op.Characters{Text: "AB"}, op.AnnotationBoundary{Boundary: end()},
		op.ElementStart{Type: "line"}, op.ElementEnd{},
		op.AnnotationBoundary{Boundary: open()}, op.Characters{Text: "CD"}, op.AnnotationBoundary{Boundary: end()},
		op.ElementEnd{},
	})
}

// TestValidateRejectsCrossParagraphClearWithNullGap is the interior-skip reproduction: a
// clear spanning two bold runs separated by a null gap (the <line> boundary) that opens
// the change at the first run but SKIPS re-asserting/ending it across the null gap leaves
// the change carrying old="bold" over null items → rejected.
func TestValidateRejectsCrossParagraphClearWithNullGap(t *testing.T) {
	doc := twoParaBoldDoc(t)
	bold := "bold"
	// retain 3, open fontWeight bold→null, retain 2 (AB), retain 2 (line gap — NO
	// boundary), re-open at C, retain 2 (CD), end, retain 1. The override carries old=bold
	// across the null [5,7) gap.
	bad := op.NewDocOp([]op.Component{
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil,
			[]op.AnnotationChange{{Key: "style/fontWeight", OldValue: &bold, NewValue: nil}})},
		op.Retain{Count: 2},
		op.Retain{Count: 2}, // the null gap, no boundary — the defect
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil,
			[]op.AnnotationChange{{Key: "style/fontWeight", OldValue: &bold, NewValue: nil}})},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"style/fontWeight"}, nil)},
		op.Retain{Count: 1},
	})
	if err := op.Validate(doc, bad); err == nil {
		t.Fatal("expected the cross-paragraph clear with an un-ended null gap to be REJECTED")
	} else if !strings.Contains(err.Error(), "annotation") {
		t.Fatalf("expected an annotation error, got: %v", err)
	}
}

// TestValidateAcceptsCrossParagraphClear is the state-machine fix: ending the override
// over the null gap (and re-opening on the next bold run) validates cleanly.
func TestValidateAcceptsCrossParagraphClear(t *testing.T) {
	doc := twoParaBoldDoc(t)
	bold := "bold"
	good := op.NewDocOp([]op.Component{
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil,
			[]op.AnnotationChange{{Key: "style/fontWeight", OldValue: &bold, NewValue: nil}})},
		op.Retain{Count: 2}, // AB
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"style/fontWeight"}, nil)},
		op.Retain{Count: 2}, // the null gap — no override
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil,
			[]op.AnnotationChange{{Key: "style/fontWeight", OldValue: &bold, NewValue: nil}})},
		op.Retain{Count: 2}, // CD
		op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"style/fontWeight"}, nil)},
		op.Retain{Count: 1}, // body-end
	})
	if err := op.Validate(doc, good); err != nil {
		t.Fatalf("the state-machine cross-paragraph clear should validate, got: %v", err)
	}
}
