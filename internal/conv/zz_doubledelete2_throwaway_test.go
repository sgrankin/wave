package conv

import (
	"fmt"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

func TestDoubleDeleteValues(t *testing.T) {
	m := EmptyManifest()
	a1 := AppendBlipToRootThread(m, "b+2")
	manifest, err := op.Apply(m, a1)
	if err != nil {
		t.Fatal(err)
	}
	delA, _ := SetBlipDeleted(manifest, "b+2")
	delB, _ := SetBlipDeleted(manifest, "b+2")
	delBPrime, _, err := op.Transform(delB, delA)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range delBPrime.Components() {
		if u, ok := c.(op.UpdateAttributes); ok {
			for _, ch := range u.Update.All() {
				ov, nv := "nil", "nil"
				if ch.OldValue != nil {
					ov = *ch.OldValue
				}
				if ch.NewValue != nil {
					nv = *ch.NewValue
				}
				fmt.Printf("transformed update %q: old=%s new=%s\n", ch.Name, ov, nv)
			}
		}
	}

	// Sequential re-delete: a client that SEES deleted=true and authors a fresh
	// SetBlipDeleted against the already-deleted manifest. SetBlipDeleted always
	// emits old=nil, so it must be REJECTED at validate (no transform involved).
	afterA, _ := op.Apply(manifest, delA)
	freshDelete, err := SetBlipDeleted(afterA, "b+2")
	if err != nil {
		t.Fatalf("SetBlipDeleted on already-deleted blip errored at build: %v", err)
	}
	verr := op.Validate(afterA, freshDelete)
	fmt.Printf("fresh re-delete of an already-deleted blip: Validate = %v\n", verr)
	if verr == nil {
		t.Error("expected a fresh re-delete (old=nil vs deleted=true) to be REJECTED, but it validated")
	}
}
