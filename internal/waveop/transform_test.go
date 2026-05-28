package waveop_test

import (
	"errors"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ctx(creator id.ParticipantID) waveop.Context {
	return waveop.Context{Creator: creator, Timestamp: waveop.NoTimestamp, VersionIncrement: 1}
}

func blipContent(c waveop.Context, contentOp op.DocOp) waveop.BlipContentOperation {
	return waveop.BlipContentOperation{Ctx: c, ContentOp: contentOp}
}

// assertBlip checks that got is a WaveletBlipOperation for blipID whose inner
// BlipContentOperation carries want. (Blip operations transitively contain a
// DocOp slice, so they cannot be compared with ==.)
func assertBlip(t *testing.T, got waveop.Operation, blipID string, want op.DocOp) {
	t.Helper()
	w, ok := got.(waveop.WaveletBlipOperation)
	if !ok {
		t.Fatalf("expected WaveletBlipOperation, got %T", got)
	}
	if w.BlipID != blipID {
		t.Errorf("blip id = %q, want %q", w.BlipID, blipID)
	}
	bc, ok := w.BlipOp.(waveop.BlipContentOperation)
	if !ok {
		t.Fatalf("expected BlipContentOperation, got %T", w.BlipOp)
	}
	if !bc.ContentOp.Equal(want) {
		t.Errorf("content op = %v, want %v", bc.ContentOp.Components(), want.Components())
	}
}

func TestTransformSameBlipDelegatesToDocOpTransform(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	// Two edits to the same blip's "ab" document.
	clientDoc := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 1}})
	serverDoc := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "Y"}})

	client := waveop.WaveletBlipOperation{BlipID: "b1", BlipOp: blipContent(c, clientDoc)}
	server := waveop.WaveletBlipOperation{BlipID: "b1", BlipOp: blipContent(c, serverDoc)}

	gotC, gotS, err := waveop.Transform(client, server)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	wantClientDoc, wantServerDoc, err := op.Transform(clientDoc, serverDoc)
	if err != nil {
		t.Fatalf("op.Transform: %v", err)
	}
	cOut := gotC.(waveop.WaveletBlipOperation).BlipOp.(waveop.BlipContentOperation).ContentOp
	sOut := gotS.(waveop.WaveletBlipOperation).BlipOp.(waveop.BlipContentOperation).ContentOp
	if !cOut.Equal(wantClientDoc) {
		t.Errorf("client inner DocOp not transformed:\n got %v\n want %v", cOut.Components(), wantClientDoc.Components())
	}
	if !sOut.Equal(wantServerDoc) {
		t.Errorf("server inner DocOp not transformed:\n got %v\n want %v", sOut.Components(), wantServerDoc.Components())
	}
	if gotC.(waveop.WaveletBlipOperation).BlipID != "b1" {
		t.Errorf("blip id changed")
	}
}

func TestTransformDifferentBlipsAreIdentity(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	d := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 1}})
	client := waveop.WaveletBlipOperation{BlipID: "b1", BlipOp: blipContent(c, d)}
	server := waveop.WaveletBlipOperation{BlipID: "b2", BlipOp: blipContent(c, d)}
	gotC, gotS, err := waveop.Transform(client, server)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	assertBlip(t, gotC, "b1", d)
	assertBlip(t, gotS, "b2", d)
}

func TestTransformConcurrentAddSameParticipant(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	bob := pid(t, "bob@example.com")
	add := waveop.AddParticipant{Ctx: c, Participant: bob}
	gotC, gotS, err := waveop.Transform(add, add)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if _, ok := gotC.(waveop.NoOp); !ok {
		t.Errorf("client add should become NoOp, got %T", gotC)
	}
	if _, ok := gotS.(waveop.NoOp); !ok {
		t.Errorf("server add should become NoOp, got %T", gotS)
	}
}

func TestTransformConcurrentAddDifferentParticipants(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	client := waveop.AddParticipant{Ctx: c, Participant: pid(t, "bob@example.com")}
	server := waveop.AddParticipant{Ctx: c, Participant: pid(t, "carol@example.com")}
	gotC, gotS, err := waveop.Transform(client, server)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if gotC != client || gotS != server {
		t.Errorf("adding different participants should be identity")
	}
}

