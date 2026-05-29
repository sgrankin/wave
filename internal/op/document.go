package op

// A document is represented as an insertion-only DocOp — a "DocInitialization",
// the operation that builds the document from empty. Applying an operation to a
// document is therefore just composing the document with the operation.

// EmptyDoc returns the empty document (a DocOp with no components).
func EmptyDoc() DocOp { return DocOp{} }

// IsInitialization reports whether d is insertion-only (characters, element
// start/end, and annotation boundaries only): i.e. a document rather than a
// general mutating operation.
func (d DocOp) IsInitialization() bool {
	for _, c := range d.components {
		switch c.(type) {
		case Characters, ElementStart, ElementEnd, AnnotationBoundary:
		default:
			return false
		}
	}
	return true
}

// DocumentLength returns the number of items in the document this op
// represents — meaningful for a DocInitialization, where it is the document's
// length (start tags, end tags, and characters each count as one item). For a
// general op it is the op's output length.
func (d DocOp) DocumentLength() int { return d.outputLength() }

// Apply applies op to the document doc, returning the resulting document. It is
// defined as Compose(doc, op): op must cover the whole document (op's input
// length must equal doc's length). An error is returned if op is invalid against
// doc.
func Apply(doc, op DocOp) (DocOp, error) {
	return Compose(doc, op)
}

// Invert returns the operation that exactly undoes d: applying d then Invert(d)
// to a document leaves it unchanged. It is a per-component mapping; component
// order is preserved (spec §Invert).
func Invert(d DocOp) DocOp {
	out := make([]Component, len(d.components))
	for i, c := range d.components {
		switch v := c.(type) {
		case Retain:
			out[i] = v
		case Characters:
			out[i] = DeleteCharacters(v)
		case ElementStart:
			out[i] = DeleteElementStart(v)
		case ElementEnd:
			out[i] = DeleteElementEnd{}
		case DeleteCharacters:
			out[i] = Characters(v)
		case DeleteElementStart:
			out[i] = ElementStart(v)
		case DeleteElementEnd:
			out[i] = ElementEnd{}
		case ReplaceAttributes:
			out[i] = ReplaceAttributes{OldAttributes: v.NewAttributes, NewAttributes: v.OldAttributes}
		case UpdateAttributes:
			out[i] = UpdateAttributes{Update: v.Update.invert()}
		case AnnotationBoundary:
			out[i] = AnnotationBoundary{Boundary: v.Boundary.swap()}
		}
	}
	return DocOp{components: out}
}

// Normalize returns the canonical form of d: adjacent retains/characters/
// deleteCharacters merged, zero-width pieces dropped, consecutive annotation
// boundaries coalesced.
func (d DocOp) Normalize() DocOp {
	var b builder
	for _, c := range d.components {
		b.add(c)
	}
	return b.finish()
}

// Equal reports whether d and other are equivalent operations (equal after
// normalization). This is the comparison used to check OT convergence.
func (d DocOp) Equal(other DocOp) bool {
	a := d.Normalize().components
	b := other.Normalize().components
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !componentEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func componentEqual(a, b Component) bool {
	switch av := a.(type) {
	case Retain:
		bv, ok := b.(Retain)
		return ok && av.Count == bv.Count
	case Characters:
		bv, ok := b.(Characters)
		return ok && av.Text == bv.Text
	case ElementStart:
		bv, ok := b.(ElementStart)
		return ok && av.Type == bv.Type && av.Attributes.Equal(bv.Attributes)
	case ElementEnd:
		_, ok := b.(ElementEnd)
		return ok
	case DeleteCharacters:
		bv, ok := b.(DeleteCharacters)
		return ok && av.Text == bv.Text
	case DeleteElementStart:
		bv, ok := b.(DeleteElementStart)
		return ok && av.Type == bv.Type && av.Attributes.Equal(bv.Attributes)
	case DeleteElementEnd:
		_, ok := b.(DeleteElementEnd)
		return ok
	case ReplaceAttributes:
		bv, ok := b.(ReplaceAttributes)
		return ok && av.OldAttributes.Equal(bv.OldAttributes) && av.NewAttributes.Equal(bv.NewAttributes)
	case UpdateAttributes:
		bv, ok := b.(UpdateAttributes)
		return ok && av.Update.Equal(bv.Update)
	case AnnotationBoundary:
		bv, ok := b.(AnnotationBoundary)
		return ok && av.Boundary.Equal(bv.Boundary)
	default:
		return false
	}
}
