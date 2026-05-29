// Package doc reads a document — represented as an insertion-only DocOp (a
// DocInitialization) — as a navigable element tree. This is the read-side
// projection of the operation model: the Java indexed mutable document model is
// not ported (see docs/architecture/02-porting-plan.md), so structure is
// recovered by walking the initialization's components.
//
// Annotations are a parallel layer and are ignored by this structural reader.
package doc

import (
	"fmt"
	"strings"

	"github.com/sgrankin/wave/internal/op"
)

// Node is a document tree node: either an Element or Text.
type Node interface{ isNode() }

// Element is an XML-like element with a tag, attributes, and ordered children.
type Element struct {
	Type       string
	Attributes op.Attributes
	Children   []Node
}

func (*Element) isNode() {}

// Text is a run of character data.
type Text struct{ Data string }

func (Text) isNode() {}

// Read parses a DocInitialization into its top-level nodes (its forest, usually
// a single root element). It errors if d is not insertion-only (contains
// retains or deletions) or its elements are unbalanced.
func Read(d op.DocOp) ([]Node, error) {
	var roots []Node
	var stack []*Element
	add := func(n Node) {
		if len(stack) == 0 {
			roots = append(roots, n)
		} else {
			top := stack[len(stack)-1]
			top.Children = append(top.Children, n)
		}
	}
	for _, c := range d.Components() {
		switch v := c.(type) {
		case op.ElementStart:
			el := &Element{Type: v.Type, Attributes: v.Attributes}
			add(el)
			stack = append(stack, el)
		case op.ElementEnd:
			if len(stack) == 0 {
				return nil, fmt.Errorf("doc: unbalanced element end")
			}
			stack = stack[:len(stack)-1]
		case op.Characters:
			add(Text{Data: v.Text})
		case op.AnnotationBoundary:
			// Parallel annotation layer; not part of the structural tree.
		default:
			return nil, fmt.Errorf("doc: not a document initialization: unexpected %T component", c)
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("doc: %d unclosed element(s)", len(stack))
	}
	return roots, nil
}

// Root parses d and returns its single root element, erroring if there is not
// exactly one top-level element (ignoring nothing — stray top-level text is an
// error).
func Root(d op.DocOp) (*Element, error) {
	roots, err := Read(d)
	if err != nil {
		return nil, err
	}
	if len(roots) != 1 {
		return nil, fmt.Errorf("doc: expected a single root element, found %d top-level nodes", len(roots))
	}
	el, ok := roots[0].(*Element)
	if !ok {
		return nil, fmt.Errorf("doc: root node is text, not an element")
	}
	return el, nil
}

// Attr returns the value of the named attribute and whether it is present.
func (e *Element) Attr(name string) (string, bool) { return e.Attributes.Get(name) }

// ChildElements returns e's element children in order, skipping text nodes.
func (e *Element) ChildElements() []*Element {
	var els []*Element
	for _, c := range e.Children {
		if el, ok := c.(*Element); ok {
			els = append(els, el)
		}
	}
	return els
}

// Text returns the concatenation of e's immediate text children (not recursive).
func (e *Element) Text() string {
	var b strings.Builder
	for _, c := range e.Children {
		if t, ok := c.(Text); ok {
			b.WriteString(t.Data)
		}
	}
	return b.String()
}
