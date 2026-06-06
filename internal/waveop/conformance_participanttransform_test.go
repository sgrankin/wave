package waveop_test

// Ported from
// wave/src/test/java/org/waveprotocol/wave/model/operation/wave/ParticipantTransformTest.java
//
// Transformations of operations that add or remove participants (and a blip
// mutation authored by a participant being removed). Java's checkTransform
// asserts structural equality of the transformed ops ignoring context, which we
// express with waveop.EqualOps (the package's context-ignoring payload
// comparison). RemovedAuthorException -> *waveop.RemovedAuthorError;
// TransformException -> *waveop.TransformError.

import (
	"errors"
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

// ptCtx builds a context for the participant-transform fixtures. Java uses
// timestamp=1, increment=1 for all of them; only the creator varies.
func ptCtx(t *testing.T, creator string) waveop.Context {
	t.Helper()
	return waveop.Context{Creator: pid(t, creator), Timestamp: 1, VersionIncrement: 1}
}

// checkTransformEqual asserts Transform(client, server) succeeds and yields ops
// payload-equal to (wantClient, wantServer). Mirrors Java checkTransform.
func checkTransformEqual(t *testing.T, client, server, wantClient, wantServer waveop.Operation) {
	t.Helper()
	gotC, gotS, err := waveop.Transform(client, server)
	if err != nil {
		t.Fatalf("Transform(%T, %T): unexpected error %v", client, server, err)
	}
	if !waveop.EqualOps([]waveop.Operation{gotC}, []waveop.Operation{wantClient}) {
		t.Errorf("transformed client = %v, want %v", gotC, wantClient)
	}
	if !waveop.EqualOps([]waveop.Operation{gotS}, []waveop.Operation{wantServer}) {
		t.Errorf("transformed server = %v, want %v", gotS, wantServer)
	}
}

// checkIdentityTransform asserts the transform leaves both ops unchanged.
func checkIdentityTransform(t *testing.T, client, server waveop.Operation) {
	t.Helper()
	checkTransformEqual(t, client, server, client, server)
}

func ptFixtures(t *testing.T) (remove1a, remove2a, remove2b, add1a, add2a, add2b, noop1, noop2, mutation waveop.Operation) {
	t.Helper()
	ctx1 := ptCtx(t, "p1@google.com")
	ctx2 := ptCtx(t, "p2@google.com")
	ctxA := ptCtx(t, "a@google.com")
	a := pid(t, "a@google.com")
	b := pid(t, "b@google.com")
	remove1a = waveop.RemoveParticipant{Ctx: ctx1, Participant: a}
	remove2a = waveop.RemoveParticipant{Ctx: ctx2, Participant: a}
	remove2b = waveop.RemoveParticipant{Ctx: ctx2, Participant: b}
	add1a = waveop.AddParticipant{Ctx: ctx1, Participant: a}
	add2a = waveop.AddParticipant{Ctx: ctx2, Participant: a}
	add2b = waveop.AddParticipant{Ctx: ctx2, Participant: b}
	noop1 = waveop.NoOp{Ctx: ctx1}
	noop2 = waveop.NoOp{Ctx: ctx2}
	mutation = waveop.WaveletBlipOperation{
		BlipID: "dummy",
		BlipOp: waveop.BlipContentOperation{
			Ctx:       ctxA,
			ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "x"}}),
		},
	}
	return
}

func TestConformanceParticipantTransform_RemovedAuthor(t *testing.T) {
	_, remove2a, _, _, _, _, _, _, mutation := ptFixtures(t)
	// A mutation authored by a@google.com transformed against a removal of
	// a@google.com raises RemovedAuthorError.
	_, _, err := waveop.Transform(mutation, remove2a)
	if err == nil {
		t.Fatal("expected RemovedAuthorError, got nil")
	}
	var rae *waveop.RemovedAuthorError
	if !errors.As(err, &rae) {
		t.Errorf("want *waveop.RemovedAuthorError, got %T: %v", err, err)
	}
}

func TestConformanceParticipantTransform_NoException(t *testing.T) {
	_, remove2a, remove2b, _, _, _, _, _, mutation := ptFixtures(t)
	// remove2a vs mutation: server-removal direction is the client here, so no
	// author-removal check fires (Java checks only when the *server* removes the
	// client op's author). Identity.
	checkIdentityTransform(t, remove2a, mutation)
	// mutation vs remove2b: server removes b, mutation authored by a. Identity.
	checkIdentityTransform(t, mutation, remove2b)
	checkIdentityTransform(t, remove2b, mutation)
}

func TestConformanceParticipantTransform_AdditionVsAddition(t *testing.T) {
	_, _, _, add1a, add2a, add2b, noop1, noop2, _ := ptFixtures(t)
	// Adding the same participant concurrently collapses both sides to NoOp.
	checkTransformEqual(t, add1a, add2a, noop1, noop2)
	// Adding different participants is independent.
	checkIdentityTransform(t, add1a, add2b)
}

func TestConformanceParticipantTransform_AdditionVsRemoval(t *testing.T) {
	_, remove2a, remove2b, add1a, _, _, _, _, _ := ptFixtures(t)
	// Concurrent add and remove of the same participant is a conflict.
	checkTransformThrowsTransformError(t, add1a, remove2a)
	checkTransformThrowsTransformError(t, remove2a, add1a)
	// Add and remove of different participants are independent.
	checkIdentityTransform(t, add1a, remove2b)
	checkIdentityTransform(t, remove2b, add1a)
}

func TestConformanceParticipantTransform_RemovalVsRemoval(t *testing.T) {
	remove1a, remove2a, remove2b, _, _, _, noop1, noop2, _ := ptFixtures(t)
	// Removing the same participant concurrently collapses to NoOp.
	checkTransformEqual(t, remove1a, remove2a, noop1, noop2)
	// Removing different participants is independent.
	checkIdentityTransform(t, remove1a, remove2b)
}

// checkTransformThrowsTransformError asserts the transform fails with a
// *waveop.TransformError that is NOT the RemovedAuthorError subtype (Java
// asserts the exact class TransformException, distinct from
// RemovedAuthorException).
func checkTransformThrowsTransformError(t *testing.T, client, server waveop.Operation) {
	t.Helper()
	_, _, err := waveop.Transform(client, server)
	if err == nil {
		t.Fatalf("Transform(%T, %T): expected TransformError, got nil", client, server)
	}
	var te *waveop.TransformError
	if !errors.As(err, &te) {
		t.Errorf("want *waveop.TransformError, got %T: %v", err, err)
	}
	var rae *waveop.RemovedAuthorError
	if errors.As(err, &rae) {
		t.Errorf("want plain TransformError, got RemovedAuthorError (Java distinguishes the exact class)")
	}
}
