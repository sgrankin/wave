// Package presence is the transient awareness channel: who is in a wave, who is
// typing, and which blip they are focused on. It is deliberately separate from the
// OT delta transport — presence is lossy, high-frequency, and not part of the hash
// chain, so it must never perturb the convergence-critical delta path. See
// docs/architecture/07-presence.md.
//
// The Hub keeps an in-memory room per wavelet and fans a participant's state out to
// the others; nothing is persisted. The HTTP Handler upgrades /presence to a
// WebSocket, binds the authenticated participant (the client's claimed identity is
// ignored — the server stamps it), gates by wavelet membership, and bridges the
// socket to the hub.
package presence

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sgrankin/wave/internal/id"
)

// sendBuffer bounds a connection's outgoing presence queue. Presence is lossy: a
// full buffer drops the update (the next one refreshes), never blocking the hub.
const sendBuffer = 32

// Keepalive: ping idle sockets so a peer that vanished without a TCP FIN (laptop
// sleep, NAT drop) is reaped promptly — the failed ping closes the socket, the read
// loop returns, and the hub broadcasts the departure — instead of being shown
// "present/typing" forever.
const (
	pingInterval = 30 * time.Second
	pingTimeout  = 10 * time.Second
)

// State is a participant's own presence (client → server; participant ignored on
// the wire and stamped server-side). Anchor/Focus are the caret's rune offsets in
// the focused blip (Focus is the moving end; Anchor==Focus is a collapsed caret);
// -1 means "no caret here". They are RAW offsets, not OT-transformed — presence is
// deliberately off the convergence path, so a remote offset is briefly stale after a
// local edit until the peer re-publishes (see docs/architecture/07-presence.md §1).
type State struct {
	Typing bool   `json:"typing"`
	BlipID string `json:"blipId"` // the focused blip, "" for none
	Anchor int    `json:"anchor"` // selection anchor rune offset, -1 for none
	Focus  int    `json:"focus"`  // selection focus (caret) rune offset, -1 for none
}

// Update is one participant's presence change (server → client). Online=false is a
// departure: the client drops that participant.
type Update struct {
	Participant string `json:"participant"`
	Typing      bool   `json:"typing"`
	BlipID      string `json:"blipId"`
	Anchor      int    `json:"anchor"`
	Focus       int    `json:"focus"`
	Online      bool   `json:"online"`
}

// conn is one open presence socket for a participant in a wave.
type conn struct {
	participant id.ParticipantID
	state       State
	send        chan Update
}

// Hub keeps a room of presence connections per wavelet and fans state out. Methods
// are safe for concurrent use.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]map[*conn]struct{} // keyed by serialized wavelet name
}

// NewHub builds an empty hub.
func NewHub() *Hub { return &Hub{rooms: map[string]map[*conn]struct{}{}} }

// join registers c in the room and returns a snapshot of the other participants'
// current presence (so a late joiner sees who is already here). It also announces c
// (online, with its initial state) to the others.
func (h *Hub) join(room string, c *conn) []Update {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[room]
	if r == nil {
		r = map[*conn]struct{}{}
		h.rooms[room] = r
	}
	snapshot := make([]Update, 0, len(r))
	for other := range r {
		snapshot = append(snapshot, updateFrom(other, true))
	}
	r[c] = struct{}{}
	h.broadcastLocked(room, c, updateFrom(c, true))
	return snapshot
}

// update records c's new state and broadcasts it to the rest of the room.
func (h *Hub) update(room string, c *conn, s State) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.state = s
	h.broadcastLocked(room, c, updateFrom(c, true))
}

// leave deregisters c and announces its departure to the room.
func (h *Hub) leave(room string, c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[room]
	if r == nil {
		return
	}
	if _, ok := r[c]; !ok {
		return
	}
	delete(r, c)
	h.broadcastLocked(room, c, Update{Participant: c.participant.Address(), Online: false})
	if len(r) == 0 {
		delete(h.rooms, room)
	}
}

