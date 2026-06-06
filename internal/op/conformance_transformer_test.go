package op_test

// Port of the hand-crafted transform edge cases from
// wave/.../model/document/operation/algorithm/DocOpTransformerTest.java.
//
// The Java suite asserts EXACT transformed-operation equality (via
// DocOpUtil.toConciseString). Our port asserts the same expected operations, but
// compared with op.DocOp.Equal — which compares component lists after
// normalization. This is the faithful equivalent for the structural and
// attribute cases.
//
// For the annotation cases, our builder deliberately does NOT reproduce the Java
// normalizer's annotation-state elision (documented in builder.go), so the
// transformed annotation boundaries can differ in representation while still
// converging. Those cases assert TP1 convergence over a concrete document
// instead (see ctpConverges); any case where even convergence diverges is
// marked t.Skip as a conformance finding.
//
// Many input builders mirror DocOpCreator (wave/.../model/testing/DocOpCreator.java).

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// --- DocOpCreator-equivalent builders (unique names; "ct" = conformance transform) ---

// ctRetain builds retain(n), dropping zero-length retains like the Java
// SimplifyingDocOpBuilder.
func ctRetain(n int) []op.Component {
	if n <= 0 {
		return nil
	}
	return []op.Component{op.Retain{Count: n}}
}

// ctInsertChars: retain(location), characters(chars), retain(size-location).
func ctInsertChars(size, location int, chars string) op.DocOp {
	var c []op.Component
	c = append(c, ctRetain(location)...)
	if chars != "" {
		c = append(c, op.Characters{Text: chars})
	}
	c = append(c, ctRetain(size-location)...)
	return op.NewDocOp(c)
}

// ctDeleteChars: retain(location), deleteCharacters(chars), retain(size-location-len).
func ctDeleteChars(size, location int, chars string) op.DocOp {
	var c []op.Component
	c = append(c, ctRetain(location)...)
	if chars != "" {
		c = append(c, op.DeleteCharacters{Text: chars})
	}
	c = append(c, ctRetain(size-location-len([]rune(chars)))...)
	return op.NewDocOp(c)
}

// ctIdentity: retain(size).
func ctIdentity(size int) op.DocOp { return op.NewDocOp(ctRetain(size)) }

// ctReplaceAttrs: retain(location), replaceAttributes(old,new), retain(size-location-1).
func ctReplaceAttrs(t *testing.T, size, location int, oldA, newA map[string]string) op.DocOp {
	t.Helper()
	var c []op.Component
	c = append(c, ctRetain(location)...)
	c = append(c, op.ReplaceAttributes{OldAttributes: mkAttrs(t, oldA), NewAttributes: mkAttrs(t, newA)})
	c = append(c, ctRetain(size-location-1)...)
	return op.NewDocOp(c)
}

// ctSetAttr: retain(location), updateAttributes(name: old->new), retain(size-location-1).
func ctSetAttr(t *testing.T, size, location int, name, oldV, newV string) op.DocOp {
	t.Helper()
	var c []op.Component
	c = append(c, ctRetain(location)...)
	c = append(c, op.UpdateAttributes{Update: mkUpdate(t, name, oldV, newV)})
	c = append(c, ctRetain(size-location-1)...)
	return op.NewDocOp(c)
}

// ctDeleteElement: retain(location), deleteElementStart(type,attrs), deleteElementEnd,
// retain(size-location-2).
func ctDeleteElement(t *testing.T, size, location int, typ string, attrs map[string]string) op.DocOp {
	t.Helper()
	var c []op.Component
	c = append(c, ctRetain(location)...)
	c = append(c, op.DeleteElementStart{Type: typ, Attributes: mkAttrs(t, attrs)})
	c = append(c, op.DeleteElementEnd{})
	c = append(c, ctRetain(size-location-2)...)
	return op.NewDocOp(c)
}

// ctSetAnnotation mirrors DocOpCreator.setAnnotation: retain(start),
// {open key:old->new, retain(end-start), end key}, retain(size-end).
// oldV/newV may be nil (Java null).
func ctSetAnnotation(t *testing.T, size, start, end int, key string, oldV, newV *string) op.DocOp {
	t.Helper()
	var c []op.Component
	c = append(c, ctRetain(start)...)
	if end-start > 0 {
		c = append(c, op.AnnotationBoundary{Boundary: ctOpenAnn(t, key, oldV, newV)})
		c = append(c, ctRetain(end-start)...)
		c = append(c, op.AnnotationBoundary{Boundary: ctEndAnn(t, key)})
	}
	c = append(c, ctRetain(size-end)...)
	return op.NewDocOp(c)
}

