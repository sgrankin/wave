package doc_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/op"
)

func attrs(t *testing.T, m map[string]string) op.Attributes {
	t.Helper()
	a, err := op.NewAttributes(m)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// manifestDoc builds <conversation><blip id="b+1"><thread id="b+2"><blip id="b+3">
// </blip></thread></blip><blip id="b+4"></blip></conversation> as a DocInitialization.
func manifestDoc(t *testing.T) op.DocOp {
	t.Helper()
	none := attrs(t, nil)
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "conversation", Attributes: none},
		op.ElementStart{Type: "blip", Attributes: attrs(t, map[string]string{"id": "b+1"})},
		op.ElementStart{Type: "thread", Attributes: attrs(t, map[string]string{"id": "b+2"})},
		op.ElementStart{Type: "blip", Attributes: attrs(t, map[string]string{"id": "b+3"})},
		op.ElementEnd{}, // blip b+3
		op.ElementEnd{}, // thread
		op.ElementEnd{}, // blip b+1
		op.ElementStart{Type: "blip", Attributes: attrs(t, map[string]string{"id": "b+4"})},
		op.ElementEnd{}, // blip b+4
		op.ElementEnd{}, // conversation
	})
}

func TestReadStructure(t *testing.T) {
	root, err := doc.Root(manifestDoc(t))
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if root.Type != "conversation" {
		t.Fatalf("root = %q, want conversation", root.Type)
	}
	topBlips := root.ChildElements()
	if len(topBlips) != 2 {
		t.Fatalf("root thread has %d blips, want 2", len(topBlips))
	}
	if id, _ := topBlips[0].Attr("id"); id != "b+1" {
		t.Errorf("first blip id = %q, want b+1", id)
	}
	if id, _ := topBlips[1].Attr("id"); id != "b+4" {
		t.Errorf("second blip id = %q, want b+4", id)
	}
	// b+1 contains a thread with one blip b+3.
	threads := topBlips[0].ChildElements()
	if len(threads) != 1 || threads[0].Type != "thread" {
		t.Fatalf("b+1 children = %v, want one thread", threads)
	}
	if id, _ := threads[0].Attr("id"); id != "b+2" {
		t.Errorf("thread id = %q, want b+2", id)
	}
	replyBlips := threads[0].ChildElements()
	if len(replyBlips) != 1 {
		t.Fatalf("thread has %d blips, want 1", len(replyBlips))
	}
	if id, _ := replyBlips[0].Attr("id"); id != "b+3" {
		t.Errorf("reply blip id = %q, want b+3", id)
	}
}

func TestReadText(t *testing.T) {
	none := attrs(t, nil)
	d := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body", Attributes: none},
		op.ElementStart{Type: "line", Attributes: none},
		op.ElementEnd{},
		op.Characters{Text: "hello world"},
		op.ElementEnd{},
	})
	body, err := doc.Root(d)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got := body.Text(); got != "hello world" {
		t.Errorf("body text = %q, want %q", got, "hello world")
	}
	// The <line/> is an element child; "hello world" is a text child.
	if len(body.ChildElements()) != 1 {
		t.Errorf("body should have one element child (line)")
	}
}

func TestReadAnnotationsIgnored(t *testing.T) {
	none := attrs(t, nil)
	val := "true"
	bold, _ := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: "style/bold", NewValue: &val}})
	endBold, _ := op.NewAnnotationBoundaryMap([]string{"style/bold"}, nil)
	d := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "p", Attributes: none},
		op.AnnotationBoundary{Boundary: bold},
		op.Characters{Text: "hi"},
		op.AnnotationBoundary{Boundary: endBold},
		op.ElementEnd{},
	})
	p, err := doc.Root(d)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if p.Text() != "hi" {
		t.Errorf("text = %q, want hi (annotations ignored by structural read)", p.Text())
	}
}

func TestReadRejectsNonInitialization(t *testing.T) {
	if _, err := doc.Read(op.NewDocOp([]op.Component{op.Retain{Count: 3}})); err == nil {
		t.Error("Read should reject an op with retains (not a document initialization)")
	}
}

func TestReadRejectsUnbalanced(t *testing.T) {
	none := attrs(t, nil)
	// Missing element end.
	if _, err := doc.Read(op.NewDocOp([]op.Component{op.ElementStart{Type: "a", Attributes: none}})); err == nil {
		t.Error("Read should reject an unclosed element")
	}
}
