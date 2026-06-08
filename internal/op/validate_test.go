package op_test

import (
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

func textDoc(s string) op.DocOp {
	return op.NewDocOp([]op.Component{op.Characters{Text: s}})
}

// elemDoc builds the document <type attrs>text</type>.
func elemDoc(t *testing.T, typ string, attrs map[string]string, text string) op.DocOp {
	t.Helper()
	a, err := op.NewAttributes(attrs)
	if err != nil {
		t.Fatal(err)
	}
	comps := []op.Component{op.ElementStart{Type: typ, Attributes: a}}
	if text != "" {
		comps = append(comps, op.Characters{Text: text})
	}
	comps = append(comps, op.ElementEnd{})
	return op.NewDocOp(comps)
}

func TestValidateAcceptsValidOps(t *testing.T) {
	doc := textDoc("hello")
	cases := []struct {
		name string
		op   op.DocOp
	}{
		{"delete prefix, keep rest", op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "he"}, op.Retain{Count: 3}})},
		{"insert in the middle", op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 4}})},
		{"retain all (no-op)", op.NewDocOp([]op.Component{op.Retain{Count: 5}})},
		{"delete everything", op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "hello"}})},
		{"append at end", op.NewDocOp([]op.Component{op.Retain{Count: 5}, op.Characters{Text: "!"}})},
		{"insert balanced element", op.NewDocOp([]op.Component{op.Retain{Count: 5}, op.ElementStart{Type: "br"}, op.ElementEnd{}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := op.Validate(doc, tc.op); err != nil {
				t.Errorf("Validate rejected a valid op: %v", err)
			}
		})
	}
}

// TestValidateRejectsContentMismatch is the core anti-corruption guarantee: deletes
// and attribute replaces whose expected content disagrees with the document are
// rejected (Compose would otherwise apply them by length and corrupt the document).
func TestValidateRejectsContentMismatch(t *testing.T) {
	withAttr := func(m map[string]string) op.Attributes {
		a, err := op.NewAttributes(m)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	elemK1 := elemDoc(t, "b", map[string]string{"k": "1"}, "x") // <b k="1">x</b>: items [start, 'x', end]

	cases := []struct {
		name    string
		doc     op.DocOp
		op      op.DocOp
		wantSub string
	}{
		{
			"deleteCharacters wrong text",
			textDoc("hello"),
			op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "xy"}, op.Retain{Count: 3}}),
			"content mismatch",
		},
		{
			"deleteElementStart wrong type",
			elemK1,
			op.NewDocOp([]op.Component{op.DeleteElementStart{Type: "i", Attributes: withAttr(map[string]string{"k": "1"})}, op.Retain{Count: 2}}),
			"type mismatch",
		},
		{
			"deleteElementStart wrong attrs",
			elemK1,
			op.NewDocOp([]op.Component{op.DeleteElementStart{Type: "b", Attributes: withAttr(map[string]string{"k": "2"})}, op.Retain{Count: 2}}),
			"attributes differ",
		},
		{
			"replaceAttributes wrong old attrs",
			elemK1,
			op.NewDocOp([]op.Component{op.ReplaceAttributes{OldAttributes: withAttr(map[string]string{"k": "9"}), NewAttributes: withAttr(map[string]string{"k": "3"})}, op.Retain{Count: 2}}),
			"old attributes differ",
		},
		{
			"updateAttributes wrong old value",
			elemK1,
			op.NewDocOp([]op.Component{op.UpdateAttributes{Update: mustUpdate(t, "k", strPtr("9"), strPtr("3"))}, op.Retain{Count: 2}}),
			"old value",
		},
		{
			// Faithful to Java: deleteElementEnd's well-formedness check (no matching
			// open deletion) precedes the document-content check (no element end),
			// so an unmatched deleteElementEnd is reported as ill-formed first.
			"deleteElementEnd at a character",
			textDoc("hi"),
			op.NewDocOp([]op.Component{op.DeleteElementEnd{}, op.Retain{Count: 1}}),
			"no matching open deletion",
		},
		{
			"deleteElementStart at a character",
			textDoc("hi"),
			op.NewDocOp([]op.Component{op.DeleteElementStart{Type: "b"}, op.Retain{Count: 1}}),
			"no element start",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := op.Validate(tc.doc, tc.op)
			if err == nil {
				t.Fatalf("Validate accepted a content-mismatched op (would corrupt the document)")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidateRejectsMalformed(t *testing.T) {
	cases := []struct {
		name    string
		doc     op.DocOp
		op      op.DocOp
		wantSub string
	}{
		{"retain past end", textDoc("hi"), op.NewDocOp([]op.Component{op.Retain{Count: 5}}), "past end"},
		{"delete past end", textDoc("hi"), op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "hix"}}), "past end"},
		{"does not cover document", textDoc("hello"), op.NewDocOp([]op.Component{op.Retain{Count: 2}}), "cover the whole document"},
		{"unclosed inserted element", op.EmptyDoc(), op.NewDocOp([]op.Component{op.ElementStart{Type: "b"}}), "unclosed"},
		{"element end with no start", op.EmptyDoc(), op.NewDocOp([]op.Component{op.ElementEnd{}}), "no matching inserted start"},
		{"non-positive retain", textDoc("hi"), op.NewDocOp([]op.Component{op.Retain{Count: 0}, op.Retain{Count: 2}}), "must be positive"},
		{"empty characters", op.EmptyDoc(), op.NewDocOp([]op.Component{op.Characters{Text: ""}}), "empty characters"},
		// Faithful to Java: the empty string is not a valid XML name (Utf16Util.isXmlName
		// returns false on empty), so an empty element type is rejected as a name violation.
		{"empty element type", op.EmptyDoc(), op.NewDocOp([]op.Component{op.ElementStart{Type: ""}, op.ElementEnd{}}), "not a valid XML name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := op.Validate(tc.doc, tc.op)
			if err == nil {
				t.Fatalf("Validate accepted a malformed op")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestValidateAgreesWithApply: any op Validate accepts must Compose cleanly (and any
// it rejects for content reasons must NOT silently produce a corrupt result). Spot-
// check the positive direction against a few docs.
func TestValidateAcceptedOpsCompose(t *testing.T) {
	doc := textDoc("abcde")
	ops := []op.DocOp{
		op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "ab"}, op.Retain{Count: 3}}),
		op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "ZZ"}, op.Retain{Count: 3}}),
		op.NewDocOp([]op.Component{op.Retain{Count: 5}}),
	}
	for i, o := range ops {
		if err := op.Validate(doc, o); err != nil {
			t.Fatalf("op %d rejected: %v", i, err)
		}
		if _, err := op.Compose(doc, o); err != nil {
			t.Errorf("op %d validated but Compose failed: %v", i, err)
		}
	}
}
