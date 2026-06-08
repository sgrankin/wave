package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/fxamacker/cbor/v2"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/waveop"
)

// Server serves the session protocol to many connections over one shared
// WaveMap. It tracks active sessions for graceful drain and exposes cumulative
// counters for operability. A zero Server is not usable — set WaveMap.
type Server struct {
	WaveMap *server.WaveMap
	Logger  *slog.Logger // nil → slog.Default()

	// Access, when non-nil, gates Open: an authenticated participant may only
	// open a wavelet for which CanAccess returns true (membership). Nil allows
	// every open (dev-permissive). It applies to authenticated connections only
	// (the trusted socket/stdio path, with no authenticated participant, is not
	// gated). See handleOpen.
	Access AccessChecker

	// Seed, when non-nil, server-side-seeds a brand-new wavelet at its first Open
	// by an authenticated participant: it returns the operations that initialise
	// the wavelet (conversation manifest + root blip + AddParticipant(opener)),
	// applied atomically before the Open's starting view is taken. Nil disables
	// seeding (the wavelet stays empty until a client submits) — the default for
	// the trusted socket/stdio transports and the raw OT/CC tests. Seeding needs
	// a known author, so it runs only on authenticated connections.
	Seed func(opener id.ParticipantID) ([]waveop.Operation, error)

	wg sync.WaitGroup // active sessions started via Accept (for drain)
	m  Metrics

	// WebSocket-session lifecycle, for Shutdown. Each live WebSocket connection
	// registers a cancel (cancelling its conn context closes it) so Shutdown can
	// drain them; wsWG tracks them so Shutdown can wait.
	wsWG      sync.WaitGroup
	wsMu      sync.Mutex
	wsNextID  uint64
	wsCancels map[uint64]context.CancelFunc
}

// Metrics is a Server's operability counters. Read them via Server.Metrics; the
// fields are atomics and must not be copied.
type Metrics struct {
	ConnectionsTotal atomic.Int64 // connections served since start
	ActiveSessions   atomic.Int64 // sessions currently running
	SubmitsTotal     atomic.Int64 // submit requests processed
	SubmitErrors     atomic.Int64 // submit requests that nacked
}

// Metrics returns the server's live counters.
func (s *Server) Metrics() *Metrics { return &s.m }

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// ServeConn serves one connection — a single wavelet session — to completion
// (clean EOF → nil). It is safe to call concurrently for distinct connections.
// The connection is trusted: submitted deltas are authored as their wire-stated
// author with no identity check. Use it for the local socket / stdio transports
// behind a trust boundary; the WebSocket transport uses the authenticated path
// (WebSocketHandler) instead.
func (s *Server) ServeConn(conn io.ReadWriter) error {
	return s.serveConn(conn, nil)
}

// serveConn serves one connection, optionally bound to an authenticated
// participant. When participant is non-nil, every submitted delta must be
// authored by it (see session.handleSubmit) — a logged-in user cannot author as
// another. A nil participant trusts the wire-stated author.
func (s *Server) serveConn(conn io.ReadWriter, participant *id.ParticipantID) error {
	s.m.ConnectionsTotal.Add(1)
	s.m.ActiveSessions.Add(1)
	defer s.m.ActiveSessions.Add(-1)
	sess := &session{srv: s, conn: conn, authParticipant: participant, out: make(chan []byte, 64), done: make(chan struct{})}
	return sess.run()
}

// Accept runs the accept loop on ln until ctx is cancelled. Each connection is
// served in its own goroutine. On cancellation it stops accepting, closes any
// still-open connections to unblock their sessions, waits for them to drain, and
// returns nil. A non-cancellation accept error is returned.
func (s *Server) Accept(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	var mu sync.Mutex
	conns := map[net.Conn]struct{}{}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				// Shutting down: force-close any lingering sessions, then drain.
				mu.Lock()
				for c := range conns {
					_ = c.Close()
				}
				mu.Unlock()
				s.wg.Wait()
				s.logger().Info("transport drained", "served", s.m.ConnectionsTotal.Load())
				return nil
			}
			return fmt.Errorf("transport: accept: %w", err)
		}
		mu.Lock()
		conns[conn] = struct{}{}
		mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				mu.Lock()
				delete(conns, conn)
				mu.Unlock()
				_ = conn.Close()
			}()
			if err := s.ServeConn(conn); err != nil {
				s.logger().Warn("session ended with error", "remote", conn.RemoteAddr(), "err", err)
			}
		}()
	}
}

