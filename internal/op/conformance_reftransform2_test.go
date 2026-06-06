package op_test

// Continuation of the reference transformer port (see conformance_reftransform_test.go):
// PreservationDeletionTransformer, the ReferenceTransformer orchestrator, and the
// oracle comparison harness.

import (
	"fmt"

	"github.com/sgrankin/wave/internal/op"
)

// ---------------------------------------------------------------------------
// PreservationDeletionTransformer. Ports PreservationDeletionTransformer.java.
// Transforms a structure-preserving op against a deletion op, returning the
// transformed preservation op, an *annotation residue* op (annotation
// boundaries re-applied around deleted ranges so annotations set by the
// preservation op survive into the other side), and the transformed deletion
// op. Returns (preservationResult, annotationResidue, deletionResult).
// ---------------------------------------------------------------------------

// refPDCache: resolve dispatch. Nil methods mean "incompatible" (Java RangeCache
// default throws InternalTransformException).
type refPDCache struct {
	resolveRetain             func(itemCount int) error
	resolveDeleteCharacters   func(chars string) error
	resolveDeleteElementStart func(typ string, a op.Attributes) error
	resolveDeleteElementEnd   func() error
	resolveReplaceAttributes  func(oldA, newA op.Attributes) error
	resolveUpdateAttributes   func(u op.AttributesUpdate) error
}

// refPDState holds the shared transformer state (the module-level fields of the
// Java class). propagatingAnnotations entries may be nil (Java null values).
type refPDState struct {
	preservationOperation  refBuf
	annotationResidue      refBuf
	deletionOperation      refBuf
	activeAnnotations      map[string]refValueUpdate
	propagatingAnnotations map[string]*refValueUpdate
	propagatingOrder       []string // deterministic iteration order
}

func newRefPDState() *refPDState {
	return &refPDState{
		activeAnnotations:      map[string]refValueUpdate{},
		propagatingAnnotations: map[string]*refValueUpdate{},
	}
}

func (s *refPDState) putPropagating(key string, v *refValueUpdate) {
	if _, ok := s.propagatingAnnotations[key]; !ok {
		s.propagatingOrder = append(s.propagatingOrder, key)
	}
	s.propagatingAnnotations[key] = v
}
func (s *refPDState) clearPropagating() {
	s.propagatingAnnotations = map[string]*refValueUpdate{}
	s.propagatingOrder = nil
}

func (s *refPDState) processDeleteCharacters(chars string) error {
	s.deletionOperation.deleteCharacters(chars)
	return s.delete(refRuneLen(chars))
}
func (s *refPDState) processDeleteElementStart(typ string, a op.Attributes) error {
	s.deletionOperation.deleteElementStart(typ, a)
	return s.delete(1)
}
func (s *refPDState) processDeleteElementEnd() error {
	s.deletionOperation.deleteElementEnd()
	return s.delete(1)
}
func (s *refPDState) processReplaceAttributes(oldA, newA op.Attributes) {
	s.annotationResidue.retain(1)
	s.deletionOperation.retain(1)
	s.preservationOperation.replaceAttributes(oldA, newA)
	s.clearPropagating()
}
func (s *refPDState) processUpdateAttributes(u op.AttributesUpdate) {
	s.annotationResidue.retain(1)
	s.deletionOperation.retain(1)
	s.preservationOperation.updateAttributes(u)
	s.clearPropagating()
}

// delete ports the private delete(size): re-open propagating annotations over
// the deleted residue range, retain the range, then close them.
func (s *refPDState) delete(size int) error {
	var keys []string
	var changes []op.AnnotationChange
	for _, key := range s.propagatingOrder {
		upd := s.propagatingAnnotations[key]
		activeUpdate, hasActive := s.activeAnnotations[key]
		if upd != nil {
			oldV := upd.oldValue
			if hasActive {
				oldV = activeUpdate.newValue
			}
			keys = append(keys, key)
			changes = append(changes, op.AnnotationChange{Key: key, OldValue: oldV, NewValue: upd.newValue})
		} else if hasActive {
			keys = append(keys, key)
			changes = append(changes, op.AnnotationChange{Key: key, OldValue: activeUpdate.newValue, NewValue: activeUpdate.oldValue})
		}
	}
	openMap, err := op.NewAnnotationBoundaryMap(nil, changes)
	if err != nil {
		return &refTransformError{"PreservationDeletion: " + err.Error()}
	}
	s.annotationResidue.annotationBoundary(openMap)
	s.annotationResidue.retain(size)
	closeMap, err := op.NewAnnotationBoundaryMap(keys, nil)
	if err != nil {
		return &refTransformError{"PreservationDeletion: " + err.Error()}
	}
	s.annotationResidue.annotationBoundary(closeMap)
	return nil
}

