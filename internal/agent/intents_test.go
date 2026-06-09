package agent_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/agent"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

// readerFrom returns a blip-reader over a fixed map (as OptimisticClient.SubmitWith
// supplies).
func readerFrom(docs map[string]op.DocOp) func(string) (op.DocOp, bool) {
	return func(id string) (op.DocOp, bool) {
		d, ok := docs[id]
		return d, ok
	}
}

// contentOf extracts the content DocOp from a blip operation targeting blipID.
func contentOf(t *testing.T, ops []waveop.Operation, blipID string) op.DocOp {
	t.Helper()
	for _, o := range ops {
		if wb, ok := o.(waveop.WaveletBlipOperation); ok && wb.BlipID == blipID {
			return wb.BlipOp.(waveop.BlipContentOperation).ContentOp
		}
	}
	t.Fatalf("no blip op for %q in %+v", blipID, ops)
	return op.DocOp{}
}

// rootManifest builds a manifest with a single root blip "b+root".
func rootManifest(t *testing.T) op.DocOp {
	t.Helper()
	m, err := op.Apply(conv.EmptyManifest(), conv.AppendBlipToRootThread(conv.EmptyManifest(), "b+root"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fixedID(id string) func() string { return func() string { return id } }

func TestTranslatePostBlipToRoot(t *testing.T) {
	alice := pid(t, "alice@example.com")
	manifest := rootManifest(t)
	docs := map[string]op.DocOp{conv.ManifestDocumentID: manifest, "b+root": conv.InitialBlipContent()}

	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentPostBlip, Text: "hello there"},
		alice, 1000, readerFrom(docs), fixedID("b+new"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2 (manifest + content)", len(ops))
	}

	// The manifest op adds b+new to the root thread.
	newManifest, err := op.Apply(manifest, contentOf(t, ops, conv.ManifestDocumentID))
	if err != nil {
		t.Fatal(err)
	}
	m, err := conv.ReadManifest(newManifest)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, b := range m.RootThread.Blips {
		ids = append(ids, b.ID)
	}
	if len(ids) != 2 || ids[0] != "b+root" || ids[1] != "b+new" {
		t.Fatalf("root thread blips = %v, want [b+root b+new]", ids)
	}
	// The new blip's content carries the text.
	if text, _ := doc.PlainText(contentOf(t, ops, "b+new")); text != "hello there" {
		t.Errorf("new blip text = %q, want %q", text, "hello there")
	}
}

func TestTranslatePostBlipToThread(t *testing.T) {
	alice := pid(t, "alice@example.com")
	base := rootManifest(t)
	// Give b+root a reply thread "b+t1".
	withThread, err := conv.ReplyToBlip(base, "b+root", "b+t1", false)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := op.Apply(base, withThread)
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]op.DocOp{conv.ManifestDocumentID: manifest}

	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentPostBlip, ThreadID: "b+t1", Text: "a reply"},
		alice, 1000, readerFrom(docs), fixedID("b+r2"))
	if err != nil {
		t.Fatal(err)
	}
	newManifest, err := op.Apply(manifest, contentOf(t, ops, conv.ManifestDocumentID))
	if err != nil {
		t.Fatal(err)
	}
	m, err := conv.ReadManifest(newManifest)
	if err != nil {
		t.Fatal(err)
	}
	// b+root's reply thread "b+t1" should now contain [b+t1, b+r2].
	var threadIDs []string
	for _, b := range m.RootThread.Blips {
		if b.ID == "b+root" {
			for _, th := range b.Threads {
				if th.ID == "b+t1" {
					for _, tb := range th.Blips {
						threadIDs = append(threadIDs, tb.ID)
					}
				}
			}
		}
	}
	if len(threadIDs) != 2 || threadIDs[1] != "b+r2" {
		t.Fatalf("thread b+t1 blips = %v, want [b+t1 b+r2]", threadIDs)
	}
}

func TestTranslateReplyBlip(t *testing.T) {
	alice := pid(t, "alice@example.com")
	manifest := rootManifest(t)
	body := conv.BlipContentWithText("question?")
	docs := map[string]op.DocOp{conv.ManifestDocumentID: manifest, "b+root": body}

	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentReplyBlip, BlipID: "b+root", Text: "an answer"},
		alice, 1000, readerFrom(docs), fixedID("b+reply"))
	if err != nil {
		t.Fatal(err)
	}
	// Non-inline reply: just the manifest mutation + the new blip content (no
	// parent-body anchor op).
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2 (manifest + content)", len(ops))
	}

	// b+root gains a reply thread whose id == the new blip id, containing the new blip.
	newManifest, err := op.Apply(manifest, contentOf(t, ops, conv.ManifestDocumentID))
	if err != nil {
		t.Fatal(err)
	}
	m, err := conv.ReadManifest(newManifest)
	if err != nil {
		t.Fatal(err)
	}
	var thread *conv.Thread
	for _, b := range m.RootThread.Blips {
		if b.ID == "b+root" {
			for i := range b.Threads {
				if b.Threads[i].ID == "b+reply" {
					thread = &b.Threads[i]
				}
			}
		}
	}
	if thread == nil {
		t.Fatalf("no reply thread b+reply under b+root; manifest = %+v", m)
	}
	if thread.Inline {
		t.Error("non-inline reply thread should not be marked inline")
	}
	if len(thread.Blips) != 1 || thread.Blips[0].ID != "b+reply" {
		t.Fatalf("reply thread blips = %+v, want [b+reply]", thread.Blips)
	}
	if text, _ := doc.PlainText(contentOf(t, ops, "b+reply")); text != "an answer" {
		t.Errorf("reply blip text = %q, want %q", text, "an answer")
	}
}

