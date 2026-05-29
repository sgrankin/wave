// Package search maintains and queries the derived read index: the per-user
// inbox (which wavelets a participant belongs to) and, layered on top, full-text
// search. It implements server.Indexer, so a WaveMap notifies it after each
// committed delta; the index is a rebuildable cache backed by storage.IndexStore
// and can be reconstructed from the delta log via Rebuild.
//
// Spec: docs/specs/11-search-indexing.md.
package search

import (
	"log/slog"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/wavelet"
)

// Index maintains the derived read index.
type Index struct {
	store  storage.IndexStore
	logger *slog.Logger
}

// New returns an Index backed by store. A nil logger uses slog.Default.
func New(store storage.IndexStore, logger *slog.Logger) *Index {
	if logger == nil {
		logger = slog.Default()
	}
	return &Index{store: store, logger: logger}
}

// OnCommit reindexes the wavelet's participant set from its post-delta state.
// Recording the full current set (rather than applying add/remove ops) is
// idempotent and self-correcting. Best-effort: an error is logged, not
// propagated — the index is rebuildable.
//
// NOTE: this writes synchronously while the container holds its lock (like the
// snapshot writer). Fine at single-machine scale; a busy server could move it to
// a background queue keyed by wavelet to keep the submit path lock-free.
func (i *Index) OnCommit(name id.WaveletName, w *wavelet.Data, _ cc.TransformedWaveletDelta) {
	if err := i.store.SetWaveletParticipants(name, w.Participants()); err != nil {
		i.logger.Warn("index: set participants failed", "wavelet", name.String(), "err", err)
	}
}

// Inbox returns the wavelets a participant currently belongs to.
func (i *Index) Inbox(participant id.ParticipantID) ([]id.WaveletName, error) {
	return i.store.InboxWavelets(participant)
}

// Rebuild reconstructs the entire index from the delta log: it enumerates every
// wave/wavelet, replays each to its current state, and re-records the index.
// Use it to backfill after enabling indexing, or to repair drift. It is a
// maintenance operation — run it without concurrent submits.
func Rebuild(deltas storage.DeltaStore, index storage.IndexStore, clk clock.Clock) error {
	waves, err := deltas.WaveIDs()
	if err != nil {
		return err
	}
	for _, wave := range waves {
		wavelets, err := deltas.Lookup(wave)
		if err != nil {
			return err
		}
		for _, wl := range wavelets {
			name := id.NewWaveletName(wave, wl)
			access, err := deltas.Open(name)
			if err != nil {
				return err
			}
			c, err := server.Load(name, access, clk)
			if err != nil {
				return err
			}
			w := c.Wavelet()
			if w == nil {
				if err := index.DeleteWaveletIndex(name); err != nil {
					return err
				}
				continue
			}
			if err := index.SetWaveletParticipants(name, w.Participants()); err != nil {
				return err
			}
		}
	}
	return nil
}