func (s *refPDState) retainCache() *refPDCache {
	return &refPDCache{
		resolveRetain: func(itemCount int) error {
			s.preservationOperation.retain(itemCount)
			s.annotationResidue.retain(itemCount)
			s.deletionOperation.retain(itemCount)
			s.clearPropagating()
			return nil
		},
		resolveDeleteCharacters:   func(chars string) error { return s.processDeleteCharacters(chars) },
		resolveDeleteElementStart: func(typ string, a op.Attributes) error { return s.processDeleteElementStart(typ, a) },
		resolveDeleteElementEnd:   func() error { return s.processDeleteElementEnd() },
		resolveReplaceAttributes: func(oldA, newA op.Attributes) error {
			s.processReplaceAttributes(oldA, newA)
			return nil
		},
		resolveUpdateAttributes: func(u op.AttributesUpdate) error {
			s.processUpdateAttributes(u)
			return nil
		},
	}
}

// --- preservation target ---

type refPDPresTarget struct {
	s     *refPDState
	rel   refRelPos
	other *refPDDelTarget
	cache *refPDCache
}

func (t *refPDPresTarget) replaceAttributesCache(oldAttributes, newAttributes op.Attributes) *refPDCache {
	return &refPDCache{
		resolveRetain: func(itemCount int) error {
			t.s.processReplaceAttributes(oldAttributes, newAttributes)
			return nil
		},
		resolveDeleteElementStart: func(typ string, a op.Attributes) error {
			return t.s.processDeleteElementStart(typ, newAttributes)
		},
	}
}
func (t *refPDPresTarget) updateAttributesCache(update op.AttributesUpdate) *refPDCache {
	return &refPDCache{
		resolveRetain: func(itemCount int) error {
			t.s.processUpdateAttributes(update)
			return nil
		},
		resolveDeleteElementStart: func(typ string, a op.Attributes) error {
			updated, err := refAttrsUpdateWith(a, update)
			if err != nil {
				return err
			}
			return t.s.processDeleteElementStart(typ, updated)
		},
	}
}

func (t *refPDPresTarget) resolveRange(size int, resolver func(size int, cache *refPDCache) error) (int, error) {
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

func (t *refPDPresTarget) retain(itemCount int) error {
	if _, err := t.resolveRange(itemCount, pdRetainResolver); err != nil {
		return err
	}
	t.cache = t.s.retainCache()
	return nil
}
func (t *refPDPresTarget) replaceAttributes(oldA, newA op.Attributes) error {
	n, err := t.resolveRange(1, pdReplaceAttributesResolver(oldA, newA))
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.replaceAttributesCache(oldA, newA)
	}
	return nil
}
func (t *refPDPresTarget) updateAttributes(u op.AttributesUpdate) error {
	n, err := t.resolveRange(1, pdUpdateAttributesResolver(u))
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.updateAttributesCache(u)
	}
	return nil
}
func (t *refPDPresTarget) annotationBoundary(m op.AnnotationBoundaryMap) {
	t.s.preservationOperation.annotationBoundary(m)
	for _, key := range m.EndKeys() {
		if _, ok := t.s.propagatingAnnotations[key]; !ok {
			if av, has := t.s.activeAnnotations[key]; has {
				cp := av
				t.s.putPropagating(key, &cp)
			} else {
				t.s.putPropagating(key, nil)
			}
		}
		delete(t.s.activeAnnotations, key)
	}
	for _, ch := range m.Changes() {
		if _, ok := t.s.propagatingAnnotations[ch.Key]; !ok {
			if av, has := t.s.activeAnnotations[ch.Key]; has {
				cp := av
				t.s.putPropagating(ch.Key, &cp)
			} else {
				t.s.putPropagating(ch.Key, nil)
			}
		}
		t.s.activeAnnotations[ch.Key] = refValueUpdate{oldValue: ch.OldValue, newValue: ch.NewValue}
	}
}
func (t *refPDPresTarget) apply(c op.Component) error {
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
		return &refTransformError{fmt.Sprintf("PreservationDeletion: unexpected preservation component %T", c)}
	}
}

// --- deletion target ---

type refPDDelTarget struct {
	s     *refPDState
	rel   refRelPos
	other *refPDPresTarget
	cache *refPDCache
}