func TestTranslateReplyBlipInline(t *testing.T) {
	alice := pid(t, "alice@example.com")
	manifest := rootManifest(t)
	body := conv.BlipContentWithText("question?")
	docs := map[string]op.DocOp{conv.ManifestDocumentID: manifest, "b+root": body}

	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentReplyBlip, BlipID: "b+root", Text: "inline answer", Inline: true},
		alice, 1000, readerFrom(docs), fixedID("b+reply"))
	if err != nil {
		t.Fatal(err)
	}
	// Inline reply: manifest + new blip content + the parent-body anchor op.
	if len(ops) != 3 {
		t.Fatalf("got %d ops, want 3 (manifest + content + anchor)", len(ops))
	}

	// The manifest thread is marked inline.
	newManifest, err := op.Apply(manifest, contentOf(t, ops, conv.ManifestDocumentID))
	if err != nil {
		t.Fatal(err)
	}
	m, err := conv.ReadManifest(newManifest)
	if err != nil {
		t.Fatal(err)
	}
	var inline bool
	for _, b := range m.RootThread.Blips {
		if b.ID == "b+root" {
			for _, th := range b.Threads {
				if th.ID == "b+reply" {
					inline = th.Inline
				}
			}
		}
	}
	if !inline {
		t.Error("inline reply thread should be marked inline=true")
	}

	// The parent body gains a <reply id="b+reply"/> anchor, near the end (before
	// the final </body>).
	newBody, err := op.Apply(body, contentOf(t, ops, "b+root"))
	if err != nil {
		t.Fatal(err)
	}
	anchors := conv.ReadReplyAnchors(newBody)
	if len(anchors) != 1 || anchors[0].ThreadID != "b+reply" {
		t.Fatalf("parent anchors = %+v, want one for b+reply", anchors)
	}
	if anchors[0].Offset != body.DocumentLength()-1 {
		t.Errorf("anchor offset = %d, want %d (just before </body>)", anchors[0].Offset, body.DocumentLength()-1)
	}
}

func TestTranslateReplyBlipErrors(t *testing.T) {
	alice := pid(t, "alice@example.com")
	manifest := rootManifest(t)
	docs := map[string]op.DocOp{conv.ManifestDocumentID: manifest, "b+root": conv.InitialBlipContent()}

	// No such parent blip.
	if _, err := agent.Translate(
		agent.Intent{Kind: agent.IntentReplyBlip, BlipID: "ghost", Text: "x"},
		alice, 1000, readerFrom(docs), fixedID("b+x")); err == nil {
		t.Error("want error replying to a missing blip")
	}
	// No manifest.
	if _, err := agent.Translate(
		agent.Intent{Kind: agent.IntentReplyBlip, BlipID: "b+root", Text: "x"},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("b+x")); err == nil {
		t.Error("want error when the manifest is absent")
	}
}

func TestTranslatePostBlipMissingThread(t *testing.T) {
	alice := pid(t, "alice@example.com")
	docs := map[string]op.DocOp{conv.ManifestDocumentID: rootManifest(t)}
	_, err := agent.Translate(
		agent.Intent{Kind: agent.IntentPostBlip, ThreadID: "nope", Text: "x"},
		alice, 1000, readerFrom(docs), fixedID("b+x"))
	if err == nil {
		t.Fatal("want error posting to a nonexistent thread")
	}
}

func TestTranslatePostBlipNoManifest(t *testing.T) {
	alice := pid(t, "alice@example.com")
	_, err := agent.Translate(
		agent.Intent{Kind: agent.IntentPostBlip, Text: "x"},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("b+x"))
	if err == nil {
		t.Fatal("want error when the manifest is absent")
	}
}

func TestTranslateEditBlipAppends(t *testing.T) {
	alice := pid(t, "alice@example.com")
	cur := conv.BlipContentWithText("hi")
	docs := map[string]op.DocOp{"b1": cur}

	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b1", Text: " there"},
		alice, 1000, readerFrom(docs), fixedID("unused"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	edited, err := op.Apply(cur, contentOf(t, ops, "b1"))
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := doc.PlainText(edited); text != "hi there" {
		t.Errorf("edited text = %q, want %q", text, "hi there")
	}
}

func TestTranslateEditBlipErrors(t *testing.T) {
	alice := pid(t, "alice@example.com")
	docs := map[string]op.DocOp{"b1": conv.BlipContentWithText("hi")}
	// Missing target.
	if _, err := agent.Translate(agent.Intent{Kind: agent.IntentEditBlip, BlipID: "ghost", Text: "x"}, alice, 1000, readerFrom(docs), fixedID("")); err == nil {
		t.Error("want error editing a missing blip")
	}
	// Empty text.
	if _, err := agent.Translate(agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b1", Text: ""}, alice, 1000, readerFrom(docs), fixedID("")); err == nil {
		t.Error("want error on empty edit text")
	}
}

func TestTranslateAddParticipant(t *testing.T) {
	alice := pid(t, "alice@example.com")
	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentAddParticipant, Participant: "bob@example.com"},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	ap, ok := ops[0].(waveop.AddParticipant)
	if !ok || ap.Participant != pid(t, "bob@example.com") {
		t.Fatalf("op = %+v, want AddParticipant(bob)", ops[0])
	}
}

