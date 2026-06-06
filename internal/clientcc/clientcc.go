// Package clientcc is the client-side optimistic concurrency control for one
// wavelet: the Jupiter-style state machine a collaborative client runs to edit
// locally without waiting for the server, while staying convergent with everyone
// else.
//
// It is PURE: no goroutines, no I/O, no clock. A transport adapter drives it with
// wire events — server deltas, submit acks, nacks — and sends the deltas it
// emits. Keeping the OT bookkeeping side-effect-free makes it deterministically
// testable (drive it against a simulated server and assert convergence) and lets
// the adapter own timing/reconnection separately (tested with testing/synctest).
//
// # Model
//
// The client holds at most one in-flight delta (submitted, awaiting ack) plus a
// queue of locally-authored ops not yet sent. Local edits apply to an optimistic
// replica immediately. Incoming server deltas (from OTHER participants — the
// server suppresses this connection's own deltas, see
// docs/architecture/03-delta-channel-protocol.md) are transformed past the
// unacknowledged ops before being applied, and the unacknowledged ops are
// transformed past them in step, so they always apply on top of the latest
// confirmed server version.
//
// # Suppression and the ack/delta race
//
// Because the server suppresses the client's own delta, the client never receives
// it back; the submit ack is the sole signal of its outcome. The ack can arrive
// before the server deltas that preceded the in-flight delta (they race on the
// connection). The client tolerates this (option 1 in the protocol doc): it
// settles the in-flight delta only once it is both acked AND the client has
// received every delta the server applied before it (recv has reached the
// in-flight delta's applied-at version). A post-in-flight delta arriving first
// reveals the in-flight delta's slot as a version gap and settles it without
// needing the ack.
//
// The hash chain is the server's concern; this client tracks only the confirmed
// server HashedVersion (for targeting submits and, later, resync).
//
// Not yet implemented here (deferred to the transport adapter + resync work):
//   - Nack recovery (VersionError / TooOld / InvalidOperation) and resync.
//   - A transform error from OnServerDelta (e.g. a concurrent participant
//     removing this author — waveop.RemovedAuthorError) currently surfaces as a
//     hard error with no recovery; it folds into the same error-recovery work.
//   - Queue merging (composing consecutive same-author ops) — a future
//     optimization; the queue is a plain op list today.
//
// The core is single-threaded; the caller serializes calls. It also assumes the
// transport delivers a wavelet's server deltas in version order (the gap/settle
// logic relies on it); the adapter must preserve that.
package clientcc

