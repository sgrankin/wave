package op

import (
	"fmt"
	"unicode/utf8"
)

// docOpAutomaton is a faithful port of Java's DocOpAutomaton
// (wave/.../document/operation/automaton/DocOpAutomaton.java) run with
// NO_SCHEMA_CONSTRAINTS: a streaming well-formedness + validity checker over a
// flat document item stream. The caller drives it one component at a time —
// check(ci, c) inspects without mutating, do(c) advances state, and checkFinish()
// decides acceptance at end of stream. do must never run on an ill-formed
// component (Validate returns on the first check error before calling do).
//
// State mirrors Java's tracked fields, minus the two schema fields (resultingPos
// was diagnostics-only; nextRequiredElement is dead under NO_SCHEMA_CONSTRAINTS):
//   - effectivePos: cursor into the original doc, advanced by retain + all three
//     deletions + replace/update (NOT by insertions). May exceed len(items) when
//     an op over-retains/over-deletes; that makes it invalid but the automaton
//     keeps going.
//   - insertionStack: open inserted element-starts (tags) awaiting their end.
//     Non-empty => "inside an insertion".
//   - deletionStackDepth: open deleteElementStart awaiting their end. >0 => "inside
//     a deletion".
//   - annUpdate: the currently-open annotation key→(old,new) changes, folded across
//     annotationBoundary components. Non-empty at finish => unbalanced annotation.
//   - afterAnnBoundary: true immediately after an annotationBoundary; reset by every
//     other do. Rejects two adjacent boundaries.
//   - delTarget/delTargetValid: the annotation state deleted content must carry —
//     inheritedAnnotations() (items[effectivePos-1].ann, or empty at pos 0/over-end)
//     overlaid by annUpdate's new values with no compatibility check. Recomputed by
//     updateDeletionTargetAnnotations after each advancing do; delTargetValid=false
//     once effectivePos>len(items) (Java's null sentinel), disabling deletion-
//     annotation checks.
type docOpAutomaton struct {
	items []docItem

	effectivePos       int
	insertionStack     []string // tags, bottom-of-stack first
	deletionStackDepth int
	annUpdate          map[string]valueUpdate
	afterAnnBoundary   bool
	delTarget          map[string]string
	delTargetValid     bool
}

func newAutomaton(items []docItem) *docOpAutomaton {
	a := &docOpAutomaton{
		items:     items,
		annUpdate: map[string]valueUpdate{},
	}
	a.updateDeletionTargetAnnotations()
	return a
}

func (a *docOpAutomaton) docLength() int { return len(a.items) }

func (a *docOpAutomaton) insertionStackEmpty() bool { return len(a.insertionStack) == 0 }
func (a *docOpAutomaton) deletionStackEmpty() bool  { return a.deletionStackDepth == 0 }

// --- effective document symbol queries (DocOpAutomaton.effectiveDocSymbol) ---

// at returns the item at effectivePos, or false if past end.
func (a *docOpAutomaton) at() (docItem, bool) {
	if a.effectivePos < 0 || a.effectivePos >= len(a.items) {
		return docItem{}, false
	}
	return a.items[a.effectivePos], true
}

// --- annotation queries (bounds-safe, mirroring AutomatonDocument) ---

// getAnnotation returns the document's annotation value for key at pos and whether
// it is present. Out-of-range positions return ("", false), like Java's
// EMPTY_ANNOTATIONS at the document edges.
func (a *docOpAutomaton) getAnnotation(pos int, key string) (string, bool) {
	if pos < 0 || pos >= len(a.items) {
		return "", false
	}
	v, ok := a.items[pos].ann[key]
	return v, ok
}

// firstAnnotationChange returns the first position p in [start, end) where the
// document's annotation for key differs from from (a *string; nil = absent), or -1
// if it equals from throughout. Positions past the document end count as absent.
func (a *docOpAutomaton) firstAnnotationChange(start, end int, key string, from *string) int {
	for p := start; p < end; p++ {
		v, ok := a.getAnnotation(p, key)
		if !annEqual(ptr(v, ok), from) {
			return p
		}
	}
	return -1
}