// ctOpenAnn builds a one-key value-change boundary (beginAnnotation).
func ctOpenAnn(t *testing.T, key string, oldV, newV *string) op.AnnotationBoundaryMap {
	t.Helper()
	m, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: key, OldValue: oldV, NewValue: newV}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ctEndAnn builds a one-key end boundary (finishAnnotation).
func ctEndAnn(t *testing.T, key string) op.AnnotationBoundaryMap {
	t.Helper()
	m, err := op.NewAnnotationBoundaryMap([]string{key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// --- assertion helpers ---

// ctExpect transforms (client, server) and asserts the transformed pair equals
// the Java expected (clientPrime, serverPrime) via DocOp.Equal.
func ctExpect(t *testing.T, client, server, wantClientPrime, wantServerPrime op.DocOp) {
	t.Helper()
	cp, sp, err := op.Transform(client, server)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !cp.Equal(wantClientPrime) {
		t.Errorf("clientPrime = %v\n          want %v", cp.Components(), wantClientPrime.Components())
	}
	if !sp.Equal(wantServerPrime) {
		t.Errorf("serverPrime = %v\n          want %v", sp.Components(), wantServerPrime.Components())
	}
}

// ctReversible runs ctExpect both ways (the Java ReversibleTestParameters): the
// transform of (server, client) must yield (serverPrime, clientPrime).
func ctReversible(t *testing.T, client, server, clientPrime, serverPrime op.DocOp) {
	t.Helper()
	ctExpect(t, client, server, clientPrime, serverPrime)
	ctExpect(t, server, client, serverPrime, clientPrime)
}

// --- testDeleteVsDelete ---

func TestConformanceTransformDeleteVsDelete(t *testing.T) {
	// A's deletion spatially before B's deletion.
	ctReversible(t,
		ctDeleteChars(20, 1, "abcde"),
		ctDeleteChars(20, 7, "fg"),
		ctDeleteChars(18, 1, "abcde"),
		ctDeleteChars(15, 2, "fg"))
	// A's deletion spatially adjacent to and before B's deletion.
	ctReversible(t,
		ctDeleteChars(20, 1, "abcde"),
		ctDeleteChars(20, 6, "fg"),
		ctDeleteChars(18, 1, "abcde"),
		ctDeleteChars(15, 1, "fg"))
	// A's deletion overlaps B's deletion.
	ctReversible(t,
		ctDeleteChars(20, 1, "abcde"),
		ctDeleteChars(20, 3, "cdefghi"),
		ctDeleteChars(13, 1, "ab"),
		ctDeleteChars(15, 1, "fghi"))
	// A's deletion a subset of B's deletion.
	ctReversible(t,
		ctDeleteChars(20, 1, "abcdefg"),
		ctDeleteChars(20, 3, "cd"),
		ctDeleteChars(18, 1, "abefg"),
		ctIdentity(13))
	// A's deletion identical to B's deletion.
	ctReversible(t,
		ctDeleteChars(20, 1, "abcdefg"),
		ctDeleteChars(20, 1, "abcdefg"),
		ctIdentity(13),
		ctIdentity(13))
}

// --- testInsertVsDelete ---

func TestConformanceTransformInsertVsDelete(t *testing.T) {
	// A's insertion spatially before B's deletion.
	ctReversible(t,
		ctInsertChars(20, 1, "abc"),
		ctDeleteChars(20, 2, "de"),
		ctInsertChars(18, 1, "abc"),
		ctDeleteChars(23, 5, "de"))
	// A's insertion spatially inside B's deletion.
	ctReversible(t,
		ctInsertChars(20, 2, "abc"),
		ctDeleteChars(20, 1, "ce"),
		ctInsertChars(18, 1, "abc"),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 1},
			op.DeleteCharacters{Text: "c"},
			op.Retain{Count: 3},
			op.DeleteCharacters{Text: "e"},
			op.Retain{Count: 17},
		}))
	// A's insertion spatially at the start of B's deletion.
	ctReversible(t,
		ctInsertChars(20, 1, "abc"),
		ctDeleteChars(20, 1, "de"),
		ctInsertChars(18, 1, "abc"),
		ctDeleteChars(23, 4, "de"))
	// A's insertion spatially at the end of B's deletion.
	ctReversible(t,
		ctInsertChars(20, 3, "abc"),
		ctDeleteChars(20, 1, "de"),
		ctInsertChars(18, 1, "abc"),
		ctDeleteChars(23, 1, "de"))
	// A's insertion spatially after B's deletion.
	ctReversible(t,
		ctInsertChars(20, 4, "abc"),
		ctDeleteChars(20, 1, "de"),
		ctInsertChars(18, 2, "abc"),
		ctDeleteChars(23, 1, "de"))
}

