package transport

import (
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
)

// AccessChecker decides whether an authenticated participant may Open (and thus
// read and follow) a wavelet. It is the membership/authorization predicate
// checked once at Open, before the session subscribes — distinct from delta
// authorship (enforced per-submit). A nil AccessChecker on the Server allows all
// opens (dev-permissive). It mirrors attachapi.AccessChecker; the interface is
// redeclared here rather than shared so the two subsystems evolve independently.
type AccessChecker interface {
	// CanAccess reports whether participant may open wavelet. An error signals
	// that the check itself failed (distinct from a clean "not allowed").
	CanAccess(participant id.ParticipantID, wavelet id.WaveletName) (bool, error)
}

// MembershipChecker is the strict AccessChecker: a participant may open a wavelet
// only if they are in its participant set. A not-yet-created wavelet (no deltas
// applied) is openable by anyone — open-or-create — so the first opener can
// create and seed it and thereby become its first participant; an existing
// wavelet requires membership (the participants UI is the invite path that lets
// another user in).
type MembershipChecker struct{ WaveMap *server.WaveMap }

// CanAccess reports whether p is a member of the named wavelet (or it does not
// exist yet). The membership read goes through WaveletContainer.HasParticipant,
// which holds the container lock — reading the live wavelet directly would race
// a concurrent Submit mutating the participant set.
func (m MembershipChecker) CanAccess(p id.ParticipantID, name id.WaveletName) (bool, error) {
	c, err := m.WaveMap.Container(name)
	if err != nil {
		return false, err
	}
	exists, created := c.HasParticipant(p)
	if !created {
		return true, nil // never created: open-or-create allows the first opener
	}
	return exists, nil
}
