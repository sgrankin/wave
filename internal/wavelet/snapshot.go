package wavelet

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
)

// SnapshotState is a flat, serializable capture of a wavelet's full state at a
// version — everything needed to reconstruct Data without replaying from zero.
// It is a derivable cache (see the snapshot store), NOT part of the hash chain,
// so its shape may evolve freely: a format change just invalidates snapshots,
// which are rebuilt from the delta log. Participant/author identities are
// serialized addresses; ids are serialized strings.
type SnapshotState struct {
	WaveID           string
	WaveletID        string
	Creator          string
	CreationTime     int64
	LastModifiedTime int64
	Version          uint64
	HistoryHash      []byte
	Participants     []string
	Blips            []BlipSnapshot
}

// BlipSnapshot is one blip's state within a SnapshotState.
type BlipSnapshot struct {
	ID                  string
	Author              string
	Contributors        []string
	LastModifiedTime    int64
	LastModifiedVersion uint64
	Content             op.DocOp
}

// State captures the wavelet's current state. Blips are emitted in sorted id
// order for a deterministic snapshot.
func (w *Data) State() SnapshotState {
	s := SnapshotState{
		WaveID:           w.waveID.Serialize(),
		WaveletID:        w.waveletID.Serialize(),
		Creator:          w.creator.Address(),
		CreationTime:     w.creationTime,
		LastModifiedTime: w.lastModifiedTime,
		Version:          w.hashedVersion.Version(),
		HistoryHash:      w.hashedVersion.HistoryHash(),
	}
	for _, p := range w.participants {
		s.Participants = append(s.Participants, p.Address())
	}
	for _, blipID := range w.BlipIDs() {
		b := w.blips[blipID]
		bs := BlipSnapshot{
			ID:                  b.id,
			Author:              b.author.Address(),
			LastModifiedTime:    b.lastModifiedTime,
			LastModifiedVersion: b.lastModifiedVersion,
			Content:             b.content,
		}
		for _, c := range b.contributors {
			bs.Contributors = append(bs.Contributors, c.Address())
		}
		s.Blips = append(s.Blips, bs)
	}
	return s
}

// FromState reconstructs a wavelet from a snapshot. It is the inverse of State.
func FromState(s SnapshotState) (*Data, error) {
	waveID, err := id.ParseWaveID(s.WaveID)
	if err != nil {
		return nil, fmt.Errorf("wavelet: snapshot wave id %q: %w", s.WaveID, err)
	}
	waveletID, err := id.ParseWaveletID(s.WaveletID)
	if err != nil {
		return nil, fmt.Errorf("wavelet: snapshot wavelet id %q: %w", s.WaveletID, err)
	}
	creator, err := id.NewParticipantID(s.Creator)
	if err != nil {
		return nil, fmt.Errorf("wavelet: snapshot creator %q: %w", s.Creator, err)
	}
	w := &Data{
		waveID:           waveID,
		waveletID:        waveletID,
		creator:          creator,
		creationTime:     s.CreationTime,
		lastModifiedTime: s.LastModifiedTime,
		hashedVersion:    version.NewHashedVersion(s.Version, s.HistoryHash),
		blips:            map[string]*BlipData{},
	}
	for _, addr := range s.Participants {
		p, err := id.NewParticipantID(addr)
		if err != nil {
			return nil, fmt.Errorf("wavelet: snapshot participant %q: %w", addr, err)
		}
		w.participants = append(w.participants, p)
	}
	for _, bs := range s.Blips {
		author, err := id.NewParticipantID(bs.Author)
		if err != nil {
			return nil, fmt.Errorf("wavelet: snapshot blip %q author %q: %w", bs.ID, bs.Author, err)
		}
		bd := &BlipData{
			id:                  bs.ID,
			author:              author,
			lastModifiedTime:    bs.LastModifiedTime,
			lastModifiedVersion: bs.LastModifiedVersion,
			content:             bs.Content,
		}
		for _, addr := range bs.Contributors {
			c, err := id.NewParticipantID(addr)
			if err != nil {
				return nil, fmt.Errorf("wavelet: snapshot blip %q contributor %q: %w", bs.ID, addr, err)
			}
			bd.contributors = append(bd.contributors, c)
		}
		w.blips[bs.ID] = bd
	}
	return w, nil
}