// --- testInsertVsInsert ---

func TestConformanceTransformInsertVsInsert(t *testing.T) {
	// A's insertion spatially before B's insertion.
	ctReversible(t,
		ctInsertChars(20, 1, "a"),
		ctInsertChars(20, 2, "1"),
		ctInsertChars(21, 1, "a"),
		ctInsertChars(21, 3, "1"))
	// Client's insertion at the same location as server's: client goes first.
	ctExpect(t,
		ctInsertChars(20, 2, "abc"),
		ctInsertChars(20, 2, "123"),
		ctInsertChars(23, 2, "abc"),
		ctInsertChars(23, 5, "123"))
}

// --- testStructuralVsInsert ---

func TestConformanceTransformStructuralVsInsert(t *testing.T) {
	// A's insertion spatially before B's insertion.
	ctReversible(t,
		ctInsertChars(20, 1, "a"),
		ctSampleStructural(20, 2),
		ctInsertChars(33, 1, "a"),
		ctSampleStructural(21, 3))
	// A's insertion spatially after B's insertion.
	ctReversible(t,
		ctInsertChars(20, 2, "a"),
		ctSampleStructural(20, 1),
		ctInsertChars(33, 15, "a"),
		ctSampleStructural(21, 1))
	// Same location (client text vs server structural): client first.
	ctExpect(t,
		ctInsertChars(20, 2, "a"),
		ctSampleStructural(20, 2),
		ctInsertChars(33, 2, "a"),
		ctSampleStructural(21, 3))
	// Same location (client structural vs server text): client first.
	ctExpect(t,
		ctSampleStructural(20, 2),
		ctInsertChars(20, 2, "a"),
		ctSampleStructural(21, 2),
		ctInsertChars(33, 15, "a"))
}

// --- testStructuralVsStructural ---

func TestConformanceTransformStructuralVsStructural(t *testing.T) {
	// A's insertion spatially before B's insertion.
	ctReversible(t,
		ctSampleStructural(20, 1),
		ctSampleStructural(20, 2),
		ctSampleStructural(33, 1),
		ctSampleStructural(33, 15))
	// Same location: client first.
	ctExpect(t,
		ctSampleStructural(20, 2),
		ctSampleStructural(20, 2),
		ctSampleStructural(33, 2),
		ctSampleStructural(33, 15))
}

// --- testAttributesVsDelete ---

func TestConformanceTransformAttributesVsDelete(t *testing.T) {
	nameVal := map[string]string{"name": "value"}
	href := map[string]string{"href": "http://www.google.com/"}
	// A's replace before B's deletion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 3, nameVal, href),
		ctDeleteChars(20, 5, "abc"),
		ctReplaceAttrs(t, 17, 3, nameVal, href),
		ctDeleteChars(20, 5, "abc"))
	// Adjacent to and before B's deletion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 4, nameVal, href),
		ctDeleteChars(20, 5, "abc"),
		ctReplaceAttrs(t, 17, 4, nameVal, href),
		ctDeleteChars(20, 5, "abc"))
	// After B's deletion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 9, nameVal, href),
		ctDeleteChars(20, 5, "abc"),
		ctReplaceAttrs(t, 17, 6, nameVal, href),
		ctDeleteChars(20, 5, "abc"))
	// Adjacent to and after B's deletion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 8, nameVal, href),
		ctDeleteChars(20, 5, "abc"),
		ctReplaceAttrs(t, 17, 5, nameVal, href),
		ctDeleteChars(20, 5, "abc"))
}

// --- testAttributesVsInsert ---

