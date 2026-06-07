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
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/sgrankin/wave/internal/agent"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
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

// Handler serves GET /agent/socket.
type Handler struct {
	waves          *server.WaveMap
	auth           Auth
	access         AccessChecker
	clk            clock.Clock
	logger         *slog.Logger
	allowedOrigins []string
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

// ServeHTTP authenticates the agent (bearer token), checks wavelet membership,
// upgrades to a WebSocket, and runs the gateway over it as the agent.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	agentID, ok := h.auth.Agent(token)
	if token == "" || !ok {
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
