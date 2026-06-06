package transport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/sgrankin/wave/internal/id"
)

// wsReadLimit bounds a single inbound WebSocket message. Each framed envelope is
// sent as exactly one binary message (writeFrame does a single Write, which
// coder/websocket's NetConn maps to one message), so a legitimate message never
// exceeds the frame layer's own cap plus its 4-byte length prefix. The bound is
// applied AFTER NetConn, which disables the per-message limit (sets it to -1):
// without it a hostile peer could stream one unbounded message and exhaust memory
// before readFrame's own maxFrameSize check (which only bounds the decoded length,
// not the bytes already buffered off the wire).
const wsReadLimit = maxFrameSize + frameHeaderSize

// WebSocket keepalive: ping idle connections so a peer that vanished without a TCP
// FIN (laptop sleep, NAT drop, partition — common for long-lived realtime sockets)
// is reaped promptly instead of leaking a session, writer, subscription, and
// registry entry until the OS TCP keepalive trips hours later.
const (
	wsPingInterval = 30 * time.Second
	wsPingTimeout  = 10 * time.Second
)

// WebSocketHandler returns an http.Handler that upgrades a request to a WebSocket
// and serves one wavelet session over it, speaking the same framed-CBOR protocol
// as ServeConn does over any byte stream. The WebSocket's binary-message stream is
// wrapped as an io.ReadWriteCloser (coder/websocket's NetConn) and handed to the
// ordinary session loop, so the protocol, framing, fan-out, and the headless
// client/tests all carry over unchanged — the WebSocket is a leaf transport, not a
// second protocol.
//
// identify resolves the authenticated participant from the request; the upgrade is
// rejected with 401 before the WebSocket handshake when it returns ok=false. Mount
// the handler behind an authentication middleware (e.g. auth.Service.Middleware
// with identify = auth.ParticipantFrom) so the session cookie is verified before
// Accept. The resolved participant is bound to the session: every submitted delta
// must be authored by it (a mismatch is nacked), so a logged-in user cannot author
// as another.
//
// AUTHORIZATION SCOPE: this binds delta AUTHORSHIP only. It does NOT check that the
// participant is a member of the wavelet they Open — any authenticated user can
// open and read any wavelet by name. Wavelet access control (a participation
// predicate, cf. attachapi.AccessChecker) is a separate, not-yet-wired layer.
//
// By default only same-origin upgrades are accepted (the browser sends an Origin
// header; a cross-origin one is rejected, blocking cross-site WebSocket hijacking;
// a request with no Origin — a non-browser client — is allowed). Pass allowedOrigins
// to authorize additional origin hosts when the client is served from a different
// host than the API. Do NOT pass "*" (allow-all); list specific hosts.
func (s *Server) WebSocketHandler(identify func(*http.Request) (id.ParticipantID, bool), allowedOrigins ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		participant, ok := identify(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: allowedOrigins})
		if err != nil {
			// Accept has already written an error response to w.
			s.logger().Debug("websocket accept failed", "remote", r.RemoteAddr, "err", err)
			return
		}

		// Count the session before it is reachable by Shutdown: registerWS makes the
		// connection visible to Shutdown's cancel sweep, so wsWG must already be
		// incremented or Shutdown could observe a zero count and "drain" a session
		// that is still starting (a WaitGroup Add racing a returned Wait).
		s.wsWG.Add(1)
		defer s.wsWG.Done()

		// The NetConn's context bounds the connection's lifetime. Do NOT derive it
		// from r.Context() — after Accept hijacks the connection that context's
		// cancellation is unreliable. registerWS exposes cancel to Shutdown; the
		// deferred cancel runs before wsWG.Done (LIFO) so the keepalive goroutine and
		// any in-flight read/write are torn down before the session is marked drained.
		ctx, cancel := context.WithCancel(context.Background())
		h := s.registerWS(cancel)
		defer s.unregisterWS(h)
		defer cancel()

		conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
		c.SetReadLimit(wsReadLimit) // after NetConn, which would otherwise leave it disabled (-1)

		go s.wsKeepalive(ctx, cancel, c)

		// participant is this handler invocation's own local (per-request copy), so
		// taking its address for the session's lifetime is safe — no aliasing.
		if err := s.serveConn(conn, &participant); err != nil {
			s.logger().Warn("websocket session ended with error",
				"participant", participant.Address(), "err", err)
		}
	})
}

// wsKeepalive pings c every wsPingInterval; if a ping is not ponged within
// wsPingTimeout (a dead or wedged peer), it cancels the connection so the session
// tears down. It exits when ctx is cancelled (the session ended). Ping is safe to
// call concurrently with the NetConn's reader/writer (coder/websocket serializes
// control frames internally; pongs are processed by the active reader).
func (s *Server) wsKeepalive(ctx context.Context, cancel context.CancelFunc, c *websocket.Conn) {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, pc := context.WithTimeout(ctx, wsPingTimeout)
			err := c.Ping(pingCtx)
			pc()
			if err != nil {
				// ctx already cancelled → the session is tearing down, not a dead peer.
				if ctx.Err() == nil {
					s.logger().Debug("websocket keepalive failed; closing", "err", err)
					cancel()
				}
				return
			}
		}
	}
}

// DialWebSocket connects to a wavelet-session WebSocket endpoint at url (ws:// or
// wss://) and returns the connection as the io.ReadWriteCloser the client dial
// seam expects, carrying the framed-CBOR protocol. ctx bounds the connection's
// lifetime (cancelling it closes the connection). opts may carry request headers
// (e.g. a session cookie via HTTPHeader) and a custom HTTPClient; nil uses
// defaults. Prefer WebSocketDialer for OptimisticClient/NewClient, which reconnect.
func DialWebSocket(ctx context.Context, url string, opts *websocket.DialOptions) (io.ReadWriteCloser, error) {
	c, resp, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, fmt.Errorf("transport: websocket dial: %w", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close() // the upgrade response body carries nothing for us
	}
	conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
	c.SetReadLimit(wsReadLimit) // after NetConn, which would otherwise leave it disabled (-1)
	return conn, nil
}

// WebSocketDialer returns a dial function for OptimisticClient/NewClient: it opens
// a fresh WebSocket to url on each call (the supervisor calls it once per
// connect/reconnect). ctx bounds every connection, so it must outlive the client;
// each connection derives its own cancellation, so closing one (on reconnect) does
// not disturb the others. newOpts, if non-nil, is called per dial to build fresh
// DialOptions — use it to attach a current session cookie or auth header that may
// have rotated between reconnects.
func WebSocketDialer(ctx context.Context, url string, newOpts func() *websocket.DialOptions) func() (io.ReadWriteCloser, error) {
	return func() (io.ReadWriteCloser, error) {
		var opts *websocket.DialOptions
		if newOpts != nil {
			opts = newOpts()
		}
		return DialWebSocket(ctx, url, opts)
	}
}