func TestConformanceTransformAttributesVsInsert(t *testing.T) {
	nameVal := map[string]string{"name": "value"}
	href := map[string]string{"href": "http://www.google.com/"}
	// Before B's insertion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 3, nameVal, href),
		ctInsertChars(20, 6, "hello"),
		ctReplaceAttrs(t, 25, 3, nameVal, href),
		ctInsertChars(20, 6, "hello"))
	// After B's insertion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 3, nameVal, href),
		ctInsertChars(20, 2, "hello"),
		ctReplaceAttrs(t, 25, 8, nameVal, href),
		ctInsertChars(20, 2, "hello"))
	// Adjacent to and after B's insertion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 3, nameVal, href),
		ctInsertChars(20, 3, "hello"),
		ctReplaceAttrs(t, 25, 8, nameVal, href),
		ctInsertChars(20, 3, "hello"))
	// Adjacent to and before B's insertion.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 3, nameVal, href),
		ctInsertChars(20, 4, "hello"),
		ctReplaceAttrs(t, 25, 3, nameVal, href),
		ctInsertChars(20, 4, "hello"))
}

// --- testAttributesVsAttributes ---

func TestConformanceTransformAttributesVsAttributes(t *testing.T) {
	nameVal := map[string]string{"name": "value"}
	google := map[string]string{"href": "http://www.google.com/"}
	yahoo := map[string]string{"href": "http://www.yahoo.com/"}
	// A's replace before B's replace.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 5, nameVal, google),
		ctReplaceAttrs(t, 20, 9, nameVal, yahoo),
		ctReplaceAttrs(t, 20, 5, nameVal, google),
		ctReplaceAttrs(t, 20, 9, nameVal, yahoo))
	// Coinciding replaces (different new attr name): client wins, server -> identity.
	ctExpect(t,
		ctReplaceAttrs(t, 20, 9, nameVal, google),
		ctReplaceAttrs(t, 20, 9, nameVal, map[string]string{"name": "Google!"}),
		ctReplaceAttrs(t, 20, 9, map[string]string{"name": "Google!"}, google),
		ctIdentity(20))
	// Coinciding replaces (same new attr key): client wins, server -> identity.
	ctExpect(t,
		ctReplaceAttrs(t, 20, 9, nameVal, google),
		ctReplaceAttrs(t, 20, 9, nameVal, yahoo),
		ctReplaceAttrs(t, 20, 9, yahoo, google),
		ctIdentity(20))
}

// --- testAttributesVsElementDeletion ---

func TestConformanceTransformAttributesVsElementDeletion(t *testing.T) {
	nameVal := map[string]string{"name": "value"}
	href := map[string]string{"href": "http://www.google.com/"}
	// A's replace coincides with B's element deletion: replace -> identity,
	// deletion carries the new attributes.
	ctReversible(t,
		ctReplaceAttrs(t, 20, 6, nameVal, href),
		ctDeleteElement(t, 20, 6, "type", nameVal),
		ctIdentity(18),
		ctDeleteElement(t, 20, 6, "type", href))
}

// --- testAttributeVsDelete ---

func TestConformanceTransformAttributeVsDelete(t *testing.T) {
	const initial, google = "initial", "http://www.google.com/"
	// Before B's deletion.
	ctReversible(t,
		ctSetAttr(t, 20, 3, "href", initial, google),
		ctDeleteChars(20, 5, "abc"),
		ctSetAttr(t, 17, 3, "href", initial, google),
		ctDeleteChars(20, 5, "abc"))
	// Adjacent to and before B's deletion.
	ctReversible(t,
		ctSetAttr(t, 20, 4, "href", initial, google),
		ctDeleteChars(20, 5, "abc"),
		ctSetAttr(t, 17, 4, "href", initial, google),
		ctDeleteChars(20, 5, "abc"))
	// After B's deletion.
	ctReversible(t,
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctDeleteChars(20, 5, "abc"),
		ctSetAttr(t, 17, 6, "href", initial, google),
		ctDeleteChars(20, 5, "abc"))
	// Adjacent to and after B's deletion.
	ctReversible(t,
		ctSetAttr(t, 20, 8, "href", initial, google),
		ctDeleteChars(20, 5, "abc"),
		ctSetAttr(t, 17, 5, "href", initial, google),
		ctDeleteChars(20, 5, "abc"))
}

// --- testAttributeVsInsert ---