// inheritedAnnotations returns the annotation state inherited from the position to
// the left of effectivePos: empty at pos 0 or past the document end, else
// items[effectivePos-1].ann (DocOpAutomaton.inheritedAnnotations).
func (a *docOpAutomaton) inheritedAnnotations() map[string]string {
	if a.effectivePos == 0 || a.effectivePos > len(a.items) {
		return nil
	}
	return a.items[a.effectivePos-1].ann
}

// updateDeletionTargetAnnotations recomputes delTarget as the inherited annotations
// overlaid by annUpdate's new values, with no compatibility check; invalidates the
// target once effectivePos runs past the document end
// (DocOpAutomaton.updateDeletionTargetAnnotations).
func (a *docOpAutomaton) updateDeletionTargetAnnotations() {
	if a.effectivePos > len(a.items) {
		a.delTarget = nil
		a.delTargetValid = false
		return
	}
	t := map[string]string{}
	for k, v := range a.inheritedAnnotations() {
		t[k] = v
	}
	for key, vu := range a.annUpdate {
		if vu.new == nil {
			delete(t, key)
		} else {
			t[key] = *vu.new
		}
	}
	a.delTarget = t
	a.delTargetValid = true
}

func (a *docOpAutomaton) advance(n int) { a.effectivePos += n }

// --- dispatch ---

func (a *docOpAutomaton) check(ci int, c Component) error {
	switch c := c.(type) {
	case Retain:
		return a.checkRetain(ci, c.Count)
	case Characters:
		return a.checkCharacters(ci, c.Text)
	case ElementStart:
		return a.checkElementStart(ci, c.Type, c.Attributes)
	case ElementEnd:
		return a.checkElementEnd(ci)
	case DeleteCharacters:
		return a.checkDeleteCharacters(ci, c.Text)
	case DeleteElementStart:
		return a.checkDeleteElementStart(ci, c.Type, c.Attributes)
	case DeleteElementEnd:
		return a.checkDeleteElementEnd(ci)
	case ReplaceAttributes:
		return a.checkReplaceAttributes(ci, c.OldAttributes, c.NewAttributes)
	case UpdateAttributes:
		return a.checkUpdateAttributes(ci, c.Update)
	case AnnotationBoundary:
		return a.checkAnnotationBoundary(ci, c.Boundary)
	default:
		return fmt.Errorf("op component %d: unknown component type %T", ci, c)
	}
}

func (a *docOpAutomaton) do(c Component) {
	switch c := c.(type) {
	case Retain:
		a.advance(c.Count)
		a.updateDeletionTargetAnnotations()
		a.afterAnnBoundary = false
	case Characters:
		a.updateDeletionTargetAnnotations()
		a.afterAnnBoundary = false
	case ElementStart:
		a.updateDeletionTargetAnnotations()
		a.insertionStack = append(a.insertionStack, c.Type)
		a.afterAnnBoundary = false
	case ElementEnd:
		a.updateDeletionTargetAnnotations()
		a.insertionStack = a.insertionStack[:len(a.insertionStack)-1]
		a.afterAnnBoundary = false
	case DeleteCharacters:
		a.advance(utf8.RuneCountInString(c.Text))
		a.afterAnnBoundary = false
	case DeleteElementStart:
		a.deletionStackDepth++
		a.advance(1)
		a.afterAnnBoundary = false
	case DeleteElementEnd:
		a.deletionStackDepth--
		a.advance(1)
		a.afterAnnBoundary = false
	case ReplaceAttributes:
		a.advance(1)
		a.updateDeletionTargetAnnotations()
		a.afterAnnBoundary = false
	case UpdateAttributes:
		a.advance(1)
		a.updateDeletionTargetAnnotations()
		a.afterAnnBoundary = false
	case AnnotationBoundary:
		a.foldAnnotation(c.Boundary)
		a.afterAnnBoundary = true
	}
}

