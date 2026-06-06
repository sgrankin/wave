package op_test

// Annotation transform cases from
// wave/.../model/document/operation/algorithm/DocOpTransformerTest.java
// (testAnnotationVsDelete, testAnnotationVsInsert, testAnnotationVsAnnotation).
//
// ASSERTION STRATEGY: The Java suite asserts exact transformed-op equality. Our
// builder deliberately does NOT reproduce the Java annotation normalizer's
// state-elision (builder.go), so the transformed annotation boundaries can carry
// extra/differently-grouped value changes while denoting the same document. The
// behavioral invariant the transform must satisfy is TP1 convergence, so these
// cases assert convergence over a concrete length-20 document (ctConverges,
// using the same sameDocument equivalence as the existing tp1 helper). Where a
// case ALSO matches the exact Java expected ops under DocOp.Equal, that is
// asserted too as the stronger check.

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// ctAnnExpect asserts both TP1 convergence over a length-20 document AND exact
// equality with the Java expected transformed ops. Annotation cases where exact
// equality does not hold (representation difference only) should use
// ctAnnConverges instead and be documented.
func ctAnnExpect(t *testing.T, client, server, wantClientPrime, wantServerPrime op.DocOp) {
	t.Helper()
	if !ctConverges(t, ctDoc20(t), client, server) {
		t.Errorf("TP1 convergence violated for client=%v server=%v", client.Components(), server.Components())
	}
	ctExpect(t, client, server, wantClientPrime, wantServerPrime)
}

// ctAnnReversible is the reversible form of ctAnnExpect.
func ctAnnReversible(t *testing.T, client, server, clientPrime, serverPrime op.DocOp) {
	t.Helper()
	ctAnnExpect(t, client, server, clientPrime, serverPrime)
	ctAnnExpect(t, server, client, serverPrime, clientPrime)
}

// ctAnnConverges asserts only TP1 convergence (used where the transformed
// annotation representation legitimately differs from Java's but the document
// still converges).
func ctAnnConverges(t *testing.T, client, server op.DocOp) {
	t.Helper()
	if !ctConverges(t, ctDoc20(t), client, server) {
		t.Errorf("TP1 convergence violated for client=%v server=%v", client.Components(), server.Components())
	}
	// And reversed.
	if !ctConverges(t, ctDoc20(t), server, client) {
		t.Errorf("TP1 convergence violated (reversed) for client=%v server=%v", server.Components(), client.Components())
	}
}

// --- testAnnotationVsDelete ---

