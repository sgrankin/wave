package op

import "fmt"

// insertionNoninsertionTransform transforms an insertion-form operation against
// an insertion-free operation (ports InsertionNoninsertionTransformer). It
// returns (insertionOp', noninsertionOp').
//
// Mechanism: the noninsertion side, on reading a component, sets up a pending
// "range cache" (its effect) and resolves whatever range overlap is immediately
// available; the insertion side's retain then drives that cache to emit the
// noninsertion effect over the region it advances across. Insertions that land
// inside a deleted *element* (depth > 0) are absorbed — emitted as deletes on
// the noninsertion output and dropped from the insertion output. Deleted
// character runs do not absorb insertions.
func insertionNoninsertionTransform(insertionOp, noninsertionOp DocOp) (DocOp, DocOp, error) {
	pt := &positionTracker{}
	st := &insNonState{
		insPos: relativePosition{t: pt, sign: 1},
		nonPos: relativePosition{t: pt, sign: -1},
	}
	st.cache = st.resolveRetain // default range cache

	ic, nc := insertionOp.components, noninsertionOp.components
	ii, ni := 0, 0
	for ii < len(ic) {
		if err := st.applyInsertion(ic[ii]); err != nil {
			return DocOp{}, DocOp{}, err
		}
		ii++
		for st.insPos.get() > 0 {
			if ni >= len(nc) {
				return DocOp{}, DocOp{}, fmt.Errorf("op: insertion-noninsertion transform: ran out of noninsertion components")
			}
			if err := st.applyNoninsertion(nc[ni]); err != nil {
				return DocOp{}, DocOp{}, err
			}
			ni++
		}
	}
	for ni < len(nc) {
		if err := st.applyNoninsertion(nc[ni]); err != nil {
			return DocOp{}, DocOp{}, err
		}
		ni++
	}
	return st.insOut.finish(), st.nonOut.finish(), nil
}

// insNonState holds both outputs, both relative cursors, the element-deletion
// depth, and the noninsertion side's pending range cache.
type insNonState struct {
	insOut builder
	nonOut builder
	insPos relativePosition
	nonPos relativePosition
	depth  int
	cache  func(itemCount int) // the noninsertion side's pending effect
}

// resolveRetain is the default range cache: both outputs retain.
func (st *insNonState) resolveRetain(itemCount int) {
	st.nonOut.retain(itemCount)
	st.insOut.retain(itemCount)
}

func (st *insNonState) applyInsertion(c Component) error {
	switch v := c.(type) {
	case Retain:
		oldPos := st.insPos.get()
		st.insPos.increase(v.Count)
		if st.insPos.get() < 0 {
			st.cache(v.Count)
		} else if oldPos < 0 {
			st.cache(-oldPos)
		}
	case Characters:
		if st.depth > 0 {
			st.nonOut.deleteCharacters(v.Text)
		} else {
			st.insOut.characters(v.Text)
			st.nonOut.retain(runeLen(v.Text))
		}
	case ElementStart:
		if st.depth > 0 {
			st.nonOut.deleteElementStart(v.Type, v.Attributes)
		} else {
			st.insOut.elementStart(v.Type, v.Attributes)
			st.nonOut.retain(1)
		}
	case ElementEnd:
		if st.depth > 0 {
			st.nonOut.deleteElementEnd()
		} else {
			st.insOut.elementEnd()
			st.nonOut.retain(1)
		}
	default:
		return fmt.Errorf("op: insertion-noninsertion transform: unexpected component %T in insertion op", c)
	}
	return nil
}

func (st *insNonState) applyNoninsertion(c Component) error {
	switch v := c.(type) {
	case Retain:
		st.resolveRange(v.Count, st.resolveRetain)
		st.cache = st.resolveRetain
	case DeleteCharacters:
		chars := v.Text
		cache := func(itemCount int) {
			st.nonOut.deleteCharacters(firstRunes(chars, itemCount))
			chars = restRunes(chars, itemCount)
		}
		if st.resolveRange(runeLen(v.Text), cache) >= 0 {
			st.cache = cache
		}
	case DeleteElementStart:
		cache := func(itemCount int) {
			st.nonOut.deleteElementStart(v.Type, v.Attributes)
			st.depth++
		}
		if st.resolveRange(1, cache) == 0 {
			st.cache = cache
		}
	case DeleteElementEnd:
		cache := func(itemCount int) {
			st.nonOut.deleteElementEnd()
			st.depth--
		}
		if st.resolveRange(1, cache) == 0 {
			st.cache = cache
		}
	case ReplaceAttributes:
		cache := func(itemCount int) {
			st.nonOut.replaceAttributes(v.OldAttributes, v.NewAttributes)
			st.insOut.retain(1)
		}
		if st.resolveRange(1, cache) == 0 {
			st.cache = cache
		}
	case UpdateAttributes:
		cache := func(itemCount int) {
			st.nonOut.updateAttributes(v.Update)
			st.insOut.retain(1)
		}
		if st.resolveRange(1, cache) == 0 {
			st.cache = cache
		}
	case AnnotationBoundary:
		st.nonOut.annotationBoundary(v.Boundary)
	default:
		return fmt.Errorf("op: insertion-noninsertion transform: unexpected component %T in noninsertion op", c)
	}
	return nil
}

// resolveRange advances the noninsertion cursor by size and resolves the freshly
// created cache over whatever overlap is immediately available, returning the
// portion resolved (>= 0) or -1 if the whole range was resolved here.
func (st *insNonState) resolveRange(size int, cache func(int)) int {
	oldPosition := st.nonPos.get()
	st.nonPos.increase(size)
	if st.nonPos.get() > 0 {
		if oldPosition < 0 {
			cache(-oldPosition)
		}
		return -oldPosition
	}
	cache(size)
	return -1
}
