package op_test

// Port of Wave's independent *reference* document-operation transformer, used as
// a ground-truth oracle to validate our optimized op.Transform.
//
// Java sources (wave/src/test/java/org/waveprotocol/wave/model/operation/testing/reference/):
//   ReferenceTransformer.java            (orchestrator: decompose -> 6 pairwise transforms -> recompose)
//   Decomposer.java                      (split into insertion / preservation / deletion)
//   PositionTracker / RelativePosition   (two-cursor relative positioning)
//   ValueUpdate.java                     (old/new value pair)
//   InsertionInsertionTransformer.java
//   InsertionPreservationTransformer.java
//   InsertionDeletionTransformer.java
//   PreservationPreservationTransformer.java
//   PreservationDeletionTransformer.java
//   DeletionDeletionTransformer.java
//   AnnotationTamenessChecker.java
// and the harness DocOpTransformerReferenceLargeTest.java.
//
// ADAPTATION: Java drives transformation through the EvaluatingDocOpCursor
// visitor plus RangeNormalizer/OperationNormalizer + DocOpBuffer. We have no
// such cursor in package op (it is internal), so this port accumulates raw
// components into a refBuf and Normalize()s at finish — equivalent for the
// purpose of convergence/equality comparison, which is all the oracle is for.
// The reference is intentionally inefficient (per the Java doc) and the Java
// authors themselves note it drifts from the optimized transform past ~100
// iterations; we therefore compare with a MODEST fixed iteration count.

import (
	"fmt"
	"unicode/utf8"

	"github.com/sgrankin/wave/internal/op"
)

// ---------------------------------------------------------------------------
// refBuf: a normalizing component sink (stand-in for RangeNormalizer/
// OperationNormalizer + DocOpBuffer). Records the cursor calls the Java targets
// make, then Normalize()s on finish.
// ---------------------------------------------------------------------------

type refBuf struct {
	comps []op.Component
}

func (b *refBuf) retain(n int) {
	if n <= 0 {
		return
	}
	b.comps = append(b.comps, op.Retain{Count: n})
}
func (b *refBuf) characters(s string) {
	if s == "" {
		return
	}
	b.comps = append(b.comps, op.Characters{Text: s})
}
func (b *refBuf) elementStart(typ string, a op.Attributes) {
	b.comps = append(b.comps, op.ElementStart{Type: typ, Attributes: a})
}
func (b *refBuf) elementEnd() {
	b.comps = append(b.comps, op.ElementEnd{})
}
func (b *refBuf) deleteCharacters(s string) {
	if s == "" {
		return
	}
	b.comps = append(b.comps, op.DeleteCharacters{Text: s})
}
func (b *refBuf) deleteElementStart(typ string, a op.Attributes) {
	b.comps = append(b.comps, op.DeleteElementStart{Type: typ, Attributes: a})
}
func (b *refBuf) deleteElementEnd() {
	b.comps = append(b.comps, op.DeleteElementEnd{})
}
func (b *refBuf) replaceAttributes(oldA, newA op.Attributes) {
	b.comps = append(b.comps, op.ReplaceAttributes{OldAttributes: oldA, NewAttributes: newA})
}
func (b *refBuf) updateAttributes(u op.AttributesUpdate) {
	b.comps = append(b.comps, op.UpdateAttributes{Update: u})
}
func (b *refBuf) annotationBoundary(m op.AnnotationBoundaryMap) {
	if m.Empty() {
		return
	}
	b.comps = append(b.comps, op.AnnotationBoundary{Boundary: m})
}
func (b *refBuf) finish() op.DocOp {
	return op.NewDocOp(b.comps).Normalize()
}

// refRuneLen counts runes — Java's String.length() counts UTF-16 units, but the
// test alphabet (and our generators) use only BMP characters except 😀, which is
// a surrogate pair in Java. To stay faithful to OUR op model (which counts
// runes), we count runes throughout; the document-length accounting in both
// transforms must agree, and our op package counts runes everywhere.
func refRuneLen(s string) int { return utf8.RuneCountInString(s) }

// runeSub returns s[start:end) measured in runes (Java substring is by char
// index; our positions are rune indices).
func runeSub(s string, start, end int) string {
	r := []rune(s)
	return string(r[start:end])
}
func runeFrom(s string, start int) string {
	r := []rune(s)
	return string(r[start:])
}

// refTransformError is the analogue of Java's TransformException: the reference
// reports that two concurrent ops are not compatible.
type refTransformError struct{ msg string }

func (e *refTransformError) Error() string { return "reference transform: " + e.msg }

// ---------------------------------------------------------------------------
// Decomposer: split an op into (insertion, preservation, deletion). Ports
// Decomposer.java.
// ---------------------------------------------------------------------------

type refDecomposition struct {
	insertion    op.DocOp
	preservation op.DocOp
	deletion     op.DocOp
}

func refDecompose(o op.DocOp) refDecomposition {
	var ins, pre, del refBuf
	for _, c := range o.Components() {
		switch v := c.(type) {
		case op.Retain:
			ins.retain(v.Count)
			pre.retain(v.Count)
			del.retain(v.Count)
		case op.Characters:
			ins.characters(v.Text)
			pre.retain(refRuneLen(v.Text))
			del.retain(refRuneLen(v.Text))
		case op.ElementStart:
			ins.elementStart(v.Type, v.Attributes)
			pre.retain(1)
			del.retain(1)
		case op.ElementEnd:
			ins.elementEnd()
			pre.retain(1)
			del.retain(1)
		case op.DeleteCharacters:
			ins.retain(refRuneLen(v.Text))
			pre.retain(refRuneLen(v.Text))
			del.deleteCharacters(v.Text)
		case op.DeleteElementStart:
			ins.retain(1)
			pre.retain(1)
			del.deleteElementStart(v.Type, v.Attributes)
		case op.DeleteElementEnd:
			ins.retain(1)
			pre.retain(1)
			del.deleteElementEnd()
		case op.ReplaceAttributes:
			ins.retain(1)
			pre.replaceAttributes(v.OldAttributes, v.NewAttributes)
			del.retain(1)
		case op.UpdateAttributes:
			ins.retain(1)
			pre.updateAttributes(v.Update)
			del.retain(1)
		case op.AnnotationBoundary:
			pre.annotationBoundary(v.Boundary)
		}
	}
	return refDecomposition{insertion: ins.finish(), preservation: pre.finish(), deletion: del.finish()}
}