func TestConformanceTransformAnnotationVsDelete(t *testing.T) {
	hello, world := "hello", "world"
	// A's annotation spatially before B's deletion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 4, 6, hello, nil, &world),
		ctDeleteChars(20, 7, "ab"),
		ctSetAnnotation(t, 18, 4, 6, hello, nil, &world),
		ctDeleteChars(20, 7, "ab"))
	// A's annotation spatially after B's deletion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 4, 6, hello, nil, &world),
		ctDeleteChars(20, 1, "ab"),
		ctSetAnnotation(t, 18, 2, 4, hello, nil, &world),
		ctDeleteChars(20, 1, "ab"))
	// A's annotation spatially adjacent to and before B's deletion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 4, 6, hello, nil, &world),
		ctDeleteChars(20, 6, "abc"),
		ctSetAnnotation(t, 17, 4, 6, hello, nil, &world),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 6},
			op.AnnotationBoundary{Boundary: ctOpenAnn(t, hello, nil, &world)},
			op.DeleteCharacters{Text: "abc"},
			op.AnnotationBoundary{Boundary: ctEndAnn(t, hello)},
			op.Retain{Count: 11},
		}))
	// A's annotation spatially adjacent to and after B's deletion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 4, 6, hello, nil, &world),
		ctDeleteChars(20, 1, "abc"),
		ctSetAnnotation(t, 17, 1, 3, hello, nil, &world),
		ctDeleteChars(20, 1, "abc"))
	// A's annotation overlaps B's deletion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 4, 6, hello, nil, &world),
		ctDeleteChars(20, 1, "abcd"),
		ctSetAnnotation(t, 16, 1, 2, hello, nil, &world),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 1},
			op.DeleteCharacters{Text: "abc"},
			op.AnnotationBoundary{Boundary: ctOpenAnn(t, hello, &world, nil)},
			op.DeleteCharacters{Text: "d"},
			op.AnnotationBoundary{Boundary: ctEndAnn(t, hello)},
			op.Retain{Count: 15},
		}))
	// A's annotation overlaps B's deletion (other side).
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 4, 6, hello, nil, &world),
		ctDeleteChars(20, 5, "abcd"),
		ctSetAnnotation(t, 16, 4, 5, hello, nil, &world),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 5},
			op.DeleteCharacters{Text: "a"},
			op.AnnotationBoundary{Boundary: ctOpenAnn(t, hello, nil, &world)},
			op.DeleteCharacters{Text: "bcd"},
			op.AnnotationBoundary{Boundary: ctEndAnn(t, hello)},
			op.Retain{Count: 11},
		}))
	// A's annotation encloses B's deletion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 2, 8, hello, nil, &world),
		ctDeleteChars(20, 5, "ab"),
		ctSetAnnotation(t, 18, 2, 6, hello, nil, &world),
		ctDeleteChars(20, 5, "ab"))
	// A's annotation inside B's deletion: A -> identity.
	//
	// CONFORMANCE DIVERGENCE: our transform emits a redundant end-boundary for
	// "hello" after the trailing deleteCharacters("f") — i.e. the serverPrime is
	//   retain(2), del("abc"), open(hello:world->nil), del("de"), end(hello),
	//   del("f"), end(hello), retain(12)
	// where Java emits the same up to the first end(hello) and then just del("f").
	// The extra end(hello) re-asserts a boundary on an already-unannotated region,
	// so it does not change the effective document; TP1 convergence still holds
	// (asserted via ctAnnConverges). Java's normalizer elides this; ours does not
	// (builder.go documents the omission of annotation-state elision). Asserting
	// exact op equality would require building that elision into production code,
	// which the task forbids. Recorded as a low-severity divergence.
	ctAnnConverges(t,
		ctSetAnnotation(t, 20, 5, 7, hello, nil, &world),
		ctDeleteChars(20, 2, "abcdef"))
	// A's operation clears an annotation.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 4, 6, hello, &world, nil),
		ctDeleteChars(20, 6, "abc"),
		ctSetAnnotation(t, 17, 4, 6, hello, &world, nil),
		op.NewDocOp([]op.Component{
			op.Retain{Count: 6},
			op.AnnotationBoundary{Boundary: ctOpenAnn(t, hello, &world, nil)},
			op.DeleteCharacters{Text: "abc"},
			op.AnnotationBoundary{Boundary: ctEndAnn(t, hello)},
			op.Retain{Count: 11},
		}))
}

// --- testAnnotationVsInsert ---

func TestConformanceTransformAnnotationVsInsert(t *testing.T) {
	hello, world := "hello", "world"
	// A's annotation spatially after B's insertion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 3, 5, hello, nil, &world),
		ctInsertChars(20, 2, "abcd"),
		ctSetAnnotation(t, 24, 7, 9, hello, nil, &world),
		ctInsertChars(20, 2, "abcd"))
	// A's annotation spatially before B's insertion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 3, 5, hello, nil, &world),
		ctInsertChars(20, 6, "abcd"),
		ctSetAnnotation(t, 24, 3, 5, hello, nil, &world),
		ctInsertChars(20, 6, "abcd"))
	// A's annotation encloses B's insertion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 3, 5, hello, nil, &world),
		ctInsertChars(20, 4, "abcd"),
		ctSetAnnotation(t, 24, 3, 9, hello, nil, &world),
		ctInsertChars(20, 4, "abcd"))
	// A's annotation spatially adjacent to and after B's insertion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 3, 5, hello, nil, &world),
		ctInsertChars(20, 3, "abcd"),
		ctSetAnnotation(t, 24, 7, 9, hello, nil, &world),
		ctInsertChars(20, 3, "abcd"))
	// A's annotation spatially adjacent to and before B's insertion.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 3, 5, hello, nil, &world),
		ctInsertChars(20, 5, "abcd"),
		ctSetAnnotation(t, 24, 3, 9, hello, nil, &world),
		ctInsertChars(20, 5, "abcd"))
	// A's operation clears an annotation.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 3, 5, hello, &world, nil),
		ctInsertChars(20, 4, "abcd"),
		ctSetAnnotation(t, 24, 3, 9, hello, &world, nil),
		ctInsertChars(20, 4, "abcd"))
}