// --- retain ---

func (a *docOpAutomaton) checkRetain(ci, count int) error {
	// well-formedness
	if count <= 0 {
		return fmt.Errorf("op component %d: retain count %d must be positive", ci, count)
	}
	if !a.insertionStackEmpty() {
		return fmt.Errorf("op component %d: retain inside an insertion", ci)
	}
	if !a.deletionStackEmpty() {
		return fmt.Errorf("op component %d: retain inside a deletion", ci)
	}
	// validity
	if a.effectivePos+count > a.docLength() {
		return fmt.Errorf("op component %d: retain %d runs past end of document (%d item(s) left)", ci, count, a.docLength()-a.effectivePos)
	}
	return a.checkAnnotationsForRetain(ci, count)
}

// checkAnnotationsForRetain verifies that, over [effectivePos, effectivePos+n),
// each open annotation key's existing document value uniformly equals that key's
// expected old value (DocOpAutomaton.checkAnnotationsForRetain).
func (a *docOpAutomaton) checkAnnotationsForRetain(ci, n int) error {
	for key, vu := range a.annUpdate {
		if a.firstAnnotationChange(a.effectivePos, a.effectivePos+n, key, vu.old) != -1 {
			return fmt.Errorf("op component %d: annotation old value for %q differs from document", ci, key)
		}
	}
	return nil
}

// --- characters / element insertion ---

func (a *docOpAutomaton) checkCharacters(ci int, text string) error {
	// well-formedness
	if text == "" {
		return fmt.Errorf("op component %d: empty characters insertion", ci)
	}
	if bad := firstBadTextRune(text); bad >= 0 {
		return fmt.Errorf("op component %d: characters contain invalid text at byte %d", ci, bad)
	}
	if !a.deletionStackEmpty() {
		return fmt.Errorf("op component %d: insertion inside a deletion", ci)
	}
	// validity
	return a.checkAnnotationsForInsertion(ci)
}

func (a *docOpAutomaton) checkElementStart(ci int, typ string, attrs Attributes) error {
	// well-formedness. The element TYPE is checked here; the attributes are
	// XML-name-keyed and valid-UTF-16-valued by construction (NewAttributes enforces
	// checkAttributesWellFormed's invariant).
	if !isXMLName(typ) {
		return fmt.Errorf("op component %d: element type %q is not a valid XML name", ci, typ)
	}
	if !a.deletionStackEmpty() {
		return fmt.Errorf("op component %d: insertion inside a deletion", ci)
	}
	// validity
	return a.checkAnnotationsForInsertion(ci)
}

func (a *docOpAutomaton) checkElementEnd(ci int) error {
	// well-formedness
	if !a.deletionStackEmpty() {
		return fmt.Errorf("op component %d: insertion inside a deletion", ci)
	}
	if a.insertionStackEmpty() {
		return fmt.Errorf("op component %d: element end with no matching inserted start", ci)
	}
	// validity
	return a.checkAnnotationsForInsertion(ci)
}

// checkAnnotationsForInsertion verifies that each open annotation key's expected
// old value equals the annotation inherited from effectivePos-1 (the position to
// the left), so the inserted content's annotations are correct
// (DocOpAutomaton.checkAnnotationsForInsertion). Skipped when effectivePos is past
// the document end (an already-invalid op).
func (a *docOpAutomaton) checkAnnotationsForInsertion(ci int) error {
	if a.effectivePos > a.docLength() {
		return nil
	}
	posToInheritFrom := a.effectivePos - 1
	for key, vu := range a.annUpdate {
		var inherited *string
		if posToInheritFrom >= 0 {
			if v, ok := a.getAnnotation(posToInheritFrom, key); ok {
				inherited = &v
			}
		}
		if !annEqual(vu.old, inherited) {
			return fmt.Errorf("op component %d: annotation old value for %q differs from document (inserting)", ci, key)
		}
	}
	return nil
}

