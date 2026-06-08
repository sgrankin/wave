package op

import (
	"fmt"
)

// Validate reports whether op is well-formed and valid against the document doc
// (an insertion-only DocOp), returning nil if op may be safely applied
// (Compose(doc, op)), else an error describing the first violation.
//
// This is the guard Java's DocOpValidator/DocOpAutomaton provided that the port
// otherwise lacks: Apply == Compose cancels deletions by LENGTH only, so a
// length-correct but content-wrong operation — a deleteCharacters whose text does
// not match the document, a deleteElementStart with the wrong tag/attributes, a
// replace/updateAttributes whose expected old value disagrees — is otherwise
// applied silently, corrupting the stored document and desyncing every client
// (their hashed versions diverge). Validate catches exactly these, plus the full
// structural well-formedness automaton (insertion/deletion scope nesting, balanced
// inserted/deleted elements, annotation balance/adjacency, XML-name tags, BMP-only
// text) and the document-content/coverage class. It is meant for the SERVER SUBMIT
// PATH (untrusted client ops); replay of already-accepted stored deltas need not
// re-validate.
//
// This is a faithful port of Java's DocOpValidator/DocOpAutomaton run with
// NO_SCHEMA_CONSTRAINTS (the box server uses SchemaCollection.empty()): the
// ILL_FORMED (well-formedness) and INVALID_DOCUMENT (content/coverage) axes are
// enforced; the INVALID_SCHEMA axis (permitsChild / permittedCharacters /
// permitsAttribute / required-initial-children) is authoritatively dropped — see
// docs/architecture/01 §8. Like the Java automaton, the first violation in stream
// order short-circuits; on a single component the well-formedness check precedes
// the content (validity) check, so a malformed op is reported as ill-formed.
//
// NOTE on annotation-deletion coverage: checkAnnotationsForDeletion's O(items*keys)
// "every annotation present on a deleted item is reset by the update or already
// equals the inherited target" sub-predicate is the single most divergence-prone
// rule (Java DocOpAutomaton.java:1168-1193). It is implemented faithfully here;
// because the box server runs SchemaCollection.empty() and replay is not
// re-validated, a tightened reject can only affect a live client submitting a
// malformed delete, never stored history.
func Validate(doc, op DocOp) error {
	items := documentItems(doc)
	a := newAutomaton(items)
	for ci, c := range op.components {
		if err := a.check(ci, c); err != nil {
			return err
		}
		a.do(c)
	}
	return a.checkFinish()
}

// checkUpdateOldValues verifies an attribute update's expected old values against
// the element's current attributes — the same compatibility check Attributes.
// updateWith enforces during Compose, but returned as an error instead of a panic
// (so a malformed update is a rejected submit, not a server crash).
func checkUpdateOldValues(cur Attributes, u AttributesUpdate) error {
	for _, c := range u.All() {
		v, present := cur.Get(c.Name)
		if present {
			if c.OldValue == nil || *c.OldValue != v {
				return fmt.Errorf("updateAttributes old value for %q differs from document", c.Name)
			}
		} else if c.OldValue != nil {
			return fmt.Errorf("updateAttributes expected attribute %q present, but document has none", c.Name)
		}
	}
	return nil
}

// itemKind classifies a flattened document item.
type itemKind int

const (
	itemChar itemKind = iota
	itemStart
	itemEnd
)

// docItem is one position in a document: a single character, an element start
// (with its tag/attributes), or an element end. Annotation boundaries are
// zero-width and contribute no items; instead each item carries ann, the
// effective annotation snapshot (key→value) in force at that position — the form
// the automaton's annotationsAt/getAnnotation/firstAnnotationChange queries read
// (Java AutomatonDocument.annotationsAt/getAnnotation).
type docItem struct {
	kind  itemKind
	r     rune              // itemChar
	typ   string            // itemStart
	attrs Attributes        // itemStart
	ann   map[string]string // effective annotations at this position (nil => none)
}

// documentItems flattens an insertion-only DocOp into its sequence of items
// (each character is one item), the form against which an operation's
// retains/deletions are validated. It threads the document's running annotation
// state left-to-right: each AnnotationBoundary applies its Changes() (NewValue
// sets, nil NewValue clears) and drops its EndKeys(), and every item is stamped
// with a copy of the annotation state in force at it. This mirrors how a
// DocInitialization's boundaries determine the effective annotation at each item
// (matching sameDocument/docTokens), which the deletion-annotation rules read.
func documentItems(doc DocOp) []docItem {
	var items []docItem
	cur := map[string]string{} // running effective annotation state
	stamp := func() map[string]string {
		if len(cur) == 0 {
			return nil
		}
		cp := make(map[string]string, len(cur))
		for k, v := range cur {
			cp[k] = v
		}
		return cp
	}
	for _, c := range doc.components {
		switch c := c.(type) {
		case Characters:
			snap := stamp()
			for _, r := range c.Text {
				items = append(items, docItem{kind: itemChar, r: r, ann: snap})
			}
		case ElementStart:
			items = append(items, docItem{kind: itemStart, typ: c.Type, attrs: c.Attributes, ann: stamp()})
		case ElementEnd:
			items = append(items, docItem{kind: itemEnd, ann: stamp()})
		case AnnotationBoundary:
			for _, k := range c.Boundary.EndKeys() {
				delete(cur, k)
			}
			for _, ch := range c.Boundary.Changes() {
				if ch.NewValue == nil {
					delete(cur, ch.Key)
				} else {
					cur[ch.Key] = *ch.NewValue
				}
			}
		default:
			// A non-insertion component in a "document" means doc is not actually a
			// DocInitialization; leave it out of the item stream (the coverage check
			// will then reject any op that assumed those items).
		}
	}
	return items
}
