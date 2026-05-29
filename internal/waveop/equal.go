package waveop

// EqualOps reports whether two operation lists are structurally equal, ignoring
// each operation's Context (creator/timestamp/version) — it compares the
// operation payloads only. It is the double-submit predicate: a resent delta
// carries the same payload but may carry different context metadata (e.g. a
// re-stamped timestamp).
func EqualOps(a, b []Operation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalOp(a[i], b[i]) {
			return false
		}
	}
	return true
}

func equalOp(a, b Operation) bool {
	switch x := a.(type) {
	case WaveletBlipOperation:
		y, ok := b.(WaveletBlipOperation)
		if !ok || x.BlipID != y.BlipID {
			return false
		}
		xc, ok1 := x.BlipOp.(BlipContentOperation)
		yc, ok2 := y.BlipOp.(BlipContentOperation)
		if !ok1 || !ok2 {
			return false
		}
		return xc.Method == yc.Method && xc.ContentOp.Equal(yc.ContentOp)
	case AddParticipant:
		y, ok := b.(AddParticipant)
		return ok && x.Participant == y.Participant
	case RemoveParticipant:
		y, ok := b.(RemoveParticipant)
		return ok && x.Participant == y.Participant
	case NoOp:
		_, ok := b.(NoOp)
		return ok
	default:
		return false
	}
}
