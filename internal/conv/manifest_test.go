package conv_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/conv"
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

func TestEmptyManifestRoundTrip(t *testing.T) {
	m, err := conv.ReadManifest(conv.EmptyManifest())
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(m.RootThread.Blips) != 0 {
		t.Errorf("empty manifest root thread has %d blips, want 0", len(m.RootThread.Blips))
	}
	if m.AnchorWavelet != "" || m.AnchorBlip != "" {
		t.Errorf("empty manifest should not be anchored, got %q/%q", m.AnchorWavelet, m.AnchorBlip)
	}
}

func TestAppendBlipsRoundTrip(t *testing.T) {
	manifest := conv.EmptyManifest()
	for _, id := range []string{"b+1", "b+2", "b+3"} {
		next, err := op.Apply(manifest, conv.AppendBlipToRootThread(manifest, id))
		if err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
		manifest = next
	}
	m, err := conv.ReadManifest(manifest)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	got := make([]string, len(m.RootThread.Blips))
	for i, b := range m.RootThread.Blips {
		got[i] = b.ID
	}
	want := []string{"b+1", "b+2", "b+3"}
	if len(got) != len(want) {
		t.Fatalf("root thread blips = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("blip[%d] = %q, want %q (append order)", i, got[i], want[i])
		}
	}
}

// Appending must skip past a nested reply-thread subtree under the last root
// blip and land the new blip as a root-thread sibling (not inside the thread).
func TestAppendBlipPastNestedThread(t *testing.T) {
	none := attrs(t, nil)
	manifest := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "conversation", Attributes: none},
		op.ElementStart{Type: "blip", Attributes: attrs(t, map[string]string{"id": "b+1"})},
		op.ElementStart{Type: "thread", Attributes: attrs(t, map[string]string{"id": "b+2"})},
		op.ElementStart{Type: "blip", Attributes: attrs(t, map[string]string{"id": "b+3"})},
		op.ElementEnd{}, // b+3
		op.ElementEnd{}, // thread
		op.ElementEnd{}, // b+1
		op.ElementEnd{}, // conversation
	})
	next, err := op.Apply(manifest, conv.AppendBlipToRootThread(manifest, "b+new"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	m, err := conv.ReadManifest(next)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(m.RootThread.Blips) != 2 {
		t.Fatalf("root thread blips = %d, want 2", len(m.RootThread.Blips))
	}
	if m.RootThread.Blips[0].ID != "b+1" || m.RootThread.Blips[1].ID != "b+new" {
		t.Errorf("root thread = [%s, %s], want [b+1, b+new]",
			m.RootThread.Blips[0].ID, m.RootThread.Blips[1].ID)
	}
	// The pre-existing nested reply thread under b+1 must be intact.
	b1 := m.RootThread.Blips[0]
	if len(b1.Threads) != 1 || b1.Threads[0].ID != "b+2" ||
		len(b1.Threads[0].Blips) != 1 || b1.Threads[0].Blips[0].ID != "b+3" {
		t.Errorf("b+1's nested thread was disturbed: %+v", b1.Threads)
	}
	// The new blip is a leaf at root level (no threads).
	if len(m.RootThread.Blips[1].Threads) != 0 {
		t.Errorf("appended blip should have no threads, got %+v", m.RootThread.Blips[1].Threads)
	}
}

func TestReadReplyThread(t *testing.T) {
	none := attrs(t, nil)
	manifest := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "conversation", Attributes: none},
		op.ElementStart{Type: "blip", Attributes: attrs(t, map[string]string{"id": "b+1"})},
		op.ElementStart{Type: "thread", Attributes: attrs(t, map[string]string{"id": "b+2", "inline": "true"})},
		op.ElementStart{Type: "blip", Attributes: attrs(t, map[string]string{"id": "b+3"})},
		op.ElementEnd{}, // b+3
		op.ElementEnd{}, // thread
		op.ElementEnd{}, // b+1
		op.ElementEnd{}, // conversation
	})
	m, err := conv.ReadManifest(manifest)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(m.RootThread.Blips) != 1 {
		t.Fatalf("root thread blips = %d, want 1", len(m.RootThread.Blips))
	}
	b := m.RootThread.Blips[0]
	if b.ID != "b+1" {
		t.Errorf("root blip id = %q, want b+1", b.ID)
	}
	if len(b.Threads) != 1 {
		t.Fatalf("b+1 has %d reply threads, want 1", len(b.Threads))
	}
	th := b.Threads[0]
	if th.ID != "b+2" {
		t.Errorf("thread id = %q, want b+2", th.ID)
	}
	if !th.Inline {
		t.Error("thread should be inline")
	}
	if len(th.Blips) != 1 || th.Blips[0].ID != "b+3" {
		t.Errorf("thread blips = %v, want [b+3]", th.Blips)
	}
}

