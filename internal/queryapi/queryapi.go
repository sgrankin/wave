// Package queryapi serves the browser's read-side wave queries — the inbox and
// search — as authenticated JSON HTTP endpoints over the existing search/index.
// It turns the lightweight index results (a wavelet name + version) into wave
// "digests" (title, snippet, participants) the client list renders, loading each
// matched wavelet and reading it under the container lock.
//
// Mount Routes() behind auth.Service.Middleware so the authenticated participant
// is bound to the request; the handlers read it via the injected identify func.
// Search is already scoped to the searcher's inbox by the index, so a participant
// only ever sees waves they belong to.
package queryapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/wavelet"
)

const (
	defaultLimit = 50
	maxLimit     = 200
	snippetRunes = 140
)

// Index is the read side of the search/index this API serves (satisfied by
// *search.Index).
type Index interface {
	// Inbox lists the wavelets the participant currently belongs to.
	Inbox(participant id.ParticipantID) ([]id.WaveletName, error)
	// Search returns wavelets matching the query string, scoped to the
	// participant's inbox.
	Search(participant id.ParticipantID, query string, limit int) ([]storage.SearchResult, error)
}

// Waves loads a wavelet's state for digest computation. Read must run fn under
// the wavelet's lock (fn receives nil for a never-created wavelet).
type Waves interface {
	Read(name id.WaveletName, fn func(*wavelet.Data)) error
}

// Digest is one wave's summary for the list view.
type Digest struct {
	Wave             string   `json:"wave"`    // serialized WaveletName
	Title            string   `json:"title"`   // first non-empty line of the root blip
	Snippet          string   `json:"snippet"` // truncated plain text of the root blip
	Creator          string   `json:"creator"`
	Participants     []string `json:"participants"`
	Version          uint64   `json:"version"`
	LastModifiedTime int64    `json:"lastModifiedTime"`
}

// Handler serves /api/inbox and /api/search.
type Handler struct {
	index    Index
	waves    Waves
	identify func(*http.Request) (id.ParticipantID, bool)
	logger   *slog.Logger
}

// New builds a Handler over the index and wave source. identify resolves the
// authenticated participant from the request (e.g. auth.ParticipantFrom when
// mounted behind auth.Service.Middleware). A nil logger uses slog.Default().
func New(index Index, waves Waves, identify func(*http.Request) (id.ParticipantID, bool), logger *slog.Logger) *Handler {
	return &Handler{index: index, waves: waves, identify: identify, logger: logger}
}

func (h *Handler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// Routes returns the API mux (GET /api/inbox, GET /api/search). Mount it behind
// the authentication middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/inbox", h.inbox)
	mux.HandleFunc("GET /api/search", h.search)
	return mux
}

func (h *Handler) inbox(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	names, err := h.index.Inbox(p)
	if err != nil {
		http.Error(w, "inbox: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Bound the per-request fan-out (each digest loads a container): cap to the
	// same limit as search. A real paging story can come later; for now an inbox
	// is naturally bounded by participation.
	if limit := parseLimit(r); len(names) > limit {
		names = names[:limit]
	}
	h.writeDigests(w, names)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	results, err := h.index.Search(p, r.URL.Query().Get("q"), parseLimit(r))
	if err != nil {
		http.Error(w, "search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	names := make([]id.WaveletName, len(results))
	for i, res := range results {
		names[i] = res.Wavelet
	}
	h.writeDigests(w, names)
}

func (h *Handler) writeDigests(w http.ResponseWriter, names []id.WaveletName) {
	digests := make([]Digest, 0, len(names))
	for _, name := range names {
		if d, ok := h.digest(name); ok {
			digests = append(digests, d)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"waves": digests})
}

// digest loads name and reads a summary under the container lock. ok is false if
// the wavelet has not been created yet (a benign, quiet skip) or fails to load (a
// load error is logged before skipping, so index/store drift or corruption is
// observable rather than a silently missing list entry).
//
// NOTE: title/snippet are taken from the root blip only and the snippet is not
// title-stripped; the spec (docs/specs/11-search-indexing.md) computes the snippet
// from the most-recently-modified blip with the title prefix removed. For a
// single-blip wave these coincide; refining it is deferred to a later batch.
func (h *Handler) digest(name id.WaveletName) (Digest, bool) {
	d := Digest{Wave: name.Serialize(), Participants: []string{}}
	found := false
	if err := h.waves.Read(name, func(wv *wavelet.Data) {
		if wv == nil {
			return
		}
		found = true
		d.Creator = wv.Creator().Address()
		d.Version = wv.Version()
		d.LastModifiedTime = wv.LastModifiedTime()
		for _, p := range wv.Participants() {
			d.Participants = append(d.Participants, p.Address())
		}
		if blip, ok := wv.Blip(conv.RootBlipID); ok {
			content := blip.Content()
			if title, err := doc.Title(content); err == nil {
				d.Title = title
			}
			if snippet, err := doc.Snippet(content, snippetRunes); err == nil {
				d.Snippet = snippet
			}
		}
	}); err != nil {
		// The index named a wavelet the store could not materialize (corruption,
		// replay hash mismatch, storage error). Skip it, but make the skip visible.
		h.log().Warn("queryapi: skipping wavelet that failed to load", "wavelet", name, "err", err)
		return Digest{}, false
	}
	if !found {
		return Digest{}, false // uncreated: benign, no log
	}
	return d, true
}

func parseLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// waveMapReader adapts a *server.WaveMap to Waves.
type waveMapReader struct{ wm *server.WaveMap }

// NewWaveMapReader adapts a WaveMap to the Waves interface: it loads (or returns
// the cached) container for a name and reads its wavelet under the container lock.
func NewWaveMapReader(wm *server.WaveMap) Waves { return waveMapReader{wm} }

func (r waveMapReader) Read(name id.WaveletName, fn func(*wavelet.Data)) error {
	c, err := r.wm.Container(name)
	if err != nil {
		return err
	}
	c.Read(fn)
	return nil
}
