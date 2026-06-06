package waveop_test

// Ported from
// wave/src/test/java/org/waveprotocol/wave/model/operation/wave/WorthyChangeCheckerTest.java
//
// The Java test builds an IndexedDocument and derives the deletion op with a
// Nindo (skip 2, delete the inline-reply anchor element start + end), then
// asserts WorthyChangeChecker.isWorthy(op) is false. Our port has no
// IndexedDocument / Nindo machinery; instead we construct the equivalent
// resulting DocOp directly (retain 2 over the leading characters, then delete the
// "reply" element start/end) and assert IsWorthyChange is false. The behavioral
// claim — deleting only an inline-reply anchor is not a worthy change — is
// preserved.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

func TestConformanceAnchorRemovalIsUnworthy(t *testing.T) {
	empty, err := op.NewAttributes(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Equivalent of Nindo{ skip(2); deleteElementStart(); deleteElementEnd(); }
	// applied to a document whose tail is the inline-reply anchor <reply></reply>.
	deletion := op.NewDocOp([]op.Component{
		op.Retain{Count: 2},
		op.DeleteElementStart{Type: "reply", Attributes: empty},
		op.DeleteElementEnd{},
	})
	if waveop.IsWorthyChange(deletion) {
		t.Error("deleting only an inline-reply anchor should be unworthy")
	}
}

// XtestAnchorRemovalIsUnworthy2 in Java is disabled (prefixed with X) with the
// comment "This one is still failing; fixing it is not as easy." It deletes an
// anchor whose element start/end straddle an annotation boundary end. We do not
// port a disabled test; recorded in skipped[].
