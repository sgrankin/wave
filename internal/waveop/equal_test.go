package waveop_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

func mkpid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func chars(s string) op.DocOp { return op.NewDocOp([]op.Component{op.Characters{Text: s}}) }

func blipOp(ctx waveop.Context, blipID string, content op.DocOp) waveop.Operation {
	return waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: content}}
}

func TestEqualOpsIgnoresContext(t *testing.T) {
	alice := mkpid(t, "alice@example.com")
	// Same payload, different context (timestamp/version) → equal.
	a := []waveop.Operation{blipOp(waveop.Context{Creator: alice, Timestamp: 100, VersionIncrement: 1}, "b", chars("hi"))}
	b := []waveop.Operation{blipOp(waveop.Context{Creator: alice, Timestamp: 999, VersionIncrement: 2}, "b", chars("hi"))}
	if !waveop.EqualOps(a, b) {
		t.Error("ops with equal payload but different context should be equal")
	}
}

func TestEqualOpsDistinguishesPayload(t *testing.T) {
	alice := mkpid(t, "alice@example.com")
	bob := mkpid(t, "bob@example.com")
	ctx := waveop.Context{Creator: alice, Timestamp: 100, VersionIncrement: 1}

	base := []waveop.Operation{blipOp(ctx, "b", chars("hi"))}
	cases := map[string][]waveop.Operation{
		"different content": {blipOp(ctx, "b", chars("bye"))},
		"different blip id": {blipOp(ctx, "c", chars("hi"))},
		"different length":  {blipOp(ctx, "b", chars("hi")), blipOp(ctx, "b", chars("hi"))},
		"different op type": {waveop.AddParticipant{Ctx: ctx, Participant: alice}},
	}
	for name, other := range cases {
		if waveop.EqualOps(base, other) {
			t.Errorf("%s: should not be equal to base", name)
		}
	}

	// Participant ops compare by participant.
	addA := []waveop.Operation{waveop.AddParticipant{Ctx: ctx, Participant: alice}}
	addA2 := []waveop.Operation{waveop.AddParticipant{Ctx: waveop.Context{Creator: bob, Timestamp: 5}, Participant: alice}}
	addB := []waveop.Operation{waveop.AddParticipant{Ctx: ctx, Participant: bob}}
	if !waveop.EqualOps(addA, addA2) {
		t.Error("AddParticipant with same participant should be equal regardless of context")
	}
	if waveop.EqualOps(addA, addB) {
		t.Error("AddParticipant with different participants should differ")
	}
	// Add vs Remove of the same participant differ.
	rmA := []waveop.Operation{waveop.RemoveParticipant{Ctx: ctx, Participant: alice}}
	if waveop.EqualOps(addA, rmA) {
		t.Error("Add and Remove of the same participant should differ")
	}
}