func TestConformanceTransformAttributeVsInsert(t *testing.T) {
	const initial, google = "initial", "http://www.google.com/"
	// Before B's insertion.
	ctReversible(t,
		ctSetAttr(t, 20, 3, "href", initial, google),
		ctInsertChars(20, 6, "hello"),
		ctSetAttr(t, 25, 3, "href", initial, google),
		ctInsertChars(20, 6, "hello"))
	// Adjacent to and before B's insertion.
	ctReversible(t,
		ctSetAttr(t, 20, 3, "href", initial, google),
		ctInsertChars(20, 4, "hello"),
		ctSetAttr(t, 25, 3, "href", initial, google),
		ctInsertChars(20, 4, "hello"))
	// After B's insertion.
	ctReversible(t,
		ctSetAttr(t, 20, 3, "href", initial, google),
		ctInsertChars(20, 2, "hello"),
		ctSetAttr(t, 25, 8, "href", initial, google),
		ctInsertChars(20, 2, "hello"))
	// Adjacent to and after B's insertion.
	ctReversible(t,
		ctSetAttr(t, 20, 3, "href", initial, google),
		ctInsertChars(20, 3, "hello"),
		ctSetAttr(t, 25, 8, "href", initial, google),
		ctInsertChars(20, 3, "hello"))
}

// --- testAttributeVsAttributes ---

func TestConformanceTransformAttributeVsAttributes(t *testing.T) {
	const initial, google = "initial", "http://www.google.com/"
	nameVal := map[string]string{"name": "value"}
	yahoo := map[string]string{"href": "http://www.yahoo.com/"}
	// A's update before B's replace.
	ctReversible(t,
		ctSetAttr(t, 20, 5, "href", initial, google),
		ctReplaceAttrs(t, 20, 9, nameVal, yahoo),
		ctSetAttr(t, 20, 5, "href", initial, google),
		ctReplaceAttrs(t, 20, 9, nameVal, yahoo))
	// A's update after B's replace.
	ctReversible(t,
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctReplaceAttrs(t, 20, 5, nameVal, yahoo),
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctReplaceAttrs(t, 20, 5, nameVal, yahoo))
	// A's update coincides with B's replace (new name key): update -> identity.
	ctReversible(t,
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctReplaceAttrs(t, 20, 9, map[string]string{"href": initial}, map[string]string{"name": "Google!"}),
		ctIdentity(20),
		ctReplaceAttrs(t, 20, 9, map[string]string{"href": google}, map[string]string{"name": "Google!"}))
	// A's update coincides with B's replace (same href key): update -> identity.
	ctReversible(t,
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctReplaceAttrs(t, 20, 9, map[string]string{"href": initial}, yahoo),
		ctIdentity(20),
		ctReplaceAttrs(t, 20, 9, map[string]string{"href": google}, yahoo))
}

// --- testAttributeVsAttribute ---

func TestConformanceTransformAttributeVsAttribute(t *testing.T) {
	const initial, google, yahoo = "initial", "http://www.google.com/", "http://www.yahoo.com/"
	// A's update before B's update.
	ctReversible(t,
		ctSetAttr(t, 20, 5, "href", initial, google),
		ctSetAttr(t, 20, 9, "href", initial, yahoo),
		ctSetAttr(t, 20, 5, "href", initial, google),
		ctSetAttr(t, 20, 9, "href", initial, yahoo))
	// A's update has a different key than B's.
	ctReversible(t,
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctSetAttr(t, 20, 9, "name", initial, "Google!"),
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctSetAttr(t, 20, 9, "name", initial, "Google!"))
	// Client's update same key as server's: client wins (server -> empty update).
	ctExpect(t,
		ctSetAttr(t, 20, 9, "href", initial, google),
		ctSetAttr(t, 20, 9, "href", initial, yahoo),
		ctSetAttr(t, 20, 9, "href", yahoo, google),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 9},
			op.UpdateAttributes{Update: ctEmptyUpdate(t)},
			op.Retain{Count: 10},
		}))
}

// --- testAttributeVsElementDeletion ---