func (t *refPDDelTarget) makeDeleteCharactersCache(chars string) *refPDCache {
	remaining := chars
	return &refPDCache{
		resolveRetain: func(itemCount int) error {
			if err := t.s.processDeleteCharacters(runeSub(remaining, 0, itemCount)); err != nil {
				return err
			}
			remaining = runeFrom(remaining, itemCount)
			return nil
		},
		resolveDeleteCharacters: func(c string) error {
			remaining = runeFrom(remaining, refRuneLen(c))
			return nil
		},
	}
}
func (t *refPDDelTarget) makeDeleteElementStartCache(typ string, a op.Attributes) *refPDCache {
	return &refPDCache{
		resolveRetain: func(itemCount int) error {
			return t.s.processDeleteElementStart(typ, a)
		},
		resolveDeleteElementStart: func(string, op.Attributes) error { return nil },
		// Java marks these unreachable (assert false) but keeps a fallthrough; we
		// reproduce the fallthrough behavior without the assert.
		resolveReplaceAttributes: func(oldA, newA op.Attributes) error {
			return t.s.processDeleteElementStart(typ, newA)
		},
		resolveUpdateAttributes: func(u op.AttributesUpdate) error {
			updated, err := refAttrsUpdateWith(a, u)
			if err != nil {
				return err
			}
			return t.s.processDeleteElementStart(typ, updated)
		},
	}
}
func (t *refPDDelTarget) deleteElementEndCache() *refPDCache {
	return &refPDCache{
		resolveRetain:           func(itemCount int) error { return t.s.processDeleteElementEnd() },
		resolveDeleteElementEnd: func() error { return nil },
	}
}

func (t *refPDDelTarget) resolveRange(size int, resolver func(size int, cache *refPDCache) error) (int, error) {
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

func (t *refPDDelTarget) retain(itemCount int) error {
	if _, err := t.resolveRange(itemCount, pdRetainResolver); err != nil {
		return err
	}
	t.cache = t.s.retainCache()
	return nil
}
func (t *refPDDelTarget) deleteCharacters(chars string) error {
	resolutionSize, err := t.resolveRange(refRuneLen(chars), pdDeleteCharactersResolver(chars))
	if err != nil {
		return err
	}
	if resolutionSize >= 0 {
		t.cache = t.makeDeleteCharactersCache(runeFrom(chars, resolutionSize))
	}
	return nil
}
func (t *refPDDelTarget) deleteElementStart(typ string, a op.Attributes) error {
	n, err := t.resolveRange(1, pdDeleteElementStartResolver(typ, a))
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.makeDeleteElementStartCache(typ, a)
	}
	return nil
}
func (t *refPDDelTarget) deleteElementEnd() error {
	n, err := t.resolveRange(1, pdDeleteElementEndResolver)
	if err != nil {
		return err
	}
	if n == 0 {
		t.cache = t.deleteElementEndCache()
	}
	return nil
}
func (t *refPDDelTarget) apply(c op.Component) error {
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
		return &refTransformError{fmt.Sprintf("PreservationDeletion: unexpected deletion component %T", c)}
	}
}

// --- resolvers (dispatch to a cache method, defaulting to incompatible) ---

func pdRetainResolver(size int, cache *refPDCache) error {
	if cache.resolveRetain == nil {
		return &refTransformError{"PreservationDeletion: incompatible operations"}
	}
	return cache.resolveRetain(size)
}
func pdDeleteCharactersResolver(chars string) func(int, *refPDCache) error {
	return func(size int, cache *refPDCache) error {
		if cache.resolveDeleteCharacters == nil {
			return &refTransformError{"PreservationDeletion: incompatible operations"}
		}
		return cache.resolveDeleteCharacters(runeSub(chars, 0, size))
	}
}
func pdDeleteElementStartResolver(typ string, a op.Attributes) func(int, *refPDCache) error {
	return func(size int, cache *refPDCache) error {
		if cache.resolveDeleteElementStart == nil {
			return &refTransformError{"PreservationDeletion: incompatible operations"}
		}
		return cache.resolveDeleteElementStart(typ, a)
	}
}
func pdDeleteElementEndResolver(size int, cache *refPDCache) error {
	if cache.resolveDeleteElementEnd == nil {
		return &refTransformError{"PreservationDeletion: incompatible operations"}
	}
	return cache.resolveDeleteElementEnd()
}
func pdReplaceAttributesResolver(oldA, newA op.Attributes) func(int, *refPDCache) error {
	return func(size int, cache *refPDCache) error {
		if cache.resolveReplaceAttributes == nil {
			return &refTransformError{"PreservationDeletion: incompatible operations"}
		}
		return cache.resolveReplaceAttributes(oldA, newA)
	}
}
func pdUpdateAttributesResolver(u op.AttributesUpdate) func(int, *refPDCache) error {
	return func(size int, cache *refPDCache) error {
		if cache.resolveUpdateAttributes == nil {
			return &refTransformError{"PreservationDeletion: incompatible operations"}
		}
		return cache.resolveUpdateAttributes(u)
	}
}

