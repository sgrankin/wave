package op_test

// Port of wave/.../model/document/operation/algorithm/DocOpCollectorTest.java.
//
// Java's DocOpCollector accumulates a sequence of ops and folds them with the
// Composer, returning null for an empty collection (there is no DocOp value for
// a "universal no-op"). Our internal/op has no Collector type; the behavioral
// equivalent is left-folding op.Compose over the sequence, which is what the
// adaptation note in the task specifies. The empty-collection-is-null case is
// modeled with the (DocOp, ok) shape below.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// composeAll left-folds Compose over ops, mirroring DocOpCollector.composeAll().
// ok is false for the empty collection (Java returns null: a universal no-op).
func composeAll(t *testing.T, ops []op.DocOp) (result op.DocOp, ok bool) {
	t.Helper()
	if len(ops) == 0 {
		return op.DocOp{}, false
	}
	acc := ops[0]
	for _, next := range ops[1:] {
		var err error
		acc, err = op.Compose(acc, next)
		if err != nil {
			t.Fatalf("compose: %v", err)
		}
	}
	return acc, true
}

// DocOpCollectorTest.testSimpleMonotonicComposition: composing
//
//	"a", retain(1)+"b", retain(2)+"c", retain(3)+"d"
//
// — each inserting one character at the growing end — yields "abcd".
func TestConformanceCollectorSimpleMonotonicComposition(t *testing.T) {
	a := op.NewDocOp([]op.Component{op.Characters{Text: "a"}})
	b := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "b"}})
	c := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "c"}})
	d := op.NewDocOp([]op.Component{op.Retain{Count: 3}, op.Characters{Text: "d"}})

	got, ok := composeAll(t, []op.DocOp{a, b, c, d})
	if !ok {
		t.Fatal("composeAll returned no-op for a non-empty collection")
	}
	want := op.NewDocOp([]op.Component{op.Characters{Text: "abcd"}})
	if !got.Equal(want) {
		t.Errorf("composeAll = %v, want characters(\"abcd\")", got.Components())
	}
}

// DocOpCollectorTest.testEmptyCollectionComposesToNull: an empty collection
// composes to the universal no-op (null in Java).
func TestConformanceCollectorEmptyCollectionComposesToNull(t *testing.T) {
	if _, ok := composeAll(t, nil); ok {
		t.Error("composeAll of an empty collection should be the universal no-op (null)")
	}
}
