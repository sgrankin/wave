package op_test

import (
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// These tables exercise the structural-automaton rules ported from Java's
// DocOpAutomaton (insertion/deletion scope nesting, annotation balance/adjacency,
// XML-name tags, BMP-only text, and the three annotation-old-value predicates) —
// the rules the previous partial validator deferred. Error messages are matched by
// SUBSTRING only.

func mustAnnBoundary(t *testing.T, ends []string, changes []op.AnnotationChange) op.AnnotationBoundaryMap {
	t.Helper()
	m, err := op.NewAnnotationBoundaryMap(ends, changes)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// annDoc builds a single-character document with one annotation key set over it:
// boundary(set key=val), 'c', boundary(end key). Item 0 carries ann{key:val}.
func annDoc(t *testing.T, ch string, key, val string) op.DocOp {
	t.Helper()
	set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: key, NewValue: &val}})
	end := mustAnnBoundary(t, []string{key}, nil)
	return op.NewDocOp([]op.Component{
		op.AnnotationBoundary{Boundary: set},
		op.Characters{Text: ch},
		op.AnnotationBoundary{Boundary: end},
	})
}

func TestValidateRejectsStructural(t *testing.T) {
	withAttr := func(m map[string]string) op.Attributes {
		a, err := op.NewAttributes(m)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	cases := []struct {
		name    string
		doc     op.DocOp
		op      op.DocOp
		wantSub string
	}{
		{
			"retain inside an insertion",
			textDoc("ab"),
			op.NewDocOp([]op.Component{
				op.ElementStart{Type: "x"}, op.Retain{Count: 1}, op.ElementEnd{}, op.Retain{Count: 2},
			}),
			"retain inside an insertion",
		},
		{
			"retain inside a deletion",
			elemDoc(t, "x", nil, "a"), // <x>a</x>: [start,'a',end]
			op.NewDocOp([]op.Component{
				op.DeleteElementStart{Type: "x"}, op.Retain{Count: 1}, op.DeleteCharacters{Text: "a"}, op.DeleteElementEnd{},
			}),
			"retain inside a deletion",
		},
		{
			"characters inside a deletion",
			elemDoc(t, "x", nil, "a"),
			op.NewDocOp([]op.Component{
				op.DeleteElementStart{Type: "x"}, op.Characters{Text: "Q"}, op.DeleteCharacters{Text: "a"}, op.DeleteElementEnd{},
			}),
			"insertion inside a deletion",
		},
		{
			"deleteCharacters inside an insertion",
			textDoc("ab"),
			op.NewDocOp([]op.Component{
				op.ElementStart{Type: "x"}, op.DeleteCharacters{Text: "a"}, op.ElementEnd{}, op.Retain{Count: 2},
			}),
			"deletion inside an insertion",
		},
		{
			"attribute change inside an insertion",
			textDoc("ab"),
			op.NewDocOp([]op.Component{
				op.ElementStart{Type: "x"},
				op.ReplaceAttributes{OldAttributes: withAttr(nil), NewAttributes: withAttr(map[string]string{"k": "v"})},
				op.ElementEnd{}, op.Retain{Count: 2},
			}),
			"attribute change inside an insertion or deletion",
		},
		{
			"element type not an XML name (1 bad char)",
			op.EmptyDoc(),
			op.NewDocOp([]op.Component{op.ElementStart{Type: "a b"}, op.ElementEnd{}}),
			"not a valid XML name",
		},
		{
			// document item 0 carries the raw noncharacter rune; deleting it via a
			// matching DeleteCharacters is rejected by firstBadTextRune, not by content.
			"deleteCharacters of an invalid (noncharacter) text",
			textDoc("﷐"),
			op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "﷐"}}),
			"deleteCharacters contain invalid text",
		},
		{
			"characters of an invalid (noncharacter) text",
			op.EmptyDoc(),
			op.NewDocOp([]op.Component{op.Characters{Text: "￾"}}),
			"characters contain invalid text",
		},
		{
			"adjacent annotation boundaries",
			textDoc("ab"),
			op.NewDocOp([]op.Component{
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", NewValue: sp("1")}})},
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "j", NewValue: sp("2")}})},
				op.Retain{Count: 2},
			}),
			"adjacent annotation boundaries",
		},
		{
			"ending an annotation that is not open",
			textDoc("ab"),
			op.NewDocOp([]op.Component{
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"k"}, nil)},
				op.Retain{Count: 2},
			}),
			"is not open",
		},
		{
			"unclosed deletion",
			elemDoc(t, "x", nil, "a"),
			op.NewDocOp([]op.Component{
				op.DeleteElementStart{Type: "x"}, op.DeleteCharacters{Text: "a"},
			}),
			"deletion(s) unclosed",
		},
		{
			"unclosed annotation",
			textDoc("ab"),
			op.NewDocOp([]op.Component{
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", NewValue: sp("1")}})},
				op.Retain{Count: 2},
			}),
			"unclosed annotation",
		},
		{
			// retain predicate: open annotation's expected old value disagrees with
			// the document over the retained range (doc has k=1, op expects old absent).
			"retain annotation old value differs",
			annDoc(t, "c", "k", "1"),
			op.NewDocOp([]op.Component{
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", OldValue: sp("9"), NewValue: sp("2")}})},
				op.Retain{Count: 1},
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"k"}, nil)},
			}),
			"annotation old value for \"k\" differs from document",
		},
		{
			// insertion predicate: inserting with an open key whose expected old value
			// does not match the annotation inherited from the left (here pos -1 => absent,
			// but op expects old="1").
			"insertion annotation old value differs from inherited",
			op.EmptyDoc(),
			op.NewDocOp([]op.Component{
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", OldValue: sp("1"), NewValue: sp("2")}})},
				op.Characters{Text: "Q"},
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"k"}, nil)},
			}),
			"differs from document (inserting)",
		},
		{
			// deletion predicate (relative): deleting a char that carries annotation k=1
			// when the inherited target does NOT have k=1 and the op does not reset k —
			// this orphans the annotation (DocOpAutomaton missingAnnotationForDeletion).
			"deletion without resetting an inherited-mismatched annotation",
			func() op.DocOp {
				// doc: 'a' (no ann), then 'b' with k=1, then ann ends. Deleting 'b' alone
				// without an update resetting k is rejected: 'b' carries k=1 but the target
				// (inherited from 'a') has no k.
				set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", NewValue: sp("1")}})
				end := mustAnnBoundary(t, []string{"k"}, nil)
				return op.NewDocOp([]op.Component{
					op.Characters{Text: "a"},
					op.AnnotationBoundary{Boundary: set},
					op.Characters{Text: "b"},
					op.AnnotationBoundary{Boundary: end},
				})
			}(),
			op.NewDocOp([]op.Component{
				op.Retain{Count: 1},
				op.DeleteCharacters{Text: "b"},
			}),
			"inconsistent with target",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := op.Validate(tc.doc, tc.op)
			if err == nil {
				t.Fatalf("Validate accepted a structurally-invalid op")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidateAcceptsStructural(t *testing.T) {
	withAttr := func(m map[string]string) op.Attributes {
		a, err := op.NewAttributes(m)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	cases := []struct {
		name string
		doc  op.DocOp
		op   op.DocOp
	}{
		{
			"balanced delete-element pair",
			elemDoc(t, "x", nil, "a"),
			op.NewDocOp([]op.Component{
				op.DeleteElementStart{Type: "x"}, op.DeleteCharacters{Text: "a"}, op.DeleteElementEnd{},
			}),
		},
		{
			"opened and closed annotation over a retain",
			annDoc(t, "c", "k", "1"),
			op.NewDocOp([]op.Component{
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", OldValue: sp("1"), NewValue: sp("2")}})},
				op.Retain{Count: 1},
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"k"}, nil)},
			}),
		},
		{
			"valid XML-name element and attribute keys",
			op.EmptyDoc(),
			op.NewDocOp([]op.Component{
				op.ElementStart{Type: "ns:el-1", Attributes: withAttr(map[string]string{"a-b": "v", "c.d": "w"})},
				op.ElementEnd{},
			}),
		},
		{
			// deletion that resets the annotation back to the target: deleting 'b'
			// (carrying k=1) while the update sets k -> nil (matching inherited absent
			// from 'a') is accepted (DocOpAutomaton testDeletionAnnotationsAreRelative3
			// positive case).
			"delete that resets the annotation",
			func() op.DocOp {
				set := mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", NewValue: sp("1")}})
				end := mustAnnBoundary(t, []string{"k"}, nil)
				return op.NewDocOp([]op.Component{
					op.Characters{Text: "a"},
					op.AnnotationBoundary{Boundary: set},
					op.Characters{Text: "b"},
					op.AnnotationBoundary{Boundary: end},
				})
			}(),
			op.NewDocOp([]op.Component{
				op.Retain{Count: 1},
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, nil, []op.AnnotationChange{{Key: "k", OldValue: sp("1"), NewValue: nil}})},
				op.DeleteCharacters{Text: "b"},
				op.AnnotationBoundary{Boundary: mustAnnBoundary(t, []string{"k"}, nil)},
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := op.Validate(tc.doc, tc.op); err != nil {
				t.Errorf("Validate rejected a valid op: %v", err)
			}
		})
	}
}
