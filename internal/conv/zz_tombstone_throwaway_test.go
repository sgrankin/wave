package conv

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// Delete a blip that is the FIRST blip of a reply thread (thread id == blip id).
// The thread must survive; the blip must be marked deleted; offsets must be right.
func TestDeleteFirstBlipOfReplyThread(t *testing.T) {
	m := EmptyManifest()
	// root: <blip id=b+root>
	step, _ := op.Apply(m, AppendBlipToRootThread(m, "b+root"))
	// reply thread under b+root, first blip b+reply (thread id == b+reply)
	rop, err := ReplyToBlip(step, "b+root", "b+reply", false)
	if err != nil {
		t.Fatal(err)
	}
	step2, err := op.Apply(step, rop)
	if err != nil {
		t.Fatal(err)
	}

	// Delete b+reply (the thread's first blip, id == thread id).
	del, err := SetBlipDeleted(step2, "b+reply")
	if err != nil {
		t.Fatalf("SetBlipDeleted(b+reply): %v", err)
	}
	if err := op.Validate(step2, del); err != nil {
		t.Fatalf("delete op invalid: %v", err)
	}
	after, err := op.Apply(step2, del)
	if err != nil {
		t.Fatalf("apply delete: %v", err)
	}

	man, err := ReadManifest(after)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	// b+root present, has one reply thread id=b+reply, whose first blip b+reply is deleted.
	root := man.RootThread
	if len(root.Blips) != 1 || root.Blips[0].ID != "b+root" {
		t.Fatalf("root thread = %+v", root.Blips)
	}
	rb := root.Blips[0]
	if len(rb.Threads) != 1 {
		t.Fatalf("b+root threads = %+v", rb.Threads)
	}
	th := rb.Threads[0]
	if th.ID != "b+reply" {
		t.Errorf("reply thread id = %q, want b+reply (thread id must survive deleting its first blip)", th.ID)
	}
	if len(th.Blips) != 1 || th.Blips[0].ID != "b+reply" {
		t.Fatalf("reply thread blips = %+v", th.Blips)
	}
	if !th.Blips[0].Deleted {
		t.Error("b+reply should be marked deleted")
	}
}

// elementStartOffset must point exactly at the matched <blip>'s ElementStart, even
// when the blip is nested deep in a reply thread (count interleaved items correctly).
func TestElementStartOffsetNested(t *testing.T) {
	m := EmptyManifest()
	step, _ := op.Apply(m, AppendBlipToRootThread(m, "b+root"))
	rop, _ := ReplyToBlip(step, "b+root", "b+reply", false)
	manifest, _ := op.Apply(step, rop)

	start, ok := elementStartOffset(manifest, func(tag, id string) bool {
		return tag == tagBlip && id == "b+reply"
	})
	if !ok {
		t.Fatal("did not find b+reply")
	}
	// Confirm the item at `start` is indeed the b+reply <blip> ElementStart.
	items := flatten(manifest)
	if start < 0 || start >= len(items) {
		t.Fatalf("start %d out of range %d", start, len(items))
	}
	it := items[start]
	if it.kind != "start" || it.typ != tagBlip || it.id != "b+reply" {
		t.Fatalf("item at start %d = %+v, want <blip id=b+reply>", start, it)
	}
}

type flatItem struct {
	kind string // "start","end","char"
	typ  string
	id   string
}

func flatten(d op.DocOp) []flatItem {
	var out []flatItem
	for _, c := range d.Components() {
		switch c := c.(type) {
		case op.ElementStart:
			id, _ := c.Attributes.Get("id")
			out = append(out, flatItem{kind: "start", typ: c.Type, id: id})
		case op.ElementEnd:
			out = append(out, flatItem{kind: "end"})
		case op.Characters:
			for range c.Text {
				out = append(out, flatItem{kind: "char"})
			}
		}
	}
	return out
}
