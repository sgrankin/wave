package waveop_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

func TestWaveletDelta(t *testing.T) {
	alice := pid(t, "alice@example.com")
	target := version.NewHashedVersion(5, []byte("hash"))
	d := op.NewDocOp([]op.Component{op.Characters{Text: "x"}})
	ops := []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b1", BlipOp: blipContent(ctx(alice), d)},
		waveop.AddParticipant{Ctx: ctx(alice), Participant: pid(t, "bob@example.com")},
	}
	delta := waveop.NewWaveletDelta(alice, target, ops)

	if delta.Author() != alice {
		t.Errorf("author = %v, want alice", delta.Author())
	}
	if delta.TargetVersion().Version() != 5 {
		t.Errorf("target version = %d, want 5", delta.TargetVersion().Version())
	}
	if delta.Len() != 2 {
		t.Errorf("len = %d, want 2", delta.Len())
	}
	if got := delta.ResultingVersion(); got != 7 {
		t.Errorf("resulting version = %d, want 7 (5 + 2 ops)", got)
	}
}

func TestWaveletDeltaIsImmutable(t *testing.T) {
	alice := pid(t, "alice@example.com")
	ops := []waveop.Operation{waveop.NoOp{Ctx: ctx(alice)}}
	delta := waveop.NewWaveletDelta(alice, version.NewHashedVersion(0, []byte("h")), ops)
	// Mutating the source slice must not affect the delta.
	ops[0] = waveop.AddParticipant{Ctx: ctx(alice), Participant: alice}
	if _, ok := delta.Op(0).(waveop.NoOp); !ok {
		t.Error("delta shares its operations slice with the caller")
	}
	// Mutating the returned slice must not affect the delta either.
	got := delta.Ops()
	got[0] = waveop.AddParticipant{Ctx: ctx(alice), Participant: alice}
	if _, ok := delta.Op(0).(waveop.NoOp); !ok {
		t.Error("Ops() exposes the internal slice")
	}
}
