// Package op defines Wave's document operation (DocOp) algebra: the operation
// component model and the transform, compose, and invert algorithms that make
// concurrent editing converge. A DocOp is an ordered sequence of components
// applied over a document cursor.
//
// A document is itself just an insertion-only DocOp (a "DocInitialization"):
// applying operation B to document A is compose(A, B). This collapses the
// document model into the operation algebra.
//
// Spec: docs/specs/02-operational-transform.md.
package op

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// Attribute is a single XML attribute name/value pair.
type Attribute struct {
	Name  string
	Value string
}

// Attributes is an immutable, strictly name-sorted set of XML attribute
// name→value pairs. Names are unique and non-empty; names and values are valid
// UTF-8. The zero value is the empty attribute set.
type Attributes struct {
	attrs []Attribute
}

// NewAttributes builds an Attributes from a name→value map (which guarantees
// unique names), validating each name/value and sorting by name.
func NewAttributes(m map[string]string) (Attributes, error) {
	if len(m) == 0 {
		return Attributes{}, nil
	}
	attrs := make([]Attribute, 0, len(m))
	for name, value := range m {
		if name == "" {
			return Attributes{}, fmt.Errorf("op: empty attribute name")
		}
		if !utf8.ValidString(name) {
			return Attributes{}, fmt.Errorf("op: attribute name %q is not valid UTF-8", name)
		}
		if !utf8.ValidString(value) {
			return Attributes{}, fmt.Errorf("op: attribute %q value is not valid UTF-8", name)
		}
		attrs = append(attrs, Attribute{Name: name, Value: value})
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	return Attributes{attrs: attrs}, nil
}

// Len returns the number of attributes.
func (a Attributes) Len() int { return len(a.attrs) }

// Get returns the value for name and whether it is present.
func (a Attributes) Get(name string) (string, bool) {
	for _, at := range a.attrs {
		if at.Name == name {
			return at.Value, true
		}
		if at.Name > name {
			break
		}
	}
	return "", false
}

// All returns the attributes in name order. The returned slice is a copy; the
// caller may not mutate the receiver through it.
func (a Attributes) All() []Attribute {
	return append([]Attribute(nil), a.attrs...)
}

// Equal reports whether a and other hold the same name/value pairs.
func (a Attributes) Equal(other Attributes) bool {
	if len(a.attrs) != len(other.attrs) {
		return false
	}
	for i := range a.attrs {
		if a.attrs[i] != other.attrs[i] {
			return false
		}
	}
	return true
}

// AttributeChange is a single attribute mutation: a name with its expected old
// value and new value. A nil OldValue means the attribute was absent; a nil
// NewValue means the attribute is removed.
type AttributeChange struct {
	Name     string
	OldValue *string
	NewValue *string
}

// AttributesUpdate is an immutable, strictly name-sorted set of attribute
// mutations. The zero value is the empty update.
type AttributesUpdate struct {
	updates []AttributeChange
}

// NewAttributesUpdate builds an AttributesUpdate from the given changes,
// validating names/values, rejecting duplicate names, and sorting by name.
func NewAttributesUpdate(changes []AttributeChange) (AttributesUpdate, error) {
	if len(changes) == 0 {
		return AttributesUpdate{}, nil
	}
	cp := append([]AttributeChange(nil), changes...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })
	for i, c := range cp {
		if c.Name == "" {
			return AttributesUpdate{}, fmt.Errorf("op: empty attribute name in update")
		}
		if i > 0 && cp[i-1].Name == c.Name {
			return AttributesUpdate{}, fmt.Errorf("op: duplicate attribute name %q in update", c.Name)
		}
		if !utf8.ValidString(c.Name) {
			return AttributesUpdate{}, fmt.Errorf("op: attribute name %q is not valid UTF-8", c.Name)
		}
		if c.OldValue != nil && !utf8.ValidString(*c.OldValue) {
			return AttributesUpdate{}, fmt.Errorf("op: attribute %q old value is not valid UTF-8", c.Name)
		}
		if c.NewValue != nil && !utf8.ValidString(*c.NewValue) {
			return AttributesUpdate{}, fmt.Errorf("op: attribute %q new value is not valid UTF-8", c.Name)
		}
	}
	return AttributesUpdate{updates: cp}, nil
}