func TestTransformConcurrentRemoveSameParticipant(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	bob := pid(t, "bob@example.com")
	rm := waveop.RemoveParticipant{Ctx: c, Participant: bob}
	gotC, gotS, err := waveop.Transform(rm, rm)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if _, ok := gotC.(waveop.NoOp); !ok {
		t.Errorf("client remove should become NoOp, got %T", gotC)
	}
	if _, ok := gotS.(waveop.NoOp); !ok {
		t.Errorf("server remove should become NoOp, got %T", gotS)
	}
}

func TestTransformConcurrentAddAndRemoveSameParticipant(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	bob := pid(t, "bob@example.com")
	add := waveop.AddParticipant{Ctx: c, Participant: bob}
	rm := waveop.RemoveParticipant{Ctx: c, Participant: bob}
	// client adds, server removes the same participant.
	if _, _, err := waveop.Transform(add, rm); err == nil {
		t.Error("add vs remove of same participant should error")
	} else {
		var te *waveop.TransformError
		if !errors.As(err, &te) {
			t.Errorf("want TransformError, got %T: %v", err, err)
		}
	}
	// And the mirror: client removes, server adds.
	if _, _, err := waveop.Transform(rm, add); err == nil {
		t.Error("remove vs add of same participant should error")
	} else {
		var te *waveop.TransformError
		if !errors.As(err, &te) {
			t.Errorf("mirror: want TransformError, got %T: %v", err, err)
		}
	}
}

// Identity dispatch cells not covered elsewhere: NoOp inputs and unrelated
// participant pairs must pass through unchanged without error.
func TestTransformIdentityDispatchCells(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	bob := pid(t, "bob@example.com")
	carol := pid(t, "carol@example.com")
	noop := waveop.NoOp{Ctx: c}
	addBob := waveop.AddParticipant{Ctx: c, Participant: bob}
	rmBob := waveop.RemoveParticipant{Ctx: c, Participant: bob}
	rmCarol := waveop.RemoveParticipant{Ctx: c, Participant: carol}

	cases := []struct {
		name           string
		client, server waveop.Operation
	}{
		{"noop vs add", noop, addBob},
		{"add vs noop", addBob, noop},
		{"noop vs noop", noop, noop},
		{"remove vs unrelated add", rmCarol, addBob},
		{"remove vs unrelated remove", rmBob, rmCarol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotC, gotS, err := waveop.Transform(tc.client, tc.server)
			if err != nil {
				t.Fatalf("Transform: %v", err)
			}
			if gotC != tc.client || gotS != tc.server {
				t.Errorf("expected identity transform, got (%v, %v)", gotC, gotS)
			}
		})
	}
}

func TestTransformAuthorConcurrentlyRemoved(t *testing.T) {
	alice := pid(t, "alice@example.com")
	d := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 1}})
	// Client edits a blip as alice; server removes alice concurrently.
	client := waveop.WaveletBlipOperation{BlipID: "b1", BlipOp: blipContent(ctx(alice), d)}
	server := waveop.RemoveParticipant{Ctx: ctx(pid(t, "admin@example.com")), Participant: alice}
	_, _, err := waveop.Transform(client, server)
	if err == nil {
		t.Fatal("removing the client op's author should error")
	}
	var rae *waveop.RemovedAuthorError
	if !errors.As(err, &rae) {
		t.Errorf("want RemovedAuthorError, got %T: %v", err, err)
	}
}

func TestTransformBlipVsUnrelatedAddIsIdentity(t *testing.T) {
	alice := pid(t, "alice@example.com")
	d := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "X"}, op.Retain{Count: 1}})
	client := waveop.WaveletBlipOperation{BlipID: "b1", BlipOp: blipContent(ctx(alice), d)}
	server := waveop.AddParticipant{Ctx: ctx(alice), Participant: pid(t, "bob@example.com")}
	gotC, gotS, err := waveop.Transform(client, server)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	assertBlip(t, gotC, "b1", d)
	if gotS != server { // AddParticipant is comparable
		t.Errorf("unrelated add should be unchanged")
	}
}