// ---------------------------------------------------------------------------
// PositionTracker / RelativePosition. Ports PositionTracker.java.
// ---------------------------------------------------------------------------

type refPosTracker struct{ position int }

// refRelPos: sign +1 is side 1 (adds), sign -1 is side 2 (subtracts).
type refRelPos struct {
	t    *refPosTracker
	sign int
}

func (r refRelPos) increase(amount int) { r.t.position += r.sign * amount }
func (r refRelPos) get() int            { return r.sign * r.t.position }

// ---------------------------------------------------------------------------
// ValueUpdate. Ports ValueUpdate.java. Values are *string (nil == Java null).
// ---------------------------------------------------------------------------

type refValueUpdate struct {
	oldValue *string
	newValue *string
}

// ---------------------------------------------------------------------------
// InsertionInsertionTransformer. Ports InsertionInsertionTransformer.java.
// ---------------------------------------------------------------------------

type refIITarget struct {
	out   refBuf
	rel   refRelPos
	other *refIITarget
}

func (t *refIITarget) retain(itemCount int) {
	oldPos := t.rel.get()
	t.rel.increase(itemCount)
	switch {
	case t.rel.get() < 0:
		t.out.retain(itemCount)
		t.other.out.retain(itemCount)
	case oldPos < 0:
		t.out.retain(-oldPos)
		t.other.out.retain(-oldPos)
	}
}
func (t *refIITarget) characters(chars string) {
	t.out.characters(chars)
	t.other.out.retain(refRuneLen(chars))
}
func (t *refIITarget) elementStart(typ string, a op.Attributes) {
	t.out.elementStart(typ, a)
	t.other.out.retain(1)
}
func (t *refIITarget) elementEnd() {
	t.out.elementEnd()
	t.other.out.retain(1)
}
func (t *refIITarget) apply(c op.Component) error {
	switch v := c.(type) {
	case op.Retain:
		t.retain(v.Count)
	case op.Characters:
		t.characters(v.Text)
	case op.ElementStart:
		t.elementStart(v.Type, v.Attributes)
	case op.ElementEnd:
		t.elementEnd()
	default:
		return &refTransformError{fmt.Sprintf("InsertionInsertion: unexpected component %T", c)}
	}
	return nil
}

