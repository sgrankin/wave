package transport

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/snapshot"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// errClosed is the read error recorded when the client is closed locally.
var errClosed = errors.New("transport: client closed")

// Client is a wavelet session client over a byte stream. It maintains a local
// wavelet replica by applying the server's broadcast deltas (the same verified
// replay the server uses on load), so callers can read the converged document.
//
// This is the simple, "pessimistic" client used by the headless harness and the
// CLI: it does not apply its own edits optimistically — they are reflected when
// the server broadcasts them back. Submit is synchronous and serialized, which
// gives "one delta in flight per wavelet" for free. Optimistic local apply with
// client-side transform (the in-flight/queue model) is a separate increment.
type Client struct {
	conn   io.ReadWriter
	name   id.WaveletName
	author id.ParticipantID

	out  chan []byte
	done chan struct{}
	once sync.Once

	submitMu sync.Mutex          // serializes Submit: at most one delta in flight
	acks     chan submitResponse // ack/nack for the in-flight submit (depth 1)

	mu      sync.Mutex
	cond    *sync.Cond
	zero    version.HashedVersion
	cur     version.HashedVersion
	replica *wavelet.Data // nil until the first delta; built by applying broadcasts
	opened  bool
	openErr error
	readErr error
	notify  chan struct{} // coalesced "state changed" signal for UIs (buffered 1)
}

// NewClient creates a client over conn for the given wavelet, authoring as
// author. It starts the read/write goroutines immediately; call Open next.
func NewClient(conn io.ReadWriter, name id.WaveletName, author id.ParticipantID) *Client {
	c := &Client{
		conn:   conn,
		name:   name,
		author: author,
		out:    make(chan []byte, 64),
		done:   make(chan struct{}),
		acks:   make(chan submitResponse, 1),
		zero:   version.Zero(name),
		notify: make(chan struct{}, 1),
	}
	c.cur = c.zero
	c.cond = sync.NewCond(&c.mu)
	go c.writeLoop()
	go c.readLoop()
	return c
}

// Open binds the connection to the wavelet and applies the history snapshot,
// blocking until the server's open response is processed.
func (c *Client) Open() error {
	// suppressEcho=false: this pessimistic replica client advances by applying its
	// own delta when the server echoes it back, so it must receive that echo.
	if err := c.send(encodeOpen(c.name.Serialize(), false)); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for !c.opened && c.readErr == nil {
		c.cond.Wait()
	}
	if c.readErr != nil {
		return c.readErr
	}
	return c.openErr
}

// Submit sends ops as a delta targeting the client's current version. See
// SubmitWith for the lifecycle (synchronous ack, then catch-up).
func (c *Client) Submit(ops []waveop.Operation) (version.HashedVersion, error) {
	return c.SubmitWith(func(func(string) (op.DocOp, bool)) []waveop.Operation { return ops })
}

// SubmitWith builds and submits a delta from a consistent snapshot. build is
// called exactly once with the client lock held, receiving a reader for the
// local replica's blips; the version it targets is captured in the same locked
// section. This guarantees the operations are built against precisely the
// version they target — essential for position-dependent ops (a retain count)
// under concurrent edits: sampling document length and target version
// separately could yield an op that is malformed for its target version (when a
// broadcast advances the version in between, the op no longer needs transforming
// yet was built for an earlier length). build must not block or call back into
// the client (it runs under the lock).
//
// After the server's ack, SubmitWith waits until the local replica has caught up
// to the resulting version, so the next submit targets a current version
// (avoiding a re-submit at a stale version). It returns the resulting hashed
// version. A transformed-away (no-op) submit returns the server's current
// version with no error; a nack returns a *cc.Error carrying the response code.
func (c *Client) SubmitWith(build func(blip func(blipID string) (op.DocOp, bool)) []waveop.Operation) (version.HashedVersion, error) {
	c.submitMu.Lock()
	defer c.submitMu.Unlock()

	c.mu.Lock()
	target := c.cur
	ops := build(c.blipLocked)
	c.mu.Unlock()

	db := codec.EncodeClientDelta(codec.ClientDelta{Author: c.author, TargetVersion: target, Ops: ops})
	if err := c.send(encodeSubmit(db)); err != nil {
		return version.HashedVersion{}, err
	}

	select {
	case r := <-c.acks:
		if !r.OK {
			return version.HashedVersion{}, &cc.Error{Code: cc.ResponseCode(r.Code), Msg: r.Msg}
		}
		rv, err := codec.DecodeHashedVersion(r.ResultingVersion)
		if err != nil {
			return version.HashedVersion{}, fmt.Errorf("transport: bad resulting version in ack: %w", err)
		}
		if err := c.WaitVersion(rv.Version()); err != nil {
			return version.HashedVersion{}, err
		}
		return rv, nil
	case <-c.done:
		return version.HashedVersion{}, c.err()
	}
}

// WaitVersion blocks until the local replica has applied through version v (or
// the connection fails). It returns the read error if the connection failed.
func (c *Client) WaitVersion(v uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.cur.Version() < v && c.readErr == nil {
		c.cond.Wait()
	}
	return c.readErr
}

// Version returns the client's current (last applied) hashed version.
func (c *Client) Version() version.HashedVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

// BlipContent returns the content document of the named blip in the local
// replica, and whether the blip exists.
func (c *Client) BlipContent(blipID string) (op.DocOp, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blipLocked(blipID)
}

// blipLocked reads a blip's content from the replica. Must be called with c.mu
// held (it is the reader handed to SubmitWith's build callback).
func (c *Client) blipLocked(blipID string) (op.DocOp, bool) {
	if c.replica == nil {
		return op.DocOp{}, false
	}
	b, ok := c.replica.Blip(blipID)
	if !ok {
		return op.DocOp{}, false
	}
	return b.Content(), true
}

