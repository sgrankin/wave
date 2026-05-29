// Package wavelet is the wavelet data model: the live state a sequence of
// wavelet deltas builds up. A wavelet holds its participant set and its blips;
// each blip's content is an insertion-only DocOp (a DocInitialization), and
// operations are applied by composing into that content (Apply = Compose, see
// package op). This is the lean backend model — the Java indexed mutable
// document model is intentionally not ported (see docs/architecture/02-porting-plan.md).
//
// Spec: docs/specs/01-data-model.md §2.2 (Wavelet), §2.4 (Blip), §8 (apply).
package wavelet

import (
	"sort"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
)

// Data is the mutable state of a single wavelet. Its version is always
// hashedVersion.Version(); the participant set is ordered by time of addition
// with no duplicates; an empty/never-written document is absent from blips.
type Data struct {
	waveID           id.WaveID
	waveletID        id.WaveletID
	creator          id.ParticipantID
	creationTime     int64
	lastModifiedTime int64
	hashedVersion    version.HashedVersion
	participants     []id.ParticipantID
	blips            map[string]*BlipData
}

// New creates a wavelet at the given (typically version-zero) hashed version.
// The participant set starts EMPTY: in the delta-based model a wavelet springs
// into existence from its first delta, which carries the AddParticipant(creator)
// op (spec §8.1 step 4 is a delta operation, not pre-existing version-0 state).
// creator is recorded as metadata only — pre-adding it would make the first
// delta's AddParticipant(creator) a duplicate, and break replay.
func New(waveID id.WaveID, waveletID id.WaveletID, creator id.ParticipantID, creationTime int64, hashedVersion version.HashedVersion) *Data {
	return &Data{
		waveID:           waveID,
		waveletID:        waveletID,
		creator:          creator,
		creationTime:     creationTime,
		lastModifiedTime: creationTime,
		hashedVersion:    hashedVersion,
		participants:     nil,
		blips:            map[string]*BlipData{},
	}
}

// WaveID returns the containing wave's id.
func (w *Data) WaveID() id.WaveID { return w.waveID }

// WaveletID returns this wavelet's id.
func (w *Data) WaveletID() id.WaveletID { return w.waveletID }

// Creator returns the participant that created the wavelet.
func (w *Data) Creator() id.ParticipantID { return w.creator }

// CreationTime returns the wavelet creation time (ms since epoch).
func (w *Data) CreationTime() int64 { return w.creationTime }

// LastModifiedTime returns the time of the last applied operation (ms since epoch).
func (w *Data) LastModifiedTime() int64 { return w.lastModifiedTime }

// Version returns the wavelet version (operation count since creation).
func (w *Data) Version() uint64 { return w.hashedVersion.Version() }

// HashedVersion returns the authoritative hashed version.
func (w *Data) HashedVersion() version.HashedVersion { return w.hashedVersion }

// Participants returns the participant set in addition order (a copy).
func (w *Data) Participants() []id.ParticipantID {
	return append([]id.ParticipantID(nil), w.participants...)
}

// HasParticipant reports whether p is a participant.
func (w *Data) HasParticipant(p id.ParticipantID) bool {
	return indexOf(w.participants, p) >= 0
}

// Blip returns the blip with the given id and whether it exists.
func (w *Data) Blip(blipID string) (*BlipData, bool) {
	b, ok := w.blips[blipID]
	return b, ok
}

// BlipIDs returns the ids of all blips, sorted.
func (w *Data) BlipIDs() []string {
	ids := make([]string, 0, len(w.blips))
	for k := range w.blips {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	return ids
}

// BlipData is a blip's wavelet-tracked metadata plus its content document. The
// content is held as an insertion-only DocOp (a DocInitialization).
type BlipData struct {
	id                  string
	author              id.ParticipantID
	contributors        []id.ParticipantID
	lastModifiedTime    int64
	lastModifiedVersion uint64
	content             op.DocOp
}

// ID returns the blip's document id.
func (b *BlipData) ID() string { return b.id }

// Author returns the participant that created the blip.
func (b *BlipData) Author() id.ParticipantID { return b.author }

// Contributors returns the contributor set in addition order (a copy).
func (b *BlipData) Contributors() []id.ParticipantID {
	return append([]id.ParticipantID(nil), b.contributors...)
}

// LastModifiedTime returns the time of the blip's last modification (ms since epoch).
func (b *BlipData) LastModifiedTime() int64 { return b.lastModifiedTime }

// LastModifiedVersion returns the wavelet version at the blip's last modification.
func (b *BlipData) LastModifiedVersion() uint64 { return b.lastModifiedVersion }

// Content returns the blip's content document (an insertion-only DocOp).
func (b *BlipData) Content() op.DocOp { return b.content }

// --- ordered-set helpers ---

func indexOf(set []id.ParticipantID, p id.ParticipantID) int {
	for i, e := range set {
		if e == p {
			return i
		}
	}
	return -1
}

// addToSet appends p if absent, preserving insertion order.
func addToSet(set []id.ParticipantID, p id.ParticipantID) []id.ParticipantID {
	if indexOf(set, p) >= 0 {
		return set
	}
	return append(set, p)
}

// removeFromSet removes p if present, preserving order.
func removeFromSet(set []id.ParticipantID, p id.ParticipantID) []id.ParticipantID {
	if i := indexOf(set, p); i >= 0 {
		return append(set[:i], set[i+1:]...)
	}
	return set
}
