package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/fxamacker/cbor/v2"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/waveop"
)

// Serve handles one client connection: a single wavelet session. The first
// message must be Open (binding the connection to a wavelet); thereafter the
// client submits deltas and receives the wavelet's applied-delta stream (history
// snapshot, then live updates with no gap). Serve returns when the connection
// ends (clean EOF → nil) or a protocol error occurs.
func Serve(conn io.ReadWriter, wm *server.WaveMap) error {
	s := &session{conn: conn, wm: wm, out: make(chan []byte, 64), done: make(chan struct{})}
	return s.run()
}

// ListenAndServe accepts connections on ln and serves each (one goroutine per
// connection) until ctx is cancelled, at which point it closes ln and returns.
// It is the reusable multi-client binding; the production server (cmd/waved)
// wraps it with config, graceful drain, and operability.
func ListenAndServe(ctx context.Context, ln net.Listener, wm *server.WaveMap) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			_ = Serve(conn, wm)
		}()
	}
}

// session is the per-connection server state. A single writer goroutine drains
// out to the connection, so the read loop and the update forwarder can enqueue
// frames concurrently without interleaving.
type session struct {
	conn io.ReadWriter
	wm   *server.WaveMap

	out  chan []byte
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup

	container *server.WaveletContainer // set on Open (one wavelet per connection)
	sub       *server.Subscription
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
			// collapsed here too: with no logging story yet there is nothing to do
			// but end the session; revisit (log/surface the distinction) once waved
			// adds structured logging.
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
	nameStr, err := decodeOpen(raw)
	if err != nil {
		return err
	}
	name, err := id.ParseWaveletName(nameStr)
	if err != nil {
		s.push(encodeError("bad wavelet name: " + err.Error()))
		return nil
	}
	c, err := s.wm.Container(name)
	if err != nil {
		s.push(encodeError("open: " + err.Error()))
		return nil
	}
	history, sub := c.Open()
	s.container = c
	s.sub = sub

	hist := make([][]byte, len(history))
	for i, u := range history {
		hist[i] = encodeStored(u.Delta)
	}
	s.push(encodeOpenResponse(hist))

	s.wg.Add(1)
	go s.forward(sub)
	return nil
}

// forward streams the wavelet's live applied deltas to the client. If the
// subscription is dropped (the client fell too far behind — fan-out overflow),
// it tells the client to resync, then returns.
func (s *session) forward(sub *server.Subscription) {
	defer s.wg.Done()
	for {
		select {
		case u, ok := <-sub.Updates():
			if !ok {
				s.push(encodeError("update stream dropped; reopen to resync"))
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
	cd, err := codec.DecodeClientDelta(db)
	if err != nil {
		s.push(encodeSubmitResponse(false, uint64(cc.BadRequest), "bad delta: "+err.Error(), nil))
		return nil
	}
	delta := waveop.NewWaveletDelta(cd.Author, cd.TargetVersion, cd.Ops)
	res, err := s.container.Submit(delta)
	if err != nil {
		code := cc.InternalError
		var ce *cc.Error
		if errors.As(err, &ce) {
			code = ce.Code
		}
		s.push(encodeSubmitResponse(false, uint64(code), err.Error(), nil))
		return nil
	}
	s.push(encodeSubmitResponse(true, uint64(cc.OK), "", codec.EncodeHashedVersion(res.ResultingVersion)))
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