// applyAppend is a tiny helper: append a blip to a thread and apply the op.
func applyAppend(t *testing.T, manifest op.DocOp, threadID, blipID string) op.DocOp {
	t.Helper()
	mut, err := conv.AppendBlipToThread(manifest, threadID, blipID)
	if err != nil {
		t.Fatalf("AppendBlipToThread(%q, %q): %v", threadID, blipID, err)
	}
	next, err := op.Apply(manifest, mut)
	if err != nil {
		t.Fatalf("apply append: %v", err)
	}
	return next
}

func applyReply(t *testing.T, manifest op.DocOp, parentBlipID, newBlipID string, inline bool) op.DocOp {
	t.Helper()
	mut, err := conv.ReplyToBlip(manifest, parentBlipID, newBlipID, inline)
	if err != nil {
		t.Fatalf("ReplyToBlip(%q, %q): %v", parentBlipID, newBlipID, err)
	}
	next, err := op.Apply(manifest, mut)
	if err != nil {
		t.Fatalf("apply reply: %v", err)
	}
	return next
}

// AppendBlipToThread with an empty threadID is exactly AppendBlipToRootThread.
func TestAppendBlipToThreadRootMatchesRootHelper(t *testing.T) {
	manifest := conv.EmptyManifest()
	viaGeneral := applyAppend(t, manifest, "", "b+1")
	viaRoot, err := op.Apply(manifest, conv.AppendBlipToRootThread(manifest, "b+1"))
	if err != nil {
		t.Fatalf("apply root helper: %v", err)
	}
	if !viaGeneral.Equal(viaRoot) {
		t.Errorf("AppendBlipToThread(\"\") != AppendBlipToRootThread")
	}
}

// A reply creates a thread (id == new blip id) under the parent, containing the
// new blip; a further append to that thread continues it.
func TestReplyAndContinueThread(t *testing.T) {
	manifest := conv.EmptyManifest()
	manifest = applyAppend(t, manifest, "", "b+1")          // root blip
	manifest = applyReply(t, manifest, "b+1", "b+2", false) // reply thread b+2 under b+1
	manifest = applyAppend(t, manifest, "b+2", "b+3")       // continue thread b+2

	m, err := conv.ReadManifest(manifest)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(m.RootThread.Blips) != 1 || m.RootThread.Blips[0].ID != "b+1" {
		t.Fatalf("root thread = %+v, want single b+1", m.RootThread.Blips)
	}
	b1 := m.RootThread.Blips[0]
	if len(b1.Threads) != 1 {
		t.Fatalf("b+1 threads = %d, want 1", len(b1.Threads))
	}
	th := b1.Threads[0]
	if th.ID != "b+2" {
		t.Errorf("reply thread id = %q, want b+2", th.ID)
	}
	if th.Inline {
		t.Errorf("reply thread should not be inline")
	}
	gotIDs := make([]string, len(th.Blips))
	for i, b := range th.Blips {
		gotIDs[i] = b.ID
	}
	if len(gotIDs) != 2 || gotIDs[0] != "b+2" || gotIDs[1] != "b+3" {
		t.Errorf("thread blips = %v, want [b+2 b+3]", gotIDs)
	}
}

