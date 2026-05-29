package doc_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/op"
)

// blip builds a <body> with the given lines, each preceded by a <line/> marker.
func blip(lines ...string) op.DocOp {
	none, _ := op.NewAttributes(nil)
	comps := []op.Component{op.ElementStart{Type: "body", Attributes: none}}
	for _, line := range lines {
		comps = append(comps, op.ElementStart{Type: "line", Attributes: none}, op.ElementEnd{})
		if line != "" {
			comps = append(comps, op.Characters{Text: line})
		}
	}
	comps = append(comps, op.ElementEnd{}) // body
	return op.NewDocOp(comps)
}

func TestPlainText(t *testing.T) {
	got, err := doc.PlainText(blip("Hello", "World"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello\nWorld" {
		t.Errorf("PlainText = %q, want %q", got, "Hello\nWorld")
	}
}

func TestPlainTextFlat(t *testing.T) {
	// A flat character document (no body/line structure) is just its text.
	got, err := doc.PlainText(op.NewDocOp([]op.Component{op.Characters{Text: "hi there"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi there" {
		t.Errorf("PlainText = %q, want %q", got, "hi there")
	}
}

func TestPlainTextEmpty(t *testing.T) {
	// <body><line/></body> — a fresh blip — has no text.
	got, err := doc.PlainText(blip(""))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("PlainText = %q, want empty", got)
	}
}

func TestTitle(t *testing.T) {
	got, err := doc.Title(blip("  My Title  ", "body text"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "My Title" {
		t.Errorf("Title = %q, want %q", got, "My Title")
	}
	// Leading empty lines are skipped.
	got, _ = doc.Title(blip("", "Second line is the title"))
	if got != "Second line is the title" {
		t.Errorf("Title with leading blank = %q", got)
	}
}

func TestSnippet(t *testing.T) {
	// Whitespace (including the line break) collapses to single spaces.
	got, err := doc.Snippet(blip("Hello", "World"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello World" {
		t.Errorf("Snippet(full) = %q, want %q", got, "Hello World")
	}
	// Truncation appends an ellipsis, counted in runes.
	got, _ = doc.Snippet(blip("Hello World"), 8)
	if got != "Hello Wo…" {
		t.Errorf("Snippet(8) = %q, want %q", got, "Hello Wo…")
	}
	// Rune-aware truncation of multi-byte content.
	got, _ = doc.Snippet(op.NewDocOp([]op.Component{op.Characters{Text: "héllo wörld"}}), 5)
	if got != "héllo…" {
		t.Errorf("Snippet multibyte = %q, want %q", got, "héllo…")
	}
}

func TestProjectionErrorsOnNonInitialization(t *testing.T) {
	bad := op.NewDocOp([]op.Component{op.Retain{Count: 1}}) // a retain is not insertion-only
	if _, err := doc.PlainText(bad); err == nil {
		t.Error("PlainText should error on a non-initialization doc")
	}
}