// --- deletion ---

func (a *docOpAutomaton) checkDeleteCharacters(ci int, text string) error {
	// well-formedness
	if text == "" {
		return fmt.Errorf("op component %d: empty characters deletion", ci)
	}
	if bad := firstBadTextRune(text); bad >= 0 {
		return fmt.Errorf("op component %d: deleteCharacters contain invalid text at byte %d", ci, bad)
	}
	if !a.insertionStackEmpty() {
		return fmt.Errorf("op component %d: deletion inside an insertion", ci)
	}
	// validity
	offset := 0
	for _, r := range text {
		pos := a.effectivePos + offset
		if pos >= a.docLength() {
			return fmt.Errorf("op component %d: deleteCharacters runs past end of document", ci)
		}
		it := a.items[pos]
		if it.kind != itemChar {
			return fmt.Errorf("op component %d: deleteCharacters at a non-character (element) position", ci)
		}
		if it.r != r {
			return fmt.Errorf("op component %d: deleteCharacters content mismatch: document has %q, op deletes %q", ci, string(it.r), string(r))
		}
		offset++
	}
	return a.checkAnnotationsForDeletion(ci, offset)
}

func (a *docOpAutomaton) checkDeleteElementStart(ci int, typ string, attrs Attributes) error {
	// well-formedness. The element TYPE is checked here; the attributes are
	// XML-name-keyed and valid-UTF-16-valued by construction (NewAttributes enforces
	// checkAttributesWellFormed's invariant).
	if !isXMLName(typ) {
		return fmt.Errorf("op component %d: element type %q is not a valid XML name", ci, typ)
	}
	if !a.insertionStackEmpty() {
		return fmt.Errorf("op component %d: deletion inside an insertion", ci)
	}
	// validity
	it, ok := a.at()
	if !ok || it.kind != itemStart {
		return fmt.Errorf("op component %d: deleteElementStart but document has no element start here", ci)
	}
	if it.typ != typ {
		return fmt.Errorf("op component %d: deleteElementStart type mismatch: document %q, op %q", ci, it.typ, typ)
	}
	if !it.attrs.Equal(attrs) {
		return fmt.Errorf("op component %d: deleteElementStart attributes differ from document", ci)
	}
	return a.checkAnnotationsForDeletion(ci, 1)
}

func (a *docOpAutomaton) checkDeleteElementEnd(ci int) error {
	// well-formedness
	if !a.insertionStackEmpty() {
		return fmt.Errorf("op component %d: deletion inside an insertion", ci)
	}
	if a.deletionStackEmpty() {
		return fmt.Errorf("op component %d: deleteElementEnd with no matching open deletion", ci)
	}
	// validity
	it, ok := a.at()
	if !ok || it.kind != itemEnd {
		return fmt.Errorf("op component %d: deleteElementEnd but document has no element end here", ci)
	}
	return a.checkAnnotationsForDeletion(ci, 1)
}