// Len returns the number of attribute changes.
func (u AttributesUpdate) Len() int { return len(u.updates) }

// All returns the changes in name order (a copy).
func (u AttributesUpdate) All() []AttributeChange {
	return append([]AttributeChange(nil), u.updates...)
}

// Equal reports whether u and other hold the same changes.
func (u AttributesUpdate) Equal(other AttributesUpdate) bool {
	if len(u.updates) != len(other.updates) {
		return false
	}
	for i := range u.updates {
		if !changeEqual(u.updates[i], other.updates[i]) {
			return false
		}
	}
	return true
}

func changeEqual(a, b AttributeChange) bool {
	return a.Name == b.Name && ptrEqual(a.OldValue, b.OldValue) && ptrEqual(a.NewValue, b.NewValue)
}

// ptrEqual reports whether two *string values are equal, treating nil (null) as
// distinct from any non-nil value.
func ptrEqual(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// attributesFromMap builds an Attributes from a name→value map, sorted by name,
// without re-validating (callers pass values that came from already-validated
// operations). Internal use only.
func attributesFromMap(m map[string]string) Attributes {
	if len(m) == 0 {
		return Attributes{}
	}
	attrs := make([]Attribute, 0, len(m))
	for name, value := range m {
		attrs = append(attrs, Attribute{Name: name, Value: value})
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	return Attributes{attrs: attrs}
}

// updateWith returns the attributes after applying u: each change sets its
// attribute to NewValue, or removes it when NewValue is nil. Attributes not
// named by u are unchanged. (Ports AttributesImpl.updateWith.)
func (a Attributes) updateWith(u AttributesUpdate) Attributes {
	m := make(map[string]string, len(a.attrs))
	for _, at := range a.attrs {
		m[at.Name] = at.Value
	}
	for _, c := range u.updates {
		// The update's expected old value must match the current state
		// (ImmutableStateMap.updateWith with compatibility checking, as used by
		// the Composer). A mismatch is an illegal composition.
		cur, present := m[c.Name]
		if present {
			if c.OldValue == nil || *c.OldValue != cur {
				panic(composeError("updateWith: old value mismatch for attribute " + c.Name))
			}
		} else if c.OldValue != nil {
			panic(composeError("updateWith: attribute " + c.Name + " expected present but absent"))
		}
		if c.NewValue == nil {
			delete(m, c.Name)
		} else {
			m[c.Name] = *c.NewValue
		}
	}
	return attributesFromMap(m)
}

// composeWith returns the update equivalent to applying u then u2. For a key
// changed by both, the result is (u's old value, u2's new value); keys changed
// by only one side pass through. (Ports ImmutableUpdateMap.composeWith.)
func (u AttributesUpdate) composeWith(u2 AttributesUpdate) AttributesUpdate {
	type pair struct{ old, new *string }
	m := make(map[string]pair, len(u.updates)+len(u2.updates))
	for _, c := range u.updates {
		m[c.Name] = pair{old: c.OldValue, new: c.NewValue}
	}
	for _, c := range u2.updates {
		if p, ok := m[c.Name]; ok {
			// For a shared key, u2's expected old value must equal u1's new value
			// (ImmutableUpdateMap.composeWith); otherwise the composition is illegal.
			if !ptrEqual(p.new, c.OldValue) {
				panic(composeError("composeWith: old value mismatch for attribute " + c.Name))
			}
			m[c.Name] = pair{old: p.old, new: c.NewValue} // first's old, second's new
		} else {
			m[c.Name] = pair{old: c.OldValue, new: c.NewValue}
		}
	}
	changes := make([]AttributeChange, 0, len(m))
	for name, p := range m {
		changes = append(changes, AttributeChange{Name: name, OldValue: p.old, NewValue: p.new})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return AttributesUpdate{updates: changes}
}

// invert returns the update that reverses u, swapping each (old, new) pair.
func (u AttributesUpdate) invert() AttributesUpdate {
	inv := make([]AttributeChange, len(u.updates))
	for i, c := range u.updates {
		inv[i] = AttributeChange{Name: c.Name, OldValue: c.NewValue, NewValue: c.OldValue}
	}
	return AttributesUpdate{updates: inv}
}
