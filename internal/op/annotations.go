package op

import (
	"fmt"
	"sort"
	"strings"
)

// AnnotationChange records, for one annotation key, the value change at a
// boundary point. A nil OldValue means no expected current value; a nil
// NewValue means the annotation is cleared.
type AnnotationChange struct {
	Key      string
	OldValue *string
	NewValue *string
}

// AnnotationBoundaryMap describes the annotation state change at a point in the
// document: a set of keys whose ranges end here, and a set of keys whose values
// change here. It is immutable; the zero value is the empty boundary.
//
// Invariants (enforced at construction): end keys and change keys are each
// strictly sorted and unique; a key may not appear in both; keys are valid
// UTF-8 and contain neither '?' nor '@'.
type AnnotationBoundaryMap struct {
	endKeys []string
	changes []AnnotationChange
}

// NewAnnotationBoundaryMap builds and validates an AnnotationBoundaryMap. The
// inputs need not be pre-sorted; they are sorted and checked here.
func NewAnnotationBoundaryMap(endKeys []string, changes []AnnotationChange) (AnnotationBoundaryMap, error) {
	ends := append([]string(nil), endKeys...)
	sort.Strings(ends)
	for i, k := range ends {
		if err := validateAnnotationKey(k); err != nil {
			return AnnotationBoundaryMap{}, err
		}
		if i > 0 && ends[i-1] == k {
			return AnnotationBoundaryMap{}, fmt.Errorf("op: duplicate end key %q in annotation boundary", k)
		}
	}

	chg := append([]AnnotationChange(nil), changes...)
	sort.Slice(chg, func(i, j int) bool { return chg[i].Key < chg[j].Key })
	for i, c := range chg {
		if err := validateAnnotationKey(c.Key); err != nil {
			return AnnotationBoundaryMap{}, err
		}
		if i > 0 && chg[i-1].Key == c.Key {
			return AnnotationBoundaryMap{}, fmt.Errorf("op: duplicate change key %q in annotation boundary", c.Key)
		}
		if c.OldValue != nil && !isValidUTF16Doc(*c.OldValue) {
			return AnnotationBoundaryMap{}, fmt.Errorf("op: annotation %q old value is not valid UTF-16 document text", c.Key)
		}
		if c.NewValue != nil && !isValidUTF16Doc(*c.NewValue) {
			return AnnotationBoundaryMap{}, fmt.Errorf("op: annotation %q new value is not valid UTF-16 document text", c.Key)
		}
	}

	// A key must not appear in both end and change sets (both are sorted).
	for i, j := 0, 0; i < len(ends) && j < len(chg); {
		switch {
		case ends[i] == chg[j].Key:
			return AnnotationBoundaryMap{}, fmt.Errorf("op: key %q in both end and change sets", ends[i])
		case ends[i] < chg[j].Key:
			i++
		default:
			j++
		}
	}

	return AnnotationBoundaryMap{endKeys: ends, changes: chg}, nil
}

// validateAnnotationKey enforces the key character constraints (Java
// DocOpAutomaton.validateAnnotationKey): non-empty, free of '?' and '@', and
// valid UTF-16 document text (valid UTF-8 with no surrogate/noncharacter code
// points — isValidUtf16, which is strictly stronger than utf8.ValidString).
// Annotation keys are NOT required to be XML names (unlike attribute names);
// keys such as "style/bold" are valid.
func validateAnnotationKey(k string) error {
	if k == "" {
		return fmt.Errorf("op: empty annotation key")
	}
	if strings.ContainsAny(k, "?@") {
		return fmt.Errorf("op: annotation key %q contains '?' or '@'", k)
	}
	if !isValidUTF16Doc(k) {
		return fmt.Errorf("op: annotation key %q is not valid UTF-16 document text", k)
	}
	return nil
}

// Empty reports whether the boundary changes nothing.
func (m AnnotationBoundaryMap) Empty() bool {
	return len(m.endKeys) == 0 && len(m.changes) == 0
}

// EndKeys returns the ending keys in sorted order (a copy).
func (m AnnotationBoundaryMap) EndKeys() []string {
	return append([]string(nil), m.endKeys...)
}

// Changes returns the value changes in key order (a copy).
func (m AnnotationBoundaryMap) Changes() []AnnotationChange {
	return append([]AnnotationChange(nil), m.changes...)
}

// swap returns the boundary with each change's old and new values exchanged,
// used to invert an operation. End keys are unaffected.
func (m AnnotationBoundaryMap) swap() AnnotationBoundaryMap {
	changes := make([]AnnotationChange, len(m.changes))
	for i, c := range m.changes {
		changes[i] = AnnotationChange{Key: c.Key, OldValue: c.NewValue, NewValue: c.OldValue}
	}
	return AnnotationBoundaryMap{endKeys: m.endKeys, changes: changes}
}

// Equal reports whether m and other describe the same boundary.
func (m AnnotationBoundaryMap) Equal(other AnnotationBoundaryMap) bool {
	if len(m.endKeys) != len(other.endKeys) || len(m.changes) != len(other.changes) {
		return false
	}
	for i := range m.endKeys {
		if m.endKeys[i] != other.endKeys[i] {
			return false
		}
	}
	for i := range m.changes {
		a, b := m.changes[i], other.changes[i]
		if a.Key != b.Key || !ptrEqual(a.OldValue, b.OldValue) || !ptrEqual(a.NewValue, b.NewValue) {
			return false
		}
	}
	return true
}