func TestTranslateAddParticipantBadAddress(t *testing.T) {
	alice := pid(t, "alice@example.com")
	if _, err := agent.Translate(agent.Intent{Kind: agent.IntentAddParticipant, Participant: "not-an-address"}, alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("")); err == nil {
		t.Error("want error for an invalid participant address")
	}
}

func TestTranslateRemoveParticipant(t *testing.T) {
	alice := pid(t, "alice@example.com")
	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentRemoveParticipant, Participant: "bob@example.com"},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	rp, ok := ops[0].(waveop.RemoveParticipant)
	if !ok || rp.Participant != pid(t, "bob@example.com") {
		t.Fatalf("op = %+v, want RemoveParticipant(bob)", ops[0])
	}
}

func TestTranslateRemoveParticipantBadAddress(t *testing.T) {
	alice := pid(t, "alice@example.com")
	if _, err := agent.Translate(agent.Intent{Kind: agent.IntentRemoveParticipant, Participant: "not-an-address"}, alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("")); err == nil {
		t.Error("want error for an invalid participant address")
	}
}

func TestTranslateRemoveSelf(t *testing.T) {
	alice := pid(t, "alice@example.com")
	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentRemoveSelf},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("got %d ops, want 1", len(ops))
	}
	rp, ok := ops[0].(waveop.RemoveParticipant)
	if !ok || rp.Participant != alice {
		t.Fatalf("op = %+v, want RemoveParticipant(alice/self)", ops[0])
	}
}

func TestTranslateUnknownIntent(t *testing.T) {
	alice := pid(t, "alice@example.com")
	if _, err := agent.Translate(agent.Intent{Kind: "bogus"}, alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("")); err == nil {
		t.Error("want error for an unknown intent kind")
	}
}

func TestTranslateSetState(t *testing.T) {
	alice := pid(t, "alice@example.com")

	// No state doc yet → set.state lazily creates it (initialization content), so the
	// op stands alone (no compose needed) and ReadState sees the entry.
	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentSetState, Key: "status", Value: "processing"},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("x"))
	if err != nil {
		t.Fatal(err)
	}
	stateDoc := contentOf(t, ops, conv.StateDocumentID)
	if got := conv.ReadState(stateDoc); got["status"] != "processing" || len(got) != 1 {
		t.Fatalf("created state = %v, want {status:processing}", got)
	}

	// With an existing state doc → set.state emits a MUTATION that composes onto it.
	ops2, err := agent.Translate(
		agent.Intent{Kind: agent.IntentSetState, Key: "n", Value: "3"},
		alice, 1000, readerFrom(map[string]op.DocOp{conv.StateDocumentID: stateDoc}), fixedID("x"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := op.Apply(stateDoc, contentOf(t, ops2, conv.StateDocumentID))
	if err != nil {
		t.Fatal(err)
	}
	if got := conv.ReadState(next); got["status"] != "processing" || got["n"] != "3" {
		t.Fatalf("updated state = %v, want status+n", got)
	}

	// An empty key is rejected.
	if _, err := agent.Translate(
		agent.Intent{Kind: agent.IntentSetState, Key: "", Value: "x"},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("x")); err == nil {
		t.Error("set.state with an empty key should error")
	}
}

func TestTranslateDeleteState(t *testing.T) {
	alice := pid(t, "alice@example.com")
	mut, err := conv.SetStateValue(conv.EmptyState(), "status", "done")
	if err != nil {
		t.Fatal(err)
	}
	stateDoc, err := op.Apply(conv.EmptyState(), mut)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := agent.Translate(
		agent.Intent{Kind: agent.IntentDeleteState, Key: "status"},
		alice, 1000, readerFrom(map[string]op.DocOp{conv.StateDocumentID: stateDoc}), fixedID("x"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := op.Apply(stateDoc, contentOf(t, ops, conv.StateDocumentID))
	if err != nil {
		t.Fatal(err)
	}
	if got := conv.ReadState(next); len(got) != 0 {
		t.Fatalf("after delete, state = %v, want {}", got)
	}

	// delete.state with no state document errors.
	if _, err := agent.Translate(
		agent.Intent{Kind: agent.IntentDeleteState, Key: "x"},
		alice, 1000, readerFrom(map[string]op.DocOp{}), fixedID("x")); err == nil {
		t.Error("delete.state with no state document should error")
	}
}
