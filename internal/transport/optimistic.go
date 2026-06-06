package transport

import (
	"fmt"
	"io"
	"sync"

	"github.com/sgrankin/wave/internal/clientcc"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/snapshot"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// OptimisticClient is the collaborative wavelet client: it applies local edits
// immediately to an optimistic replica (via the clientcc state machine) and
// transforms concurrent server deltas, instead of waiting for the server to echo
// its own edits. It opens with self-suppression, so its update stream carries only
// other participants' deltas and it learns its own outcomes from submit acks.
//
// This is the in-flight/queue ("optimistic") counterpart to Client (the pessimistic
// replica client). The clientcc core owns all OT bookkeeping; this adapter owns the
// connection, goroutines, and serialization. It drives a single connection — nack
// recovery and reconnect/resync are not yet wired (a dropped stream is fatal here).
type OptimisticClient struct {
	conn   io.ReadWriter
	name   id.WaveletName
	author id.ParticipantID

	out  chan []byte
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	cond    *sync.Cond
	cc      *clientcc.CC // guarded by mu
	opened  bool
	openErr error
	readErr error
	notify  chan struct{} // coalesced "replica changed" signal (buffered 1)
}

// NewOptimisticClient creates an optimistic client over conn for the given wavelet,
// authoring as author. It starts the read/write goroutines; call Open next.
func NewOptimisticClient(conn io.ReadWriter, name id.WaveletName, author id.ParticipantID) *OptimisticClient {
	oc := &OptimisticClient{
		conn:   conn,
		name:   name,
		author: author,
		out:    make(chan []byte, 64),
		done:   make(chan struct{}),
		cc:     clientcc.New(name, author, version.Zero(name)),
		notify: make(chan struct{}, 1),
	}
	oc.cond = sync.NewCond(&oc.mu)
	go oc.writeLoop()
	go oc.readLoop()
	return oc
}

// Open binds the connection to the wavelet (requesting self-suppression) and
// applies the starting view, blocking until the open response is processed.
func (oc *OptimisticClient) Open() error {
	if err := oc.send(encodeOpen(oc.name.Serialize(), true)); err != nil {
		return err
	}
	oc.mu.Lock()
	defer oc.mu.Unlock()
	for !oc.opened && oc.readErr == nil {
		oc.cond.Wait()
	}
	if oc.readErr != nil {
		return oc.readErr
	}
	return oc.openErr
}

// Submit applies ops as a local edit (optimistically, against the current replica)
// and submits them. It returns as soon as the edit is applied locally — the ack and
// any transforms are handled asynchronously. See SubmitWith for building ops from a
// consistent snapshot.
func (oc *OptimisticClient) Submit(ops []waveop.Operation) error {
	return oc.SubmitWith(func(func(string) (op.DocOp, bool)) []waveop.Operation { return ops })
}

// SubmitWith builds and submits a local edit from a consistent snapshot: build is
// called once under the client lock with a reader for the optimistic replica's
// blips, so position-dependent ops are authored against exactly the state they
// apply to. It applies the ops optimistically and sends a delta if the in-flight
// slot is free (else they queue).
func (oc *OptimisticClient) SubmitWith(build func(blip func(blipID string) (op.DocOp, bool)) []waveop.Operation) error {
	oc.mu.Lock()
	if oc.readErr != nil {
		oc.mu.Unlock()
		return oc.readErr
	}
	ops := build(oc.cc.Blip)
	d, err := oc.cc.Edit(ops)
	oc.signalLocked()
	oc.mu.Unlock()
	if err != nil {
		return fmt.Errorf("transport: optimistic submit: %w", err)
	}
	oc.sendDelta(d)
	return nil
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
// connection fails), returning the read error if it failed.
func (oc *OptimisticClient) WaitServerVersion(v uint64) error {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	for oc.cc.ServerVersion().Version() < v && oc.readErr == nil {
		oc.cond.Wait()
	}
	return oc.readErr
}

// Updates returns a coalesced signal that fires when the optimistic replica
// changes (a local edit, an applied server delta, or a settled ack).
func (oc *OptimisticClient) Updates() <-chan struct{} { return oc.notify }

// Close ends the session and unblocks any waiters.
func (oc *OptimisticClient) Close() error {
	oc.fail(errClosed)
	return nil
}

// --- internals ---

// sendDelta encodes and enqueues a client delta produced by the core, if any. The
// core's one-in-flight invariant serializes emissions, so frames stay ordered even
// though this runs outside the lock.
func (oc *OptimisticClient) sendDelta(d *waveop.WaveletDelta) {
	if d == nil {
		return
	}
	db := codec.EncodeClientDelta(codec.ClientDelta{
		Author: d.Author(), TargetVersion: d.TargetVersion(), Ops: d.Ops(),
	})
	_ = oc.send(encodeSubmit(db))
}

func (oc *OptimisticClient) send(f []byte) error {
	select {
	case oc.out <- f:
		return nil
	case <-oc.done:
		return oc.err()
	}
}

func (oc *OptimisticClient) err() error {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if oc.readErr != nil {
		return oc.readErr
	}
	return errClosed
}

func (oc *OptimisticClient) writeLoop() {
	for {
		select {
		case f := <-oc.out:
			if err := writeFrame(oc.conn, f); err != nil {
				oc.fail(err)
				return
			}
		case <-oc.done:
			return
		}
	}
}

func (oc *OptimisticClient) readLoop() {
	for {
		data, err := readFrame(oc.conn)
		if err != nil {
			oc.fail(err)
			return
		}
		if err := oc.handle(data); err != nil {
			oc.fail(err)
			return
		}
	}
}

func (oc *OptimisticClient) fail(err error) {
	oc.mu.Lock()
	if oc.readErr == nil {
		oc.readErr = err
	}
	oc.cond.Broadcast()
	oc.mu.Unlock()
	oc.once.Do(func() {
		close(oc.done)
		if cl, ok := oc.conn.(io.Closer); ok {
			_ = cl.Close()
		}
	})
}

func (oc *OptimisticClient) handle(data []byte) error {
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
		return oc.applyOpen(snapshotBlob, history)
	case mUpdate:
		db, err := decodeUpdate(raw)
		if err != nil {
			return err
		}
		return oc.applyServerDelta(db)
	case mSubmitResponse:
		r, err := decodeSubmitResponse(raw)
		if err != nil {
			return err
		}
		return oc.applyAck(r)
	case mResyncRequired:
		// Reconnect/resync is not yet wired; a dropped live stream is fatal here.
		return fmt.Errorf("transport: live stream dropped (resync required); reconnect not yet supported")
	case mResyncResponse:
		return fmt.Errorf("transport: unexpected resync response (resync not yet driven by this client)")
	case mError:
		msg, _ := decodeError(raw)
		return fmt.Errorf("transport: server error: %s", msg)
	default:
		return fmt.Errorf("transport: unexpected message kind %d", kind)
	}
}

func (oc *OptimisticClient) applyOpen(snapshotBlob []byte, history [][]byte) error {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if oc.opened {
		return fmt.Errorf("transport: duplicate open response")
	}
	if err := oc.initLocked(snapshotBlob, history); err != nil {
		oc.openErr = err
	}
	oc.opened = true
	oc.cond.Broadcast()
	oc.signalLocked()
	return nil
}

// initLocked seeds the core from the starting view: a current-state snapshot, or a
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
			b, ok := w.Blip(bid)
			if ok {
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
		if _, err := oc.cc.OnServerDelta(sd.Ops, sd.ResultingVersion); err != nil {
			return err
		}
	}
	return nil
}

