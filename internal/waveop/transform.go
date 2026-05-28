package waveop

import (
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
)

// TransformError signals that two concurrent operations are structurally
// incompatible (ports TransformException).
type TransformError struct {
	Msg string
	Err error
}

func (e *TransformError) Error() string {
	if e.Err != nil {
		return "waveop: " + e.Msg + ": " + e.Err.Error()
	}
	return "waveop: " + e.Msg
}

func (e *TransformError) Unwrap() error { return e.Err }

// RemovedAuthorError signals the specific case where the client operation's
// author was concurrently removed by the server (ports RemovedAuthorException,
// a TransformException subclass).
type RemovedAuthorError struct {
	Participant id.ParticipantID
}

func (e *RemovedAuthorError) Error() string {
	return "waveop: operation author concurrently removed: " + e.Participant.Address()
}

// Transform reconciles a concurrent client and server wavelet operation into
// (clientOp', serverOp') such that applying server then clientOp' converges with
// applying client then serverOp' (ports Transform.transform). It dispatches on
// operation type: same-blip operations recurse into the DocOp transform;
// different blips and unrelated participant operations are identity; concurrent
// idempotent participant operations collapse to NoOp; and concurrent add+remove
// or author-removal raise errors.
func Transform(clientOp, serverOp Operation) (Operation, Operation, error) {
	cBlip, cIsBlip := clientOp.(WaveletBlipOperation)
	sBlip, sIsBlip := serverOp.(WaveletBlipOperation)
	if cIsBlip && sIsBlip {
		if cBlip.BlipID == sBlip.BlipID {
			ct, st, err := transformBlip(cBlip.BlipOp, sBlip.BlipOp)
			if err != nil {
				return nil, nil, err
			}
			return WaveletBlipOperation{BlipID: cBlip.BlipID, BlipOp: ct},
				WaveletBlipOperation{BlipID: sBlip.BlipID, BlipOp: st}, nil
		}
		// Different blips never conflict.
		return clientOp, serverOp, nil
	}

	// At least one side is a participant or no-op operation.
	//
	// The author-removal check is intentionally SERVER-SIDE ONLY: it fires only
	// when the server removes the client op's author, never the reverse (Java
	// parity — checkParticipantRemoval is invoked solely from the server-remove
	// branch). It must also run BEFORE the same-participant collapse below, so
	// that removing one's own creator still raises RemovedAuthorError.
	switch s := serverOp.(type) {
	case RemoveParticipant:
		if err := checkParticipantRemoval(s, clientOp); err != nil {
			return nil, nil, err
		}
		switch c := clientOp.(type) {
		case RemoveParticipant:
			if c.Participant == s.Participant {
				return NoOp{Ctx: c.Ctx}, NoOp{Ctx: s.Ctx}, nil
			}
		case AddParticipant:
			if err := checkParticipantRemovalAndAddition(s, c); err != nil {
				return nil, nil, err
			}
		}
	case AddParticipant:
		switch c := clientOp.(type) {
		case AddParticipant:
			if c.Participant == s.Participant {
				return NoOp{Ctx: c.Ctx}, NoOp{Ctx: s.Ctx}, nil
			}
		case RemoveParticipant:
			if err := checkParticipantRemovalAndAddition(c, s); err != nil {
				return nil, nil, err
			}
		}
	}
	// Identity transform by default.
	return clientOp, serverOp, nil
}

func transformBlip(clientOp, serverOp BlipOperation) (BlipOperation, BlipOperation, error) {
	c, cok := clientOp.(BlipContentOperation)
	s, sok := serverOp.(BlipContentOperation)
	if cok && sok {
		ct, st, err := op.Transform(c.ContentOp, s.ContentOp)
		if err != nil {
			return nil, nil, &TransformError{Msg: "blip content transform failed", Err: err}
		}
		// Java rewraps via the two-arg BlipContentOperation constructor, which
		// resets the contributor-update method to ADD (discarding the original).
		// We match that — the original is the behavioral source of truth — rather
		// than preserving c.Method/s.Method.
		return BlipContentOperation{Ctx: c.Ctx, ContentOp: ct, Method: ContributorAdd},
			BlipContentOperation{Ctx: s.Ctx, ContentOp: st, Method: ContributorAdd}, nil
	}
	// Other blip-operation pairs have identity transforms.
	return clientOp, serverOp, nil
}

// checkParticipantRemoval raises RemovedAuthorError if the server removes the
// participant who authored the client operation.
func checkParticipantRemoval(rp RemoveParticipant, other Operation) error {
	if rp.Participant == other.Context().Creator {
		return &RemovedAuthorError{Participant: rp.Participant}
	}
	return nil
}

// checkParticipantRemovalAndAddition raises TransformError if the same
// participant is concurrently added and removed.
func checkParticipantRemovalAndAddition(rp RemoveParticipant, ap AddParticipant) error {
	if rp.Participant == ap.Participant {
		return &TransformError{Msg: "concurrent add and remove of participant " + rp.Participant.Address()}
	}
	return nil
}