// refTransformPreservationDeletion returns (preservationResult, annotationResidue, deletionResult).
func refTransformPreservationDeletion(preservationOp, deletionOp op.DocOp) (op.DocOp, op.DocOp, op.DocOp, error) {
	s := newRefPDState()
	pt := &refPosTracker{}
	pres := &refPDPresTarget{s: s, rel: refRelPos{t: pt, sign: 1}}
	del := &refPDDelTarget{s: s, rel: refRelPos{t: pt, sign: -1}}
	pres.other = del
	del.other = pres
	pres.cache = s.retainCache()
	del.cache = s.retainCache()

	pc, dc := preservationOp.Components(), deletionOp.Components()
	pi, di := 0, 0
	for pi < len(pc) {
		if err := pres.apply(pc[pi]); err != nil {
			return op.DocOp{}, op.DocOp{}, op.DocOp{}, err
		}
		pi++
		for pres.rel.get() > 0 {
			if di >= len(dc) {
				return op.DocOp{}, op.DocOp{}, op.DocOp{}, &refTransformError{"PreservationDeletion: ran out of deletion components"}
			}
			if err := del.apply(dc[di]); err != nil {
				return op.DocOp{}, op.DocOp{}, op.DocOp{}, err
			}
			di++
		}
	}
	for di < len(dc) {
		if err := del.apply(dc[di]); err != nil {
			return op.DocOp{}, op.DocOp{}, op.DocOp{}, err
		}
		di++
	}
	return s.preservationOperation.finish(), s.annotationResidue.finish(), s.deletionOperation.finish(), nil
}

// ---------------------------------------------------------------------------
// ReferenceTransformer orchestrator. Ports ReferenceTransformer.java.
// ---------------------------------------------------------------------------

// refComposeAll left-folds Compose over the collected ops, returning an error
// on illegal composition (the reference can produce incompatible residues).
// This mirrors DocOpCollector.composeAll but is error-returning (unlike the
// existing test helper composeAll, which fails the test). An empty list yields
// the empty (identity) op — acceptable since the orchestrator always seeds the
// collectors with the insertion parts.
func refComposeAll(ops []op.DocOp) (result op.DocOp, err error) {
	if len(ops) == 0 {
		return op.EmptyDoc(), nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = &refTransformError{fmt.Sprintf("composeAll: %v", r)}
		}
	}()
	acc := ops[0]
	for _, next := range ops[1:] {
		acc, err = op.Compose(acc, next)
		if err != nil {
			return op.DocOp{}, err
		}
	}
	return acc, nil
}

// refTransform is the reference oracle: it transforms a pair of DocOps using the
// decompose / pairwise-transform / recompose pipeline of ReferenceTransformer.
func refTransform(clientOp, serverOp op.DocOp) (clientPrime, serverPrime op.DocOp, err error) {
	c := refDecompose(clientOp)
	s := refDecompose(serverOp)
	ci, cp, cd := c.insertion, c.preservation, c.deletion
	si, sp, sd := s.insertion, s.preservation, s.deletion

	if ci, si, err = refTransformInsertionInsertion(ci, si); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	if ci, sp, err = refTransformInsertionPreservation(ci, sp); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	if si, cp, err = refTransformInsertionPreservation(si, cp); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	if ci, sd, err = refTransformInsertionDeletion(ci, sd); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	if si, cd, err = refTransformInsertionDeletion(si, cd); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}

	clientCollector := []op.DocOp{ci}
	serverCollector := []op.DocOp{si}

	for {
		tame, terr := refCheckTameness(cp, sp, cd, sd)
		if terr != nil {
			return op.DocOp{}, op.DocOp{}, terr
		}
		if tame {
			break
		}
		if cp, sp, err = refTransformPreservationPreservation(cp, sp); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		var rcFirst, rcResidue, rcDeletion op.DocOp
		if rcFirst, rcResidue, rcDeletion, err = refTransformPreservationDeletion(cp, sd); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		var rsFirst, rsResidue, rsDeletion op.DocOp
		if rsFirst, rsResidue, rsDeletion, err = refTransformPreservationDeletion(sp, cd); err != nil {
			return op.DocOp{}, op.DocOp{}, err
		}
		clientCollector = append(clientCollector, rcFirst)
		serverCollector = append(serverCollector, rsFirst)
		// rc: (preservation', (annotationResidue, deletion'))
		//   sp = rc.second.first; sd = rc.second.second
		// rs: sp side
		//   cp = rs.second.first; cd = rs.second.second
		sp = rcResidue
		sd = rcDeletion
		cp = rsResidue
		cd = rsDeletion
	}

	var cdd, sdd op.DocOp
	if cdd, sdd, err = refTransformDeletionDeletion(cd, sd); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	clientCollector = append(clientCollector, cdd)
	serverCollector = append(serverCollector, sdd)

	if clientPrime, err = refComposeAll(clientCollector); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	if serverPrime, err = refComposeAll(serverCollector); err != nil {
		return op.DocOp{}, op.DocOp{}, err
	}
	return clientPrime, serverPrime, nil
}