// An inline reply marks the thread inline="true".
func TestReplyInline(t *testing.T) {
	manifest := applyAppend(t, conv.EmptyManifest(), "", "b+1")
	manifest = applyReply(t, manifest, "b+1", "b+2", true)
	m, err := conv.ReadManifest(manifest)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	th := m.RootThread.Blips[0].Threads[0]
	if !th.Inline {
		t.Errorf("inline reply thread should be inline")
	}
}

// Reply targets the right (nested) parent and leaves siblings intact.
func TestReplyToNestedBlip(t *testing.T) {
	manifest := conv.EmptyManifest()
	manifest = applyAppend(t, manifest, "", "b+1")
	manifest = applyAppend(t, manifest, "", "b+2")          // two root blips
	manifest = applyReply(t, manifest, "b+2", "b+3", false) // reply under the second
	manifest = applyReply(t, manifest, "b+3", "b+4", false) // reply under the reply

	m, err := conv.ReadManifest(manifest)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(m.RootThread.Blips) != 2 {
		t.Fatalf("root blips = %d, want 2", len(m.RootThread.Blips))
	}
	if len(m.RootThread.Blips[0].Threads) != 0 {
		t.Errorf("b+1 should be untouched, got threads %+v", m.RootThread.Blips[0].Threads)
	}
	b2 := m.RootThread.Blips[1]
	if len(b2.Threads) != 1 || b2.Threads[0].ID != "b+3" {
		t.Fatalf("b+2 threads = %+v, want [b+3]", b2.Threads)
	}
	b3 := b2.Threads[0].Blips[0]
	if b3.ID != "b+3" || len(b3.Threads) != 1 || b3.Threads[0].ID != "b+4" {
		t.Errorf("b+3 nested reply = %+v, want thread b+4", b3.Threads)
	}
}

func TestAuthoringErrorsOnMissingTarget(t *testing.T) {
	manifest := conv.EmptyManifest()
	if _, err := conv.AppendBlipToThread(manifest, "no-such-thread", "b+x"); err == nil {
		t.Error("AppendBlipToThread should error on a missing thread")
	}
	if _, err := conv.ReplyToBlip(manifest, "no-such-blip", "b+x", false); err == nil {
		t.Error("ReplyToBlip should error on a missing blip")
	}
}

func TestReadManifestAnchor(t *testing.T) {
	manifest := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "conversation", Attributes: attrs(t, map[string]string{
			"anchorWavelet": "example.com!conv+root", "anchorBlip": "b+parent", "sort": "m"})},
		op.ElementEnd{},
	})
	m, err := conv.ReadManifest(manifest)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.AnchorWavelet != "example.com!conv+root" {
		t.Errorf("anchorWavelet = %q", m.AnchorWavelet)
	}
	if m.AnchorBlip != "b+parent" {
		t.Errorf("anchorBlip = %q", m.AnchorBlip)
	}
	if m.Sort != "m" {
		t.Errorf("sort = %q", m.Sort)
	}
}

func TestInitialBlipContent(t *testing.T) {
	body, err := doc.Root(conv.InitialBlipContent())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if body.Type != "body" {
		t.Fatalf("root = %q, want body", body.Type)
	}
	els := body.ChildElements()
	if len(els) != 1 || els[0].Type != "line" {
		t.Errorf("body children = %v, want a single <line/>", els)
	}
	// No <head> is ever emitted (spec §3.3 implementation note).
	if body.Type == "head" {
		t.Error("blip content must not contain a head element")
	}
}

func TestNonManifestRejected(t *testing.T) {
	none := attrs(t, nil)
	notManifest := op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body", Attributes: none},
		op.ElementEnd{},
	})
	if _, err := conv.ReadManifest(notManifest); err == nil {
		t.Error("ReadManifest should reject a non-<conversation> root")
	}
}
