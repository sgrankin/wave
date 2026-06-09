package transport

import (
	"testing"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// removalCutoff is the deterministic core of the resync-tail membership boundary: a
// removed participant must receive a resync tail only THROUGH their removal, never the
// deltas applied after it (which can ride the tail when the removal raced the membership
// check — see handleResync). The integration guarantee is covered by
// TestWebSocketRemovedParticipantStreamCutOff; this pins the cutoff arithmetic, which is
// otherwise only reachable via that rare race.
func TestRemovalCutoff(t *testing.T) {
	mkPID := func(addr string) id.ParticipantID {
		p, err := id.NewParticipantID(addr)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	bob := mkPID("bob@example.com")
	carol := mkPID("carol@example.com")

	upd := func(v uint64, ops ...waveop.Operation) server.WaveletUpdate {
		rv := version.NewHashedVersion(v, nil)
		return server.WaveletUpdate{
			Delta:            cc.TransformedWaveletDelta{ResultingVersion: rv, Ops: ops},
			ResultingVersion: rv,
		}
	}
	remove := func(p id.ParticipantID) waveop.Operation { return waveop.RemoveParticipant{Participant: p} }
	edit := upd(0) // a benign, op-less delta (does not touch any participant)

	cases := []struct {
		name    string
		tail    []server.WaveletUpdate
		p       *id.ParticipantID
		wantN   int
		wantCut bool
	}{
		{"nil participant → whole tail, no cut", []server.WaveletUpdate{edit, upd(6, remove(bob))}, nil, 2, false},
		{"no self-removal → whole tail", []server.WaveletUpdate{edit, upd(6, remove(carol))}, &bob, 2, false},
		{"removal last → through it, cut", []server.WaveletUpdate{edit, upd(6, remove(bob))}, &bob, 2, true},
		{"removal first, post-removal edit dropped", []server.WaveletUpdate{upd(5, remove(bob)), edit}, &bob, 1, true},
		{"removal middle → drop the tail after it", []server.WaveletUpdate{edit, upd(5, remove(bob)), edit, edit}, &bob, 2, true},
		{"empty tail", nil, &bob, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, cut := removalCutoff(c.tail, c.p)
			if n != c.wantN || cut != c.wantCut {
				t.Fatalf("removalCutoff = (%d, %v), want (%d, %v)", n, cut, c.wantN, c.wantCut)
			}
		})
	}
}
