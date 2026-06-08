// Package queryapi serves the browser's read-side wave queries — the inbox and
// search — as authenticated JSON HTTP endpoints over the derived index. The index
// already stores each wave's digest projection (title, snippet, creator,
// participants, version, last-modified time), so these handlers answer entirely
// from the index: they never load a wavelet, which is what keeps a frequently
// polled inbox from pinning every inbox wave in the in-memory cache.
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

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Index is the read side of the search/index this API serves (satisfied by
// *search.Index). Both methods return digest projections straight from the index —
// no wavelet load — already ordered (inbox: most-recently-modified first; search:
// the index's order) and capped at limit.
type Index interface {
	// InboxDigests returns the participant's inbox, newest-modified first, ≤ limit.
	InboxDigests(participant id.ParticipantID, limit int) ([]storage.WaveDigest, error)
	// Search returns digests matching the query string, scoped to the participant's
	// inbox, ≤ limit.
	Search(participant id.ParticipantID, query string, limit int) ([]storage.WaveDigest, error)
}

// ReadState is the per-participant read-progress store backing the unread
// indicator (satisfied by *sqlite.Store).
type ReadState interface {
	// ReadVersions returns the participant's read versions keyed by serialized
	// wavelet name (absent ⇒ 0).
	ReadVersions(participant id.ParticipantID) (map[string]uint64, error)
	// SetReadVersion records that the participant has read the wavelet through
	// version (monotonic).
	SetReadVersion(participant id.ParticipantID, wavelet id.WaveletName, version uint64) error
	// SetBlipReadVersion records that the participant has read one blip through a
	// version (monotonic), the per-blip granularity behind "which blips are unread".
	SetBlipReadVersion(participant id.ParticipantID, wavelet id.WaveletName, blipID string, version uint64) error
	// BlipReadVersions returns the participant's per-blip read versions for a wavelet.
	BlipReadVersions(participant id.ParticipantID, wavelet id.WaveletName) (map[string]uint64, error)
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
	Unread           bool     `json:"unread"` // version > the participant's read version
}

// Handler serves /api/inbox, /api/search, and /api/read.
type Handler struct {
	index    Index
	reads    ReadState
	identify func(*http.Request) (id.ParticipantID, bool)
	logger   *slog.Logger
}

// New builds a Handler over the index and read-state store. identify resolves the
// authenticated participant from the request (e.g. auth.ParticipantFrom when mounted
// behind auth.Service.Middleware). A nil logger uses slog.Default().
func New(index Index, reads ReadState, identify func(*http.Request) (id.ParticipantID, bool), logger *slog.Logger) *Handler {
	return &Handler{index: index, reads: reads, identify: identify, logger: logger}
}

func (h *Handler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// Routes returns the API mux (GET /api/inbox, GET /api/search, POST /api/read).
// Mount it behind the authentication middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/inbox", h.inbox)
	mux.HandleFunc("GET /api/search", h.search)
	mux.HandleFunc("POST /api/read", h.markRead)
	mux.HandleFunc("GET /api/read", h.readState)
	return mux
}

func (h *Handler) inbox(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	wds, err := h.index.InboxDigests(p, parseLimit(r))
	if err != nil {
		http.Error(w, "inbox: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.writeDigests(w, p, wds)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	wds, err := h.index.Search(p, r.URL.Query().Get("q"), parseLimit(r))
	if err != nil {
		http.Error(w, "search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.writeDigests(w, p, wds)
}

// markRead records that the participant has read a wavelet through a version
// (POST /api/read?wave=<name>&version=<n>), clearing its unread state. With an
// additional &blip=<id> it records PER-BLIP read progress instead (the granularity
// behind "which blips are unread"); the wavelet-level read (backing the inbox dot)
// is set by the no-blip form.
func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, err := id.ParseWaveletName(r.FormValue("wave"))
	if err != nil {
		http.Error(w, "bad wave: "+err.Error(), http.StatusBadRequest)
		return
	}
	version, err := strconv.ParseUint(r.FormValue("version"), 10, 64)
	if err != nil {
		http.Error(w, "bad version", http.StatusBadRequest)
		return
	}
	if blip := r.FormValue("blip"); blip != "" {
		if err := h.reads.SetBlipReadVersion(p, name, blip, version); err != nil {
			http.Error(w, "mark blip read: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.reads.SetReadVersion(p, name, version); err != nil {
		http.Error(w, "mark read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readState returns the participant's per-blip read versions for one wave
// (GET /api/read?wave=<name>), so the client can compute which blips are unread
// when it opens the wave: {"blipReads": {"<blipId>": <version>, ...}}.
func (h *Handler) readState(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, err := id.ParseWaveletName(r.URL.Query().Get("wave"))
	if err != nil {
		http.Error(w, "bad wave: "+err.Error(), http.StatusBadRequest)
		return
	}
	blipReads, err := h.reads.BlipReadVersions(p, name)
	if err != nil {
		http.Error(w, "read state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if blipReads == nil {
		blipReads = map[string]uint64{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"blipReads": blipReads})
}

// writeDigests converts index digests to the JSON list shape, stamping each wave's
// unread flag from the participant's read versions. The order and cap are the
// index's; this layer only annotates and serializes.
func (h *Handler) writeDigests(w http.ResponseWriter, p id.ParticipantID, wds []storage.WaveDigest) {
	readVersions, err := h.reads.ReadVersions(p)
	if err != nil {
		// Degrade gracefully: show everything as read rather than failing the list.
		h.log().Warn("queryapi: read versions", "participant", p, "err", err)
		readVersions = map[string]uint64{}
	}
	digests := make([]Digest, 0, len(wds))
	for _, wd := range wds {
		wave := wd.Wavelet.Serialize()
		parts := wd.Participants
		if parts == nil {
			parts = []string{}
		}
		digests = append(digests, Digest{
			Wave:             wave,
			Title:            wd.Title,
			Snippet:          wd.Snippet,
			Creator:          wd.Creator,
			Participants:     parts,
			Version:          wd.Version,
			LastModifiedTime: wd.LastModifiedTime,
			Unread:           wd.Version > readVersions[wave],
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"waves": digests})
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
