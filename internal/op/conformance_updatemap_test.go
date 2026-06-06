package op_test

// Port of wave/.../model/document/operation/util/ImmutableUpdateMapTest.java.
//
// Java's ImmutableUpdateMap is the immutable, sorted attribute-name->(old,new)
// map behind AttributesUpdateImpl. Our equivalent is op.AttributesUpdate. The
// Java suite is a single method, testCheckUpdatesSorted, asserting that
// checkUpdatesSorted accepts valid lists and rejects duplicate-key lists.
//
// Mapping:
//   - The accept cases (empty / one / sorted-distinct lists) ->
//     TestConformanceUpdateMapAcceptsValid.
//   - The duplicate-key reject cases -> TestConformanceUpdateMapRejectsDuplicate.
//
// SKIPPED sub-cases: the "null element in the list" cases throw NPE in Java; Go's
// AttributeChange is a value type with no nullable element, so there is no
// equivalent. The "unsorted input" reject cases also do not map: op
// .NewAttributesUpdate *sorts* its input at construction rather than requiring
// pre-sorted input, so an out-of-order (but duplicate-free) list is accepted and
// canonicalized, which is the intended adapted behavior.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// ImmutableUpdateMapTest.testCheckUpdatesSorted (accept half): empty, singleton,
// and distinct-key lists are valid. Java's AttributeUpdate(key, old, new) maps to
// our AttributeChange; a Java null value maps to a nil *string.
func TestConformanceUpdateMapAcceptsValid(t *testing.T) {
	cases := [][]op.AttributeChange{
		{}, // empty
		{{Name: "a", OldValue: nil, NewValue: sp("1")}},
		{
			{Name: "aa", OldValue: sp("0"), NewValue: sp("1")},
			{Name: "ab", OldValue: nil, NewValue: nil},
		},
		{
			{Name: "a", OldValue: sp("0"), NewValue: nil},
			{Name: "b", OldValue: sp("p"), NewValue: sp("2")},
			{Name: "c", OldValue: sp("1"), NewValue: sp("1")},
		},
	}
	for i, changes := range cases {
		u, err := op.NewAttributesUpdate(changes)
		if err != nil {
			t.Errorf("case %d: valid update list rejected: %v", i, err)
			continue
		}
		if u.Len() != len(changes) {
			t.Errorf("case %d: Len = %d, want %d", i, u.Len(), len(changes))
		}
	}
}

// ImmutableUpdateMapTest.testCheckUpdatesSorted (reject half): a list containing
// two entries with the same key is rejected, regardless of the surrounding
// values. (Java additionally rejects unsorted-but-distinct lists; we sort those
// instead — see file header.)
func TestConformanceUpdateMapRejectsDuplicate(t *testing.T) {
	dupCases := [][]op.AttributeChange{
		{
			{Name: "asdfa", OldValue: sp("a"), NewValue: sp("1")},
			{Name: "asdfb", OldValue: sp("2"), NewValue: nil},
			{Name: "asdfb", OldValue: sp("2"), NewValue: sp("3")},
		},
		{
			{Name: "rar", OldValue: nil, NewValue: sp("1")},
			{Name: "rar", OldValue: sp("2"), NewValue: nil},
			{Name: "rbr", OldValue: sp("1"), NewValue: sp("2")},
		},
		{
			{Name: "a", OldValue: sp("1"), NewValue: sp("j")},
			{Name: "a", OldValue: sp("1"), NewValue: sp("r")},
		},
		{
			// Out-of-order but duplicate: still rejected because of the duplicate.
			{Name: "ard", OldValue: sp("1"), NewValue: sp("3")},
			{Name: "ard", OldValue: sp("1"), NewValue: sp("2")},
		},
	}
	for i, changes := range dupCases {
		if _, err := op.NewAttributesUpdate(changes); err == nil {
			t.Errorf("case %d: duplicate-key update list must be rejected", i)
		}
	}
}
