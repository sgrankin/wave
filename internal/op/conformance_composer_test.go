package op_test

// Ports of the hand-crafted Composer and Transformer edge cases from the Java
// conformance suites:
//
//	wave/.../model/document/operation/algorithm/ComposerTest.java
//	wave/.../model/document/operation/algorithm/TransformerTest.java
//
// These exercise length-mismatch rejection in Compose/Transform. The Java
// "composer checking" test (testComposerChecking) drives DocOpBuilder's
// well-formedness checker against a buildUnchecked() op with an illegal element
// type — our NewDocOp does not yet run the full cross-component well-formedness
// automaton (see component.go), so that path is recorded as skipped, not ported.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// ComposerTest.testDocumentLengthMismatch: composing the empty op with retain(1)
// (and vice versa) must be rejected because op1's output length must equal op2's
// input length.
func TestConformanceComposerDocumentLengthMismatch(t *testing.T) {
	empty := op.NewDocOp(nil)
	retain1 := op.NewDocOp([]op.Component{op.Retain{Count: 1}})

	if _, err := op.Compose(empty, retain1); err == nil {
		t.Error("Compose(empty, retain(1)) should be rejected (length mismatch)")
	}
	if _, err := op.Compose(retain1, empty); err == nil {
		t.Error("Compose(retain(1), empty) should be rejected (length mismatch)")
	}
}

// TransformerTest.testClientOpLongerThanServerOp: transforming retain(1) against
// the empty op must fail (the operations have different input lengths and so are
// not valid against a common document).
func TestConformanceTransformerClientOpLongerThanServerOp(t *testing.T) {
	client := op.NewDocOp([]op.Component{op.Retain{Count: 1}})
	server := op.NewDocOp(nil)
	if _, _, err := op.Transform(client, server); err == nil {
		t.Error("Transform(retain(1), empty) should fail: mismatched input lengths")
	}
}