func (oc *OptimisticClient) applyServerDelta(db []byte) error {
	sd, err := codec.DecodeStoredDelta(db)
	if err != nil {
		return err
	}
	oc.mu.Lock()
	d, err := oc.cc.OnServerDelta(sd.Ops, sd.ResultingVersion)
	oc.signalLocked()
	oc.mu.Unlock()
	if err != nil {
		return err
	}
	oc.sendDelta(d)
	return nil
}

func (oc *OptimisticClient) applyAck(r submitResponse) error {
	if !r.OK {
		// Nack recovery (VersionError / TooOld → resync / InvalidOperation) is not
		// yet implemented; treat as fatal for now.
		return fmt.Errorf("transport: submit nacked (code %d): %s", r.Code, r.Msg)
	}
	rv, err := codec.DecodeHashedVersion(r.ResultingVersion)
	if err != nil {
		return fmt.Errorf("transport: bad resulting version in ack: %w", err)
	}
	oc.mu.Lock()
	d := oc.cc.OnAck(rv, r.OpsApplied)
	oc.signalLocked()
	oc.mu.Unlock()
	oc.sendDelta(d)
	return nil
}

// signalLocked wakes version/open waiters and fires the coalesced replica-changed
// notification. Must hold oc.mu.
func (oc *OptimisticClient) signalLocked() {
	oc.cond.Broadcast()
	select {
	case oc.notify <- struct{}{}:
	default:
	}
}
