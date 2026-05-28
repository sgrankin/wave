package op

import "fmt"

// Transform reconciles a concurrent client and server operation, both valid
// against the same document, into (clientOp', serverOp') such that
//
//	apply(apply(D, server), clientOp') == apply(apply(D, client), serverOp')
//
// (the TP1 convergence property). Concurrent insertions at the same position
// are tie-broken with the CLIENT first (spec §Transform).
//
// The algorithm decomposes each operation into an insertion-only part and an
// insertion-free part and runs four sub-transforms, then composes the results
// (Transformer.transform).
func Transform(clientOp, serverOp DocOp) (clientPrime, serverPrime DocOp, err error) {
	// Decompose both operations into insertion (i) and noninsertion (n) parts.
	ci0, cn0 := decompose(clientOp)
	si0, sn0 := decompose(serverOp)

	// Four sub-transforms, structured as in Transformer.transform:
	//       ci0     cn0
	//   si0     si1     si2
	//       ci1     cn1
	//   sn0     sn1     sn2
	//       ci2     cn2
	ci1, si1, err := insertionTransform(ci0, si0)
	if err != nil {
		return DocOp{}, DocOp{}, err
	}
	ci2, sn1, err := insertionNoninsertionTransform(ci1, sn0)
	if err != nil {
		return DocOp{}, DocOp{}, err
	}
	si2, cn1, err := insertionNoninsertionTransform(si1, cn0)
	if err != nil {
		return DocOp{}, DocOp{}, err
	}
	cn2, sn2, err := noninsertionTransform(cn1, sn1)
	if err != nil {
		return DocOp{}, DocOp{}, err
	}

	if clientPrime, err = Compose(ci2, cn2); err != nil {
		return DocOp{}, DocOp{}, err
	}
	if serverPrime, err = Compose(si2, sn2); err != nil {
		return DocOp{}, DocOp{}, err
	}
	return clientPrime, serverPrime, nil
}

// decompose splits op into an insertion-only operation and an insertion-free
// operation, such that Compose(insertion, noninsertion) == op. Annotations go
// only into the non-insertion part (spec §Decompose; ports Decomposer).
func decompose(op DocOp) (insertion, noninsertion DocOp) {
	var ins, non builder
	for _, c := range op.components {
		switch v := c.(type) {
		case Retain:
			ins.retain(v.Count)
			non.retain(v.Count)
		case Characters:
			ins.characters(v.Text)
			non.retain(runeLen(v.Text))
		case ElementStart:
			ins.elementStart(v.Type, v.Attributes)
			non.retain(1)
		case ElementEnd:
			ins.elementEnd()
			non.retain(1)
		case DeleteCharacters:
			ins.retain(runeLen(v.Text))
			non.deleteCharacters(v.Text)
		case DeleteElementStart:
			ins.retain(1)
			non.deleteElementStart(v.Type, v.Attributes)
		case DeleteElementEnd:
			ins.retain(1)
			non.deleteElementEnd()
		case ReplaceAttributes:
			ins.retain(1)
			non.replaceAttributes(v.OldAttributes, v.NewAttributes)
		case UpdateAttributes:
			ins.retain(1)
			non.updateAttributes(v.Update)
		case AnnotationBoundary:
			non.annotationBoundary(v.Boundary)
		}
	}
	return ins.finish(), non.finish()
}

// positionTracker tracks two cursors' positions relative to each other on the
// shared input document. Side 1 adds to position; side 2 subtracts. Each side's
// get() returns its own position relative to the other; a negative value means
// that side is behind (ports PositionTracker).
type positionTracker struct {
	position int
}

// relativePosition is one side's view of the shared tracker. sign is +1 for
// side 1 and -1 for side 2.
type relativePosition struct {
	t    *positionTracker
	sign int
}

func (r relativePosition) increase(amount int) { r.t.position += r.sign * amount }
func (r relativePosition) get() int            { return r.sign * r.t.position }

// insTarget processes one side of an insertion-vs-insertion transform. It writes
// transformed output to its own builder and, in coordination, to the other
// side's builder (ports InsertionTransformer.Target).
type insTarget struct {
	out   builder
	rel   relativePosition
	other *insTarget
}

func (t *insTarget) retain(itemCount int) {
	oldPos := t.rel.get()
	t.rel.increase(itemCount)
	switch {
	case t.rel.get() < 0:
		// Still behind the other side: retain the whole range on both outputs.
		t.out.retain(itemCount)
		t.other.out.retain(itemCount)
	case oldPos < 0:
		// Was behind, now caught up: retain only the overlapping portion.
		t.out.retain(-oldPos)
		t.other.out.retain(-oldPos)
	}
	// else already ahead: emit nothing.
}

func (t *insTarget) characters(chars string) {
	t.out.characters(chars)
	t.other.out.retain(runeLen(chars)) // other side skips over this side's new content
}

func (t *insTarget) elementStart(typ string, attrs Attributes) {
	t.out.elementStart(typ, attrs)
	t.other.out.retain(1)
}

func (t *insTarget) elementEnd() {
	t.out.elementEnd()
	t.other.out.retain(1)
}

func (t *insTarget) apply(c Component) error {
	switch v := c.(type) {
	case Retain:
		t.retain(v.Count)
	case Characters:
		t.characters(v.Text)
	case ElementStart:
		t.elementStart(v.Type, v.Attributes)
	case ElementEnd:
		t.elementEnd()
	default:
		return fmt.Errorf("op: insertion transform: unexpected non-insertion component %T", c)
	}
	return nil
}

// insertionTransform transforms two insertion-only operations. The client is
// processed first within each step, so concurrent insertions at the same
// position place the client's content first in both outputs (ports
// InsertionTransformer.transformOperations).
func insertionTransform(clientOp, serverOp DocOp) (DocOp, DocOp, error) {
	pt := &positionTracker{}
	client := &insTarget{rel: relativePosition{t: pt, sign: 1}}
	server := &insTarget{rel: relativePosition{t: pt, sign: -1}}
	client.other = server
	server.other = client

	cc, sc := clientOp.components, serverOp.components
	ci, si := 0, 0
	for ci < len(cc) {
		if err := client.apply(cc[ci]); err != nil {
			return DocOp{}, DocOp{}, err
		}
		ci++
		for client.rel.get() > 0 {
			if si >= len(sc) {
				return DocOp{}, DocOp{}, fmt.Errorf("op: insertion transform: ran out of server components")
			}
			if err := server.apply(sc[si]); err != nil {
				return DocOp{}, DocOp{}, err
			}
			si++
		}
	}
	for si < len(sc) {
		if err := server.apply(sc[si]); err != nil {
			return DocOp{}, DocOp{}, err
		}
		si++
	}
	return client.out.finish(), server.out.finish(), nil
}