// checkAnnotationsForDeletion verifies deletion-annotation consistency over the
// next n items (DocOpAutomaton.checkAnnotationsForDeletion): (a) each open key's
// expected old value holds across the range AND its new value equals the deletion
// target; (b) every annotation present on a deleted item — or present in the target
// — whose document value differs from the target must be covered by the update,
// else deleting it would orphan an annotation value. Skipped once the deletion
// target has been invalidated by over-running the document.
func (a *docOpAutomaton) checkAnnotationsForDeletion(ci, n int) error {
	if !a.delTargetValid {
		return nil
	}
	for key, vu := range a.annUpdate {
		if a.firstAnnotationChange(a.effectivePos, a.effectivePos+n, key, vu.old) != -1 {
			return fmt.Errorf("op component %d: annotation old value for %q differs from document (deleting)", ci, key)
		}
		target, hasTarget := a.delTarget[key]
		if !annEqual(vu.new, ptr(target, hasTarget)) {
			return fmt.Errorf("op component %d: annotation new value for %q is incorrect for deletion", ci, key)
		}
	}
	for offset := 0; offset < n; offset++ {
		pos := a.effectivePos + offset
		here := map[string]string(nil)
		if pos >= 0 && pos < len(a.items) {
			here = a.items[pos].ann
		}
		// The set of keys to check is the union of keys present at the position and
		// keys present in the target.
		for key, valueInDoc := range here {
			required, hasReq := a.delTarget[key]
			if !annEqual(ptr(valueInDoc, true), ptr(required, hasReq)) {
				if _, open := a.annUpdate[key]; !open {
					return fmt.Errorf("op component %d: deleting item leaves annotation %q (=%q) inconsistent with target", ci, key, valueInDoc)
				}
			}
		}
		for key, required := range a.delTarget {
			valueInDoc, hasDoc := "", false
			if here != nil {
				valueInDoc, hasDoc = here[key]
			}
			if !annEqual(ptr(valueInDoc, hasDoc), ptr(required, true)) {
				if _, open := a.annUpdate[key]; !open {
					return fmt.Errorf("op component %d: deleting item leaves annotation %q inconsistent with target %q", ci, key, required)
				}
			}
		}
	}
	return nil
}

// --- attribute mutation ---

func (a *docOpAutomaton) checkReplaceAttributes(ci int, oldAttrs, newAttrs Attributes) error {
	// well-formedness: oldAttrs/newAttrs are XML-name-keyed, valid-UTF-16-valued,
	// and sorted by construction (NewAttributes enforces checkAttributesWellFormed's
	// invariant), so only the cross-component scope state remains to check.
	if !a.deletionStackEmpty() || !a.insertionStackEmpty() {
		return fmt.Errorf("op component %d: attribute change inside an insertion or deletion", ci)
	}
	// validity
	it, ok := a.at()
	if !ok || it.kind != itemStart {
		return fmt.Errorf("op component %d: replaceAttributes but document has no element start here", ci)
	}
	if !it.attrs.Equal(oldAttrs) {
		return fmt.Errorf("op component %d: replaceAttributes old attributes differ from document", ci)
	}
	return a.checkAnnotationsForRetain(ci, 1)
}

func (a *docOpAutomaton) checkUpdateAttributes(ci int, u AttributesUpdate) error {
	// well-formedness: the update is XML-name-keyed, valid-UTF-16-valued, and sorted
	// by construction (NewAttributesUpdate enforces checkAttributesUpdateWellFormed's
	// invariant), so only the cross-component scope state remains to check.
	if !a.deletionStackEmpty() || !a.insertionStackEmpty() {
		return fmt.Errorf("op component %d: attribute change inside an insertion or deletion", ci)
	}
	// validity
	it, ok := a.at()
	if !ok || it.kind != itemStart {
		return fmt.Errorf("op component %d: updateAttributes but document has no element start here", ci)
	}
	if err := checkUpdateOldValues(it.attrs, u); err != nil {
		return fmt.Errorf("op component %d: %w", ci, err)
	}
	return a.checkAnnotationsForRetain(ci, 1)
}

// --- annotation boundary ---

func (a *docOpAutomaton) checkAnnotationBoundary(ci int, m AnnotationBoundaryMap) error {
	// well-formedness. Per-boundary internal well-formedness (key sorting,
	// uniqueness, disjoint end/change sets, '?'/'@'-free keys, valid values) is
	// enforced at NewAnnotationBoundaryMap construction; what remains is the
	// cross-component automaton state.
	if a.afterAnnBoundary {
		return fmt.Errorf("op component %d: adjacent annotation boundaries", ci)
	}
	for _, key := range m.EndKeys() {
		if _, open := a.annUpdate[key]; !open {
			return fmt.Errorf("op component %d: ends an annotation %q that is not open", ci, key)
		}
	}
	return nil
}