// Serve handles one connection with a transient default Server. Convenience for
// tests and simple single-connection callers; prefer a Server for real use.
func Serve(conn io.ReadWriter, wm *server.WaveMap) error {
	return (&Server{WaveMap: wm}).ServeConn(conn)
}

// ListenAndServe accepts and serves connections on ln until ctx is cancelled,
// using a transient default Server.
func ListenAndServe(ctx context.Context, ln net.Listener, wm *server.WaveMap) error {
	return (&Server{WaveMap: wm}).Accept(ctx, ln)
}

// registerWS records a live WebSocket connection's cancel func and returns its
// handle; unregisterWS removes it when the session ends.
func (s *Server) registerWS(cancel context.CancelFunc) uint64 {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.wsCancels == nil {
		s.wsCancels = map[uint64]context.CancelFunc{}
	}
	s.wsNextID++
	h := s.wsNextID
	s.wsCancels[h] = cancel
	return h
}

func (s *Server) unregisterWS(h uint64) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	delete(s.wsCancels, h)
}

// Shutdown drains the WebSocket sessions served via WebSocketHandler: it cancels
// every live connection (closing it, which ends its session) and waits for them
// to finish, or for ctx to expire. It returns ctx.Err() on timeout. Sessions
// served via Accept are drained by cancelling Accept's own context instead; this
// only covers the WebSocket transport (an http.Server's Shutdown does not close
// hijacked WebSocket connections).
func (s *Server) Shutdown(ctx context.Context) error {
	s.wsMu.Lock()
	for _, cancel := range s.wsCancels {
		cancel()
	}
	s.wsMu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// session is the per-connection server state. A single writer goroutine drains
// out to the connection, so the read loop and the update forwarder can enqueue
// frames concurrently without interleaving.
type session struct {
	srv  *Server
	conn io.ReadWriter

	out  chan []byte
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup

	container    *server.WaveletContainer // set on Open/Resync (one wavelet per connection)
	sub          *server.Subscription
	suppressEcho bool   // omit this connection's own deltas from its update stream
	waveletName  string // canonical name string, set on Open/Resync for logging

	// authParticipant, when non-nil, is the connection's authenticated identity:
	// every submitted delta must be authored by it. Nil on the trusted (socket /
	// stdio) transports.
	authParticipant *id.ParticipantID
}

func (s *session) run() error {
	s.wg.Add(1)
	go s.writeLoop()
	err := s.readLoop()
	s.shutdown()
	s.wg.Wait()
	return err
}

// shutdown tears the session down once: it stops the writer, ends the
// subscription, and closes the connection (if it is a Closer) to unblock a read
// in progress.
func (s *session) shutdown() {
	s.once.Do(func() {
		close(s.done)
		if s.sub != nil {
			s.sub.Close()
		}
		if cl, ok := s.conn.(io.Closer); ok {
			_ = cl.Close()
		}
	})
}

func (s *session) writeLoop() {
	defer s.wg.Done()
	for {
		select {
		case f := <-s.out:
			if err := writeFrame(s.conn, f); err != nil {
				s.shutdown()
				return
			}
		case <-s.done:
			return
		}
	}
}

// push enqueues a frame for the writer, abandoning it if the session is shutting
// down (rather than blocking forever on a dead connection).
func (s *session) push(f []byte) {
	select {
	case s.out <- f:
	case <-s.done:
	}
}

func (s *session) readLoop() error {
	for {
		data, err := readFrame(s.conn)
		if err != nil {
			// A connection ending — cleanly at a frame boundary (io.EOF), mid-frame
			// (io.ErrUnexpectedEOF), via our own shutdown close (net.ErrClosed), or
			// via a cancelled connection context (context.Canceled, used to drain a
			// WebSocket on Shutdown) — is a normal session end, not a server error.
			// Mid-frame truncation is collapsed here too: there is nothing to
			// recover, so we end the session.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
				errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		kind, raw, err := messageKind(data)
		if err != nil {
			return err
		}
		switch kind {
		case mOpen:
			if err := s.handleOpen(raw); err != nil {
				return err
			}
		case mResync:
			if err := s.handleResync(raw); err != nil {
				return err
			}
		case mSubmit:
			if err := s.handleSubmit(raw); err != nil {
				return err
			}
		default:
			s.push(encodeError(fmt.Sprintf("unexpected message kind %d", kind)))
		}
	}
}

// checkAccess enforces the Server's AccessChecker for an authenticated
// connection opening/resyncing name: it returns true if the participant may
// proceed, and false — after pushing an error frame the client surfaces — if
// access is denied or the check itself failed. A nil AccessChecker (dev-
// permissive) or an unauthenticated (trusted socket/stdio) connection always
// passes. action is the message name used in the error/log line ("open"/"resync").
//
// Membership is enforced ONLY at Open/Resync, not per delivered delta: once a
// session is subscribed (forward), a participant later removed from the wavelet
// keeps receiving its live stream until they disconnect. Revoking an in-flight
// subscription on RemoveParticipant is a deliberate future refinement (doc 04 §8),
// out of scope here.
func (s *session) checkAccess(name id.WaveletName, nameStr, action string) bool {
	if s.authParticipant == nil || s.srv.Access == nil {
		return true
	}
	allowed, err := s.srv.Access.CanAccess(*s.authParticipant, name)
	if err != nil {
		s.push(encodeError(action + ": access check failed: " + err.Error()))
		return false
	}
	if !allowed {
		s.push(encodeError("access denied: not a participant of " + nameStr))
		s.srv.logger().Debug(action+" denied", "wavelet", nameStr, "participant", s.authParticipant.Address())
		return false
	}
	return true
}

func (s *session) handleOpen(raw []cbor.RawMessage) error {
	if s.container != nil {
		s.push(encodeError("already opened (one wavelet per connection)"))
		return nil
	}
	nameStr, suppressEcho, err := decodeOpen(raw)
	if err != nil {
		return err
	}
	name, err := id.ParseWaveletName(nameStr)
	if err != nil {
		s.push(encodeError("bad wavelet name: " + err.Error()))
		return nil
	}
	c, err := s.srv.WaveMap.Container(name)
	if err != nil {
		s.push(encodeError("open: " + err.Error()))
		return nil
	}

	// Open-or-create: on an authenticated connection, seed a brand-new wavelet
	// (conversation manifest + root blip + AddParticipant(opener)) atomically
	// before taking the starting view, so the opener creates and joins it in one
	// step. SeedIfEmpty is a no-op if the wavelet already exists, so a second
	// opener racing the first does not double-seed. Seeding needs a known author,
	// so it runs only on authenticated connections.
	if s.authParticipant != nil && s.srv.Seed != nil {
		ops, err := s.srv.Seed(*s.authParticipant)
		if err != nil {
			s.push(encodeError("open: seed: " + err.Error()))
			return nil
		}
		if _, err := c.SeedIfEmpty(*s.authParticipant, ops); err != nil {
			s.push(encodeError("open: seed: " + err.Error()))
			return nil
		}
	}
	// Membership: an existing wavelet may be opened only by a participant (a fresh
	// opener just became one via the seed above).
	if !s.checkAccess(name, nameStr, "open") {
		return nil
	}

	s.suppressEcho = suppressEcho
	s.waveletName = nameStr
	snapshotBlob, history, sub := c.Open()
	s.container = c
	s.sub = sub

	hist := make([][]byte, len(history))
	for i, u := range history {
		hist[i] = encodeStored(u.Delta)
	}
	s.push(encodeOpenResponse(snapshotBlob, hist))
	s.srv.logger().Debug("wavelet opened", "wavelet", nameStr, "snapshot", len(snapshotBlob) > 0, "history", len(hist))

	s.wg.Add(1)
	go s.forward(sub)
	return nil
}

// handleResync is the reconnect join: a fresh connection that already holds state
// through (knownVersion, knownHash) catches up incrementally. On a hit the server
// sends the tail since that version and streams onward; on a miss (fork or pruned)
// it sends a reset (the full view) so the client rebuilds. Like Open, it binds the
// connection to one wavelet and is rejected if already bound.
func (s *session) handleResync(raw []cbor.RawMessage) error {
	if s.container != nil {
		s.push(encodeError("already opened (one wavelet per connection)"))
		return nil
	}
	nameStr, knownVersion, knownHash, suppressEcho, err := decodeResync(raw)
	if err != nil {
		return err
	}
	name, err := id.ParseWaveletName(nameStr)
	if err != nil {
		s.push(encodeError("bad wavelet name: " + err.Error()))
		return nil
	}
	c, err := s.srv.WaveMap.Container(name)
	if err != nil {
		s.push(encodeError("resync: " + err.Error()))
		return nil
	}
	// Membership: a reconnecting client must still be a participant. Resync does
	// not seed (a wavelet you can resync was already created); it only re-reads,
	// so without this check a non-member could bypass the Open membership gate by
	// reconnecting via Resync.
	if !s.checkAccess(name, nameStr, "resync") {
		return nil
	}
	s.suppressEcho = suppressEcho
	s.waveletName = nameStr

	if tail, sub, ok := c.OpenAt(knownVersion, knownHash); ok {
		s.container = c
		s.sub = sub
		td := make([][]byte, len(tail))
		for i, u := range tail {
			td[i] = encodeStored(u.Delta)
		}
		s.push(encodeResyncResponse(resyncTail, td, nil, nil))
		s.srv.logger().Debug("wavelet resynced", "wavelet", nameStr, "from", knownVersion, "tail", len(td))
		s.wg.Add(1)
		go s.forward(sub)
		return nil
	}

	// Reset: the known point is a fork or below the pruned floor. Send the full
	// view (as an open would) and let the client discard and rebuild.
	snapshotBlob, history, sub := c.Open()
	s.container = c
	s.sub = sub
	hist := make([][]byte, len(history))
	for i, u := range history {
		hist[i] = encodeStored(u.Delta)
	}
	s.push(encodeResyncResponse(resyncReset, nil, snapshotBlob, hist))
	s.srv.logger().Debug("wavelet resync reset", "wavelet", nameStr, "from", knownVersion, "history", len(hist))
	s.wg.Add(1)
	go s.forward(sub)
	return nil
}

// forward streams the wavelet's live applied deltas to the client. If the
// subscription is dropped (the client fell too far behind — fan-out overflow),
// it signals the client to reconnect and Resync, then returns.
func (s *session) forward(sub *server.Subscription) {
	defer s.wg.Done()
	for {
		select {
		case u, ok := <-sub.Updates():
			if !ok {
				s.push(encodeResyncRequired())
				return
			}
			s.srv.logger().Debug("delta delivered",
				"wavelet", s.waveletName,
				"resulting", u.Delta.ResultingVersion.Version(),
				"nonce", u.Delta.Nonce,
			)
			s.push(encodeUpdate(encodeStored(u.Delta)))
			// Authorization boundary: if this delta removed the connection's
			// participant, cut the stream. Membership is otherwise only checked at
			// Open/Resync, so without this a removed participant keeps receiving live
			// edits on their open connection until they disconnect. The removal delta
			// itself is pushed first (best-effort — the writer may not flush it before
			// the cut), but the GUARANTEE is that no edit applied AFTER the removal
			// reaches a non-member: the stream is cut here and a reconnect+resync is
			// then denied by checkAccess.
			if s.removesSelf(u.Delta) {
				s.srv.logger().Debug("participant removed; closing stream",
					"wavelet", s.waveletName, "participant", s.authParticipant.Address())
				s.shutdown()
				return
			}
		case <-s.done:
			return
		}
	}
}

// removesSelf reports whether delta removes this session's authenticated
// participant — the point past which they are no longer a member and must stop
// receiving the wavelet's live stream. Returns false on an unauthenticated
// (dev/local) connection, which has no participant boundary to enforce.
func (s *session) removesSelf(delta cc.TransformedWaveletDelta) bool {
	if s.authParticipant == nil {
		return false
	}
	for _, o := range delta.Ops {
		if rp, ok := o.(waveop.RemoveParticipant); ok && rp.Participant == *s.authParticipant {
			return true
		}
	}
	return false
}

func (s *session) handleSubmit(raw []cbor.RawMessage) error {
	if s.container == nil {
		s.push(encodeError("submit before open"))
		return nil
	}
	db, err := decodeSubmit(raw)
	if err != nil {
		return err
	}
	s.srv.m.SubmitsTotal.Add(1)
	cd, err := codec.DecodeClientDelta(db)
	if err != nil {
		s.srv.m.SubmitErrors.Add(1)
		s.push(encodeSubmitResponse(false, uint64(cc.BadRequest), "bad delta: "+err.Error(), nil, 0))
		s.srv.logger().Debug("delta rejected", "wavelet", s.waveletName, "nonce", "", "code", cc.BadRequest, "err", "bad delta: "+err.Error())
		return nil
	}
	s.srv.logger().Debug("delta submitted",
		"wavelet", s.waveletName,
		"author", cd.Author,
		"target", cd.TargetVersion.Version(),
		"ops", len(cd.Ops),
		"blips", blipIDs(cd.Ops),
		"nonce", cd.Nonce,
	)
	// On an authenticated connection, the delta must be authored by the verified
	// participant: a client may not submit deltas as someone else.
	if s.authParticipant != nil && cd.Author != *s.authParticipant {
		s.srv.m.SubmitErrors.Add(1)
		s.push(encodeSubmitResponse(false, uint64(cc.BadRequest),
			"delta author does not match authenticated participant", nil, 0))
		s.srv.logger().Debug("delta rejected", "wavelet", s.waveletName, "nonce", cd.Nonce, "code", cc.BadRequest, "err", "delta author does not match authenticated participant")
		return nil
	}
	delta := waveop.NewWaveletDelta(cd.Author, cd.TargetVersion, cd.Ops)
	var exclude *server.Subscription
	if s.suppressEcho {
		exclude = s.sub
	}
	res, err := s.container.SubmitFrom(delta, cd.Nonce, exclude)
	if err != nil {
		code := cc.InternalError
		var ce *cc.Error
		if errors.As(err, &ce) {
			code = ce.Code
		}
		s.srv.m.SubmitErrors.Add(1)
		s.push(encodeSubmitResponse(false, uint64(code), err.Error(), nil, 0))
		s.srv.logger().Debug("delta rejected", "wavelet", s.waveletName, "nonce", cd.Nonce, "code", code, "err", err.Error())
		return nil
	}
	s.srv.logger().Debug("delta applied",
		"wavelet", s.waveletName,
		"nonce", cd.Nonce,
		"resulting", res.ResultingVersion.Version(),
		"opsApplied", res.OpsApplied,
	)
	s.push(encodeSubmitResponse(true, uint64(cc.OK), "", codec.EncodeHashedVersion(res.ResultingVersion), uint64(res.OpsApplied)))
	return nil
}

// encodeStored encodes an applied delta for the wire (history and updates share
// the StoredDelta payload: author, resulting version, timestamp, ops).
func encodeStored(d cc.TransformedWaveletDelta) []byte {
	return codec.EncodeStoredDelta(codec.StoredDelta{
		Author:           d.Author,
		ResultingVersion: d.ResultingVersion,
		Timestamp:        d.Timestamp,
		Ops:              d.Ops,
		Nonce:            d.Nonce,
	})
}

// blipIDs extracts the distinct blip IDs touched by the given wavelet
// operations. Used only in debug log lines (behind the Debug level).
func blipIDs(ops []waveop.Operation) []string {
	seen := map[string]bool{}
	var ids []string
	for _, op := range ops {
		if blip, ok := op.(waveop.WaveletBlipOperation); ok {
			if !seen[blip.BlipID] {
				seen[blip.BlipID] = true
				ids = append(ids, blip.BlipID)
			}
		}
	}
	return ids
}
