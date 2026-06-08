package conv

import (
	"fmt"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// Build a manifest with two blips, then have two concurrent SetBlipDeleted ops on
// the SAME blip. Transform the second against the first and check the result
// validates against the post-first content (should REJECT, fail-safe).
func TestConcurrentDoubleDelete(t *testing.T) {
	// manifest: <conversation><blip id=b+1/><blip id=b+2/></conversation>
	m := EmptyManifest()
	a1 := AppendBlipToRootThread(m, "b+1")
	m1, err := op.Apply(m, a1)
	if err != nil {
		t.Fatal(err)
	}
	a2 := AppendBlipToRootThread(m1, "b+2")
	manifest, err := op.Apply(m1, a2)
	if err != nil {
		t.Fatal(err)
	}

	// Two users both delete b+2 concurrently, each authored against `manifest`.
	delA, err := SetBlipDeleted(manifest, "b+2")
	if err != nil {
		t.Fatal(err)
	}
	delB, err := SetBlipDeleted(manifest, "b+2")
	if err != nil {
		t.Fatal(err)
	}

	// Server applies delA first.
	afterA, err := op.Apply(manifest, delA)
	if err != nil {
		t.Fatalf("apply delA: %v", err)
	}

	// delB must be transformed against delA before applying.
	delBPrime, _, err := op.Transform(delB, delA)
	if err != nil {
		t.Fatalf("transform delB vs delA: %v", err)
	}
	fmt.Println("delB' components:")
	for i, c := range delBPrime.Components() {
		fmt.Printf("  [%d] %T %+v\n", i, c, c)
	}

	// Now validate delB' against afterA (post-first-delete content).
	verr := op.Validate(afterA, delBPrime)
	fmt.Printf("Validate(afterA, delB') = %v\n", verr)

	// Whether it rejects or no-ops, it must NOT corrupt: if validate passes, the
	// applied result must keep deleted=true (idempotent), not flip/duplicate.
	if verr == nil {
		res, aerr := op.Apply(afterA, delBPrime)
		if aerr != nil {
			t.Fatalf("apply delB' after validate-pass: %v", aerr)
		}
		man, perr := ReadManifest(res)
		if perr != nil {
			t.Fatalf("read manifest after double-delete: %v", perr)
		}
		for _, b := range man.RootThread.Blips {
			if b.ID == "b+2" && !b.Deleted {
				t.Errorf("b+2 lost its deleted flag after concurrent double-delete")
			}
		}
		fmt.Println("double-delete validated-pass path produced a consistent manifest")
	} else {
		fmt.Println("double-delete second op REJECTED at validate (fail-safe) — no corruption")
	}
}