// foldAnnotation folds a boundary into annUpdate: end keys remove the open change,
// change keys set/replace it (ports AnnotationsUpdateImpl.composeWith for the
// no-schema case). End and change key sets are disjoint by construction.
func (a *docOpAutomaton) foldAnnotation(m AnnotationBoundaryMap) {
	for _, key := range m.EndKeys() {
		delete(a.annUpdate, key)
	}
	for _, ch := range m.Changes() {
		a.annUpdate[ch.Key] = valueUpdate{old: ch.OldValue, new: ch.NewValue}
	}
}

// --- finish ---

func (a *docOpAutomaton) checkFinish() error {
	// well-formedness
	if !a.insertionStackEmpty() {
		return fmt.Errorf("op leaves %d inserted element(s) unclosed", len(a.insertionStack))
	}
	if !a.deletionStackEmpty() {
		return fmt.Errorf("op leaves %d deletion(s) unclosed", a.deletionStackDepth)
	}
	if len(a.annUpdate) > 0 {
		return fmt.Errorf("op leaves an unclosed annotation")
	}
	// validity
	if a.effectivePos != a.docLength() {
		return fmt.Errorf("op does not cover the whole document: consumed %d of %d item(s)", a.effectivePos, a.docLength())
	}
	return nil
}

// --- helpers ---

// ptr returns &v when present, else nil — a *string view of a (value, present)
// pair, the representation the annotation comparisons use for "absent".
func ptr(v string, present bool) *string {
	if !present {
		return nil
	}
	return &v
}

// annEqual reports whether two annotation values are equal, treating nil (absent)
// as distinct from any present value (Java's equal(Object,Object) over annotation
// values).
func annEqual(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// firstBadTextRune returns the byte index of the first rune in text that is not
// valid document text, or -1 if all runes are valid. This ports the Java
// firstSurrogate + isValidUtf16 well-formedness gate: reject surrogate-range runes
// / the UTF-8 error rune, and reject noncharacters — code points U+FDD0..U+FDEF and
// any whose low 16 bits are 0xFFFE/0xFFFF. Go strings are UTF-8 so surrogates cannot
// even be encoded; an invalid byte sequence decodes to utf8.RuneError, which is
// rejected here.
//
// DELIBERATE DIVERGENCE from Java's UTF-16 model: Java is BMP-only — firstSurrogate
// rejects any string containing a surrogate code unit, so a supplementary code
// point (> U+FFFF, e.g. an emoji) is rejected because it is a surrogate PAIR in
// UTF-16. The Go port models text as UTF-8 runes where a supplementary code point is
// a single valid item (one rune); the whole op package — inputItems/outputItems,
// Compose, Transform, the fuzz generators — counts and composes such characters as
// one item. We therefore ACCEPT valid supplementary runes (rejecting only surrogate
// and noncharacter code points). The rune/UTF-16-code-unit item counts then coincide
// for BMP text and intentionally differ (Go: 1, Java: 2 + rejected) for supplementary
// text; the Go model's choice is self-consistent across the package.
func firstBadTextRune(text string) int {
	for i, r := range text {
		if r == utf8.RuneError {
			// Either a genuine U+FFFD or an invalid byte sequence; distinguish by
			// re-decoding the one rune at this index.
			_, size := utf8.DecodeRuneInString(text[i:])
			if size <= 1 {
				return i // invalid encoding
			}
			// A genuine U+FFFD: low 16 bits 0xFFFD, not a noncharacter; allowed.
			continue
		}
		if r >= 0xD800 && r <= 0xDFFF {
			return i // surrogate code point
		}
		if d := r & 0xFFFF; d == 0xFFFE || d == 0xFFFF {
			return i // noncharacter
		}
		if r >= 0xFDD0 && r <= 0xFDEF {
			return i // noncharacter
		}
	}
	return -1
}

// isXMLName reports whether s is a valid XML Name over valid Unicode, a verbatim
// port of Utf16Util.isXmlName / isXmlNameStartChar / isXmlNameChar
// (Utf16Util.java:294-371). The empty string is not a name.
func isXMLName(s string) bool {
	if s == "" {
		return false
	}
	first := true
	for i := 0; i < len(s); {
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size <= 1 {
			// Invalid encoding (Go's stand-in for an unpaired surrogate); Java's
			// unpairedSurrogate handler returns false.
			return false
		}
		i += size
		if first {
			if !isXMLNameStartChar(c) {
				return false
			}
			first = false
		} else {
			if !isXMLNameChar(c) {
				return false
			}
		}
	}
	return true
}

// isValidUTF16Doc ports Utf16Util.isValidUtf16 to the op package's UTF-8 rune
// model: a string is valid document text iff it is valid UTF-8 (Go's stand-in
// for "no unpaired surrogates") and every rune is a valid code point — i.e. not
// a surrogate and not a noncharacter (U+FDD0..U+FDEF or any whose low 16 bits
// are 0xFFFE/0xFFFF). This is strictly stronger than utf8.ValidString, which
// accepts noncharacters; Java's isValidUtf16 rejects them as ILL_FORMED. It is
// the predicate the automaton's checkAttributesWellFormed/checkAnnotation* use
// for attribute and annotation values; attribute/annotation construction
// (NewAttributes/NewAttributesUpdate/NewAnnotationBoundaryMap) enforces it so
// those value types carry the same well-formedness invariant the Java automaton
// checks. It matches firstBadTextRune for the text path modulo the package's
// already-documented supplementary-rune choice (firstBadTextRune is for inserted
// TEXT, where Java additionally rejects supplementary runes as surrogate pairs;
// attribute/annotation values are not subject to that UTF-16 surrogate-pair gate
// in Java's isValidUtf16, only to the noncharacter/surrogate code-point gate).
func isValidUTF16Doc(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !isCodePointValid(r) {
			return false
		}
	}
	return true
}

