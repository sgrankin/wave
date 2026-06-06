package transport

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clientcc"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/snapshot"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// newSessionID returns a random per-session token used to scope submission
// nonces (so a client recognizes only its own deltas).
func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// reconnectDelay is the pause before redialing after a recoverable failure.
// (Inside a testing/synctest bubble this is faked, so it costs no real time.)
const reconnectDelay = 100 * time.Millisecond

// fatalError marks a non-recoverable session error: the supervisor stops rather
// than reconnecting.
type fatalError struct{ err error }

func (e fatalError) Error() string { return e.err.Error() }
func (e fatalError) Unwrap() error { return e.err }

// OptimisticClient is the collaborative wavelet client: it applies local edits
// immediately to an optimistic replica (via the clientcc state machine) and
// transforms concurrent server deltas, instead of waiting for the server to echo
// its own edits. It opens with self-suppression, so its update stream carries only
// other participants' deltas and it learns its own outcomes from submit acks.
//
// A supervisor goroutine owns the connection lifecycle: it dials, runs a session
// (the first sends Open, reconnections send Resync), and on a recoverable failure
// (dropped connection, dropped live stream, or a TooOld/VersionError nack) redials
// and resyncs — preserving the optimistic edits the clientcc core holds across the
// gap (recognizing its own committed delta in the resync tail by nonce, or
// re-submitting an uncommitted one). A fatal failure (illegal op, protocol error)
// stops the supervisor. The clientcc core owns OT bookkeeping; this adapter owns
// connections, goroutines, and serialization.
type OptimisticClient struct {
	dial      func() (io.ReadWriteCloser, error)
	name      id.WaveletName
	author    id.ParticipantID
	sessionID string
	logger    *slog.Logger

	mu      sync.Mutex
	cond    *sync.Cond
	cc      *clientcc.CC
	out     chan []byte   // current session's outbound queue; nil while disconnected
	outDone chan struct{} // closed when the current session ends; escapes a blocked enqueue
	opened  bool          // first open completed
	openErr error
	fatal   error // non-recoverable failure; stops the supervisor and fails waiters
	closed  bool
	notify  chan struct{} // coalesced "replica changed" signal (buffered 1)

	closeOnce sync.Once
	done      chan struct{}
}

