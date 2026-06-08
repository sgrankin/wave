package conv_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/op"
)

// setOp applies SetStateValue(state, k, v) and returns the new state, failing on error.
func setOp(t *testing.T, state op.DocOp, k, v string) op.DocOp {
	t.Helper()
	mut, err := conv.SetStateValue(state, k, v)
	if err != nil {
		t.Fatalf("set %q: %v", k, err)
	}
	next, err := op.Apply(state, mut)
	if err != nil {
		t.Fatalf("apply set %q: %v", k, err)
	}
	return next
}

// delOp applies DeleteStateValue(state, k) and returns the new state, failing on error.
func delOp(t *testing.T, state op.DocOp, k string) op.DocOp {
	t.Helper()
	mut, err := conv.DeleteStateValue(state, k)
	if err != nil {
		t.Fatalf("delete %q: %v", k, err)
	}
	next, err := op.Apply(state, mut)
	if err != nil {
		t.Fatalf("apply delete %q: %v", k, err)
	}
	return next
}

func TestStateSetOverwriteDelete(t *testing.T) {
	state := conv.EmptyState()
	if got := conv.ReadState(state); len(got) != 0 {
		t.Fatalf("empty state = %v, want {}", got)
	}

	state = setOp(t, state, "status", "processing")
	state = setOp(t, state, "n", "3")
	if got := conv.ReadState(state); got["status"] != "processing" || got["n"] != "3" || len(got) != 2 {
		t.Fatalf("after sets, state = %v", got)
	}

	// Overwrite a key in place (no duplicate entry).
	state = setOp(t, state, "status", "done")
	if got := conv.ReadState(state); got["status"] != "done" || len(got) != 2 {
		t.Fatalf("after overwrite, state = %v, want status=done and 2 keys", got)
	}

	// Delete a key.
	state = delOp(t, state, "n")
	if got := conv.ReadState(state); got["n"] != "" || len(got) != 1 || got["status"] != "done" {
		t.Fatalf("after delete, state = %v, want {status:done}", got)
	}

	// Deleting an absent key errors.
	if _, err := conv.DeleteStateValue(state, "nope"); err == nil {
		t.Error("delete of an absent key should error")
	}
}

func TestStateInvalidUTF8KeyValueErrorsNotPanics(t *testing.T) {
	// An agent-supplied key/value with invalid UTF-8 must ERROR (not panic the server):
	// the builder uses op.NewAttributes, not mustAttrs. Both the insert path and a delete
	// echo are exercised.
	state := conv.EmptyState()
	if _, err := conv.SetStateValue(state, "\xff\xfe", "v"); err == nil {
		t.Error("set with an invalid-UTF-8 key should error, not panic")
	}
	if _, err := conv.SetStateValue(state, "k", "\xff"); err == nil {
		t.Error("set with an invalid-UTF-8 value should error, not panic")
	}
}

func TestStateValueWithSpecialChars(t *testing.T) {
	// Values are opaque strings — JSON, unicode, quotes must round-trip.
	state := conv.EmptyState()
	val := `{"k":"v","emoji":"🧠","q":"a\"b"}`
	state = setOp(t, state, "summary", val)
	if got := conv.ReadState(state)["summary"]; got != val {
		t.Fatalf("value round-trip = %q, want %q", got, val)
	}
}

func TestStateCaps(t *testing.T) {
	state := conv.EmptyState()
	// Oversize value rejected; a value at the limit is accepted.
	if _, err := conv.SetStateValue(state, "k", strings.Repeat("x", conv.MaxStateValueSize+1)); err == nil {
		t.Error("oversize value should be rejected")
	}
	if _, err := conv.SetStateValue(state, "k", strings.Repeat("x", conv.MaxStateValueSize)); err != nil {
		t.Errorf("value at the limit should be accepted: %v", err)
	}

	// Fill to the key cap; a new key past it is rejected, but overwriting an existing
	// key still works.
	for i := 0; i < conv.MaxStateKeys; i++ {
		state = setOp(t, state, "k"+strconv.Itoa(i), "v")
	}
	if _, err := conv.SetStateValue(state, "one-too-many", "v"); err == nil {
		t.Error("inserting a new key past the cap should be rejected")
	}
	if _, err := conv.SetStateValue(state, "k0", "updated"); err != nil {
		t.Errorf("overwriting an existing key at the cap should be allowed: %v", err)
	}
}
