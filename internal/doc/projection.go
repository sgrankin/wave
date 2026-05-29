package doc

import (
	"strings"

	"github.com/sgrankin/wave/internal/op"
)

// PlainText extracts a document's character content as plain text. Each <line>
// element (Wave's line marker) becomes a newline so words on separate lines do
// not merge; all other structure is flattened. This is the text fed to the
// search index. A non-initialization document errors (via Read).
func PlainText(d op.DocOp) (string, error) {
	roots, err := Read(d)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	nlPending := false
	var walk func(nodes []Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			switch v := n.(type) {
			case Text:
				if nlPending && b.Len() > 0 {
					b.WriteByte('\n')
				}
				nlPending = false
				b.WriteString(v.Data)
			case *Element:
				if v.Type == lineTag {
					nlPending = true
				}
				walk(v.Children)
			}
		}
	}
	walk(roots)
	return b.String(), nil
}

// lineTag is Wave's line-break element.
const lineTag = "line"

// Title returns the document's title: the first non-empty line of its plain
// text, trimmed. Empty if the document has no text.
func Title(d op.DocOp) (string, error) {
	text, err := PlainText(d)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t, nil
		}
	}
	return "", nil
}

// Snippet returns the document's plain text with all runs of whitespace
// collapsed to single spaces, truncated to maxRunes runes (an ellipsis is
// appended when truncated). maxRunes <= 0 returns the full collapsed text.
// Length is counted in runes, matching the model's rune-based character count.
func Snippet(d op.DocOp, maxRunes int) (string, error) {
	text, err := PlainText(d)
	if err != nil {
		return "", err
	}
	collapsed := strings.Join(strings.Fields(text), " ")
	if maxRunes <= 0 {
		return collapsed, nil
	}
	r := []rune(collapsed)
	if len(r) <= maxRunes {
		return collapsed, nil
	}
	return string(r[:maxRunes]) + "…", nil
}
