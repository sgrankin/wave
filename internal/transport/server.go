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

	wg sync.WaitGroup // active sessions started via Accept (for drain)
	m  Metrics
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
func (s *Server) ServeConn(conn io.ReadWriter) error {
	s.m.ConnectionsTotal.Add(1)
	s.m.ActiveSessions.Add(1)
	defer s.m.ActiveSessions.Add(-1)
	sess := &session{srv: s, conn: conn, out: make(chan []byte, 64), done: make(chan struct{})}
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
	suppressEcho bool // omit this connection's own deltas from its update stream
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
			// (io.ErrUnexpectedEOF), or via our own shutdown close (net.ErrClosed) —
			// is a normal session end, not a server error. Mid-frame truncation is
			// collapsed here too: there is nothing to recover, so we end the session.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
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
	s.suppressEcho = suppressEcho
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
	s.suppressEcho = suppressEcho

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
			s.push(encodeUpdate(encodeStored(u.Delta)))
		case <-s.done:
			return
		}
	}
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
		return nil
	}
	delta := waveop.NewWaveletDelta(cd.Author, cd.TargetVersion, cd.Ops)
	var exclude *server.Subscription
	if s.suppressEcho {
		exclude = s.sub
	}
	res, err := s.container.SubmitFrom(delta, exclude)
	if err != nil {
		code := cc.InternalError
		var ce *cc.Error
		if errors.As(err, &ce) {
			code = ce.Code
		}
		s.srv.m.SubmitErrors.Add(1)
		s.push(encodeSubmitResponse(false, uint64(code), err.Error(), nil, 0))
		return nil
	}
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
	})
}
