package op_test

import (
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// These tests pin the attribute/annotation well-formedness invariants that Java's
// DocOpAutomaton enforces in checkAttributesWellFormed /
// checkAttributesUpdateWellFormed / validateAnnotationKey / validateAnnotationValue
// (ILL_FORMED on a non-XML-name attribute name or a noncharacter/surrogate value).
//
// The Go port enforces them at construction (NewAttributes / NewAttributesUpdate /
// NewAnnotationBoundaryMap), so the bad value can never be built — and therefore can
// never reach op.Validate. Previously these constructors used only utf8.ValidString,
// which (a) accepted any UTF-8 attribute NAME including non-XML names like "a b" and
// "1x", and (b) accepted noncharacter VALUES (U+FFFE/U+FFFF/U+FDD0..U+FDEF) that
// Java's isValidUtf16 rejects.

// TestNewAttributesRejectsNonXMLName covers findings #1/#4: attribute NAMES must be
// valid XML names, not merely valid UTF-8.
func TestNewAttributesRejectsNonXMLName(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		accept bool
	}{
		{"simple", "a", true},
		{"colon namespace", "ns:el", true},
		{"underscore start", "_x", true},
		{"hyphen/dot/digit not first", "a-b.c1", true},
		{"space inside", "a b", false},
		{"digit first", "1x", false},
		{"digit-first word", "1abc", false},
		{"hyphen first", "-a", false},
		{"angle bracket", "a<b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := op.NewAttributes(map[string]string{tc.key: "v"})
			if tc.accept && err != nil {
				t.Errorf("NewAttributes rejected valid XML-name key %q: %v", tc.key, err)
			}
			if !tc.accept && err == nil {
				t.Errorf("NewAttributes accepted non-XML-name key %q (Java rejects as ILL_FORMED)", tc.key)
			}
			if !tc.accept && err != nil && !strings.Contains(err.Error(), "XML name") {
				t.Errorf("error for key %q = %q, want it to mention an XML-name violation", tc.key, err.Error())
			}
		})
	}
}

// TestNewAttributesUpdateRejectsNonXMLName covers findings #1/#4 on the update path:
// an update CHANGE key must be a valid XML name.
func TestNewAttributesUpdateRejectsNonXMLName(t *testing.T) {
	good, badV := "v", "w"
	cases := []struct {
		name   string
		key    string
		accept bool
	}{
		{"simple", "a", true},
		{"space inside", "a b", false},
		{"digit first", "1x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := op.NewAttributesUpdate([]op.AttributeChange{
				{Name: tc.key, OldValue: &good, NewValue: &badV},
			})
			if tc.accept && err != nil {
				t.Errorf("NewAttributesUpdate rejected valid XML-name key %q: %v", tc.key, err)
			}
			if !tc.accept && err == nil {
				t.Errorf("NewAttributesUpdate accepted non-XML-name key %q (Java rejects)", tc.key)
			}
		})
	}
}

// TestNewAttributesRejectsNoncharacterValue covers findings #2/#5: attribute VALUES
// must be valid UTF-16 document text — noncharacters are rejected, not merely
// checked with utf8.ValidString.
func TestNewAttributesRejectsNoncharacterValue(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		accept bool
	}{
		{"plain ascii", "v", true},
		{"accented BMP", "café", true},
		{"genuine U+FFFD allowed", "v�", true},
		{"noncharacter U+FFFE", "v￾", false},
		{"noncharacter U+FFFF", "v￿", false},
		{"noncharacter U+FDD0", "﷐", false},
		{"noncharacter U+FDEF", "﷯", false},
		{"plane-1 noncharacter U+1FFFE", "v\U0001FFFE", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := op.NewAttributes(map[string]string{"k": tc.value})
			if tc.accept && err != nil {
				t.Errorf("NewAttributes rejected valid value %q: %v", tc.value, err)
			}
			if !tc.accept && err == nil {
				t.Errorf("NewAttributes accepted noncharacter value %q (Java isValidUtf16 rejects)", tc.value)
			}
		})
	}
}

// TestNewAttributesUpdateRejectsNoncharacterValue covers findings #2/#5 on the update
// path: both OldValue and NewValue must be valid UTF-16 document text when present.
func TestNewAttributesUpdateRejectsNoncharacterValue(t *testing.T) {
	bad := "￾"
	good := "ok"
	if _, err := op.NewAttributesUpdate([]op.AttributeChange{
		{Name: "a", OldValue: &bad, NewValue: &good},
	}); err == nil {
		t.Error("NewAttributesUpdate accepted a noncharacter OLD value (Java isValidUtf16 rejects)")
	}
	if _, err := op.NewAttributesUpdate([]op.AttributeChange{
		{Name: "a", OldValue: &good, NewValue: &bad},
	}); err == nil {
		t.Error("NewAttributesUpdate accepted a noncharacter NEW value (Java isValidUtf16 rejects)")
	}
	// A nil value is fine (means absent / removed) and must NOT be rejected.
	if _, err := op.NewAttributesUpdate([]op.AttributeChange{
		{Name: "a", OldValue: nil, NewValue: &good},
	}); err != nil {
		t.Errorf("NewAttributesUpdate rejected a valid update with nil OldValue: %v", err)
	}
}

// TestNewAnnotationBoundaryRejectsNoncharacterKeyValue covers finding #5 on the
// annotation path: keys and values must be valid UTF-16 document text. Annotation
// keys are NOT XML names — "style/bold" (with '/') must still be accepted.
func TestNewAnnotationBoundaryRejectsNoncharacterKeyValue(t *testing.T) {
	good := "true"

	// Valid: a slash-bearing key (not an XML name) with a plain value.
	if _, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{
		{Key: "style/bold", NewValue: &good},
	}); err != nil {
		t.Errorf("NewAnnotationBoundaryMap rejected valid non-XML key style/bold: %v", err)
	}

	// Noncharacter in the KEY is rejected.
	if _, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{
		{Key: "k￾", NewValue: &good},
	}); err == nil {
		t.Error("NewAnnotationBoundaryMap accepted a noncharacter annotation KEY (Java isValidUtf16 rejects)")
	}

	// Noncharacter in the change VALUE (old and new) is rejected.
	bad := "﷐"
	if _, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{
		{Key: "k", NewValue: &bad},
	}); err == nil {
		t.Error("NewAnnotationBoundaryMap accepted a noncharacter annotation NEW value (Java isValidUtf16 rejects)")
	}
	if _, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{
		{Key: "k", OldValue: &bad, NewValue: &good},
	}); err == nil {
		t.Error("NewAnnotationBoundaryMap accepted a noncharacter annotation OLD value (Java isValidUtf16 rejects)")
	}
}

// TestValidateAcceptsAstralAttributeAndAnnotationValues pins finding #3's deliberate
// divergence at the value level: a supplementary (astral) code point is a valid
// single rune in the Go UTF-8 model, so it is accepted in attribute and annotation
// values (Java's UTF-16 firstSurrogate gate does not apply to isValidUtf16 values —
// only noncharacters/surrogate code points are rejected, and an emoji is neither).
func TestValidateAcceptsAstralAttributeAndAnnotationValues(t *testing.T) {
	if _, err := op.NewAttributes(map[string]string{"k": "ok\U0001F600"}); err != nil {
		t.Errorf("NewAttributes rejected a valid supplementary attribute value: %v", err)
	}
	v := "v\U0001F600"
	if _, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{
		{Key: "k", NewValue: &v},
	}); err != nil {
		t.Errorf("NewAnnotationBoundaryMap rejected a valid supplementary annotation value: %v", err)
	}
}