// isCodePointValid ports Utf16Util.isCodePointValid: false for surrogates and
// noncharacters. (Surrogates cannot be encoded in a Go string, but the range check
// is kept for fidelity.)
func isCodePointValid(c rune) bool {
	if c >= 0xD800 && c <= 0xDFFF {
		return false
	}
	if d := c & 0xFFFF; d == 0xFFFE || d == 0xFFFF {
		return false
	}
	if c >= 0xFDD0 && c <= 0xFDEF {
		return false
	}
	return true
}

// isXMLNameStartChar ports Utf16Util.isXmlNameStartChar (Utf16Util.java:294-312).
func isXMLNameStartChar(c rune) bool {
	return c == ':' || ('A' <= c && c <= 'Z') || c == '_' || ('a' <= c && c <= 'z') ||
		(0xC0 <= c && c <= 0xD6) || (0xD8 <= c && c <= 0xF6) || (0xF8 <= c && c <= 0x2FF) ||
		(0x370 <= c && c <= 0x37D) || (0x37F <= c && c <= 0x1FFF) ||
		(0x200C <= c && c <= 0x200D) || (0x2070 <= c && c <= 0x218F) ||
		(0x2C00 <= c && c <= 0x2FEF) || (0x3001 <= c && c <= 0xD7FF) ||
		(0xF900 <= c && c <= 0xFDCF) || (0xFDF0 <= c && c <= 0xFFFD) ||
		((0x10000 <= c && c <= 0xEFFFF) && isCodePointValid(c))
}

// isXMLNameChar ports Utf16Util.isXmlNameChar (Utf16Util.java:320-330).
func isXMLNameChar(c rune) bool {
	if !isCodePointValid(c) {
		return false
	}
	return isXMLNameStartChar(c) || c == '-' || c == '.' || ('0' <= c && c <= '9') ||
		c == 0xB7 || (0x0300 <= c && c <= 0x036F) || (0x203F <= c && c <= 0x2040)
}
