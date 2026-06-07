// Package playbackapi serves a wavelet's history for the client's playback
// scrubber: a timeline of applied deltas, and the rendered conversation as it
// stood at any past version. Both are read-only and reconstructed from the
// persisted delta log (server-side forward-apply), so playback never disturbs the
// live wavelet.
//
// Mount Routes() behind auth.Service.Middleware. Every request is gated by wavelet
// membership (an AccessChecker) — playback exposes full historical content, so only
// participants may scrub, regardless of auth mode.
package playbackapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/wavelet"
)

// Waves is the read side of the wave store this API needs (satisfied by a WaveMap
// adapter): a delta-header timeline and state reconstruction at a version.
type Waves interface {
	DeltaHeaders(name id.WaveletName) ([]server.DeltaHeader, error)
	StateAt(name id.WaveletName, version uint64) (*wavelet.Data, error)
}

// AccessChecker gates playback by wavelet membership (mirrors
// transport.AccessChecker / attachapi.AccessChecker).
type AccessChecker interface {
	CanAccess(participant id.ParticipantID, wavelet id.WaveletName) (bool, error)
}

// DeltaDigest is one entry in the playback timeline (mirrors server.DeltaHeader).
type DeltaDigest struct {
	Author    string `json:"author"`
	Version   uint64 `json:"version"` // resulting wavelet version after this delta
	Timestamp int64  `json:"timestamp"`
	OpCount   int    `json:"opCount"`
}

// ConversationView is the rendered conversation at a past version: participants and
// per-blip plain text, in conversation (manifest) order. It deliberately renders to
// plain text (not DocOps) so playback needs no document-op wire codec.
type ConversationView struct {
	Version      uint64     `json:"version"`
	Participants []string   `json:"participants"`
	Blips        []BlipView `json:"blips"`
}

// BlipView is one blip's rendered state in a ConversationView.
type BlipView struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// Handler serves GET /api/playback/deltas and GET /api/playback/state.
type Handler struct {
	waves    Waves
	access   AccessChecker
	identify func(*http.Request) (id.ParticipantID, bool)
	logger   *slog.Logger
}

// New builds a Handler over the wave store. access gates by membership; identify
// resolves the authenticated participant (e.g. auth.ParticipantFrom). A nil logger
// uses slog.Default().
func New(waves Waves, access AccessChecker, identify func(*http.Request) (id.ParticipantID, bool), logger *slog.Logger) *Handler {
	return &Handler{waves: waves, access: access, identify: identify, logger: logger}
}

func (h *Handler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// Routes returns the API mux. Mount it behind the authentication middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/playback/deltas", h.deltas)
	mux.HandleFunc("GET /api/playback/state", h.state)
	return mux
}

// authorize resolves the participant and checks wavelet membership. On any failure
// it writes the response and returns ok=false.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (id.ParticipantID, id.WaveletName, bool) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return id.ParticipantID{}, id.WaveletName{}, false
	}
	name, err := id.ParseWaveletName(r.FormValue("wave"))
	if err != nil {
		http.Error(w, "bad wave: "+err.Error(), http.StatusBadRequest)
		return id.ParticipantID{}, id.WaveletName{}, false
	}
	allowed, err := h.access.CanAccess(p, name)
	if err != nil {
		h.log().Error("playbackapi: access check", "participant", p, "wavelet", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return id.ParticipantID{}, id.WaveletName{}, false
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return id.ParticipantID{}, id.WaveletName{}, false
	}
	return p, name, true
}

// deltas serves the playback timeline: GET /api/playback/deltas?wave=<name>.
func (h *Handler) deltas(w http.ResponseWriter, r *http.Request) {
	_, name, ok := h.authorize(w, r)
	if !ok {
		return
	}
	headers, err := h.waves.DeltaHeaders(name)
	if err != nil {
		h.log().Error("playbackapi: delta headers", "wavelet", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	digests := make([]DeltaDigest, len(headers))
	for i, d := range headers {
		digests[i] = DeltaDigest{Author: d.Author.Address(), Version: d.Version, Timestamp: d.Timestamp, OpCount: d.OpCount}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"deltas": digests})
}

// state serves the rendered conversation at a version:
// GET /api/playback/state?wave=<name>&version=<n>.
func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	_, name, ok := h.authorize(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(r.FormValue("version"), 10, 64)
	if err != nil {
		http.Error(w, "bad version", http.StatusBadRequest)
		return
	}
	wv, err := h.waves.StateAt(name, version)
	if err != nil {
		// An unknown/mid-delta version is a client error, not a server fault.
		http.Error(w, "no such version", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(conversationView(version, wv))
}

// conversationView renders a reconstructed wavelet to plain-text blips in
// conversation order. wv is nil for version 0 (the empty, never-created state).
func conversationView(version uint64, wv *wavelet.Data) ConversationView {
	view := ConversationView{Version: version, Participants: []string{}, Blips: []BlipView{}}
	if wv == nil {
		return view
	}
	for _, p := range wv.Participants() {
		view.Participants = append(view.Participants, p.Address())
	}
	for _, blipID := range orderedBlipIDs(wv) {
		b, ok := wv.Blip(blipID)
		if !ok {
			continue
		}
		text, err := doc.PlainText(b.Content())
		if err != nil {
			text = "" // unreadable content: render empty rather than failing the whole view
		}
		view.Blips = append(view.Blips, BlipView{ID: blipID, Author: b.Author().Address(), Text: text})
	}
	return view
}

// orderedBlipIDs returns the conversation's content blip ids (excluding the
// manifest document) in manifest reading order, then any blips the manifest did not
// reference appended in the wavelet's sorted order. A missing/unreadable manifest
// (e.g. a pre-seed version) just yields the sorted content blips.
func orderedBlipIDs(wv *wavelet.Data) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(blipID string) {
		if blipID == "" || blipID == conv.ManifestDocumentID || seen[blipID] {
			return
		}
		seen[blipID] = true
		ids = append(ids, blipID)
	}
	if manifest, ok := wv.Blip(conv.ManifestDocumentID); ok {
		if m, err := conv.ReadManifest(manifest.Content()); err == nil {
			var walk func(conv.Thread)
			walk = func(t conv.Thread) {
				for _, blip := range t.Blips {
					add(blip.ID)
					for _, reply := range blip.Threads {
						walk(reply)
					}
				}
			}
			walk(m.RootThread)
		}
	}
	for _, blipID := range wv.BlipIDs() { // defensive: don't drop unreferenced content
		add(blipID)
	}
	return ids
}

// waveMapReader adapts a *server.WaveMap to Waves.
type waveMapReader struct{ wm *server.WaveMap }

// NewWaveMapReader adapts a WaveMap to the Waves interface.
func NewWaveMapReader(wm *server.WaveMap) Waves { return waveMapReader{wm} }

func (r waveMapReader) DeltaHeaders(name id.WaveletName) ([]server.DeltaHeader, error) {
	c, err := r.wm.Container(name)
	if err != nil {
		return nil, err
	}
	return c.DeltaHeaders()
}

func (r waveMapReader) StateAt(name id.WaveletName, version uint64) (*wavelet.Data, error) {
	c, err := r.wm.Container(name)
	if err != nil {
		return nil, err
	}
	return c.StateAt(version)
}
