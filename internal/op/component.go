package op

import "unicode/utf8"

// Component is one element of a DocOp's ordered sequence. The set of components
// is closed: the only implementations are the types declared in this package
// (the unexported marker method seals the interface).
type Component interface {
	isComponent()
}

// Retain advances the cursor over Count existing items, copying them unchanged.
// Count must be positive.
type Retain struct {
	Count int
}

// Characters inserts the runes of Text at the cursor. Text is non-empty.
type Characters struct {
	Text string
}

// ElementStart inserts an element open tag with the given type and attributes.
type ElementStart struct {
	Type       string
	Attributes Attributes
}

// ElementEnd inserts an element close tag.
type ElementEnd struct{}

// DeleteCharacters deletes the runes of Text at the cursor (which must match the
// document). Text is non-empty.
type DeleteCharacters struct {
	Text string
}

// DeleteElementStart deletes the element open tag at the cursor (which must
// match the given type and attributes).
type DeleteElementStart struct {
	Type       string
	Attributes Attributes
}

// DeleteElementEnd deletes the element close tag at the cursor.
type DeleteElementEnd struct{}

// ReplaceAttributes replaces the attributes of the element-start at the cursor,
// from OldAttributes (the expected current value) to NewAttributes.
type ReplaceAttributes struct {
	OldAttributes Attributes
	NewAttributes Attributes
}

// UpdateAttributes mutates individual attributes of the element-start at the
// cursor per Update.
type UpdateAttributes struct {
	Update AttributesUpdate
}

// AnnotationBoundary opens and/or closes annotation ranges at the cursor without
// consuming or producing any items (it is zero-width).
type AnnotationBoundary struct {
	Boundary AnnotationBoundaryMap
}

func (Retain) isComponent()             {}
func (Characters) isComponent()         {}
func (ElementStart) isComponent()       {}
func (ElementEnd) isComponent()         {}
func (DeleteCharacters) isComponent()   {}
func (DeleteElementStart) isComponent() {}
func (DeleteElementEnd) isComponent()   {}
func (ReplaceAttributes) isComponent()  {}
func (UpdateAttributes) isComponent()   {}
func (AnnotationBoundary) isComponent() {}

// inputItems reports how many existing document items the component consumes
// (spec §"DocOp component types"). Characters are counted as runes.
func inputItems(c Component) int {
	switch c := c.(type) {
	case Retain:
		return c.Count
	case DeleteCharacters:
		return utf8.RuneCountInString(c.Text)
	case DeleteElementStart, DeleteElementEnd, ReplaceAttributes, UpdateAttributes:
		return 1
	default: // Characters, ElementStart, ElementEnd, AnnotationBoundary
		return 0
	}
}

// outputItems reports how many items the component produces in the resulting
// document (spec §"DocOp component types"). Characters are counted as runes.
func outputItems(c Component) int {
	switch c := c.(type) {
	case Retain:
		return c.Count
	case Characters:
		return utf8.RuneCountInString(c.Text)
	case ElementStart, ElementEnd, ReplaceAttributes, UpdateAttributes:
		return 1
	default: // DeleteCharacters, DeleteElementStart, DeleteElementEnd, AnnotationBoundary
		return 0
	}
}

// DocOp is an immutable, ordered sequence of components.
//
// NOTE: NewDocOp does not yet enforce full well-formedness (the cross-component
// nesting/scope/annotation-balance automaton, spec rules 3–8); that arrives with
// the validator in the apply/compose increment. The component value types it
// carries (Attributes, AttributesUpdate, AnnotationBoundaryMap) are already
// validated at their own construction.
type DocOp struct {
	components []Component
}

// NewDocOp returns a DocOp over a copy of the given components.
func NewDocOp(components []Component) DocOp {
	return DocOp{components: append([]Component(nil), components...)}
}

// Size returns the number of components.
func (d DocOp) Size() int { return len(d.components) }

// Components returns the components in order (a copy).
func (d DocOp) Components() []Component {
	return append([]Component(nil), d.components...)
}

// inputLength returns the total document length this op consumes (sum of input
// items across components).
func (d DocOp) inputLength() int {
	n := 0
	for _, c := range d.components {
		n += inputItems(c)
	}
	return n
}

// outputLength returns the total document length this op produces.
func (d DocOp) outputLength() int {
	n := 0
	for _, c := range d.components {
		n += outputItems(c)
	}
	return n
}
