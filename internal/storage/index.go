package storage

import "github.com/sgrankin/wave/internal/id"

// IndexStore maintains the derived per-user read index — the inbox (which
// wavelets a participant belongs to) and, layered on top, full-text search. It
// is a rebuildable cache, never authoritative: it can be dropped and rebuilt by
// replaying the delta log, so it is maintained off the commit path rather than
// inside the delta-append transaction.
type IndexStore interface {
	// SetWaveletParticipants replaces the recorded participant set for a wavelet.
	SetWaveletParticipants(name id.WaveletName, participants []id.ParticipantID) error
	// DeleteWaveletIndex removes all index rows for a wavelet (for rebuild/delete).
	DeleteWaveletIndex(name id.WaveletName) error
	// InboxWavelets returns the wavelets a participant currently belongs to.
	// Ordering is the query layer's concern.
	InboxWavelets(participant id.ParticipantID) ([]id.WaveletName, error)
}
