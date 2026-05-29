// Package server is the wavelet-serving core: it integrates the OT engine
// (op/waveop), the wavelet data model, concurrency control, the canonical
// codec, and persistence into the real-time edit loop. A WaveletContainer owns
// one wavelet's live state and serializes its submit pipeline (one writer per
// wavelet).
//
// This file implements the container: load-by-replay and the local-delta submit
// lifecycle (validate version/hash → transform to head → apply → hash → persist).
// Fan-out/subscription, the wave map, and the transport layer build on top.
//
// Spec: docs/specs/06-server-architecture.md §WaveletContainer, §Delta submit lifecycle.
package server

import (
	"fmt"
	"sync"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// WaveletContainer owns one wavelet's live state and serializes access to it.
// It is created empty (the wavelet itself springs into existence at version 0
// when the first delta is submitted — wave-creation-by-first-delta) or loaded
// by replaying a persisted delta log.
type WaveletContainer struct {
	name   id.WaveletName
	deltas storage.DeltasAccess
	clk    clock.Clock
	zero   version.HashedVersion

	mu        sync.Mutex
	wavelet   *wavelet.Data // nil until the first delta creates the wavelet
	history   *cc.MemoryHistory
	corrupted bool                         // set if in-memory state diverged from storage; requires reload
	subs      map[*Subscription]struct{}   // update subscribers (fan-out)
	applied   []cc.TransformedWaveletDelta // applied deltas in order (Open snapshot)
}

// SubmitResult reports the outcome of a successful submit. A transformed-away
// (no-op) submit reports OpsApplied == 0 and the unchanged current version.
type SubmitResult struct {
	OpsApplied       int
	ResultingVersion version.HashedVersion
	Timestamp        int64
}

// Load reconstructs a container by replaying the wavelet's persisted delta log.
// Each delta's canonical bytes are recomputed and applied, and the resulting
// hashed version is verified against the stored one (the stored hash is
// authoritative; a mismatch means encoding drift or corruption).
func Load(name id.WaveletName, deltas storage.DeltasAccess, clk clock.Clock) (*WaveletContainer, error) {
	zero := version.Zero(name)
	c := &WaveletContainer{
		name:    name,
		deltas:  deltas,
		clk:     clk,
		zero:    zero,
		history: cc.NewMemoryHistory(zero),
		subs:    map[*Subscription]struct{}{},
	}
	records, err := deltas.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("server: load %s: %w", name, err)
	}
	for i, rec := range records {
		if i == 0 {
			// The wavelet is created at version 0 with the first delta's author as
			// creator (a participant); the delta log carries no re-add of the creator.
			c.wavelet = wavelet.New(name.Wave(), name.Wavelet(), rec.Author, rec.Timestamp, zero)
		}
		hashBytes := codec.HashBytes(rec.Author, rec.AppliedAtVersion, rec.Timestamp, rec.Ops)
		d := waveop.NewWaveletDelta(rec.Author, c.wavelet.HashedVersion(), rec.Ops)
		if err := c.wavelet.ApplyDelta(d, hashBytes); err != nil {
			return nil, fmt.Errorf("server: replay delta at %d: %w", rec.AppliedAtVersion, err)
		}
		if c.wavelet.HashedVersion().Compare(rec.ResultingVersion) != 0 {
			return nil, fmt.Errorf("server: replay hash mismatch at version %d (stored vs recomputed differ)",
				rec.ResultingVersion.Version())
		}
		applied := cc.TransformedWaveletDelta{
			Author: rec.Author, ResultingVersion: rec.ResultingVersion, Timestamp: rec.Timestamp, Ops: rec.Ops,
		}
		c.history.Append(applied)
		c.applied = append(c.applied, applied)
	}
	return c, nil
}

// Version returns the wavelet's current version (zero version if not yet created).
func (c *WaveletContainer) Version() version.HashedVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wavelet == nil {
		return c.zero
	}
	return c.wavelet.HashedVersion()
}