import (
	"fmt"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// CC is the client concurrency-control state machine for one wavelet.
type CC struct {
	name   id.WaveletName
	author id.ParticipantID

	// recv is the latest confirmed server version: the version on top of which the
	// unacknowledged ops (inflight, then queue) currently apply. Advanced by
	// received deltas and by settling the in-flight delta.
	recv version.HashedVersion

	inflight *pending           // the one delta awaiting ack, or nil
	queue    []waveop.Operation // local ops not yet sent, kept transformed onto recv (after inflight)

	// optimistic replica: blip contents (kept as composed DocOps) and the
	// participant set. Enough to read back the document; metadata (contributors,
	// per-blip versions) is not tracked here.
	blips map[string]op.DocOp
	parts map[id.ParticipantID]struct{}
}

// pending is the single in-flight delta. ops is kept transformed to apply on top
// of recv. versionSpan is the op count of the delta as sent (= len at send); the
// OT transform is one-to-one on ops, so this is also the count the server applies
// (except a deduped resend, which applies zero — a resync-era case). It locates
// the delta's slot in the version stream when a post-in-flight delta reveals it as
// a gap before the ack arrives. Once acked, ackedApplied carries the server's
// authoritative applied op count, which drives settling.
type pending struct {
	ops          []waveop.Operation
	sentTarget   version.HashedVersion
	versionSpan  uint64
	acked        bool
	ackedVer     version.HashedVersion
	ackedApplied uint64
}

// New creates a client state machine for wavelet name authored by author,
// starting from the given confirmed version (e.g. version.Zero for a fresh open;
// the snapshot/history version for a resync). The caller then feeds any initial
// history via OnServerDelta.
func New(name id.WaveletName, author id.ParticipantID, start version.HashedVersion) *CC {
	return &CC{
		name:   name,
		author: author,
		recv:   start,
		blips:  map[string]op.DocOp{},
		parts:  map[id.ParticipantID]struct{}{},
	}
}

// ServerVersion returns the latest confirmed server version (what a fresh idle
// submit targets, and the resync point).
func (c *CC) ServerVersion() version.HashedVersion { return c.recv }

// Blip returns the optimistic content of a blip and whether it exists.
func (c *CC) Blip(blipID string) (op.DocOp, bool) {
	d, ok := c.blips[blipID]
	return d, ok
}

// HasParticipant reports whether p is in the optimistic participant set.
func (c *CC) HasParticipant(p id.ParticipantID) bool {
	_, ok := c.parts[p]
	return ok
}

// Edit applies locally-authored ops to the optimistic replica and queues them for
// submission, returning a delta to send now or nil if one is already in flight
// (the ops wait in the queue). ops must be authored against the current optimistic
// document and carry a per-op VersionIncrement (normally 1).
func (c *CC) Edit(ops []waveop.Operation) (*waveop.WaveletDelta, error) {
	if err := c.apply(ops); err != nil {
		return nil, fmt.Errorf("clientcc: applying local edit: %w", err)
	}
	c.queue = append(c.queue, ops...)
	return c.trySend(), nil
}

// OnServerDelta incorporates a delta applied by the server (authored by another
// participant). ops are its operations and resulting is the version the server
// reached after it. The delta is transformed past the unacknowledged ops and
// applied to the replica; the unacknowledged ops are transformed past it. It may
// settle the in-flight delta and return a newly-sendable delta (else nil).
func (c *CC) OnServerDelta(ops []waveop.Operation, resulting version.HashedVersion) (*waveop.WaveletDelta, error) {
	span := versionSpan(ops)
	appliedAt := resulting.Version() - span

	// A gap (the delta applies beyond recv) is our own suppressed in-flight delta:
	// everything up to here was concurrent with it; from here on follows it. Settle
	// the in-flight delta into the confirmed sequence — its resulting version is
	// this delta's applied-at version — without needing the ack. We can't set recv
	// to that version (we lack its hash until the ack), but this delta follows it
	// and carries a real signature, so recv advances to this delta's resulting
	// version below.
	gapSettled := false
	if c.inflight != nil && appliedAt > c.recv.Version() {
		if appliedAt-c.recv.Version() != c.inflight.versionSpan {
			return nil, fmt.Errorf("clientcc: stream gap %d..%d does not match in-flight span %d",
				c.recv.Version(), appliedAt, c.inflight.versionSpan)
		}
		c.inflight = nil
		gapSettled = true
	}
	if !gapSettled && appliedAt != c.recv.Version() {
		return nil, fmt.Errorf("clientcc: out-of-order delta: applies at %d, client at %d",
			appliedAt, c.recv.Version())
	}

	d := ops
	if c.inflight != nil {
		// Concurrent with the in-flight delta: transform past it.
		inflightPrime, dPrime, err := cc.TransformOps(c.inflight.ops, d)
		if err != nil {
			return nil, fmt.Errorf("clientcc: transform server delta past in-flight: %w", err)
		}
		c.inflight.ops = inflightPrime
		d = dPrime
	}
	queuePrime, dPrime, err := cc.TransformOps(c.queue, d)
	if err != nil {
		return nil, fmt.Errorf("clientcc: transform server delta past queue: %w", err)
	}
	c.queue = queuePrime
	d = dPrime

	if err := c.apply(d); err != nil {
		return nil, fmt.Errorf("clientcc: applying server delta: %w", err)
	}
	c.recv = resulting
	return c.settleAndSend(), nil
}

// OnAck records that the in-flight delta was accepted, resulting in the given
// version with opsApplied operations applied by the server (the authoritative
// count, which the client cannot reliably infer: a deduped resend applies zero,
// and a transformed-to-NoOp delta still applies its op count). It may settle the
// in-flight delta (once all preceding server deltas have arrived) and return a
// newly-sendable delta (else nil). A late ack for a delta already settled via a
// version gap is ignored.
func (c *CC) OnAck(resulting version.HashedVersion, opsApplied uint64) *waveop.WaveletDelta {
	if c.inflight == nil {
		return nil // already settled (gap-confirmed); the ack is redundant
	}
	c.inflight.acked = true
	c.inflight.ackedVer = resulting
	c.inflight.ackedApplied = opsApplied
	return c.settleAndSend()
}

// settleAndSend settles an acked in-flight delta once the client has received
// every delta the server applied before it, then sends the next queued delta if
// the path is now clear. The delta's applied-at version is derived from the
// server's authoritative applied count (so opsApplied==0 — a deduped or fully
// transformed-away submit — settles in place at the resulting version, advancing
// nothing, rather than underflowing).
func (c *CC) settleAndSend() *waveop.WaveletDelta {
	if c.inflight != nil && c.inflight.acked {
		appliedAt := c.inflight.ackedVer.Version() - c.inflight.ackedApplied
		if c.recv.Version() == appliedAt {
			// All preceding deltas are in; the in-flight delta occupies the next slot.
			c.recv = c.inflight.ackedVer
			c.inflight = nil
		}
		// recv < appliedAt: still waiting for preceding deltas — hold.
	}
	return c.trySend()
}

// trySend promotes the queue to the in-flight slot when it is free, returning the
// delta to submit (targeting the confirmed version) or nil.
func (c *CC) trySend() *waveop.WaveletDelta {
	if c.inflight != nil || len(c.queue) == 0 {
		return nil
	}
	ops := c.queue
	c.queue = nil
	c.inflight = &pending{ops: ops, sentTarget: c.recv, versionSpan: versionSpan(ops)}
	d := waveop.NewWaveletDelta(c.author, c.recv, ops)
	return &d
}

// apply mutates the optimistic replica by the given ops (blip content composes;
// participant ops mutate the set; NoOp does nothing).
func (c *CC) apply(ops []waveop.Operation) error {
	for _, o := range ops {
		switch v := o.(type) {
		case waveop.WaveletBlipOperation:
			bco, ok := v.BlipOp.(waveop.BlipContentOperation)
			if !ok {
				return fmt.Errorf("blip %q: unsupported blip op %T", v.BlipID, v.BlipOp)
			}
			cur, ok := c.blips[v.BlipID]
			if !ok {
				cur = op.EmptyDoc()
			}
			next, err := op.Compose(cur, bco.ContentOp)
			if err != nil {
				return fmt.Errorf("blip %q: %w", v.BlipID, err)
			}
			if !next.IsInitialization() {
				return fmt.Errorf("blip %q: composed content is not an initialization", v.BlipID)
			}
			c.blips[v.BlipID] = next
		case waveop.AddParticipant:
			c.parts[v.Participant] = struct{}{}
		case waveop.RemoveParticipant:
			delete(c.parts, v.Participant)
		case waveop.NoOp:
			// no document or participant change
		default:
			// Fail loud rather than diverge silently if a new op kind is added
			// (the server errors on unknown ops too).
			return fmt.Errorf("unsupported wavelet op %T", o)
		}
	}
	return nil
}

// versionSpan is the number of versions a list of ops advances the wavelet by.
// The wavelet model advances by exactly one version per operation — the OP COUNT
// — and deliberately ignores Context.VersionIncrement (wire metadata) for version
// arithmetic, matching wavelet.ApplyDelta, cc, the history, and storage (see
// internal/wavelet/apply.go). The OT transform is one-to-one on operations, so a
// delta's op count is preserved through transform-to-head; the count the client
// sent equals the count the server applies.
func versionSpan(ops []waveop.Operation) uint64 {
	return uint64(len(ops))
}
