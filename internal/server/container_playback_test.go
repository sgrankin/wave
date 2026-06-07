package server_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/version"
)

// TestStateAtAndDeltaHeaders exercises the playback reconstruction path: replaying
// the persisted log to any past delta-boundary version, and the timeline summary.
func TestStateAtAndDeltaHeaders(t *testing.T) {
	c, _, name := newContainer(t)
	alice := pid(t, "alice@example.com")

	// v2: creation (add participant + blip "b"="hi"); v3: "hi there"; v4: "hi there more".
	r1, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi")))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := c.Submit(blipDelta(alice, r1.ResultingVersion, "b", appendText(2, " there")))
	if err != nil {
		t.Fatal(err)
	}
	r3, err := c.Submit(blipDelta(alice, r2.ResultingVersion, "b", appendText(8, " more")))
	if err != nil {
		t.Fatal(err)
	}
	if r1.ResultingVersion.Version() != 2 || r2.ResultingVersion.Version() != 3 || r3.ResultingVersion.Version() != 4 {
		t.Fatalf("versions = %d,%d,%d, want 2,3,4", r1.ResultingVersion.Version(), r2.ResultingVersion.Version(), r3.ResultingVersion.Version())
	}

	// DeltaHeaders: one per submit, in version order.
	headers, err := c.DeltaHeaders()
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 3 {
		t.Fatalf("got %d headers, want 3", len(headers))
	}
	wantVer := []uint64{2, 3, 4}
	wantOps := []int{2, 1, 1}
	for i, h := range headers {
		if h.Version != wantVer[i] || h.OpCount != wantOps[i] || h.Author != alice {
			t.Errorf("header[%d] = %+v, want v%d ops%d alice", i, h, wantVer[i], wantOps[i])
		}
	}

	// StateAt at each boundary reconstructs the blip text as it stood then.
	wantText := map[uint64]string{2: "hi", 3: "hi there", 4: "hi there more"}
	for ver, want := range wantText {
		w, err := c.StateAt(ver)
		if err != nil {
			t.Fatalf("StateAt(%d): %v", ver, err)
		}
		if w.Version() != ver {
			t.Errorf("StateAt(%d).Version = %d", ver, w.Version())
		}
		if !w.HasParticipant(alice) {
			t.Errorf("StateAt(%d): alice missing", ver)
		}
		b, ok := w.Blip("b")
		if !ok {
			t.Fatalf("StateAt(%d): blip b missing", ver)
		}
		got, err := doc.PlainText(b.Content())
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("StateAt(%d) text = %q, want %q", ver, got, want)
		}
	}

	// version 0 ⇒ the empty / never-created state.
	if w, err := c.StateAt(0); err != nil || w != nil {
		t.Errorf("StateAt(0) = (%v, %v), want (nil, nil)", w, err)
	}
	// A mid-delta version (1 falls inside the 2-op creation delta) is not a boundary.
	if _, err := c.StateAt(1); err == nil {
		t.Errorf("StateAt(1) should error (mid-delta version)")
	}
	// Past the log's end.
	if _, err := c.StateAt(99); err == nil {
		t.Errorf("StateAt(99) should error (past end)")
	}
}
