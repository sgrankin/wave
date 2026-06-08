// Package search maintains and queries the derived read index: the per-user
// inbox (which wavelets a participant belongs to) and, layered on top, full-text
// search. It implements server.Indexer, so a WaveMap notifies it after each
// committed delta; the index is a rebuildable cache backed by storage.IndexStore
// and can be reconstructed from the delta log via Rebuild.
//
// Spec: docs/specs/11-search-indexing.md.
package search

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// snippetRunes caps the digest snippet length (matches the client list view).
const snippetRunes = 140

// metaFor projects a wavelet's current digest fields for the index: creator,
// last-modified version + wall-clock time, and the title/snippet derived from the
// root blip (empty when it has none). Computed once at write time so the inbox/search
// digests need no wavelet load.
func metaFor(w *wavelet.Data) storage.WaveletMeta {
	m := storage.WaveletMeta{
		Creator:             w.Creator(),
		LastModifiedVersion: w.Version(),
		LastModifiedTime:    w.LastModifiedTime(),
	}
	if blip, ok := w.Blip(conv.RootBlipID); ok {
		if t, err := doc.Title(blip.Content()); err == nil {
			m.Title = t
		}
		if s, err := doc.Snippet(blip.Content(), snippetRunes); err == nil {
			m.Snippet = s
		}
	}
	return m
}

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

// OnCommit reindexes the wavelet's participants, metadata, and the text of the
// blips this delta changed. Recording the full current participant set (rather
// than applying add/remove ops) is idempotent and self-correcting. Best-effort:
// errors are logged, not propagated — the index is rebuildable.
//
// NOTE: this writes synchronously while the container holds its lock (like the
// snapshot writer). Fine at single-machine scale; a busy server could move it to
// a background queue keyed by wavelet to keep the submit path lock-free.
func (i *Index) OnCommit(name id.WaveletName, w *wavelet.Data, delta cc.TransformedWaveletDelta) {
	if err := i.store.SetWaveletParticipants(name, w.Participants()); err != nil {
		i.logger.Warn("index: set participants failed", "wavelet", name.String(), "err", err)
	}
	if err := i.store.SetWaveletMeta(name, metaFor(w)); err != nil {
		i.logger.Warn("index: set meta failed", "wavelet", name.String(), "err", err)
	}
	seen := map[string]bool{}
	for _, o := range delta.Ops {
		bop, ok := o.(waveop.WaveletBlipOperation)
		if !ok || seen[bop.BlipID] {
			continue
		}
		seen[bop.BlipID] = true
		i.indexBlip(name, w, bop.BlipID)
	}
}

// indexBlip projects a blip's content to text and records it. Best-effort.
func (i *Index) indexBlip(name id.WaveletName, w *wavelet.Data, blipID string) {
	blip, ok := w.Blip(blipID)
	if !ok {
		return
	}
	text, err := doc.PlainText(blip.Content())
	if err != nil {
		i.logger.Warn("index: blip text projection failed", "wavelet", name.String(), "blip", blipID, "err", err)
		return
	}
	if err := i.store.SetBlipText(name, blipID, text); err != nil {
		i.logger.Warn("index: set blip text failed", "wavelet", name.String(), "blip", blipID, "err", err)
	}
}

// Search parses a query string and runs it against the index, scoped to the
// searcher's inbox. Operators: with:<addr>, creator:<addr>, in:inbox (the
// implicit default), orderby:modified; every other token is a free-text term
// ANDed against blip text. limit <= 0 means no limit.
func (i *Index) Search(participant id.ParticipantID, queryString string, limit int) ([]storage.WaveDigest, error) {
	q := storage.SearchQuery{Participant: participant, Limit: limit}
	for _, tok := range strings.Fields(queryString) {
		switch {
		case strings.HasPrefix(tok, "with:"):
			p, err := id.NewParticipantID(strings.TrimPrefix(tok, "with:"))
			if err != nil {
				return nil, fmt.Errorf("search: bad with: %w", err)
			}
			q.With = append(q.With, p)
		case strings.HasPrefix(tok, "creator:"):
			p, err := id.NewParticipantID(strings.TrimPrefix(tok, "creator:"))
			if err != nil {
				return nil, fmt.Errorf("search: bad creator: %w", err)
			}
			q.Creator = &p
		case tok == "in:inbox":
			// The inbox is always the scope; this is a no-op alias.
		case tok == "orderby:modified" || tok == "orderby:lastmodified":
			q.OrderByModifiedDesc = true
		default:
			// Unrecognized "foo:bar" tokens fall through to free text (a search-box
			// convention: a typo'd operator searches literally rather than erroring).
			q.Terms = append(q.Terms, tok)
		}
	}
	return i.store.Search(q)
}

// Inbox returns the wavelets a participant currently belongs to (names only).
func (i *Index) Inbox(participant id.ParticipantID) ([]id.WaveletName, error) {
	return i.store.InboxWavelets(participant)
}

// InboxDigests returns the participant's inbox as digest projections (most-recently-
// modified first, capped at limit), served from the index without loading wavelets.
func (i *Index) InboxDigests(participant id.ParticipantID, limit int) ([]storage.WaveDigest, error) {
	return i.store.InboxDigests(participant, limit)
}

// CanAccess reports whether participant may access wavelet — the access-control
// predicate (participation) used to scope attachment and wave reads/writes.
func (i *Index) CanAccess(participant id.ParticipantID, wavelet id.WaveletName) (bool, error) {
	return i.store.IsParticipant(wavelet, participant)
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
			if err := index.SetWaveletMeta(name, metaFor(w)); err != nil {
				return err
			}
			for _, blipID := range w.BlipIDs() {
				blip, ok := w.Blip(blipID)
				if !ok {
					continue // BlipIDs and Blip can't disagree, but don't deref on a broken invariant
				}
				text, err := doc.PlainText(blip.Content())
				if err != nil {
					continue // skip un-projectable blips; the inbox/meta still index
				}
				if err := index.SetBlipText(name, blipID, text); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
