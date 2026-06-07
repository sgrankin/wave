package conv_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/op"
)

// bodyWithText returns <body><line/>{text}</body> for anchor tests.
func bodyWithText(t *testing.T, text string) op.DocOp {
	t.Helper()
	withText, err := op.Apply(conv.InitialBlipContent(),
		op.NewDocOp([]op.Component{op.Retain{Count: 3}, op.Characters{Text: text}, op.Retain{Count: 1}}))
	if err != nil {
		t.Fatal(err)
	}
	return withText
}

// TestInsertAndReadReplyAnchor: inserting an anchor at a caret offset and reading
// it back yields the thread id at that offset.
func TestInsertAndReadReplyAnchor(t *testing.T) {
	body := bodyWithText(t, "hello") // items: body,line/,h,e,l,l,o,/body — offset 5 = after "he"
	anchorOp, err := conv.InsertReplyAnchor(body, "b+r1", 5)
	if err != nil {
		t.Fatal(err)
	}
	withAnchor, err := op.Apply(body, anchorOp)
	if err != nil {
		t.Fatal(err)
	}
	anchors := conv.ReadReplyAnchors(withAnchor)
	if len(anchors) != 1 || anchors[0].ThreadID != "b+r1" || anchors[0].Offset != 5 {
		t.Errorf("anchors = %+v, want [{b+r1 5}]", anchors)
	}
}

// TestInsertReplyAnchorRange: offsets outside [0, len] are rejected; len (append
// at the very end) is allowed.
func TestInsertReplyAnchorRange(t *testing.T) {
	body := conv.InitialBlipContent()
	n := body.DocumentLength()
	if _, err := conv.InsertReplyAnchor(body, "b+r", -1); err == nil {
		t.Error("offset -1 should error")
	}
	if _, err := conv.InsertReplyAnchor(body, "b+r", n+1); err == nil {
		t.Error("offset > len should error")
	}
	end, err := conv.InsertReplyAnchor(body, "b+r", n)
	if err != nil {
		t.Fatalf("offset == len should be allowed: %v", err)
	}
	// And the op must actually apply (no degenerate zero-count retain).
	out, err := op.Apply(body, end)
	if err != nil {
		t.Fatalf("apply anchor at end: %v", err)
	}
	if a := conv.ReadReplyAnchors(out); len(a) != 1 || a[0].Offset != n {
		t.Errorf("anchors after end-insert = %+v, want one at offset %d", a, n)
	}
}

// TestReadReplyAnchorsOrder: anchors are returned in document order.
func TestReadReplyAnchorsOrder(t *testing.T) {
	body := bodyWithText(t, "abcdef")
	op1, err := conv.InsertReplyAnchor(body, "b+early", 4)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := op.Apply(body, op1)
	if err != nil {
		t.Fatal(err)
	}
	op2, err := conv.InsertReplyAnchor(b1, "b+late", b1.DocumentLength()-1) // just before </body>
	if err != nil {
		t.Fatal(err)
	}
	b2, err := op.Apply(b1, op2)
	if err != nil {
		t.Fatal(err)
	}
	anchors := conv.ReadReplyAnchors(b2)
	if len(anchors) != 2 || anchors[0].ThreadID != "b+early" || anchors[1].ThreadID != "b+late" {
		t.Errorf("anchors = %+v, want [b+early, b+late] in document order", anchors)
	}
}

// TestReplyToBlipInlineRoundTrip: an inline reply marks the manifest thread
// inline=true (the manifest half; the body anchor is the InsertReplyAnchor half).
func TestReplyToBlipInlineRoundTrip(t *testing.T) {
	m, err := op.Apply(conv.EmptyManifest(), conv.AppendBlipToRootThread(conv.EmptyManifest(), "b+root"))
	if err != nil {
		t.Fatal(err)
	}
	replyOp, err := conv.ReplyToBlip(m, "b+root", "b+r1", true)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := op.Apply(m, replyOp)
	if err != nil {
		t.Fatal(err)
	}
	man, err := conv.ReadManifest(m2)
	if err != nil {
		t.Fatal(err)
	}
	if len(man.RootThread.Blips) != 1 {
		t.Fatalf("root blips = %d, want 1", len(man.RootThread.Blips))
	}
	threads := man.RootThread.Blips[0].Threads
	if len(threads) != 1 || !threads[0].Inline || threads[0].ID != "b+r1" {
		t.Errorf("reply thread = %+v, want one inline thread id b+r1", threads)
	}

	// A non-inline reply leaves the thread non-inline.
	replyOp2, err := conv.ReplyToBlip(m2, "b+root", "b+r2", false)
	if err != nil {
		t.Fatal(err)
	}
	m3, err := op.Apply(m2, replyOp2)
	if err != nil {
		t.Fatal(err)
	}
	man3, err := conv.ReadManifest(m3)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range man3.RootThread.Blips[0].Threads {
		if th.ID == "b+r2" && th.Inline {
			t.Error("b+r2 should not be inline")
		}
	}
}
