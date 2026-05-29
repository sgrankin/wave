package storage

import "github.com/sgrankin/wave/internal/id"

// IndexStore maintains the derived per-user read index — the inbox (which
// wavelets a participant belongs to), per-wavelet metadata (creator,
// last-modified) for filtering/ordering, and full-text blip search. It is a
// rebuildable cache, never authoritative: it can be dropped and rebuilt by
// replaying the delta log, so it is maintained off the commit path rather than
// inside the delta-append transaction.
type IndexStore interface {
	// SetWaveletParticipants replaces the recorded participant set for a wavelet.
	SetWaveletParticipants(name id.WaveletName, participants []id.ParticipantID) error
	// SetWaveletMeta records a wavelet's creator and last-modified version (for
	// creator: filtering and orderby).
	SetWaveletMeta(name id.WaveletName, creator id.ParticipantID, lastModifiedVersion uint64) error
	// SetBlipText records (replacing any prior text) the searchable plain text of
	// a blip.
	SetBlipText(name id.WaveletName, blipID, text string) error
	// DeleteWaveletIndex removes all index rows for a wavelet (for rebuild/delete).
	DeleteWaveletIndex(name id.WaveletName) error
	// InboxWavelets returns the wavelets a participant currently belongs to.
	// Ordering is the query layer's concern.
	InboxWavelets(participant id.ParticipantID) ([]id.WaveletName, error)
	// Search returns wavelets matching q (always scoped to q.Participant's inbox).
	Search(q SearchQuery) ([]SearchResult, error)
}

// SearchQuery is a parsed search request, always scoped to the searcher's inbox.
type SearchQuery struct {
	Participant         id.ParticipantID   // inbox scope (required)
	Terms               []string           // free-text terms, ANDed; empty = no text filter
	With                []id.ParticipantID // wavelet must also include these participants
	Creator             *id.ParticipantID  // wavelet creator filter (nil = any)
	OrderByModifiedDesc bool               // order by last-modified version, newest first
	Limit               int                // max results (<= 0 = no limit)
}

// SearchResult is one matched wavelet.
type SearchResult struct {
	Wavelet             id.WaveletName
	LastModifiedVersion uint64
}
