// Package agentgw exposes the agent channel over WebSocket: an external harness
// opens one socket per (wave, agent), authenticates with a bearer token, and drives
// the wave through the agent gateway (a wave.opened snapshot then live events out;
// reply intents in — newline-delimited JSON). The agent itself runs in-process (a
// LocalClient over the live container); the socket is just the harness's link to it,
// so the harness needs no OT or Go knowledge.
//
// Intents must be newline-terminated (the gateway reads line-delimited JSON);
// sending one `{…}\n` per WebSocket text message is the simplest correct form.
package agentgw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/sgrankin/wave/internal/agent"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/waveop"
)

// Auth resolves a bearer token to the agent identity it authenticates.
type Auth interface {
	Agent(token string) (id.ParticipantID, bool)
}

// StaticAuth is a configured token→agent map for single-machine deployments. The
// tokens are bearer secrets; treat them like passwords (they appear in flags/config,
// so prefer a file/env over a command line in production, and serve only over TLS).
type StaticAuth map[string]id.ParticipantID

// Agent resolves a token to its agent identity.
func (a StaticAuth) Agent(token string) (id.ParticipantID, bool) {
	p, ok := a[token]
	return p, ok
}

// AccessChecker gates by wavelet membership: an agent may only drive waves it is a
// participant of (mirrors transport.AccessChecker).
type AccessChecker interface {
	CanAccess(participant id.ParticipantID, wavelet id.WaveletName) (bool, error)
}

// Index lists and searches an agent's waves for discovery (satisfied by
// *search.Index). Optional — when nil, GET /agent/waves returns 501.
type Index interface {
	// InboxDigests returns the agent's waves as digests, newest-modified first, ≤ limit.
	InboxDigests(participant id.ParticipantID, limit int) ([]storage.WaveDigest, error)
	// Search returns digests matching the query (free-text terms ANDed against blip
	// text, plus with:/creator:/orderby: operators), scoped to the agent's own waves,
	// ≤ limit — content-based memory recall over many waves.
	Search(participant id.ParticipantID, query string, limit int) ([]storage.WaveDigest, error)
}

// Handler serves the agent surface: the per-wave socket (GET /agent/socket) plus the
// wave-management API (create / list / leave) behind the same bearer auth.
type Handler struct {
	waves          *server.WaveMap
	auth           Auth
	access         AccessChecker
	index          Index // optional; nil ⇒ discovery disabled
	clk            clock.Clock
	logger         *slog.Logger
	allowedOrigins []string
}

// WithIndex attaches the discovery index, enabling GET /agent/waves. Chains.
func (h *Handler) WithIndex(i Index) *Handler {
	h.index = i
	return h
}

// Routes mounts the agent surface: the socket plus the management API. Mount it at
// "/agent/" so the full paths (/agent/socket, /agent/waves, /agent/leave) resolve.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /agent/socket", h) // the per-wave bridge (ServeHTTP)
	mux.HandleFunc("POST /agent/waves", h.createWave)
	mux.HandleFunc("GET /agent/waves", h.listWaves)
	mux.HandleFunc("POST /agent/leave", h.leaveWave)
	return mux
}

// New builds the endpoint. access should enforce membership (e.g.
// transport.MembershipChecker) regardless of the server's auth mode. A nil logger
// uses slog.Default().
func New(waves *server.WaveMap, auth Auth, access AccessChecker, clk clock.Clock, logger *slog.Logger, allowedOrigins ...string) *Handler {
	return &Handler{waves: waves, auth: auth, access: access, clk: clk, logger: logger, allowedOrigins: allowedOrigins}
}