// NewOptimisticClient creates an optimistic client for the given wavelet, authoring
// as author. dial opens a fresh connection to the server; the supervisor calls it
// for the initial connection and again on each reconnect. The read/write goroutines
// start immediately; call Open to block until the initial open completes.
func NewOptimisticClient(dial func() (io.ReadWriteCloser, error), name id.WaveletName, author id.ParticipantID) *OptimisticClient {
	oc := &OptimisticClient{
		dial:      dial,
		name:      name,
		author:    author,
		sessionID: newSessionID(),
		logger:    slog.Default(),
		cc:        nil,
		notify:    make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	oc.cc = clientcc.New(name, author, version.Zero(name), oc.sessionID)
	oc.cond = sync.NewCond(&oc.mu)
	go oc.supervise()
	return oc
}

// Open blocks until the initial open completes (or the client fails/closes).
func (oc *OptimisticClient) Open() error {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	for !oc.opened && oc.fatal == nil && !oc.closed {
		oc.cond.Wait()
	}
	if oc.fatal != nil {
		return oc.fatal
	}
	if oc.closed {
		return errClosed
	}
	return oc.openErr
}

// SubmitWith builds and submits a local edit from a consistent snapshot: build is
// called once under the client lock with a reader for the optimistic replica's
// blips. The ops apply optimistically immediately; a delta is sent if the in-flight
// slot is free and a connection is up (otherwise the core queues it and the
// supervisor sends/resubmits it after the next (re)connect).
func (oc *OptimisticClient) SubmitWith(build func(blip func(blipID string) (op.DocOp, bool)) []waveop.Operation) error {
	oc.mu.Lock()
	if oc.fatal != nil {
		oc.mu.Unlock()
		return oc.fatal
	}
	ops := build(oc.cc.Blip)
	o, err := oc.cc.Edit(ops)
	oc.signalLocked()
	oc.mu.Unlock()
	if err != nil {
		return fmt.Errorf("transport: optimistic submit: %w", err)
	}
	oc.sendDelta(o)
	return nil
}

// Submit applies and submits ops as a single edit.
func (oc *OptimisticClient) Submit(ops []waveop.Operation) error {
	return oc.SubmitWith(func(func(string) (op.DocOp, bool)) []waveop.Operation { return ops })
}

// BlipContent returns the optimistic content of a blip and whether it exists.
func (oc *OptimisticClient) BlipContent(blipID string) (op.DocOp, bool) {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	return oc.cc.Blip(blipID)
}

// BlipIDs returns the ids of all blips in the optimistic replica.
func (oc *OptimisticClient) BlipIDs() []string {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	return oc.cc.BlipIDs()
}

// Version returns the latest confirmed server version.
func (oc *OptimisticClient) Version() version.HashedVersion {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	return oc.cc.ServerVersion()
}

// WaitServerVersion blocks until the confirmed server version reaches v (or the
// client fails/closes), returning any fatal error.
func (oc *OptimisticClient) WaitServerVersion(v uint64) error {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	for oc.cc.ServerVersion().Version() < v && oc.fatal == nil && !oc.closed {
		oc.cond.Wait()
	}
	if oc.fatal != nil {
		return oc.fatal
	}
	if oc.closed {
		return errClosed
	}
	return nil
}

// Updates returns a coalesced signal that fires when the optimistic replica changes.
func (oc *OptimisticClient) Updates() <-chan struct{} { return oc.notify }

// Close ends the session and unblocks any waiters.
func (oc *OptimisticClient) Close() error {
	oc.closeOnce.Do(func() {
		oc.mu.Lock()
		oc.closed = true
		oc.cond.Broadcast()
		oc.mu.Unlock()
		close(oc.done)
	})
	return nil
}

// --- supervisor ---

func (oc *OptimisticClient) supervise() {
	first := true
	for {
		if oc.isDone() {
			return
		}
		conn, err := oc.dial()
		if err != nil {
			oc.logger.Debug("optimistic dial failed; retrying", "err", err)
			if !oc.sleep(reconnectDelay) {
				return
			}
			continue
		}
		serr := oc.runSession(conn, first)
		first = false
		if oc.isDone() {
			return
		}
		var fe fatalError
		if errors.As(serr, &fe) {
			oc.setFatal(fe.err)
			return
		}
		oc.logger.Debug("optimistic session ended; reconnecting", "err", serr)
		if !oc.sleep(reconnectDelay) {
			return
		}
	}
}

// runSession drives one connection to completion. It sends Open (first) or Resync
// (reconnect), pumps writes from a per-session queue, and reads+handles frames
// until the connection fails or the client closes. The returned error is nil for a
// clean teardown, a fatalError for a non-recoverable condition, or a plain error
// for a recoverable one (the supervisor reconnects).
func (oc *OptimisticClient) runSession(conn io.ReadWriteCloser, first bool) error {
	out := make(chan []byte, 64)
	sessDone := make(chan struct{})
	var wg sync.WaitGroup

	oc.mu.Lock()
	oc.out = out
	oc.outDone = sessDone
	oc.mu.Unlock()

	wg.Add(1)
	go func() { // write pump
		defer wg.Done()
		for {
			select {
			case f := <-out:
				if err := writeFrame(conn, f); err != nil {
					return
				}
			case <-sessDone:
				return
			}
		}
	}()
	// Close the connection when the client closes, to unblock an in-progress read.
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-sessDone:
		case <-oc.done:
			_ = conn.Close()
		}
	}()

	// Initiate the session: open or resync.
	if first {
		out <- encodeOpen(oc.name.Serialize(), true)
	} else {
		oc.mu.Lock()
		v := oc.cc.ServerVersion()
		oc.mu.Unlock()
		out <- encodeResync(oc.name.Serialize(), v.Version(), v.HistoryHash())
	}

	serr := oc.readSession(conn)

	oc.mu.Lock()
	oc.out = nil
	oc.outDone = nil
	oc.mu.Unlock()
	close(sessDone)
	_ = conn.Close()
	wg.Wait()
	return serr
}

func (oc *OptimisticClient) readSession(conn io.ReadWriteCloser) error {
	for {
		data, err := readFrame(conn)
		if err != nil {
			if oc.isDone() {
				return nil
			}
			return err // recoverable: reconnect
		}
		if err := oc.handle(data); err != nil {
			return err
		}
	}
}

