package storage

import "github.com/sgrankin/wave/internal/id"

// ReadStateStore persists, per participant, how far they have read each wavelet —
// the wavelet version they last marked read. It backs the inbox unread indicator
// (a wave is unread when its current version exceeds the participant's read
// version).
//
// This is a pragmatic dedicated store rather than the full Wave user-data-wavelet
// (UDW) supplement model. Read state is PRIVATE per-user state, not collaborative
// OT data, so a plain keyed table is simpler and sufficient for a single-machine
// deployment; the UDW-as-wavelet model (spec §6) can replace it later if
// cross-device OT read-state or federation is ever wanted. Like AccountStore it is
// global, keyed by (participant, wavelet).
type ReadStateStore interface {
	// SetReadVersion records that the participant has read the wavelet through
	// version. It is monotonic: a version lower than the stored one is ignored.
	SetReadVersion(participant id.ParticipantID, wavelet id.WaveletName, version uint64) error
	// ReadVersions returns all of the participant's read versions, keyed by
	// wavelet name (the WaveletName.Serialize() form), for batch use by the inbox.
	ReadVersions(participant id.ParticipantID) (map[string]uint64, error)
}