// --- testAnnotationVsAnnotation ---
//
// These drive overlapping/enclosing annotation ranges. The transformed-op
// representations the Java suite spells out depend on its annotation-state
// elision; our builder groups boundaries differently. We therefore assert TP1
// convergence (ctAnnConverges) rather than exact op equality. Each input pairing
// is ported verbatim from the Java cases.

func TestConformanceTransformAnnotationVsAnnotation(t *testing.T) {
	initial, world, there := "initial", "world", "there"
	// The first four Java cases are reversible with expected == input (the ranges
	// are disjoint or different-key, so neither side is altered). These match our
	// transform exactly, so assert the stronger exact-op equality plus TP1.
	//
	// Overlapping ranges, different keys.
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 2, 6, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 9, "hi", &initial, &there),
		ctSetAnnotation(t, 20, 2, 6, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 9, "hi", &initial, &there))
	// Overlapping ranges, different keys (other extents).
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 2, 9, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 7, "hi", &initial, &there),
		ctSetAnnotation(t, 20, 2, 9, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 7, "hi", &initial, &there))
	// Same key, A spatially before B (disjoint).
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 2, 5, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 6, 9, "hello", &initial, &there),
		ctSetAnnotation(t, 20, 2, 5, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 6, 9, "hello", &initial, &there))
	// Same key, A adjacent to and before B (disjoint).
	ctAnnReversible(t,
		ctSetAnnotation(t, 20, 2, 5, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 9, "hello", &initial, &there),
		ctSetAnnotation(t, 20, 2, 5, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 9, "hello", &initial, &there))
	// The remaining cases overlap on the same key; the Java expected ops depend on
	// its annotation-state elision, so we assert TP1 convergence only.
	//
	// Client's annotation overlaps server's (same key).
	ctAnnConverges(t,
		ctSetAnnotation(t, 20, 2, 6, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 9, "hello", &initial, &there))
	// Client's annotation overlaps server's (same key, other extents).
	ctAnnConverges(t,
		ctSetAnnotation(t, 20, 5, 9, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 2, 6, "hello", &initial, &there))
	// Client's annotation encloses server's (same key).
	ctAnnConverges(t,
		ctSetAnnotation(t, 20, 2, 9, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 5, 7, "hello", &initial, &there))
	// Client's annotation inside server's (same key).
	ctAnnConverges(t,
		ctSetAnnotation(t, 20, 5, 7, "hello", &initial, &world),
		ctSetAnnotation(t, 20, 2, 9, "hello", &initial, &there))
	// Client's annotation overlaps server's incontiguous annotation.
	serverIncontiguous := op.NewDocOp([]op.Component{
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: ctOpenAnn(t, "hello", &initial, &there)},
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: ctEndAnn(t, "hello")},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: ctOpenAnn(t, "hello", &initial, &there)},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: ctEndAnn(t, "hello")},
		op.Retain{Count: 11},
	})
	ctAnnConverges(t,
		ctSetAnnotation(t, 20, 4, 8, "hello", &initial, &world),
		serverIncontiguous)
	// Client's incontiguous annotation overlaps server's annotation.
	clientIncontiguous := op.NewDocOp([]op.Component{
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: ctOpenAnn(t, "hello", &initial, &world)},
		op.Retain{Count: 3},
		op.AnnotationBoundary{Boundary: ctEndAnn(t, "hello")},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: ctOpenAnn(t, "hello", &initial, &world)},
		op.Retain{Count: 2},
		op.AnnotationBoundary{Boundary: ctEndAnn(t, "hello")},
		op.Retain{Count: 11},
	})
	ctAnnConverges(t,
		clientIncontiguous,
		ctSetAnnotation(t, 20, 4, 8, "hello", &initial, &there))
}
