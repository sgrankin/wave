package conv

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// mirrors the TS invert(): per-component mapping including annotation swap.
func invert(d op.DocOp) op.DocOp {
	var out []op.Component
	for _, c := range d.Components() {
		switch c := c.(type) {
		case op.Retain:
			out = append(out, c)
		case op.Characters:
			out = append(out, op.DeleteCharacters{Text: c.Text})
		case op.ElementStart:
			out = append(out, op.DeleteElementStart{Type: c.Type, Attributes: c.Attributes})
		case op.ElementEnd:
			out = append(out, op.DeleteElementEnd{})
		case op.AnnotationBoundary:
			out = append(out, op.AnnotationBoundary{Boundary: invertBoundary(c.Boundary)})
		default:
			panic("unexpected component in content")
		}
	}
	return op.NewDocOp(out)
}

func invertBoundary(m op.AnnotationBoundaryMap) op.AnnotationBoundaryMap {
	// swap: changes' old/new flipped, ends preserved. We just reuse the data: for
	// a content DocInitialization the boundaries open a style at start and the
	// closing boundary ends it. The exact swap doesn't matter for the validator
	// (annotation boundaries are zero-width / out of scope). Rebuild equivalently.
	var ends []string
	ends = append(ends, m.EndKeys()...)
	var changes []op.AnnotationChange
	for _, ch := range m.Changes() {
		changes = append(changes, op.AnnotationChange{Key: ch.Key, OldValue: ch.NewValue, NewValue: ch.OldValue})
	}
	bm, err := op.NewAnnotationBoundaryMap(ends, changes)
	if err != nil {
		panic(err)
	}
	return bm
}

func clearOp(content op.DocOp) op.DocOp {
	var comps []op.Component
	comps = append(comps, invert(content).Components()...)
	comps = append(comps, InitialBlipContent().Components()...)
	return op.NewDocOp(comps)
}

// styled content: <body><line/>Hi<b>X</b>bye</body> with a style annotation over X.
func styledContent(t *testing.T) op.DocOp {
	t.Helper()
	none, _ := op.NewAttributes(nil)
	open, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: "style/fontWeight", OldValue: nil, NewValue: strp("bold")}})
	if err != nil {
		t.Fatal(err)
	}
	closeB, err := op.NewAnnotationBoundaryMap([]string{"style/fontWeight"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body", Attributes: none},
		op.ElementStart{Type: "line", Attributes: none},
		op.ElementEnd{}, // line
		op.Characters{Text: "Hi"},
		op.AnnotationBoundary{Boundary: open},
		op.Characters{Text: "X"},
		op.AnnotationBoundary{Boundary: closeB},
		op.Characters{Text: "bye"},
		op.ElementEnd{}, // body
	})
}

func strp(s string) *string { return &s }

func TestClearValidatesPlain(t *testing.T) {
	content := BlipContentWithText("hello world")
	clear := clearOp(content)
	if err := op.Validate(content, clear); err != nil {
		t.Fatalf("plain clear invalid: %v", err)
	}
	res, err := op.Apply(content, clear)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got, want := res, InitialBlipContent(); !got.Equal(want) {
		t.Fatalf("cleared content = %v, want empty body", got)
	}
}

func TestClearValidatesStyled(t *testing.T) {
	content := styledContent(t)
	clear := clearOp(content)
	if err := op.Validate(content, clear); err != nil {
		t.Fatalf("styled clear invalid: %v", err)
	}
	res, err := op.Apply(content, clear)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got, want := res, InitialBlipContent(); !got.Equal(want) {
		t.Fatalf("cleared styled content = %v, want empty body", got)
	}
}

func TestClearValidatesWithReplyAnchorAndImage(t *testing.T) {
	none, _ := op.NewAttributes(nil)
	replyAttrs, _ := op.NewAttributes(map[string]string{"id": "b+r"})
	imgAttrs, _ := op.NewAttributes(map[string]string{"attachment": "att1"})
	content := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body", Attributes: none},
		op.ElementStart{Type: "line", Attributes: none},
		op.ElementEnd{},
		op.Characters{Text: "ab"},
		op.ElementStart{Type: "reply", Attributes: replyAttrs},
		op.ElementEnd{},
		op.Characters{Text: "cd"},
		op.ElementStart{Type: "image", Attributes: imgAttrs},
		op.ElementEnd{},
		op.ElementEnd{}, // body
	})
	clear := clearOp(content)
	if err := op.Validate(content, clear); err != nil {
		t.Fatalf("reply+image clear invalid: %v", err)
	}
	if _, err := op.Apply(content, clear); err != nil {
		t.Fatalf("apply: %v", err)
	}
}
