// Package snapshot serializes a wavelet's materialized state (wavelet.SnapshotState)
// for the snapshot cache. The wire shape is deliberately separate from the frozen
// delta codec: snapshots are a derivable load-time optimization, never hashed, so
// this encoding may change at will — a format change just invalidates cached
// snapshots, which are rebuilt by replaying the delta log. Blip content (a DocOp)
// is encoded via the canonical codec; everything else rides in a CBOR envelope.
//
// Spec: docs/specs/05-storage-persistence.md §5.1 (snapshots are derived state).
package snapshot

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"

	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/wavelet"
)

type wireBlip struct {
	_                   struct{} `cbor:",toarray"`
	ID                  string
	Author              string
	Contributors        []string
	LastModifiedTime    int64
	LastModifiedVersion uint64
	Content             []byte // codec.EncodeDocOp
}

type wireSnap struct {
	_                struct{} `cbor:",toarray"`
	WaveID           string
	WaveletID        string
	Creator          string
	CreationTime     int64
	LastModifiedTime int64
	Version          uint64
	HistoryHash      []byte
	Participants     []string
	Blips            []wireBlip
}

// Encode serializes a wavelet snapshot. It panics only on an impossible CBOR
// marshal failure (our own data is always encodable), matching the codec's
// philosophy for trusted, self-produced values.
func Encode(s wavelet.SnapshotState) []byte {
	w := wireSnap{
		WaveID:           s.WaveID,
		WaveletID:        s.WaveletID,
		Creator:          s.Creator,
		CreationTime:     s.CreationTime,
		LastModifiedTime: s.LastModifiedTime,
		Version:          s.Version,
		HistoryHash:      s.HistoryHash,
		Participants:     s.Participants,
		Blips:            make([]wireBlip, len(s.Blips)),
	}
	for i, b := range s.Blips {
		w.Blips[i] = wireBlip{
			ID:                  b.ID,
			Author:              b.Author,
			Contributors:        b.Contributors,
			LastModifiedTime:    b.LastModifiedTime,
			LastModifiedVersion: b.LastModifiedVersion,
			Content:             codec.EncodeDocOp(b.Content),
		}
	}
	out, err := cbor.Marshal(w)
	if err != nil {
		panic("snapshot: encode: " + err.Error())
	}
	return out
}

// Decode parses a wavelet snapshot.
func Decode(data []byte) (wavelet.SnapshotState, error) {
	var w wireSnap
	if err := cbor.Unmarshal(data, &w); err != nil {
		return wavelet.SnapshotState{}, fmt.Errorf("snapshot: decode: %w", err)
	}
	s := wavelet.SnapshotState{
		WaveID:           w.WaveID,
		WaveletID:        w.WaveletID,
		Creator:          w.Creator,
		CreationTime:     w.CreationTime,
		LastModifiedTime: w.LastModifiedTime,
		Version:          w.Version,
		HistoryHash:      w.HistoryHash,
		Participants:     w.Participants,
		Blips:            make([]wavelet.BlipSnapshot, len(w.Blips)),
	}
	for i, b := range w.Blips {
		content, err := codec.DecodeDocOp(b.Content)
		if err != nil {
			return wavelet.SnapshotState{}, fmt.Errorf("snapshot: decode blip %q content: %w", b.ID, err)
		}
		s.Blips[i] = wavelet.BlipSnapshot{
			ID:                  b.ID,
			Author:              b.Author,
			Contributors:        b.Contributors,
			LastModifiedTime:    b.LastModifiedTime,
			LastModifiedVersion: b.LastModifiedVersion,
			Content:             content,
		}
	}
	return s, nil
}