// broadcastLocked sends u to every connection in room except the sender. A full
// receiver queue drops the update (presence is lossy); it never blocks. Caller holds h.mu.
func (h *Hub) broadcastLocked(room string, sender *conn, u Update) {
	for other := range h.rooms[room] {
		if other == sender {
			continue
		}
		select {
		case other.send <- u:
		default: // slow consumer: drop this update, the next refreshes it
		}
	}
}

func updateFrom(c *conn, online bool) Update {
	return Update{
		Participant: c.participant.Address(),
		Typing:      c.state.Typing,
		BlipID:      c.state.BlipID,
		Anchor:      c.state.Anchor,
		Focus:       c.state.Focus,
		Online:      online,
	}
}

// AccessChecker gates presence by wavelet membership (mirrors transport.AccessChecker).
// A nil checker is dev-permissive (any authenticated participant may join), matching
// the OT socket's access policy so presence is gated exactly as the delta channel is.
type AccessChecker interface {
	CanAccess(participant id.ParticipantID, wavelet id.WaveletName) (bool, error)
}

// Handler serves the /presence WebSocket. Mount it behind auth.Service.Middleware
// so the participant is bound; it gates by membership and bridges the socket to the hub.
type Handler struct {
	baseCtx        context.Context
	hub            *Hub
	access         AccessChecker
	identify       func(*http.Request) (id.ParticipantID, bool)
	logger         *slog.Logger
	allowedOrigins []string
}

// New builds the presence Handler over a hub. baseCtx bounds every connection's
// lifetime — cancel it (e.g. on server shutdown) to drain all presence sockets
// promptly; nil means context.Background (connections live until the peer leaves).
// A nil logger uses slog.Default().
func New(baseCtx context.Context, hub *Hub, access AccessChecker, identify func(*http.Request) (id.ParticipantID, bool), logger *slog.Logger, allowedOrigins ...string) *Handler {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	return &Handler{baseCtx: baseCtx, hub: hub, access: access, identify: identify, logger: logger, allowedOrigins: allowedOrigins}
}

func (h *Handler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// ServeHTTP authenticates + membership-gates the request, upgrades to a WebSocket,
// joins the hub as the authenticated participant, and bridges socket ⇄ hub until the
// connection closes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if h.access != nil { // nil ⇒ dev-permissive (matches the OT socket)
		allowed, err := h.access.CanAccess(p, name)
		if err != nil {
			h.log().Error("presence: access check", "participant", p, "wave", name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.allowedOrigins})
	if err != nil {
		h.log().Debug("presence: websocket accept failed", "remote", r.RemoteAddr, "err", err)
		return
	}
	// Per-connection ctx derives from the server-lifetime base ctx, so a server
	// shutdown (base cancel) tears every presence socket down promptly.
	ctx, cancel := context.WithCancel(h.baseCtx)
	defer cancel()
	defer ws.Close(websocket.StatusNormalClosure, "")

	// Keepalive: a dead/wedged peer is reaped when a ping is not ponged in time.
	go h.keepalive(ctx, cancel, ws)

	room := name.Serialize()
	c := &conn{participant: p, send: make(chan Update, sendBuffer)}
	snapshot := h.hub.join(room, c)
	defer h.hub.leave(room, c)

	// Writer: drain the connection's outgoing queue (the join snapshot first, then
	// live updates) to the socket. Exits when ctx is cancelled.
	go func() {
		for _, u := range snapshot {
			if err := wsjson.Write(ctx, ws, u); err != nil {
				cancel()
				return
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-c.send:
				if err := wsjson.Write(ctx, ws, u); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Reader: each inbound message is the client's own state; stamp + broadcast it.
	for {
		var s State
		if err := wsjson.Read(ctx, ws, &s); err != nil {
			return // closed or errored: leave (deferred) tears the room entry down
		}
		h.hub.update(room, c, s)
	}
}

// keepalive pings ws every pingInterval; an unanswered ping (a dead peer) cancels
// the connection so the reader returns and the hub reaps it. Exits when ctx is done.
func (h *Handler) keepalive(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, pc := context.WithTimeout(ctx, pingTimeout)
			err := ws.Ping(pingCtx)
			pc()
			if err != nil {
				if ctx.Err() == nil { // a real ping failure, not a normal teardown
					cancel()
				}
				return
			}
		}
	}
}