func refTransformInsertionInsertion(clientOp, serverOp op.DocOp) (op.DocOp, op.DocOp, error) {
	pt := &refPosTracker{}
	client := &refIITarget{rel: refRelPos{t: pt, sign: 1}}
	server := &refIITarget{rel: refRelPos{t: pt, sign: -1}}
	client.other = server
	server.other = client

	cc, sc := clientOp.Components(), serverOp.Components()
	ci, si := 0, 0
	for ci < len(cc) {
		if err := client.apply(cc[ci]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		ci++
		for client.rel.get() > 0 {
			if si >= len(sc) {
				return op.DocOp{}, op.DocOp{}, &refTransformError{"InsertionInsertion: ran out of server components"}
			}
			if err := server.apply(sc[si]); err != nil {
				return op.DocOp{}, op.DocOp{}, err
			}
			si++
		}
	}
	for si < len(sc) {
		if err := server.apply(sc[si]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		si++
	}
	return client.out.finish(), server.out.finish(), nil
}

// ---------------------------------------------------------------------------
// InsertionPreservationTransformer. Ports InsertionPreservationTransformer.java.
// The insertion target has range positioning; the preservation target caches a
// range effect (retain / replaceAttributes / updateAttributes) and resolves it
// against the insertion's retains.
// ---------------------------------------------------------------------------

// refRangeCacheIP: a cached range effect for the preservation side.
type refRangeCacheIP func(itemCount int)

type refIPInsertion struct {
	out   refBuf
	rel   refRelPos
	other *refIPNoninsertion
}
type refIPNoninsertion struct {
	out       refBuf
	rel       refRelPos
	other     *refIPInsertion
	rangeData refRangeCacheIP
}

func (t *refIPInsertion) retain(itemCount int) {
	oldPos := t.rel.get()
	t.rel.increase(itemCount)
	if t.rel.get() < 0 {
		t.other.rangeData(itemCount)
	} else if oldPos < 0 {
		t.other.rangeData(-oldPos)
	}
}
func (t *refIPInsertion) characters(chars string) {
	t.out.characters(chars)
	t.other.out.retain(refRuneLen(chars))
}
func (t *refIPInsertion) elementStart(typ string, a op.Attributes) {
	t.out.elementStart(typ, a)
	t.other.out.retain(1)
}
func (t *refIPInsertion) elementEnd() {
	t.out.elementEnd()
	t.other.out.retain(1)
}
func (t *refIPInsertion) apply(c op.Component) error {
	switch v := c.(type) {
	case op.Retain:
		t.retain(v.Count)
	case op.Characters:
		t.characters(v.Text)
	case op.ElementStart:
		t.elementStart(v.Type, v.Attributes)
	case op.ElementEnd:
		t.elementEnd()
	default:
		return &refTransformError{fmt.Sprintf("InsertionPreservation: unexpected insertion component %T", c)}
	}
	return nil
}

func (t *refIPNoninsertion) retainCache() refRangeCacheIP {
	return func(itemCount int) {
		t.out.retain(itemCount)
		t.other.out.retain(itemCount)
	}
}
func (t *refIPNoninsertion) replaceAttrsCache(oldA, newA op.Attributes) refRangeCacheIP {
	return func(itemCount int) {
		t.out.replaceAttributes(oldA, newA)
		t.other.out.retain(1)
	}
}
func (t *refIPNoninsertion) updateAttrsCache(u op.AttributesUpdate) refRangeCacheIP {
	return func(itemCount int) {
		t.out.updateAttributes(u)
		t.other.out.retain(1)
	}
}

// resolveRange ports the preservation Target.resolveRange.
func (t *refIPNoninsertion) resolveRange(size int, cache refRangeCacheIP) int {
	oldPos := t.rel.get()
	t.rel.increase(size)
	if t.rel.get() > 0 {
		if oldPos < 0 {
			cache(-oldPos)
		}
		return -oldPos
	}
	cache(size)
	return -1
}

func (t *refIPNoninsertion) retain(itemCount int) {
	t.resolveRange(itemCount, t.retainCache())
	t.rangeData = t.retainCache()
}
func (t *refIPNoninsertion) replaceAttributes(oldA, newA op.Attributes) {
	cache := t.replaceAttrsCache(oldA, newA)
	if t.resolveRange(1, cache) == 0 {
		t.rangeData = cache
	}
}
func (t *refIPNoninsertion) updateAttributes(u op.AttributesUpdate) {
	cache := t.updateAttrsCache(u)
	if t.resolveRange(1, cache) == 0 {
		// Java reconstructs a fresh UpdateAttributesCache here (identical effect).
		t.rangeData = t.updateAttrsCache(u)
	}
}
func (t *refIPNoninsertion) annotationBoundary(m op.AnnotationBoundaryMap) {
	t.out.annotationBoundary(m)
}
func (t *refIPNoninsertion) apply(c op.Component) error {
	switch v := c.(type) {
	case op.Retain:
		t.retain(v.Count)
	case op.ReplaceAttributes:
		t.replaceAttributes(v.OldAttributes, v.NewAttributes)
	case op.UpdateAttributes:
		t.updateAttributes(v.Update)
	case op.AnnotationBoundary:
		t.annotationBoundary(v.Boundary)
	default:
		return &refTransformError{fmt.Sprintf("InsertionPreservation: unexpected preservation component %T", c)}
	}
	return nil
}

func refTransformInsertionPreservation(insertionOp, preservationOp op.DocOp) (op.DocOp, op.DocOp, error) {
	pt := &refPosTracker{}
	ins := &refIPInsertion{rel: refRelPos{t: pt, sign: 1}}
	pre := &refIPNoninsertion{rel: refRelPos{t: pt, sign: -1}}
	ins.other = pre
	pre.other = ins
	pre.rangeData = pre.retainCache()

	ic, pc := insertionOp.Components(), preservationOp.Components()
	ii, pi := 0, 0
	for ii < len(ic) {
		if err := ins.apply(ic[ii]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		ii++
		for ins.rel.get() > 0 {
			if pi >= len(pc) {
				return op.DocOp{}, op.DocOp{}, &refTransformError{"InsertionPreservation: ran out of preservation components"}
			}
			if err := pre.apply(pc[pi]); err != nil {
				return op.DocOp{}, op.DocOp{}, err
			}
			pi++
		}
	}
	for pi < len(pc) {
		if err := pre.apply(pc[pi]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		pi++
	}
	return ins.out.finish(), pre.out.finish(), nil
}

// ---------------------------------------------------------------------------
// InsertionDeletionTransformer. Ports InsertionDeletionTransformer.java.
// When the deletion side has open element-deletions (depth>0), the insertion's
// inserted content is itself converted to deletions on the deletion side's
// output.
// ---------------------------------------------------------------------------

type refIDInsertion struct {
	out   refBuf
	rel   refRelPos
	other *refIDNoninsertion
}
type refIDNoninsertion struct {
	out       refBuf
	rel       refRelPos
	other     *refIDInsertion
	rangeData refRangeCacheIP // resolve(itemCount)
	depth     int
}

func (t *refIDInsertion) retain(itemCount int) {
	oldPos := t.rel.get()
	t.rel.increase(itemCount)
	if t.rel.get() < 0 {
		t.other.rangeData(itemCount)
	} else if oldPos < 0 {
		t.other.rangeData(-oldPos)
	}
}
func (t *refIDInsertion) characters(chars string) {
	if t.other.depth > 0 {
		t.other.out.deleteCharacters(chars)
	} else {
		t.out.characters(chars)
		t.other.out.retain(refRuneLen(chars))
	}
}
func (t *refIDInsertion) elementStart(typ string, a op.Attributes) {
	if t.other.depth > 0 {
		t.other.out.deleteElementStart(typ, a)
	} else {
		t.out.elementStart(typ, a)
		t.other.out.retain(1)
	}
}
func (t *refIDInsertion) elementEnd() {
	if t.other.depth > 0 {
		t.other.out.deleteElementEnd()
	} else {
		t.out.elementEnd()
		t.other.out.retain(1)
	}
}
func (t *refIDInsertion) apply(c op.Component) error {
	switch v := c.(type) {
	case op.Retain:
		t.retain(v.Count)
	case op.Characters:
		t.characters(v.Text)
	case op.ElementStart:
		t.elementStart(v.Type, v.Attributes)
	case op.ElementEnd:
		t.elementEnd()
	default:
		return &refTransformError{fmt.Sprintf("InsertionDeletion: unexpected insertion component %T", c)}
	}
	return nil
}

func (t *refIDNoninsertion) retainCache() refRangeCacheIP {
	return func(itemCount int) {
		t.out.retain(itemCount)
		t.other.out.retain(itemCount)
	}
}

// deleteCharactersCache is stateful (it consumes the string as it resolves).
func (t *refIDNoninsertion) makeDeleteCharactersCache(chars string) refRangeCacheIP {
	remaining := chars
	return func(itemCount int) {
		t.out.deleteCharacters(runeSub(remaining, 0, itemCount))
		remaining = runeFrom(remaining, itemCount)
	}
}
func (t *refIDNoninsertion) makeDeleteElementStartCache(typ string, a op.Attributes) refRangeCacheIP {
	return func(itemCount int) {
		t.out.deleteElementStart(typ, a)
		t.depth++
	}
}
func (t *refIDNoninsertion) deleteElementEndCache() refRangeCacheIP {
	return func(itemCount int) {
		t.out.deleteElementEnd()
		t.depth--
	}
}

func (t *refIDNoninsertion) resolveRange(size int, cache refRangeCacheIP) int {
	oldPos := t.rel.get()
	t.rel.increase(size)
	if t.rel.get() > 0 {
		if oldPos < 0 {
			cache(-oldPos)
		}
		return -oldPos
	}
	cache(size)
	return -1
}

func (t *refIDNoninsertion) retain(itemCount int) {
	t.resolveRange(itemCount, t.retainCache())
	t.rangeData = t.retainCache()
}
func (t *refIDNoninsertion) deleteCharacters(chars string) {
	cache := t.makeDeleteCharactersCache(chars)
	if t.resolveRange(refRuneLen(chars), cache) >= 0 {
		t.rangeData = cache
	}
}
func (t *refIDNoninsertion) deleteElementStart(typ string, a op.Attributes) {
	cache := t.makeDeleteElementStartCache(typ, a)
	if t.resolveRange(1, cache) == 0 {
		t.rangeData = cache
	}
}
func (t *refIDNoninsertion) deleteElementEnd() {
	cache := t.deleteElementEndCache()
	if t.resolveRange(1, cache) == 0 {
		t.rangeData = cache
	}
}
func (t *refIDNoninsertion) apply(c op.Component) error {
	switch v := c.(type) {
	case op.Retain:
		t.retain(v.Count)
	case op.DeleteCharacters:
		t.deleteCharacters(v.Text)
	case op.DeleteElementStart:
		t.deleteElementStart(v.Type, v.Attributes)
	case op.DeleteElementEnd:
		t.deleteElementEnd()
	default:
		return &refTransformError{fmt.Sprintf("InsertionDeletion: unexpected deletion component %T", c)}
	}
	return nil
}

func refTransformInsertionDeletion(insertionOp, deletionOp op.DocOp) (op.DocOp, op.DocOp, error) {
	pt := &refPosTracker{}
	ins := &refIDInsertion{rel: refRelPos{t: pt, sign: 1}}
	del := &refIDNoninsertion{rel: refRelPos{t: pt, sign: -1}}
	ins.other = del
	del.other = ins
	del.rangeData = del.retainCache()

	ic, dc := insertionOp.Components(), deletionOp.Components()
	ii, di := 0, 0
	for ii < len(ic) {
		if err := ins.apply(ic[ii]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		ii++
		for ins.rel.get() > 0 {
			if di >= len(dc) {
				return op.DocOp{}, op.DocOp{}, &refTransformError{"InsertionDeletion: ran out of deletion components"}
			}
			if err := del.apply(dc[di]); err != nil {
				return op.DocOp{}, op.DocOp{}, err
			}
			di++
		}
	}
	for di < len(dc) {
		if err := del.apply(dc[di]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		di++
	}
	return ins.out.finish(), del.out.finish(), nil
}

// ---------------------------------------------------------------------------
// DeletionDeletionTransformer. Ports DeletionDeletionTransformer.java.
// Each side caches a delete effect; when both sides delete the same range the
// cache resolves it to a no-op (the deletion is shared). retainCache turns the
// other side's deletes into deletes on this side's output.
// ---------------------------------------------------------------------------

// refDDCache: resolve dispatch for the deletion/deletion transform. Methods
// default to "incompatible" when not overridden, mirroring the abstract
// RangeCache that throws InternalTransformException.
type refDDCache struct {
	resolveRetain             func(itemCount int)
	resolveDeleteCharacters   func(chars string)
	resolveDeleteElementStart func(typ string, a op.Attributes)
	resolveDeleteElementEnd   func()
}

type refDDTarget struct {
	out   refBuf
	rel   refRelPos
	other *refDDTarget
	cache *refDDCache
}

func (t *refDDTarget) retainCache() *refDDCache {
	return &refDDCache{
		resolveRetain: func(itemCount int) {
			t.out.retain(itemCount)
			t.other.out.retain(itemCount)
		},
		resolveDeleteCharacters: func(chars string) {
			t.other.out.deleteCharacters(chars)
		},
		resolveDeleteElementStart: func(typ string, a op.Attributes) {
			t.other.out.deleteElementStart(typ, a)
		},
		resolveDeleteElementEnd: func() {
			t.other.out.deleteElementEnd()
		},
	}
}
func (t *refDDTarget) makeDeleteCharactersCache(chars string) *refDDCache {
	remaining := chars
	return &refDDCache{
		resolveRetain: func(itemCount int) {
			t.out.deleteCharacters(runeSub(remaining, 0, itemCount))
			remaining = runeFrom(remaining, itemCount)
		},
		resolveDeleteCharacters: func(c string) {
			remaining = runeFrom(remaining, refRuneLen(c))
		},
	}
}
func (t *refDDTarget) makeDeleteElementStartCache(typ string, a op.Attributes) *refDDCache {
	return &refDDCache{
		resolveRetain: func(itemCount int) {
			t.out.deleteElementStart(typ, a)
		},
		resolveDeleteElementStart: func(string, op.Attributes) {},
	}
}
func (t *refDDTarget) deleteElementEndCache() *refDDCache {
	return &refDDCache{
		resolveRetain: func(itemCount int) {
			t.out.deleteElementEnd()
		},
		resolveDeleteElementEnd: func() {},
	}
}

// resolveRange ports the deletion Target.resolveRange. The resolver applies one
// of the *other* target's cache methods.
func (t *refDDTarget) resolveRange(size int, resolver func(size int, cache *refDDCache) error) (int, error) {
	oldPos := t.rel.get()
	t.rel.increase(size)
	if t.rel.get() > 0 {
		if oldPos < 0 {
			if err := resolver(-oldPos, t.other.cache); err != nil {
				return 0, err
			}
		}
		return -oldPos, nil
	}
	if err := resolver(size, t.other.cache); err != nil {
		return -1, err
	}
	return -1, nil
}

func ddRetainResolver(size int, cache *refDDCache) error {
	cache.resolveRetain(size)
	return nil
}
func ddDeleteCharactersResolver(chars string) func(int, *refDDCache) error {
	return func(size int, cache *refDDCache) error {
		if cache.resolveDeleteCharacters == nil {
			return &refTransformError{"DeletionDeletion: incompatible operations"}
		}
		cache.resolveDeleteCharacters(runeSub(chars, 0, size))
		return nil
	}
}
func ddDeleteElementStartResolver(typ string, a op.Attributes) func(int, *refDDCache) error {
	return func(size int, cache *refDDCache) error {
		if cache.resolveDeleteElementStart == nil {
			return &refTransformError{"DeletionDeletion: incompatible operations"}
		}
		cache.resolveDeleteElementStart(typ, a)
		return nil
	}
}
func ddDeleteElementEndResolver(size int, cache *refDDCache) error {
	if cache.resolveDeleteElementEnd == nil {
		return &refTransformError{"DeletionDeletion: incompatible operations"}
	}
	cache.resolveDeleteElementEnd()
	return nil
}

func (t *refDDTarget) retain(itemCount int) error {
	if _, err := t.resolveRange(itemCount, ddRetainResolver); err != nil {
		return err
	}
	t.cache = t.retainCache()
	return nil
}
func (t *refDDTarget) deleteCharacters(chars string) error {
	resolutionSize, err := t.resolveRange(refRuneLen(chars), ddDeleteCharactersResolver(chars))
	if err != nil {
		return err
	}
	if resolutionSize >= 0 {
		t.cache = t.makeDeleteCharactersCache(runeFrom(chars, resolutionSize))
	}
	return nil
}
func (t *refDDTarget) deleteElementStart(typ string, a op.Attributes) error {
	n, err := t.resolveRange(1, ddDeleteElementStartResolver(typ, a))
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.makeDeleteElementStartCache(typ, a)
	}
	return nil
}
func (t *refDDTarget) deleteElementEnd() error {
	n, err := t.resolveRange(1, ddDeleteElementEndResolver)
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.deleteElementEndCache()
	}
	return nil
}
func (t *refDDTarget) apply(c op.Component) error {
	switch v := c.(type) {
	case op.Retain:
		return t.retain(v.Count)
	case op.DeleteCharacters:
		return t.deleteCharacters(v.Text)
	case op.DeleteElementStart:
		return t.deleteElementStart(v.Type, v.Attributes)
	case op.DeleteElementEnd:
		return t.deleteElementEnd()
	default:
		return &refTransformError{fmt.Sprintf("DeletionDeletion: unexpected component %T", c)}
	}
}

func refTransformDeletionDeletion(clientOp, serverOp op.DocOp) (op.DocOp, op.DocOp, error) {
	pt := &refPosTracker{}
	client := &refDDTarget{rel: refRelPos{t: pt, sign: 1}}
	server := &refDDTarget{rel: refRelPos{t: pt, sign: -1}}
	client.other = server
	server.other = client
	client.cache = client.retainCache()
	server.cache = server.retainCache()

	cc, sc := clientOp.Components(), serverOp.Components()
	ci, si := 0, 0
	for ci < len(cc) {
		if err := client.apply(cc[ci]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		ci++
		for client.rel.get() > 0 {
			if si >= len(sc) {
				return op.DocOp{}, op.DocOp{}, &refTransformError{"DeletionDeletion: ran out of server components"}
			}
			if err := server.apply(sc[si]); err != nil {
				return op.DocOp{}, op.DocOp{}, err
			}
			si++
		}
	}
	for si < len(sc) {
		if err := server.apply(sc[si]); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		si++
	}
	return client.out.finish(), server.out.finish(), nil
}

// ---------------------------------------------------------------------------
// AnnotationTamenessChecker. Ports AnnotationTamenessChecker.java. Determines
// whether a structure-preserving op is "tame" relative to a deletion op: it has
// no attribute modifications, and any annotated region is deleted by the
// deletion op. The orchestrator loops the preservation/preservation +
// preservation/deletion transforms until both preservation ops are tamed by
// both deletion ops, then finishes with deletion/deletion.
// ---------------------------------------------------------------------------

type refTameChecker struct {
	annotationsAreTame bool
	deletionState      bool
	activeAnnotations  map[string]bool
}

func newRefTameChecker() *refTameChecker {
	return &refTameChecker{annotationsAreTame: true, activeAnnotations: map[string]bool{}}
}

func (c *refTameChecker) checkAnnotations() {
	if !c.deletionState && len(c.activeAnnotations) > 0 {
		c.annotationsAreTame = false
	}
}

func (c *refTameChecker) annotationsAreTameFor(preservationOp, deletionOp op.DocOp) (bool, error) {
	// Preservation cursor uses sign +1, deletion sign -1.
	pt := &refPosTracker{}
	preRel := refRelPos{t: pt, sign: 1}
	delRel := refRelPos{t: pt, sign: -1}

	applyPre := func(comp op.Component) error {
		switch v := comp.(type) {
		case op.Retain:
			if preRel.get() < 0 {
				c.checkAnnotations()
			}
			preRel.increase(v.Count)
		case op.ReplaceAttributes:
			c.annotationsAreTame = false
			preRel.increase(1)
		case op.UpdateAttributes:
			c.annotationsAreTame = false
			preRel.increase(1)
		case op.AnnotationBoundary:
			for _, k := range v.Boundary.EndKeys() {
				delete(c.activeAnnotations, k)
			}
			for _, ch := range v.Boundary.Changes() {
				c.activeAnnotations[ch.Key] = true
			}
		default:
			return &refTransformError{fmt.Sprintf("Tameness: unexpected preservation component %T", comp)}
		}
		return nil
	}
	processDel := func(itemCount int, newDeletionState bool) {
		c.deletionState = newDeletionState
		c.checkAnnotations()
		delRel.increase(itemCount)
	}
	applyDel := func(comp op.Component) error {
		switch v := comp.(type) {
		case op.Retain:
			processDel(v.Count, false)
		case op.DeleteCharacters:
			processDel(refRuneLen(v.Text), true)
		case op.DeleteElementStart:
			processDel(1, true)
		case op.DeleteElementEnd:
			processDel(1, true)
		default:
			return &refTransformError{fmt.Sprintf("Tameness: unexpected deletion component %T", comp)}
		}
		return nil
	}

	pc, dc := preservationOp.Components(), deletionOp.Components()
	pi, di := 0, 0
	for pi < len(pc) {
		if err := applyPre(pc[pi]); err != nil {
			return false, err
		}
		pi++
		for preRel.get() > 0 {
			if di >= len(dc) {
				return false, &refTransformError{"Tameness: ran out of deletion components"}
			}
			if err := applyDel(dc[di]); err != nil {
				return false, err
			}
			di++
		}
	}
	for di < len(dc) {
		if err := applyDel(dc[di]); err != nil {
			return false, err
		}
		di++
	}
	return c.annotationsAreTame, nil
}

func refCheckTamenessOne(preservationOp, deletionOp op.DocOp) (bool, error) {
	return newRefTameChecker().annotationsAreTameFor(preservationOp, deletionOp)
}

func refCheckTameness(p1, p2, d1, d2 op.DocOp) (bool, error) {
	for _, pair := range [][2]op.DocOp{{p1, d1}, {p1, d2}, {p2, d1}, {p2, d2}} {
		ok, err := refCheckTamenessOne(pair[0], pair[1])
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Attribute helpers for the preservation transformers. These mirror the Java
// AttributesImpl/AttributesUpdateImpl methods used by the reference, kept
// test-local so the oracle is self-contained and does not depend on package-op
// internals (whose error behavior we are trying to validate, not reuse).
// ---------------------------------------------------------------------------

// refAttrsUpdateWith ports AttributesImpl.updateWith: apply an update to a set
// of attributes (set to new value, or remove when new value is nil). No
// compatibility check (matches the reference's usage where it builds expected
// old values for deletions/replacements).
func refAttrsUpdateWith(a op.Attributes, u op.AttributesUpdate) (op.Attributes, error) {
	m := map[string]string{}
	for _, at := range a.All() {
		m[at.Name] = at.Value
	}
	for _, ch := range u.All() {
		if ch.NewValue == nil {
			delete(m, ch.Name)
		} else {
			m[ch.Name] = *ch.NewValue
		}
	}
	return op.NewAttributes(m)
}

// refUpdateExclude ports AttributesUpdate.exclude: drop changes whose keys are
// in the given set.
func refUpdateExclude(u op.AttributesUpdate, keys map[string]bool) (op.AttributesUpdate, error) {
	var changes []op.AttributeChange
	for _, ch := range u.All() {
		if !keys[ch.Name] {
			changes = append(changes, ch)
		}
	}
	return op.NewAttributesUpdate(changes)
}

// ---------------------------------------------------------------------------
// PreservationPreservationTransformer. Ports PreservationPreservationTransformer.java.
// The most intricate transformer: tracks attribute caches (retain / replace /
// update) per side and a cross-side annotation tracker that rewrites concurrent
// annotation changes so both sides converge.
// ---------------------------------------------------------------------------

// refPPCache: a cached range effect with three resolve dispatches. A nil
// resolveReplaceAttributes / resolveUpdateAttributes means "incompatible".
type refPPCache struct {
	resolveRetain            func(itemCount int)
	resolveReplaceAttributes func(oldA, newA op.Attributes) error
	resolveUpdateAttributes  func(u op.AttributesUpdate) error
}

type refPPTarget struct {
	out               *refBuf
	rel               refRelPos
	other             *refPPTarget
	cache             *refPPCache
	annotationTracker *refPPAnnTracker
}

func (t *refPPTarget) retainCache() *refPPCache {
	return &refPPCache{
		resolveRetain: func(itemCount int) {
			t.out.retain(itemCount)
			t.other.out.retain(itemCount)
		},
		resolveReplaceAttributes: func(oldA, newA op.Attributes) error {
			t.out.retain(1)
			t.other.out.replaceAttributes(oldA, newA)
			return nil
		},
		resolveUpdateAttributes: func(u op.AttributesUpdate) error {
			t.out.retain(1)
			t.other.out.updateAttributes(u)
			return nil
		},
	}
}
func (t *refPPTarget) replaceAttributesCache(oldAttributes, newAttributes op.Attributes) *refPPCache {
	return &refPPCache{
		resolveRetain: func(itemCount int) {
			t.out.replaceAttributes(oldAttributes, newAttributes)
			t.other.out.retain(1)
		},
		resolveReplaceAttributes: func(oldA, newA op.Attributes) error {
			t.out.replaceAttributes(newA, newAttributes)
			t.other.out.retain(1)
			return nil
		},
		resolveUpdateAttributes: func(u op.AttributesUpdate) error {
			updated, err := refAttrsUpdateWith(oldAttributes, u)
			if err != nil {
				return err
			}
			t.out.replaceAttributes(updated, newAttributes)
			t.other.out.retain(1)
			return nil
		},
	}
}
func (t *refPPTarget) updateAttributesCache(update op.AttributesUpdate) *refPPCache {
	return &refPPCache{
		resolveRetain: func(itemCount int) {
			t.out.updateAttributes(update)
			t.other.out.retain(1)
		},
		resolveReplaceAttributes: func(oldA, newA op.Attributes) error {
			t.out.retain(1)
			updated, err := refAttrsUpdateWith(oldA, update)
			if err != nil {
				return err
			}
			t.other.out.replaceAttributes(updated, newA)
			return nil
		},
		resolveUpdateAttributes: func(u op.AttributesUpdate) error {
			// `update` is this side's cached update (Java this.update); `u` is the
			// incoming (other side's) update. For each key in the cached update,
			// its old value becomes u's new value if u also changed that key
			// (Java: updated.get(key)); else the cached old value is kept.
			uNewByKey := map[string]*string{}
			for _, ch := range u.All() {
				uNewByKey[ch.Name] = ch.NewValue
			}
			var newChanges []op.AttributeChange
			for _, ch := range update.All() {
				newOld := ch.OldValue
				if v, ok := uNewByKey[ch.Name]; ok {
					newOld = v
				}
				newChanges = append(newChanges, op.AttributeChange{Name: ch.Name, OldValue: newOld, NewValue: ch.NewValue})
			}
			newUpdate, err := op.NewAttributesUpdate(newChanges)
			if err != nil {
				return err
			}
			t.out.updateAttributes(newUpdate)
			// transformedAttributes = u.exclude(keys of the cached update).
			updateKeys := map[string]bool{}
			for _, ch := range update.All() {
				updateKeys[ch.Name] = true
			}
			transformed, err := refUpdateExclude(u, updateKeys)
			if err != nil {
				return err
			}
			t.other.out.updateAttributes(transformed)
			return nil
		},
	}
}

func (t *refPPTarget) resolveRange(size int, resolver func(size int, cache *refPPCache) error) (int, error) {
	oldPos := t.rel.get()
	t.rel.increase(size)
	if t.rel.get() > 0 {
		if oldPos < 0 {
			if err := resolver(-oldPos, t.other.cache); err != nil {
				return 0, err
			}
		}
		return -oldPos, nil
	}
	if err := resolver(size, t.other.cache); err != nil {
		return -1, err
	}
	return -1, nil
}

func ppRetainResolver(size int, cache *refPPCache) error {
	cache.resolveRetain(size)
	return nil
}
func ppReplaceAttributesResolver(oldA, newA op.Attributes) func(int, *refPPCache) error {
	return func(size int, cache *refPPCache) error {
		if cache.resolveReplaceAttributes == nil {
			return &refTransformError{"PreservationPreservation: incompatible operations"}
		}
		return cache.resolveReplaceAttributes(oldA, newA)
	}
}
func ppUpdateAttributesResolver(u op.AttributesUpdate) func(int, *refPPCache) error {
	return func(size int, cache *refPPCache) error {
		if cache.resolveUpdateAttributes == nil {
			return &refTransformError{"PreservationPreservation: incompatible operations"}
		}
		return cache.resolveUpdateAttributes(u)
	}
}

func (t *refPPTarget) retain(itemCount int) error {
	if _, err := t.resolveRange(itemCount, ppRetainResolver); err != nil {
		return err
	}
	t.cache = t.retainCache()
	return nil
}
func (t *refPPTarget) replaceAttributes(oldA, newA op.Attributes) error {
	n, err := t.resolveRange(1, ppReplaceAttributesResolver(oldA, newA))
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.replaceAttributesCache(oldA, newA)
	}
	return nil
}
func (t *refPPTarget) updateAttributes(u op.AttributesUpdate) error {
	n, err := t.resolveRange(1, ppUpdateAttributesResolver(u))
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.updateAttributesCache(u)
	}
	return nil
}
func (t *refPPTarget) annotationBoundary(m op.AnnotationBoundaryMap) {
	t.annotationTracker.register(m)
}
func (t *refPPTarget) apply(c op.Component) error {
	switch v := c.(type) {
	case op.Retain:
		return t.retain(v.Count)
	case op.ReplaceAttributes:
		return t.replaceAttributes(v.OldAttributes, v.NewAttributes)
	case op.UpdateAttributes:
		return t.updateAttributes(v.Update)
	case op.AnnotationBoundary:
		t.annotationBoundary(v.Boundary)
		return nil
	default:
		return &refTransformError{fmt.Sprintf("PreservationPreservation: unexpected component %T", c)}
	}
}

// refPPAnnTracker ports the inner AnnotationTracker. `process` is the cross-side
// rewrite; `tracked` holds this side's currently-open annotation value updates.
type refPPAnnTracker struct {
	tracked map[string]refValueUpdate
	process func(m op.AnnotationBoundaryMap) error
}

func (a *refPPAnnTracker) register(m op.AnnotationBoundaryMap) error {
	for _, k := range m.EndKeys() {
		delete(a.tracked, k)
	}
	for _, ch := range m.Changes() {
		a.tracked[ch.Key] = refValueUpdate{oldValue: ch.OldValue, newValue: ch.NewValue}
	}
	return a.process(m)
}

func refTransformPreservationPreservation(clientOp, serverOp op.DocOp) (op.DocOp, op.DocOp, error) {
	// Both the targets and the cross-side annotation trackers write into these two
	// shared buffers, preserving interleaving order (in Java they share the single
	// clientOperation/serverOperation normalizer).
	clientOut := &refBuf{}
	serverOut := &refBuf{}
	clientTracker := &refPPAnnTracker{tracked: map[string]refValueUpdate{}}
	serverTracker := &refPPAnnTracker{tracked: map[string]refValueUpdate{}}

	// clientTracker.process: client annotation boundary rewrite (ports the first
	// AnnotationTracker in PreservationPreservationTransformer).
	clientTracker.process = func(m op.AnnotationBoundaryMap) error {
		var clientEndKeys []string
		var clientChanges []op.AnnotationChange
		var serverEndKeys []string
		var serverChanges []op.AnnotationChange
		for _, k := range m.EndKeys() {
			sv, ok := serverTracker.tracked[k]
			clientEndKeys = append(clientEndKeys, k)
			if ok {
				serverChanges = append(serverChanges, op.AnnotationChange{Key: k, OldValue: sv.oldValue, NewValue: sv.newValue})
			}
		}
		for _, ch := range m.Changes() {
			sv, ok := serverTracker.tracked[ch.Key]
			cc := op.AnnotationChange{Key: ch.Key, NewValue: ch.NewValue}
			if ok {
				cc.OldValue = sv.newValue
				serverEndKeys = append(serverEndKeys, ch.Key)
			} else {
				cc.OldValue = ch.OldValue
			}
			clientChanges = append(clientChanges, cc)
		}
		cMap, err := op.NewAnnotationBoundaryMap(clientEndKeys, clientChanges)
		if err != nil {
			return &refTransformError{"PreservationPreservation: " + err.Error()}
		}
		sMap, err := op.NewAnnotationBoundaryMap(serverEndKeys, serverChanges)
		if err != nil {
			return &refTransformError{"PreservationPreservation: " + err.Error()}
		}
		clientOut.annotationBoundary(cMap)
		serverOut.annotationBoundary(sMap)
		return nil
	}
	// serverTracker.process: server annotation boundary rewrite (ports the second
	// AnnotationTracker). Note the asymmetry vs. the client tracker.
	serverTracker.process = func(m op.AnnotationBoundaryMap) error {
		var serverEndKeys []string
		var serverChanges []op.AnnotationChange
		var clientEndKeys []string
		var clientChanges []op.AnnotationChange
		for _, k := range m.EndKeys() {
			cv, ok := clientTracker.tracked[k]
			if ok {
				clientChanges = append(clientChanges, op.AnnotationChange{Key: k, OldValue: cv.oldValue, NewValue: cv.newValue})
			} else {
				serverEndKeys = append(serverEndKeys, k)
			}
		}
		for _, ch := range m.Changes() {
			cv, ok := clientTracker.tracked[ch.Key]
			if ok {
				clientChanges = append(clientChanges, op.AnnotationChange{Key: ch.Key, OldValue: ch.NewValue, NewValue: cv.newValue})
			} else {
				serverChanges = append(serverChanges, op.AnnotationChange{Key: ch.Key, OldValue: ch.OldValue, NewValue: ch.NewValue})
			}
		}
		sMap, err := op.NewAnnotationBoundaryMap(serverEndKeys, serverChanges)
		if err != nil {
			return &refTransformError{"PreservationPreservation: " + err.Error()}
		}
		cMap, err := op.NewAnnotationBoundaryMap(clientEndKeys, clientChanges)
		if err != nil {
			return &refTransformError{"PreservationPreservation: " + err.Error()}
		}
		serverOut.annotationBoundary(sMap)
		clientOut.annotationBoundary(cMap)
		return nil
	}

	pt := &refPosTracker{}
	clientTarget := &refPPTarget{out: clientOut, rel: refRelPos{t: pt, sign: 1}, annotationTracker: clientTracker}
	serverTarget := &refPPTarget{out: serverOut, rel: refRelPos{t: pt, sign: -1}, annotationTracker: serverTracker}
	clientTarget.other = serverTarget
	serverTarget.other = clientTarget
	clientTarget.cache = clientTarget.retainCache()
	serverTarget.cache = serverTarget.retainCache()

	cc, sc := clientOp.Components(), serverOp.Components()
	ci, si := 0, 0
	process := func() error {
		for ci < len(cc) {
			if err := clientTarget.apply(cc[ci]); err != nil {
				return err
			}
			ci++
			for clientTarget.rel.get() > 0 {
				if si >= len(sc) {
					return &refTransformError{"PreservationPreservation: ran out of server components"}
				}
				if err := serverTarget.apply(sc[si]); err != nil {
					return err
				}
				si++
			}
		}
		for si < len(sc) {
			if err := serverTarget.apply(sc[si]); err != nil {
				return err
			}
			si++
		}
		return nil
	}
	if err := process(); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	// Merge target output and tracker output streams. The annotation trackers and
	// the targets both append to *separate* buffers in this port; the Java code
	// writes both via the same clientOperation/serverOperation normalizer in
	// boundary order. To preserve ordering we instead made the targets and
	// trackers share the SAME buffers below — handled by using clientOut/serverOut
	// directly as the target buffers. (See wiring note in process.)
	_ = clientOut
	_ = serverOut
	return clientTarget.out.finish(), serverTarget.out.finish(), nil
}