// sendDelta encodes and enqueues a client delta produced by the core, if any,
// tagging it with the core's submission nonce. A nil current queue (disconnected)
// drops it: the core retains the delta and the supervisor re-submits it on
// reconnect via AfterResync.
func (oc *OptimisticClient) sendDelta(o *clientcc.Outgoing) {
	if o == nil {
		return
	}
	db := codec.EncodeClientDelta(codec.ClientDelta{
		Author: o.Delta.Author(), TargetVersion: o.Delta.TargetVersion(), Ops: o.Delta.Ops(), Nonce: o.Nonce,
	})
	oc.enqueue(encodeSubmit(db))
}

// enqueue puts a frame on the current session's outbound queue, blocking only on
// backpressure (a full queue); a no-op while disconnected. It escapes if that
// session ends (outDone) — dropping the frame is safe, since the core re-derives
// any unsent delta on reconnect via AfterResync/trySend — or if the client closes.
func (oc *OptimisticClient) enqueue(f []byte) {
	oc.mu.Lock()
	out, outDone := oc.out, oc.outDone
	oc.mu.Unlock()
	if out == nil {
		return
	}
	select {
	case out <- f:
	case <-outDone:
	case <-oc.done:
	}
}

func (oc *OptimisticClient) handle(data []byte) error {
	kind, raw, err := messageKind(data)
	if err != nil {
		return fatalError{err}
	}
	switch kind {
	case mOpenResponse:
		snapshotBlob, history, err := decodeOpenResponse(raw)
		if err != nil {
			return fatalError{err}
		}
		return oc.applyOpen(snapshotBlob, history)
	case mResyncResponse:
		mode, tail, snapshotBlob, history, err := decodeResyncResponse(raw)
		if err != nil {
			return fatalError{err}
		}
		return oc.applyResync(mode, tail, snapshotBlob, history)
	case mUpdate:
		db, err := decodeUpdate(raw)
		if err != nil {
			return fatalError{err}
		}
		return oc.applyServerDelta(db)
	case mSubmitResponse:
		r, err := decodeSubmitResponse(raw)
		if err != nil {
			return fatalError{err}
		}
		return oc.applyAck(r)
	case mResyncRequired:
		// The live stream was dropped; reconnect and resync (recoverable).
		return errors.New("transport: live stream dropped; resyncing")
	case mError:
		msg, _ := decodeError(raw)
		return fatalError{fmt.Errorf("transport: server error: %s", msg)}
	default:
		return fatalError{fmt.Errorf("transport: unexpected message kind %d", kind)}
	}
}

func (oc *OptimisticClient) applyOpen(snapshotBlob []byte, history [][]byte) error {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if oc.opened {
		return fatalError{fmt.Errorf("transport: duplicate open response")}
	}
	if err := oc.initLocked(snapshotBlob, history); err != nil {
		oc.openErr = err
	}
	oc.opened = true
	oc.cond.Broadcast()
	oc.signalLocked()
	return nil
}

// applyResync reconciles a reconnection: a tail mode feeds the missed deltas (the
// core recognizes its own committed delta in them by nonce) then re-submits any
// still-unacked in-flight delta; a reset mode rebuilds the core from the full view,
// discarding unacknowledged local edits (the bounded-state fallback when the known
// version is no longer retained).
func (oc *OptimisticClient) applyResync(mode uint64, tail [][]byte, snapshotBlob []byte, history [][]byte) error {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	switch mode {
	case resyncReset:
		oc.cc = clientcc.New(oc.name, oc.author, version.Zero(oc.name), oc.sessionID)
		if err := oc.initLocked(snapshotBlob, history); err != nil {
			return fatalError{err}
		}
		oc.logger.Debug("optimistic resync reset (local edits dropped)")
	case resyncTail:
		for _, db := range tail {
			sd, err := codec.DecodeStoredDelta(db)
			if err != nil {
				return fatalError{err}
			}
			if _, err := oc.cc.OnServerDelta(sd.Ops, sd.ResultingVersion, sd.Nonce); err != nil {
				return fatalError{err}
			}
		}
		oc.sendDeltaLocked(oc.cc.AfterResync())
	default:
		return fatalError{fmt.Errorf("transport: unknown resync mode %d", mode)}
	}
	// A resync response means we are synced/open; mark opened so Open() unblocks even
	// in the (server-buggy) case it arrives before any open response.
	oc.opened = true
	oc.cond.Broadcast()
	oc.signalLocked()
	return nil
}

