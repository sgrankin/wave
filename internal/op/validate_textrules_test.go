package op_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// These tests pin the text/XML-name well-formedness rules (firstBadTextRune,
// isXMLName) exercised through the exported Validate. They lock in both the Java-
// faithful rejections (noncharacters) and the deliberate UTF-8 divergence
// (supplementary code points are valid single items).

func TestValidateTextRules(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		accept bool
	}{
		{"plain ascii", "hello", true},
		{"accented BMP", "café", true},
		{"supplementary emoji (accepted in UTF-8 model)", "ok😀", true},
		{"noncharacter U+FFFE", "a￾", false},
		{"noncharacter U+FFFF", "a￿", false},
		{"noncharacter U+FDD0", "a﷐", false},
		{"noncharacter U+FDEF", "a﷯", false},
		{"genuine replacement char U+FFFD (allowed)", "a�", true},
		{"plane-1 noncharacter U+1FFFE", "a\U0001fffe", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Insert text into an empty document; only the text well-formedness rule
			// can reject it.
			err := op.Validate(op.EmptyDoc(), op.NewDocOp([]op.Component{op.Characters{Text: tc.text}}))
			if tc.accept && err != nil {
				t.Errorf("Validate rejected valid text %q: %v", tc.text, err)
			}
			if !tc.accept && err == nil {
				t.Errorf("Validate accepted invalid text %q", tc.text)
			}
		})
	}
}

func TestValidateXMLNameRules(t *testing.T) {
	cases := []struct {
		name   string
		typ    string
		accept bool
	}{
		{"simple", "p", true},
		{"colon namespace", "ns:el", true},
		{"underscore start", "_x", true},
		{"hyphen and dot and digit (not first)", "a-b.c1", true},
		{"empty", "", false},
		{"digit first", "1a", false},
		{"hyphen first", "-a", false},
		{"space inside", "a b", false},
		{"angle bracket", "<", false},
		{"dot first", ".a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := op.Validate(op.EmptyDoc(), op.NewDocOp([]op.Component{
				op.ElementStart{Type: tc.typ}, op.ElementEnd{},
			}))
			if tc.accept && err != nil {
				t.Errorf("Validate rejected valid XML-name element %q: %v", tc.typ, err)
			}
			if !tc.accept && err == nil {
				t.Errorf("Validate accepted invalid XML-name element %q", tc.typ)
			}
		})
	}
}
