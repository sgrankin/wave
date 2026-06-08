package server_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
)

// TestSubmitRejectsContentMismatchedOp is the end-to-end anti-corruption guard: a
// length-correct but content-WRONG blip op (it deletes characters the document does
// not contain) is rejected on submit, the version does not advance, and the stored
// document is left intact — rather than Compose applying the delete by length and
// silently corrupting the blip (which would desync every other client).
func TestSubmitRejectsContentMismatchedOp(t *testing.T) {
	m := newWaveMap(t)
	name := waveletName(t)
	c, _ := m.Container(name)
	alice := pid(t, "alice@example.com")

	// Create blip "b" with the text "hi".
	if _, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err)
	}
	goodVersion := c.Version()

	// A delete whose text ("XY") does not match the document ("hi") — same length, so
	// only a content check (not a length check) catches it.
	mismatch := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "XY"}})
	_, err := c.Submit(blipDelta(alice, c.Version(), "b", mismatch))
	if err == nil {
		t.Fatal("submit of a content-mismatched op must be rejected")
	}

	// The version did not advance and the document is untouched.
	if c.Version().Compare(goodVersion) != 0 {
		t.Errorf("version advanced to v%d on a rejected submit (was v%d)", c.Version().Version(), goodVersion.Version())
	}
	blip, ok := c.Wavelet().Blip("b")
	if !ok || !blip.Content().Equal(chars("hi")) {
		t.Errorf("blip content was corrupted by a rejected submit: %+v", blip)
	}

	// A well-formed edit against the same document still succeeds (the guard does not
	// over-reject): replace "hi" with "bye".
	good := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "hi"}, op.Characters{Text: "bye"}})
	if _, err := c.Submit(blipDelta(alice, c.Version(), "b", good)); err != nil {
		t.Fatalf("valid edit rejected: %v", err)
	}
	if blip, _ := c.Wavelet().Blip("b"); !blip.Content().Equal(chars("bye")) {
		t.Errorf("valid edit did not apply: %+v", blip)
	}
}