// initLocked seeds the core from a starting view: a current-state snapshot, or a
// replayed delta history from version zero. Must hold oc.mu.
func (oc *OptimisticClient) initLocked(snapshotBlob []byte, history [][]byte) error {
	if len(snapshotBlob) > 0 {
		state, err := snapshot.Decode(snapshotBlob)
		if err != nil {
			return err
		}
		w, err := wavelet.FromState(state)
		if err != nil {
			return err
		}
		blips := map[string]op.DocOp{}
		for _, bid := range w.BlipIDs() {
			if b, ok := w.Blip(bid); ok {
				blips[bid] = b.Content()
			}
		}
		oc.cc.LoadSnapshot(w.HashedVersion(), blips, w.Participants())
		return nil
	}
	for _, db := range history {
		sd, err := codec.DecodeStoredDelta(db)
		if err != nil {
			return err
		}
		if _, err := oc.cc.OnServerDelta(sd.Ops, sd.ResultingVersion, sd.Nonce); err != nil {
			return err
		}
	}
	return nil
}

func (oc *OptimisticClient) applyServerDelta(db []byte) error {
	sd, err := codec.DecodeStoredDelta(db)
	if err != nil {
		return fatalError{err}
	}
	oc.mu.Lock()
	o, err := oc.cc.OnServerDelta(sd.Ops, sd.ResultingVersion, sd.Nonce)
	if err == nil {
		oc.sendDeltaLocked(o)
	}
	oc.signalLocked()
	oc.mu.Unlock()
	if err != nil {
		return fatalError{err}
	}
	return nil
}

func (oc *OptimisticClient) applyAck(r submitResponse) error {
	if !r.OK {
		if isRecoverableNack(r.Code) {
			// Reconnect and resync; the in-flight delta is re-derived there.
			return fmt.Errorf("transport: submit nacked (code %d): %s; resyncing", r.Code, r.Msg)
		}
		return fatalError{fmt.Errorf("transport: submit nacked (code %d): %s", r.Code, r.Msg)}
	}
	rv, err := codec.DecodeHashedVersion(r.ResultingVersion)
	if err != nil {
		return fatalError{fmt.Errorf("transport: bad resulting version in ack: %w", err)}
	}
	oc.mu.Lock()
	o := oc.cc.OnAck(rv, r.OpsApplied)
	oc.sendDeltaLocked(o)
	oc.signalLocked()
	oc.mu.Unlock()
	return nil
}

// sendDeltaLocked enqueues a core-produced delta. Must hold oc.mu; it releases and
// reacquires the lock around the (potentially blocking) enqueue to avoid holding
// the lock during backpressure.
func (oc *OptimisticClient) sendDeltaLocked(o *clientcc.Outgoing) {
	if o == nil {
		return
	}
	oc.mu.Unlock()
	oc.sendDelta(o)
	oc.mu.Lock()
}

// signalLocked wakes version/open waiters and fires the replica-changed notify.
// Must hold oc.mu.
func (oc *OptimisticClient) signalLocked() {
	oc.cond.Broadcast()
	select {
	case oc.notify <- struct{}{}:
	default:
	}
}

func (oc *OptimisticClient) isDone() bool {
	select {
	case <-oc.done:
		return true
	default:
		return false
	}
}

// sleep waits d or until the client closes; returns false if the client closed.
func (oc *OptimisticClient) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-oc.done:
		return false
	}
}

func (oc *OptimisticClient) setFatal(err error) {
	oc.mu.Lock()
	if oc.fatal == nil {
		oc.fatal = err
	}
	oc.cond.Broadcast()
	oc.mu.Unlock()
}

// isRecoverableNack reports whether a nack response code should trigger a resync
// rather than fail the client. VersionError/TooOld mean the client's target was
// stale or pruned — recover by reconnecting and resyncing.
func isRecoverableNack(code uint64) bool {
	return code == uint64(cc.VersionError) || code == uint64(cc.TooOld)
}