// Wavelet returns the live wavelet state for read-only inspection, or nil if no
// delta has been applied yet.
//
// CAUTION: this returns the live object, not a snapshot. Reading it concurrently
// with a Submit races on the wavelet's internal state. It is safe only when no
// Submit can run concurrently (the current single-goroutine usage). A snapshot
// accessor will replace this once fan-out/transport introduce concurrent reads.
func (c *WaveletContainer) Wavelet() *wavelet.Data {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wavelet
}

// Submit validates, transforms-to-head, applies, hashes, and persists a client
// delta, returning the result. It is safe for concurrent callers (serialized).
//
// Errors carry a cc.ResponseCode (VersionError / InvalidOperation / ...). The
// caller (frontend) maps them to the wire response.
func (c *WaveletContainer) Submit(delta waveop.WaveletDelta) (SubmitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.corrupted {
		return SubmitResult{}, &cc.Error{Code: cc.InternalError, Msg: "wavelet corrupted; reload required"}
	}

	// Validate (target version/hash) and transform to head against the history.
	// This works whether or not the wavelet exists yet — the history is seeded
	// with the version-0 signature, so a valid first delta must target version 0.
	transformed, err := cc.TransformToHead(c.history, delta)
	if err != nil {
		return SubmitResult{}, err
	}
	if len(transformed.Ops()) == 0 {
		// Fully transformed away (or empty): a no-op. Don't materialize a phantom
		// wavelet or advance the version.
		cur := c.zero
		if c.wavelet != nil {
			cur = c.wavelet.HashedVersion()
		}
		return SubmitResult{OpsApplied: 0, ResultingVersion: cur}, nil
	}

	// Materialize the wavelet on the first delta that actually applies ops
	// (wave-creation-by-first-delta). The delta carries the AddParticipant(creator).
	createdNow := c.wavelet == nil
	if createdNow {
		c.wavelet = wavelet.New(c.name.Wave(), c.name.Wavelet(), delta.Author(),
			c.clk.Now().UnixMilli(), c.zero)
	}

	ts := c.clk.Now().UnixMilli()
	head := c.wavelet.HashedVersion()
	hashBytes := codec.HashBytes(transformed.Author(), head.Version(), ts, transformed.Ops())
	if err := c.wavelet.ApplyDelta(transformed, hashBytes); err != nil {
		if createdNow {
			c.wavelet = nil // undo the phantom: nothing was persisted
		}
		return SubmitResult{}, &cc.Error{Code: cc.InvalidOperation, Msg: "applying transformed delta", Err: err}
	}
	resulting := c.wavelet.HashedVersion()

	rec := storage.DeltaRecord{
		Author:           transformed.Author(),
		AppliedAtVersion: head.Version(),
		ResultingVersion: resulting,
		Timestamp:        ts,
		Ops:              transformed.Ops(),
	}
	if err := c.deltas.Append([]storage.DeltaRecord{rec}); err != nil {
		// The in-memory apply succeeded but persistence failed: in-memory state is
		// now ahead of storage and the history. Mark corrupted so we fail fast
		// rather than serve a wavelet whose version diverges from its log; recovery
		// is a reload from storage.
		c.corrupted = true
		return SubmitResult{}, &cc.Error{Code: cc.InternalError, Msg: "persisting delta (wavelet corrupted; reload required)", Err: err}
	}
	applied := cc.TransformedWaveletDelta{
		Author: rec.Author, ResultingVersion: resulting, Timestamp: ts, Ops: rec.Ops,
	}
	c.history.Append(applied)
	c.applied = append(c.applied, applied)
	// Fan out to subscribers in version order (still under the lock, so concurrent
	// submits deliver their deltas in order).
	c.publish(WaveletUpdate{Delta: applied, ResultingVersion: resulting})
	return SubmitResult{OpsApplied: len(rec.Ops), ResultingVersion: resulting, Timestamp: ts}, nil
}