// BlipIDs returns the ids of all blips in the local replica, sorted.
func (c *Client) BlipIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.replica == nil {
		return nil
	}
	return c.replica.BlipIDs()
}

// Updates returns a coalesced signal that fires when the local replica changes
// (history applied or a live delta applied). A UI ranges over it to re-render.
func (c *Client) Updates() <-chan struct{} { return c.notify }

// Close ends the session and unblocks any waiters.
func (c *Client) Close() error {
	c.fail(errClosed)
	return nil
}

// --- internals ---

func (c *Client) send(f []byte) error {
	select {
	case c.out <- f:
		return nil
	case <-c.done:
		return c.err()
	}
}

func (c *Client) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	return errClosed
}

func (c *Client) writeLoop() {
	for {
		select {
		case f := <-c.out:
			if err := writeFrame(c.conn, f); err != nil {
				c.fail(err)
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) readLoop() {
	for {
		data, err := readFrame(c.conn)
		if err != nil {
			c.fail(err)
			return
		}
		if err := c.handle(data); err != nil {
			c.fail(err)
			return
		}
	}
}

// fail records the first failure, closes the connection (to unblock the read),
// stops the writer, and wakes all waiters.
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	c.once.Do(func() {
		close(c.done)
		if cl, ok := c.conn.(io.Closer); ok {
			_ = cl.Close()
		}
	})
}

func (c *Client) handle(data []byte) error {
	kind, raw, err := messageKind(data)
	if err != nil {
		return err
	}
	switch kind {
	case mOpenResponse:
		snapshotBlob, history, err := decodeOpenResponse(raw)
		if err != nil {
			return err
		}
		return c.applyOpen(snapshotBlob, history)
	case mUpdate:
		db, err := decodeUpdate(raw)
		if err != nil {
			return err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.applyStoredLocked(db)
	case mSubmitResponse:
		r, err := decodeSubmitResponse(raw)
		if err != nil {
			return err
		}
		select {
		case c.acks <- r:
			return nil
		default:
			return fmt.Errorf("transport: unexpected submit response (none in flight)")
		}
	case mResyncRequired:
		// This pessimistic client does not resync incrementally; a dropped stream
		// is a fatal session error (reopen a fresh client to recover).
		return fmt.Errorf("transport: live stream dropped (resync required); reopen")
	case mError:
		msg, _ := decodeError(raw)
		return fmt.Errorf("transport: server error: %s", msg)
	default:
		return fmt.Errorf("transport: unexpected message kind %d", kind)
	}
}

func (c *Client) applyOpen(snapshotBlob []byte, history [][]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opened {
		return fmt.Errorf("transport: duplicate open response")
	}
	if len(snapshotBlob) > 0 {
		// Snapshot-based join: initialize the replica from the current-state
		// snapshot, then apply any tail history on top. (The current server sends
		// an empty history with a snapshot — the loop below is a no-op there — but
		// the client stays general in case a future server combines the two.)
		// Live deltas follow from this version (the server took the snapshot and
		// subscribed atomically).
		if err := c.initFromSnapshotLocked(snapshotBlob); err != nil {
			c.openErr = err
			c.opened = true
			c.cond.Broadcast()
			return err
		}
	}
	for _, db := range history {
		if err := c.applyStoredLocked(db); err != nil {
			c.openErr = err
			c.opened = true
			c.cond.Broadcast()
			return err
		}
	}
	c.opened = true
	c.cond.Broadcast()
	c.signal()
	return nil
}

// initFromSnapshotLocked reconstructs the replica from a current-state snapshot.
// Must be called with c.mu held, before any delta is applied.
func (c *Client) initFromSnapshotLocked(blob []byte) error {
	state, err := snapshot.Decode(blob)
	if err != nil {
		return err
	}
	w, err := wavelet.FromState(state)
	if err != nil {
		return err
	}
	c.replica = w
	c.cur = w.HashedVersion()
	return nil
}

// applyStoredLocked decodes a stored (applied) delta and applies it to the local
// replica, advancing the version. It mirrors server.Load: it recomputes the
// canonical hash bytes and verifies the resulting version against the one the
// server sent. Must be called with c.mu held.
func (c *Client) applyStoredLocked(db []byte) error {
	sd, err := codec.DecodeStoredDelta(db)
	if err != nil {
		return err
	}
	if uint64(len(sd.Ops)) > sd.ResultingVersion.Version() {
		return fmt.Errorf("transport: delta has more ops than its resulting version")
	}
	appliedAt := sd.ResultingVersion.Version() - uint64(len(sd.Ops))
	if appliedAt != c.cur.Version() {
		return fmt.Errorf("transport: stream gap: delta applies at %d, replica at %d", appliedAt, c.cur.Version())
	}
	if c.replica == nil {
		c.replica = wavelet.New(c.name.Wave(), c.name.Wavelet(), sd.Author, sd.Timestamp, c.zero)
	}
	hashBytes := codec.HashBytes(sd.Author, appliedAt, sd.Timestamp, sd.Ops)
	d := waveop.NewWaveletDelta(sd.Author, c.replica.HashedVersion(), sd.Ops)
	if err := c.replica.ApplyDelta(d, hashBytes); err != nil {
		return err
	}
	if c.replica.HashedVersion().Compare(sd.ResultingVersion) != 0 {
		return fmt.Errorf("transport: replica hash mismatch at version %d", sd.ResultingVersion.Version())
	}
	c.cur = c.replica.HashedVersion()
	c.cond.Broadcast()
	c.signal()
	return nil
}

// signal fires the coalesced update notification (non-blocking).
func (c *Client) signal() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}