func (h *Handler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// authenticate resolves the bearer token to the agent identity, or reports false.
func (h *Handler) authenticate(r *http.Request) (id.ParticipantID, bool) {
	token := bearerToken(r)
	if token == "" {
		return id.ParticipantID{}, false
	}
	return h.auth.Agent(token)
}

// ServeHTTP authenticates the agent (bearer token), checks wavelet membership,
// upgrades to a WebSocket, and runs the gateway over it as the agent.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, err := id.ParseWaveletName(r.FormValue("wave"))
	if err != nil {
		http.Error(w, "bad wave: "+err.Error(), http.StatusBadRequest)
		return
	}
	allowed, err := h.access.CanAccess(agentID, name)
	if err != nil {
		h.log().Error("agentgw: access check", "agent", agentID.Address(), "wave", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.allowedOrigins})
	if err != nil {
		h.log().Debug("agentgw: websocket accept failed", "remote", r.RemoteAddr, "err", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := websocket.NetConn(ctx, c, websocket.MessageText)

	container, err := h.waves.Container(name)
	if err != nil {
		h.log().Error("agentgw: load container", "wave", name, "err", err)
		_ = c.Close(websocket.StatusInternalError, "container")
		return
	}
	lc := agent.NewLocalClient(container, agentID, h.clk, newBlipID)
	lc.Open()
	defer lc.Close()

	gw := agent.NewGateway(lc, agentID, h.logger)
	if err := gw.Run(ctx, conn, conn); err != nil && ctx.Err() == nil {
		h.log().Debug("agentgw: gateway ended", "agent", agentID.Address(), "wave", name, "err", err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// createWave (POST /agent/waves) mints a fresh conversation wave in the agent's domain,
// seeded with the agent as creator/sole participant, and returns its serialized name.
// This is the keystone of "wave as agent memory": an agent can create its OWN waves
// (the socket's StrictMembershipChecker otherwise only lets it into waves a human
// invited it to). To SHARE the new wave, the agent adds participants over the socket
// (the add.participant intent).
//
// Unlike the socket's submit path, this is NOT rate-limited: agent tokens are operator-
// configured and trusted, and the socket already lets them submit unboundedly, so a
// per-call wave creation is no worse. If untrusted tokens are ever supported, gate this.
func (h *Handler) createWave(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	domain := agentID.Domain()
	waveID, err := id.NewWaveID(domain, "w+"+randLocalID())
	if err != nil {
		h.log().Error("agentgw: mint wave id", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	waveletID, err := id.NewWaveletID(domain, "conv+root")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	name := id.NewWaveletName(waveID, waveletID)
	container, err := h.waves.Container(name)
	if err != nil {
		h.log().Error("agentgw: create container", "wave", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ops, err := conv.SeedConversation(agentID, h.clk.Now().UnixMilli())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := container.SeedIfEmpty(agentID, ops); err != nil {
		h.log().Error("agentgw: seed wave", "wave", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"wave": name.Serialize()})
}

// listWaves (GET /agent/waves) returns the waves the agent is a participant of — its
// memory discovery. With ?q=<query> it instead SEARCHES the agent's waves by content
// (full-text terms ANDed against blip text, plus with:/creator:/orderby: operators) —
// content-based recall over many memory waves rather than scanning the whole list.
// Both forms are scoped to the agent's own waves and served from the index without a
// wavelet load. Requires the discovery index (WithIndex); 501 otherwise.
func (h *Handler) listWaves(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.index == nil {
		http.Error(w, "discovery not enabled", http.StatusNotImplemented)
		return
	}
	var (
		digests []storage.WaveDigest
		err     error
	)
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		digests, err = h.index.Search(agentID, q, 200)
	} else {
		digests, err = h.index.InboxDigests(agentID, 200)
	}
	if err != nil {
		h.log().Error("agentgw: list waves", "agent", agentID.Address(), "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type waveInfo struct {
		Wave    string `json:"wave"`
		Title   string `json:"title"`
		Snippet string `json:"snippet"` // root-blip preview, to disambiguate matches
	}
	out := make([]waveInfo, 0, len(digests))
	for _, d := range digests {
		out = append(out, waveInfo{Wave: d.Wavelet.Serialize(), Title: d.Title, Snippet: d.Snippet})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"waves": out})
}

// leaveWave (POST /agent/leave?wave=<name>) removes the agent from a wave it is a
// member of (a self-removeParticipant delta) — the agent abandons a memory it no
// longer needs. After leaving, the socket's membership check denies re-OPENING it.
// NOTE: an ALREADY-open socket is NOT cut on self-removal here (unlike the human
// transport's forward() cutoff): the agent keeps its subscription until it disconnects.
// Harmless (own token, own wave); call it out so the asymmetry isn't a surprise.
func (h *Handler) leaveWave(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, err := id.ParseWaveletName(r.FormValue("wave"))
	if err != nil {
		http.Error(w, "bad wave: "+err.Error(), http.StatusBadRequest)
		return
	}
	container, err := h.waves.Container(name)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	exists, created := container.HasParticipant(agentID)
	if !created || !exists {
		http.Error(w, "not a participant", http.StatusForbidden)
		return
	}
	ctx := waveop.Context{Creator: agentID, Timestamp: h.clk.Now().UnixMilli(), VersionIncrement: 1}
	delta := waveop.NewWaveletDelta(agentID, container.Version(),
		[]waveop.Operation{waveop.RemoveParticipant{Ctx: ctx, Participant: agentID}})
	if _, err := container.Submit(delta); err != nil {
		h.log().Error("agentgw: leave", "agent", agentID.Address(), "wave", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// bearerToken reads the token from the Authorization: Bearer header. It is
// deliberately header-only — a ?token= query param would leak the secret into
// access logs, browser history, and Referer headers (coder/websocket and any
// non-browser harness can set request headers).
func bearerToken(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if !strings.HasPrefix(a, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
}

// newBlipID mints a random id for blips the agent creates.
func newBlipID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "b+" + hex.EncodeToString(b[:])
}

// randLocalID mints a random local id (the suffix of a fresh wave's "w+<id>").
func randLocalID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
