package op

import "testing"

func TestInputOutputItems(t *testing.T) {
	tests := []struct {
		name       string
		c          Component
		wantInput  int
		wantOutput int
	}{
		{"retain", Retain{Count: 5}, 5, 5},
		{"characters", Characters{Text: "abc"}, 0, 3},
		{"characters astral rune counts once", Characters{Text: "a😀b"}, 0, 3}, // rune count, not UTF-16 units
		{"elementStart", ElementStart{Type: "p"}, 0, 1},
		{"elementEnd", ElementEnd{}, 0, 1},
		{"deleteCharacters", DeleteCharacters{Text: "abcd"}, 4, 0},
		{"deleteElementStart", DeleteElementStart{Type: "p"}, 1, 0},
		{"deleteElementEnd", DeleteElementEnd{}, 1, 0},
		{"replaceAttributes", ReplaceAttributes{}, 1, 1},
		{"updateAttributes", UpdateAttributes{}, 1, 1},
		{"annotationBoundary", AnnotationBoundary{}, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inputItems(tc.c); got != tc.wantInput {
				t.Errorf("inputItems = %d, want %d", got, tc.wantInput)
			}
			if got := outputItems(tc.c); got != tc.wantOutput {
				t.Errorf("outputItems = %d, want %d", got, tc.wantOutput)
			}
		})
	}
}

func TestDocOpLengths(t *testing.T) {
	// retain 2, insert "hi" (2), delete element start (1 in), insert element end (1 out)
	d := NewDocOp([]Component{
		Retain{Count: 2},
		Characters{Text: "hi"},
		DeleteElementStart{Type: "x"},
		ElementEnd{},
	})
	if got := d.inputLength(); got != 3 { // retain 2 + delete-start 1
		t.Errorf("inputLength = %d, want 3", got)
	}
	if got := d.outputLength(); got != 5 { // retain 2 + chars 2 + elementEnd 1
		t.Errorf("outputLength = %d, want 5", got)
	}
}
