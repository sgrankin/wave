package op

import "sort"

// builder accumulates components into a normalized DocOp: adjacent retains,
// characters, and deleteCharacters are merged; zero-width pieces are dropped;
// and consecutive annotation boundaries are coalesced into one (so the output
// never has adjacent boundaries, per the well-formedness rules). This is the Go
// stand-in for OperationNormalizer + DocOpBuffer.
//
// Normalization affects only the canonical form of the output, not its meaning:
// applying a normalized op and its un-normalized equivalent yields identical
// documents. We do not reproduce the Java normalizer's annotation-state
// elision (dropping redundant value changes) — unnecessary without byte-level
// Java interop and irrelevant to convergence.
type builder struct {
	out     []Component
	pending *pendingAnnotation // a not-yet-emitted, accumulating annotation boundary
}

// pendingAnnotation accumulates annotation boundary changes until the next
// item-bearing component forces them to be emitted.
type pendingAnnotation struct {
	ends    map[string]bool
	changes map[string]AnnotationChange
}

func (b *builder) annotationBoundary(m AnnotationBoundaryMap) {
	if m.Empty() {
		return
	}
	if b.pending == nil {
		b.pending = &pendingAnnotation{ends: map[string]bool{}, changes: map[string]AnnotationChange{}}
	}
	for _, k := range m.endKeys {
		delete(b.pending.changes, k)
		b.pending.ends[k] = true
	}
	for _, c := range m.changes {
		delete(b.pending.ends, c.Key)
		b.pending.changes[c.Key] = c
	}
}

// flushAnnotation emits the accumulated annotation boundary (if any) as a single
// component before any item-bearing component is appended. The pending state
// already guarantees the boundary's invariants (end and change key sets are
// kept disjoint as entries are added, keys come from validated maps), so the
// map is constructed directly here — only sorting is needed.
func (b *builder) flushAnnotation() {
	if b.pending == nil {
		return
	}
	p := b.pending
	b.pending = nil
	if len(p.ends) == 0 && len(p.changes) == 0 {
		return
	}
	ends := make([]string, 0, len(p.ends))
	for k := range p.ends {
		ends = append(ends, k)
	}
	sort.Strings(ends)
	changes := make([]AnnotationChange, 0, len(p.changes))
	for _, c := range p.changes {
		changes = append(changes, c)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	b.out = append(b.out, AnnotationBoundary{Boundary: AnnotationBoundaryMap{endKeys: ends, changes: changes}})
}

func (b *builder) retain(n int) {
	if n <= 0 {
		return
	}
	b.flushAnnotation()
	if k := len(b.out) - 1; k >= 0 {
		if last, ok := b.out[k].(Retain); ok {
			b.out[k] = Retain{Count: last.Count + n}
			return
		}
	}
	b.out = append(b.out, Retain{Count: n})
}

func (b *builder) characters(s string) {
	if s == "" {
		return
	}
	b.flushAnnotation()
	if k := len(b.out) - 1; k >= 0 {
		if last, ok := b.out[k].(Characters); ok {
			b.out[k] = Characters{Text: last.Text + s}
			return
		}
	}
	b.out = append(b.out, Characters{Text: s})
}

func (b *builder) deleteCharacters(s string) {
	if s == "" {
		return
	}
	b.flushAnnotation()
	if k := len(b.out) - 1; k >= 0 {
		if last, ok := b.out[k].(DeleteCharacters); ok {
			b.out[k] = DeleteCharacters{Text: last.Text + s}
			return
		}
	}
	b.out = append(b.out, DeleteCharacters{Text: s})
}

func (b *builder) elementStart(typ string, attrs Attributes) {
	b.flushAnnotation()
	b.out = append(b.out, ElementStart{Type: typ, Attributes: attrs})
}

func (b *builder) elementEnd() {
	b.flushAnnotation()
	b.out = append(b.out, ElementEnd{})
}

func (b *builder) deleteElementStart(typ string, attrs Attributes) {
	b.flushAnnotation()
	b.out = append(b.out, DeleteElementStart{Type: typ, Attributes: attrs})
}

func (b *builder) deleteElementEnd() {
	b.flushAnnotation()
	b.out = append(b.out, DeleteElementEnd{})
}

func (b *builder) replaceAttributes(oldAttrs, newAttrs Attributes) {
	b.flushAnnotation()
	b.out = append(b.out, ReplaceAttributes{OldAttributes: oldAttrs, NewAttributes: newAttrs})
}

func (b *builder) updateAttributes(u AttributesUpdate) {
	b.flushAnnotation()
	b.out = append(b.out, UpdateAttributes{Update: u})
}

// add feeds an existing component through the normalizing builder.
func (b *builder) add(c Component) {
	switch v := c.(type) {
	case Retain:
		b.retain(v.Count)
	case Characters:
		b.characters(v.Text)
	case ElementStart:
		b.elementStart(v.Type, v.Attributes)
	case ElementEnd:
		b.elementEnd()
	case DeleteCharacters:
		b.deleteCharacters(v.Text)
	case DeleteElementStart:
		b.deleteElementStart(v.Type, v.Attributes)
	case DeleteElementEnd:
		b.deleteElementEnd()
	case ReplaceAttributes:
		b.replaceAttributes(v.OldAttributes, v.NewAttributes)
	case UpdateAttributes:
		b.updateAttributes(v.Update)
	case AnnotationBoundary:
		b.annotationBoundary(v.Boundary)
	}
}

// finish emits any trailing annotation boundary and returns the built DocOp.
func (b *builder) finish() DocOp {
	b.flushAnnotation()
	return DocOp{components: b.out}
}
