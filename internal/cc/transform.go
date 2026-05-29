package cc

import "github.com/sgrankin/wave/internal/waveop"

// TransformToHead validates a client delta against the history and transforms it
// to target the current head version, returning a delta ready to apply
// (targeting the current hashed version). It implements the server-side
// version/hash check plus onClientDelta (spec §Server-side concurrency control).
//
// Errors carry a ResponseCode: VersionError if the delta targets a future or
// unknown version, InvalidOperation if a transform fails.
func TransformToHead(h DeltaHistory, delta waveop.WaveletDelta) (waveop.WaveletDelta, error) {
	target := delta.TargetVersion()
	current := h.CurrentVersion()

	if target.Version() > current {
		return waveop.WaveletDelta{}, &Error{Code: VersionError, Msg: "delta targets a future version (client ahead of server)"}
	}
	// The target must be a version the history actually reached, with a matching
	// hash — this catches a same-version hash mismatch (VERSION_ERROR).
	if !h.HasSignature(target) {
		return waveop.WaveletDelta{}, &Error{Code: VersionError, Msg: "delta target version/hash not found in history"}
	}

	ops := delta.Ops()
	at := target.Version()
	for at < current {
		serverDelta, ok := h.DeltaStartingAt(at)
		if !ok {
			// The target signature is known (HasSignature passed) but its delta
			// is no longer retained: the history prefix was pruned/GC'd. This is
			// the recoverable TOO_OLD case (client reconnects and re-transforms)
			// — distinct from a gap discovered mid-walk, which means the chain
			// broke between two known signatures (a real internal inconsistency).
			if at == target.Version() {
				return waveop.WaveletDelta{}, &Error{Code: TooOld, Msg: "target version no longer retained in history; reconnect"}
			}
			return waveop.WaveletDelta{}, &Error{Code: InternalError, Msg: "history chain broken between known signatures"}
		}
		clientOps, _, err := transformOpLists(ops, serverDelta.Ops)
		if err != nil {
			return waveop.WaveletDelta{}, &Error{Code: InvalidOperation, Msg: "transform against concurrent delta failed", Err: err}
		}
		ops = clientOps
		at = serverDelta.ResultingVersion.Version()
	}

	// ops now target the current head; package them against the current signature.
	return waveop.NewWaveletDelta(delta.Author(), h.CurrentHashedVersion(), ops), nil
}

// transformOpLists transforms a client delta's operations against a server
// delta's operations, returning both transformed lists (the DeltaPair
// transform). Each client op is transformed past every server op left-to-right,
// and symmetrically each server op past every client op (spec §DeltaPair
// transform).
//
// The identical-delta nullification (where two equal op lists cancel, producing
// an empty client list and version-update ops on the server side) is not
// special-cased here: server transform-to-head consumes only the client side,
// for which the pairwise transform of distinct concurrent deltas is correct.
//
// Double-submit dedup (a client resending a delta already in history) is NOT
// done here — it is an author+ops equality check the submission handler performs
// (server.WaveletContainer.Submit, via waveop.EqualOps against the delta already
// applied at the resend's target version), returning the original applied
// version idempotently. Bolting it onto transformOpLists would be wrong: by the
// time a duplicate reached here it would already be transformed past its twin
// and the identity lost.
func transformOpLists(client, server []waveop.Operation) (clientPrime, serverPrime []waveop.Operation, err error) {
	serverOps := append([]waveop.Operation(nil), server...)
	for _, c := range client {
		ci := c
		next := make([]waveop.Operation, len(serverOps))
		for j, s := range serverOps {
			cPrime, sPrime, terr := waveop.Transform(ci, s)
			if terr != nil {
				return nil, nil, terr
			}
			ci = cPrime
			next[j] = sPrime
		}
		clientPrime = append(clientPrime, ci)
		serverOps = next
	}
	return clientPrime, serverOps, nil
}
