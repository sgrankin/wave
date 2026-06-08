package op

import (
	"fmt"
	"unicode/utf8"
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
// (their hashed versions diverge). Validate catches exactly these, plus basic
// well-formedness (coverage of the whole document, balanced inserted elements,
// valid inserted text). It is meant for the SERVER SUBMIT PATH (untrusted client
// ops); replay of already-accepted stored deltas need not re-validate.
//
// Scope: it checks the content/coverage class that causes silent corruption. It
// does not (yet) enforce the full structural automaton (element nesting against the
// document's tree, annotation balance, XML-name tags) — those are caught later as a
// non-initialization result or are lower-severity well-formedness.
func Validate(doc, op DocOp) error {
	items := documentItems(doc)
	pos := 0         // cursor into the document's items
	insertDepth := 0 // inserted element-starts awaiting their end
	for ci, c := range op.components {
		switch c := c.(type) {
		case Retain:
			if c.Count <= 0 {
				return fmt.Errorf("op component %d: retain count %d must be positive", ci, c.Count)
			}
			if pos+c.Count > len(items) {
				return fmt.Errorf("op component %d: retain %d runs past end of document (%d item(s) left)", ci, c.Count, len(items)-pos)
			}
			pos += c.Count
		case Characters:
			if c.Text == "" {
				return fmt.Errorf("op component %d: empty characters insertion", ci)
			}
			if !utf8.ValidString(c.Text) {
				return fmt.Errorf("op component %d: inserted characters are not valid UTF-8", ci)
			}
		case ElementStart:
			if c.Type == "" {
				return fmt.Errorf("op component %d: element start with empty type", ci)
			}
			insertDepth++
		case ElementEnd:
			if insertDepth == 0 {
				return fmt.Errorf("op component %d: element end with no matching inserted start", ci)
			}
			insertDepth--
		case DeleteCharacters:
			if c.Text == "" {
				return fmt.Errorf("op component %d: empty characters deletion", ci)
			}
			for _, r := range c.Text {
				if pos >= len(items) {
					return fmt.Errorf("op component %d: deleteCharacters runs past end of document", ci)
				}
				it := items[pos]
				if it.kind != itemChar {
					return fmt.Errorf("op component %d: deleteCharacters at a non-character (element) position", ci)
				}
				if it.r != r {
					return fmt.Errorf("op component %d: deleteCharacters content mismatch: document has %q, op deletes %q", ci, string(it.r), string(r))
				}
				pos++
			}
		case DeleteElementStart:
			if pos >= len(items) || items[pos].kind != itemStart {
				return fmt.Errorf("op component %d: deleteElementStart but document has no element start here", ci)
			}
			if items[pos].typ != c.Type {
				return fmt.Errorf("op component %d: deleteElementStart type mismatch: document %q, op %q", ci, items[pos].typ, c.Type)
			}
			if !items[pos].attrs.Equal(c.Attributes) {
				return fmt.Errorf("op component %d: deleteElementStart attributes differ from document", ci)
			}
			pos++
		case DeleteElementEnd:
			if pos >= len(items) || items[pos].kind != itemEnd {
				return fmt.Errorf("op component %d: deleteElementEnd but document has no element end here", ci)
			}
			pos++
		case ReplaceAttributes:
			if pos >= len(items) || items[pos].kind != itemStart {
				return fmt.Errorf("op component %d: replaceAttributes but document has no element start here", ci)
			}
			if !items[pos].attrs.Equal(c.OldAttributes) {
				return fmt.Errorf("op component %d: replaceAttributes old attributes differ from document", ci)
			}
			pos++
		case UpdateAttributes:
			if pos >= len(items) || items[pos].kind != itemStart {
				return fmt.Errorf("op component %d: updateAttributes but document has no element start here", ci)
			}
			if err := checkUpdateOldValues(items[pos].attrs, c.Update); err != nil {
				return fmt.Errorf("op component %d: %w", ci, err)
			}
			pos++
		case AnnotationBoundary:
			// Zero-width: opens/closes annotation ranges, consumes no item. Annotation
			// balance validation is out of scope for this content-corruption guard.
		default:
			return fmt.Errorf("op component %d: unknown component type %T", ci, c)
		}
	}
	if pos != len(items) {
		return fmt.Errorf("op does not cover the whole document: consumed %d of %d item(s)", pos, len(items))
	}
	if insertDepth != 0 {
		return fmt.Errorf("op leaves %d inserted element(s) unclosed", insertDepth)
	}
	return nil
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
// zero-width and contribute no items.
type docItem struct {
	kind  itemKind
	r     rune       // itemChar
	typ   string     // itemStart
	attrs Attributes // itemStart
}

// documentItems flattens an insertion-only DocOp into its sequence of items
// (each character is one item), the form against which an operation's
// retains/deletions are validated.
func documentItems(doc DocOp) []docItem {
	var items []docItem
	for _, c := range doc.components {
		switch c := c.(type) {
		case Characters:
			for _, r := range c.Text {
				items = append(items, docItem{kind: itemChar, r: r})
			}
		case ElementStart:
			items = append(items, docItem{kind: itemStart, typ: c.Type, attrs: c.Attributes})
		case ElementEnd:
			items = append(items, docItem{kind: itemEnd})
		case AnnotationBoundary:
			// zero-width
		default:
			// A non-insertion component in a "document" means doc is not actually a
			// DocInitialization; leave it out of the item stream (the coverage check
			// will then reject any op that assumed those items).
		}
	}
	return items
}
