package waveop_test

// Ported from
//   wave/.../model/operation/wave/BlipOperationVisitorTest.java  (adapted)
//   wave/.../model/operation/wave/BlipOperationTest.java         (portable parts)
//
// The Java BlipOperation hierarchy is visited via a BlipOperationVisitor with
// visitBlipContentOperation / visitSubmitBlip methods. Our Go port has no
// visitor: a BlipOperation is dispatched with a type switch over its concrete
// types. We test that dispatch resolves to the right concrete type and that the
// payload survives. SubmitBlip is not present in our op set (the submit/draft
// machinery was dropped), so its visitor case is skipped — see skipped[].
//
// Most of BlipOperationTest exercises apply()/applyAndReturnReverse() against a
// BlipData wavelet-data model that this package does not implement (operation
// structure and transform only; the data model is a later phase). Those tests
// are skipped wholesale in conformance_skipped_test.go. The two pieces that are
// portable here are: getContext() and the WorthyChangeChecker.isWorthy assertion
// embedded in the sample-op builder.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

// dispatchBlipOp resolves a BlipOperation to its concrete kind, mirroring what a
// BlipOperationVisitor would do. Returns the visited BlipContentOperation (and
// true) when that is the concrete type.
func dispatchBlipOp(b waveop.BlipOperation) (waveop.BlipContentOperation, bool) {
	switch v := b.(type) {
	case waveop.BlipContentOperation:
		return v, true
	default:
		return waveop.BlipContentOperation{}, false
	}
}

func TestConformanceBlipOperationDispatch_BlipContentOperation(t *testing.T) {
	fred := pid(t, "fred@gwave.com")
	c := waveop.Context{Creator: fred, Timestamp: 0, VersionIncrement: 0}
	docOp := op.NewDocOp([]op.Component{op.Characters{Text: "Hello"}})
	original := waveop.BlipContentOperation{Ctx: c, ContentOp: docOp}

	var b waveop.BlipOperation = original
	visited, ok := dispatchBlipOp(b)
	if !ok {
		t.Fatal("expected dispatch to resolve BlipContentOperation")
	}
	if !visited.ContentOp.Equal(original.ContentOp) {
		t.Errorf("visited content op = %v, want %v", visited.ContentOp.Components(), original.ContentOp.Components())
	}
	if visited.Ctx != original.Ctx {
		t.Errorf("visited context = %v, want %v", visited.Ctx, original.Ctx)
	}
}

// TestConformanceBlipOperationGetContext ports BlipOperationTest.testGetContext:
// a blip operation's Context() returns the context it was constructed with.
func TestConformanceBlipOperationGetContext(t *testing.T) {
	c := waveop.Context{Creator: pid(t, "fred@example.com"), Timestamp: 175, VersionIncrement: 1}
	bc := waveop.BlipContentOperation{Ctx: c, ContentOp: op.EmptyDoc()}
	if bc.Context() != c {
		t.Errorf("Context() = %v, want %v", bc.Context(), c)
	}
	// And via the wavelet-level wrapper: WaveletBlipOperation.Context delegates to
	// the contained blip op.
	w := waveop.WaveletBlipOperation{BlipID: "blipid", BlipOp: bc}
	if w.Context() != c {
		t.Errorf("WaveletBlipOperation.Context() = %v, want %v", w.Context(), c)
	}
}

// TestConformanceBlipOperationSampleOpIsWorthy ports the embedded assertion in
// BlipOperationTest.createSampleContentOperation: the sample op (delete a "line"
// element start/end, then insert a fresh "line" element) is a worthy change.
func TestConformanceBlipOperationSampleOpIsWorthy(t *testing.T) {
	empty, err := op.NewAttributes(nil)
	if err != nil {
		t.Fatal(err)
	}
	sample := op.NewDocOp([]op.Component{
		op.Retain{Count: 1},
		op.DeleteElementStart{Type: "line", Attributes: empty},
		op.DeleteElementEnd{},
		op.ElementStart{Type: "line", Attributes: empty},
		op.ElementEnd{},
		op.Retain{Count: 1},
	})
	if !waveop.IsWorthyChange(sample) {
		t.Error("sample content operation (line element replacement) should be a worthy change")
	}
}
