package storage

import "github.com/sgrankin/wave/internal/id"

// ArchiveStore persists, per participant, which waves they have archived out of their
// inbox — private per-user state, like ReadStateStore. An archived wave is hidden from
// the default inbox view but is still a full member wave: openable, still receiving
// deltas. Archiving is a personal inbox preference, not a membership change. Global,
// keyed by (participant, wavelet).
type ArchiveStore interface {
	// SetArchived sets whether the participant has archived the wavelet out of their inbox.
	SetArchived(participant id.ParticipantID, wavelet id.WaveletName, archived bool) error
	// ArchivedWaves returns the participant's archived wavelets, keyed by the serialized
	// wavelet name (WaveletName.Serialize()).
	ArchivedWaves(participant id.ParticipantID) (map[string]bool, error)
}