func TestConformanceTransformAttributeVsElementDeletion(t *testing.T) {
	const initial, google = "initial", "http://www.google.com/"
	// A's update coincides with B's element deletion: update -> identity, the
	// deletion carries the updated attribute value.
	ctReversible(t,
		ctSetAttr(t, 20, 6, "href", initial, google),
		ctDeleteElement(t, 20, 6, "type", map[string]string{"href": initial}),
		ctIdentity(18),
		ctDeleteElement(t, 20, 6, "type", map[string]string{"href": google}))
}

// --- testStructuralDeletionTransformations ---

func TestConformanceTransformStructuralDeletionTransformations(t *testing.T) {
	empty := map[string]string(nil)
	// A's deletion engulfs B's deletion.
	a := op.NewDocOp([]op.Component{
		op.Retain{Count: 2},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "ab"},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "cdefg"},
		op.DeleteElementEnd{},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "hi"},
		op.DeleteElementEnd{},
		op.DeleteElementEnd{},
		op.Retain{Count: 3},
	})
	b := op.NewDocOp([]op.Component{
		op.Retain{Count: 5},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "cdefg"},
		op.DeleteElementEnd{},
		op.Retain{Count: 8},
	})
	aPrime := op.NewDocOp([]op.Component{
		op.Retain{Count: 2},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "ab"},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "hi"},
		op.DeleteElementEnd{},
		op.DeleteElementEnd{},
		op.Retain{Count: 3},
	})
	ctReversible(t, a, b, aPrime, ctIdentity(5))
}

// --- testStructuralDeletionVsInsert ---
//
// A's deletion is a nested structural deletion engulfing (or adjacent to) B's
// text insertion. The big deletion op is reused across cases via ctNestedDelete.

// ctNestedDelete builds the recurring nested deletion of DocOpTransformerTest:
// retain(2), <type>ab<type>cdefg</type><type>hi</type></type>, retain(3),
// with deleteCharacters(extra) injected after the inner "cdefg" deletion when
// extra != "" (used to express engulfed insertions).
func ctNestedDelete(t *testing.T) op.DocOp {
	t.Helper()
	empty := map[string]string(nil)
	return op.NewDocOp([]op.Component{
		op.Retain{Count: 2},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "ab"},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "cdefg"},
		op.DeleteElementEnd{},
		op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)},
		op.DeleteCharacters{Text: "hi"},
		op.DeleteElementEnd{},
		op.DeleteElementEnd{},
		op.Retain{Count: 3},
	})
}

func TestConformanceTransformStructuralDeletionVsInsert(t *testing.T) {
	empty := map[string]string(nil)
	es := func() op.Component { return op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)} }
	ee := func() op.Component { return op.DeleteElementEnd{} }
	del := func(s string) op.Component { return op.DeleteCharacters{Text: s} }

	// A's deletion engulfs B's insertion at location 6 ("hello" lands before "cdefg").
	ctReversible(t,
		ctNestedDelete(t),
		ctInsertChars(20, 6, "hello"),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("hellocdefg"), ee(),
			es(), del("hi"), ee(), ee(), op.Retain{Count: 3},
		}),
		ctIdentity(5))
	// A's deletion engulfs B's insertion at location 7 ("hello" lands after "c").
	ctReversible(t,
		ctNestedDelete(t),
		ctInsertChars(20, 7, "hello"),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("chellodefg"), ee(),
			es(), del("hi"), ee(), ee(), op.Retain{Count: 3},
		}),
		ctIdentity(5))
	// A's deletion engulfs B's insertion at location 16 ("hello" lands at the end).
	ctReversible(t,
		ctNestedDelete(t),
		ctInsertChars(20, 16, "hello"),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("cdefg"), ee(),
			es(), del("hi"), ee(), del("hello"), ee(), op.Retain{Count: 3},
		}),
		ctIdentity(5))
	// A's deletion spatially adjacent to and before B's insertion at location 17.
	ctReversible(t,
		ctNestedDelete(t),
		ctInsertChars(20, 17, "hello"),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("cdefg"), ee(),
			es(), del("hi"), ee(), ee(), op.Retain{Count: 8},
		}),
		ctInsertChars(5, 2, "hello"))
	// A's deletion spatially before B's insertion at location 18.
	ctReversible(t,
		ctNestedDelete(t),
		ctInsertChars(20, 18, "hello"),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("cdefg"), ee(),
			es(), del("hi"), ee(), ee(), op.Retain{Count: 8},
		}),
		ctInsertChars(5, 3, "hello"))
}

// --- testStructuralDeletionVsStructural ---

