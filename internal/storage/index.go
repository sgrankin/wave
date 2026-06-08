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
	// SetWaveletMeta records a wavelet's digest projection — creator, last-modified
	// version + wall-clock time (for filtering/ordering), and the precomputed title
	// and snippet — so the inbox and search digests are served entirely from the
	// index without loading the wavelet. title/snippet are derived from the root blip
	// at write time; "" when it has none.
	SetWaveletMeta(name id.WaveletName, meta WaveletMeta) error
	// SetBlipText records (replacing any prior text) the searchable plain text of
	// a blip.
	SetBlipText(name id.WaveletName, blipID, text string) error
	// DeleteWaveletIndex removes all index rows for a wavelet (for rebuild/delete).
	DeleteWaveletIndex(name id.WaveletName) error
	// InboxWavelets returns the wavelets a participant currently belongs to.
	// Ordering is the query layer's concern.
	InboxWavelets(participant id.ParticipantID) ([]id.WaveletName, error)
	// InboxDigests returns digest projections for the participant's inbox, ordered
	// most-recently-modified first and capped at limit (<= 0 ⇒ no cap). It is served
	// entirely from the index — no wavelet is loaded — so polling the inbox never
	// pins waves in the in-memory cache.
	InboxDigests(participant id.ParticipantID, limit int) ([]WaveDigest, error)
	// IsParticipant reports whether a participant currently belongs to a wavelet
	// (the access-control predicate for reads/writes scoped to a wavelet).
	IsParticipant(name id.WaveletName, participant id.ParticipantID) (bool, error)
	// Search returns digest projections matching q (always scoped to q.Participant's
	// inbox), served from the index like InboxDigests.
	Search(q SearchQuery) ([]WaveDigest, error)
}

// WaveletMeta is the digest projection recorded per wavelet on commit.
type WaveletMeta struct {
	Creator             id.ParticipantID
	LastModifiedVersion uint64
	LastModifiedTime    int64 // wall-clock, as the wavelet reports it
	Title               string
	Snippet             string
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

// WaveDigest is one wave's summary for a list view (inbox or search results),
// projected entirely from the index. Creator and Participants are addresses (the
// stored form); the query layer turns this into the JSON the client renders.
type WaveDigest struct {
	Wavelet          id.WaveletName // identity
	Creator          string         // creator address
	Title            string         // first non-empty line of the root blip
	Snippet          string         // truncated plain text of the root blip
	Participants     []string       // participant addresses
	Version          uint64         // last-modified version
	LastModifiedTime int64          // wall-clock, for recency ordering
}