func TestConformanceTransformStructuralDeletionVsStructural(t *testing.T) {
	empty := map[string]string(nil)
	es := func() op.Component { return op.DeleteElementStart{Type: "type", Attributes: mkAttrs(t, empty)} }
	ee := func() op.Component { return op.DeleteElementEnd{} }
	del := func(s string) op.Component { return op.DeleteCharacters{Text: s} }
	sampleEsStart := op.DeleteElementStart{Type: "sampleElement", Attributes: mkAttrs(t, empty)}
	sampleText := op.DeleteCharacters{Text: "sample text"}

	// A's deletion engulfs B's structural insertion at location 6.
	ctReversible(t,
		ctNestedDelete(t),
		ctSampleStructural(20, 6),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(),
			sampleEsStart, sampleText, ee(),
			del("cdefg"), ee(), es(), del("hi"), ee(), ee(), op.Retain{Count: 3},
		}),
		ctIdentity(5))
	// A's deletion engulfs B's structural insertion at location 7.
	ctReversible(t,
		ctNestedDelete(t),
		ctSampleStructural(20, 7),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("c"),
			sampleEsStart, sampleText, ee(),
			del("defg"), ee(), es(), del("hi"), ee(), ee(), op.Retain{Count: 3},
		}),
		ctIdentity(5))
	// A's deletion engulfs B's structural insertion at location 16.
	ctReversible(t,
		ctNestedDelete(t),
		ctSampleStructural(20, 16),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("cdefg"), ee(),
			es(), del("hi"), ee(),
			sampleEsStart, sampleText, ee(),
			ee(), op.Retain{Count: 3},
		}),
		ctIdentity(5))
	// A's deletion spatially adjacent to and before B's structural insertion at 17.
	ctReversible(t,
		ctNestedDelete(t),
		ctSampleStructural(20, 17),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("cdefg"), ee(),
			es(), del("hi"), ee(), ee(), op.Retain{Count: 16},
		}),
		ctSampleStructural(5, 2))
	// A's deletion spatially before B's structural insertion at 18.
	ctReversible(t,
		ctNestedDelete(t),
		ctSampleStructural(20, 18),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 2}, es(), del("ab"), es(), del("cdefg"), ee(),
			es(), del("hi"), ee(), ee(), op.Retain{Count: 16},
		}),
		ctSampleStructural(5, 3))
}

// --- sample structural builder ---

// ctSampleStructural mirrors DocOpTransformerTest.sampleStructural:
// retain(location), <sampleElement>sample text</sampleElement>, retain(size-location).
func ctSampleStructural(size, location int) op.DocOp {
	empty, _ := op.NewAttributes(nil)
	var c []op.Component
	c = append(c, ctRetain(location)...)
	c = append(c,
		op.ElementStart{Type: "sampleElement", Attributes: empty},
		op.Characters{Text: "sample text"},
		op.ElementEnd{},
	)
	c = append(c, ctRetain(size-location)...)
	return op.NewDocOp(c)
}

// ctEmptyUpdate is the empty AttributesUpdate (Java AttributesUpdateImpl.EMPTY_MAP).
func ctEmptyUpdate(t *testing.T) op.AttributesUpdate {
	t.Helper()
	u, err := op.NewAttributesUpdate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// ctDoc20 builds a length-20 flat document of distinct characters, for verifying
// TP1 convergence of the annotation transform cases (which annotate/delete over
// character positions 1..19 of a size-20 document).
func ctDoc20(t *testing.T) op.DocOp {
	t.Helper()
	return op.NewDocOp([]op.Component{op.Characters{Text: "abcdefghijklmnopqrst"}})
}

// ctConverges asserts TP1 convergence of (client, server) over doc using the
// same document-equivalence notion as the existing tp1 helper (sameDocument,
// which compares effective per-item annotation state and ignores boundary
// representation). Returns true if convergence holds.
func ctConverges(t *testing.T, doc, client, server op.DocOp) bool {
	t.Helper()
	cp, sp, err := op.Transform(client, server)
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
	left, err := op.Apply(afterClient, sp)
	if err != nil {
		t.Fatalf("apply server' after client: %v", err)
	}
	right, err := op.Apply(afterServer, cp)
	if err != nil {
		t.Fatalf("apply client' after server: %v", err)
	}
	return sameDocument(left, right)
}
