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
	"errors"
	"fmt"
	"sync"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/snapshot"
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
	name      id.WaveletName
	deltas    storage.DeltasAccess
	clk       clock.Clock
	zero      version.HashedVersion
	snapshots storage.SnapshotStore // nil unless snapshots are enabled
	snapEvery int                   // ops between snapshots (0 = disabled)
	indexer   Indexer               // nil unless index maintenance is enabled

	mu        sync.Mutex
	wavelet   *wavelet.Data // nil until the first delta creates the wavelet
	history   *cc.MemoryHistory
	lastSnap  uint64                       // version of the most recent snapshot (loaded or written)
	corrupted bool                         // set if in-memory state diverged from storage; requires reload
	subs      map[*Subscription]struct{}   // update subscribers (fan-out)
	applied   []cc.TransformedWaveletDelta // applied deltas in order (history-based join)
}

// SubmitResult reports the outcome of a successful submit. A transformed-away
// (no-op) submit reports OpsApplied == 0 and the unchanged current version.
type SubmitResult struct {
	OpsApplied       int
	ResultingVersion version.HashedVersion
	Timestamp        int64
}

// DeltaHeader is a lightweight summary of one applied delta for history/playback
// timelines: who, when, the resulting wavelet version, and how many ops — without
// the op payload.
type DeltaHeader struct {
	Author    id.ParticipantID
	Version   uint64 // resulting version (the wavelet version after this delta)
	Timestamp int64
	OpCount   int
}

// Load reconstructs a container by replaying the wavelet's persisted delta log
// from version zero (no snapshot cache). Each delta's canonical bytes are
// recomputed and applied, and the resulting hashed version is verified against
// the stored one (the stored hash is authoritative; a mismatch means encoding
// drift or corruption).
func Load(name id.WaveletName, deltas storage.DeltasAccess, clk clock.Clock) (*WaveletContainer, error) {
	return loadContainer(name, deltas, nil, 0, nil, clk)
}

// loadContainer reconstructs a container. With a snapshot store it tries
// snapshot + tail replay first, falling back to full replay on a miss or any
// inconsistency (the snapshot is a cache; the delta log is authoritative).
func loadContainer(name id.WaveletName, deltas storage.DeltasAccess, snapshots storage.SnapshotStore, snapEvery int, indexer Indexer, clk clock.Clock) (*WaveletContainer, error) {
	zero := version.Zero(name)
	c := &WaveletContainer{
		name:      name,
		deltas:    deltas,
		clk:       clk,
		zero:      zero,
		snapshots: snapshots,
		snapEvery: snapEvery,
		indexer:   indexer,
		history:   cc.NewMemoryHistory(zero),
		subs:      map[*Subscription]struct{}{},
	}
	if snapshots != nil {
		loaded, err := c.loadFromSnapshot()
		if err != nil {
			return nil, err
		}
		if loaded {
			return c, nil
		}
		// Snapshot missing or unusable: reset and fall through to full replay.
		c.wavelet = nil
		c.history = cc.NewMemoryHistory(zero)
		c.applied = nil
		c.lastSnap = 0
	}
	if err := c.replayFrom(zero, 0); err != nil {
		return nil, err
	}
	return c, nil
}

// replayFrom applies records with appliedAt >= from on top of the container's
// current wavelet (nil ⇒ created from the first record), verifying each
// resulting version against storage. `from` is the version the in-memory state
// already reflects.
func (c *WaveletContainer) replayFrom(start version.HashedVersion, from uint64) error {
	records, err := c.deltas.ReadFrom(from)
	if err != nil {
		return fmt.Errorf("server: load %s: %w", c.name, err)
	}
	for _, rec := range records {
		if c.wavelet == nil {
			// The wavelet is created at version 0 with the first delta's author as
			// creator (a participant); the delta log carries no re-add of the creator.
			c.wavelet = wavelet.New(c.name.Wave(), c.name.Wavelet(), rec.Author, rec.Timestamp, start)
		}
		hashBytes := codec.HashBytes(rec.Author, rec.AppliedAtVersion, rec.Timestamp, rec.Ops)
		d := waveop.NewWaveletDelta(rec.Author, c.wavelet.HashedVersion(), rec.Ops)
		if err := c.wavelet.ApplyDelta(d, hashBytes); err != nil {
			return fmt.Errorf("server: replay delta at %d: %w", rec.AppliedAtVersion, err)
		}
		if c.wavelet.HashedVersion().Compare(rec.ResultingVersion) != 0 {
			return fmt.Errorf("server: replay hash mismatch at version %d (stored vs recomputed differ)",
				rec.ResultingVersion.Version())
		}
		applied := cc.TransformedWaveletDelta{
			Author: rec.Author, ResultingVersion: rec.ResultingVersion, Timestamp: rec.Timestamp, Ops: rec.Ops, Nonce: rec.Nonce,
		}
		c.history.Append(applied)
		c.applied = append(c.applied, applied)
	}
	return nil
}

// loadFromSnapshot tries to reconstruct from the latest snapshot + tail replay.
// It returns (true, nil) on success; (false, nil) when there is no usable
// snapshot (caller should full-replay); and an error only for an authoritative
// storage failure. A snapshot that decodes, replays, but does not reproduce the
// delta log's end version is treated as unusable (false) — never trusted over
// the log.
func (c *WaveletContainer) loadFromSnapshot() (bool, error) {
	snapVer, blob, ok, err := c.snapshots.GetLatestSnapshot(c.name)
	if err != nil || !ok {
		return false, nil // no snapshot (or unreadable): full-replay
	}
	state, err := snapshot.Decode(blob)
	if err != nil {
		return false, nil // corrupt snapshot: full-replay
	}
	w, err := wavelet.FromState(state)
	if err != nil {
		return false, nil
	}
	// Seed history at the snapshot version; pre-snapshot versions are pruned
	// (a submit targeting them is TooOld, matching the Java pruned-history model).
	c.wavelet = w
	c.history = cc.NewMemoryHistory(w.HashedVersion())
	if err := c.replayFrom(w.HashedVersion(), snapVer); err != nil {
		return false, nil // tail inconsistent with snapshot: full-replay
	}
	// Authoritative check: the reconstructed state must reach the log's end.
	end, ok, err := c.deltas.EndVersion()
	if err != nil {
		return false, err
	}
	if !ok || c.wavelet.HashedVersion().Compare(end) != 0 {
		return false, nil // snapshot+tail disagrees with the log: full-replay
	}
	c.lastSnap = snapVer
	return true, nil
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

// HasParticipant reports whether p is a participant of the wavelet, and whether
// the wavelet has been created yet (created == false means no delta has been
// applied — a never-seeded wavelet). The read happens under the container lock,
// so unlike reading Wavelet() directly it is safe to call concurrently with
// Submit. The access layer uses it as the wavelet-membership predicate.
func (c *WaveletContainer) HasParticipant(p id.ParticipantID) (exists, created bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wavelet == nil {
		return false, false
	}
	return c.wavelet.HasParticipant(p), true
}

// Read runs fn under the container lock with the live wavelet (nil if no delta
// has been applied yet), for read-only inspection that is safe concurrent with
// Submit. fn must not retain the *wavelet.Data beyond the call or mutate it. It
// is the lock-safe alternative to Wavelet() for readers (e.g. inbox/search digest
// computation) that run while submits may be in flight.
func (c *WaveletContainer) Read(fn func(*wavelet.Data)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c.wavelet)
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

// DeltaHeaders returns a summary of every applied delta in version order, for a
// playback/history timeline. It reads only the persisted log (not live state), so
// it is safe concurrent with submits.
func (c *WaveletContainer) DeltaHeaders() ([]DeltaHeader, error) {
	records, err := c.deltas.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("server: read history %s: %w", c.name, err)
	}
	headers := make([]DeltaHeader, len(records))
	for i, rec := range records {
		headers[i] = DeltaHeader{
			Author:    rec.Author,
			Version:   rec.ResultingVersion.Version(),
			Timestamp: rec.Timestamp,
			OpCount:   len(rec.Ops),
		}
	}
	return headers, nil
}

// ErrNoVersion is returned by StateAt when the requested version is not a valid
// delta-boundary (mid-delta, or past the log's end) — a client error, distinct
// from a storage/replay failure (which surfaces as a different, wrapped error).
var ErrNoVersion = errors.New("server: no such version")

// StateAt reconstructs the wavelet's state at a past version by replaying the
// persisted delta log from zero onto a fresh wavelet — independent of the live
// container (it reads only storage, so it is safe concurrent with submits, and the
// returned *wavelet.Data is a private throwaway the caller may read without the
// container lock). targetVersion must be a delta-boundary version (some delta's
// resulting version); 0 returns (nil, nil) for the empty / never-created state.
// Returns an error if targetVersion is not such a boundary (e.g. mid-delta, or
// past the log's end). Each step's recomputed hash is verified against the log, so
// corruption surfaces as an error rather than a wrong reconstruction.
func (c *WaveletContainer) StateAt(targetVersion uint64) (*wavelet.Data, error) {
	if targetVersion == 0 {
		return nil, nil
	}
	records, err := c.deltas.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("server: read history %s: %w", c.name, err)
	}
	var w *wavelet.Data
	for _, rec := range records {
		if rec.ResultingVersion.Version() > targetVersion {
			break // a later boundary; targetVersion (if valid) was already reached
		}
		if w == nil {
			w = wavelet.New(c.name.Wave(), c.name.Wavelet(), rec.Author, rec.Timestamp, c.zero)
		}
		hashBytes := codec.HashBytes(rec.Author, rec.AppliedAtVersion, rec.Timestamp, rec.Ops)
		d := waveop.NewWaveletDelta(rec.Author, w.HashedVersion(), rec.Ops)
		if err := w.ApplyDelta(d, hashBytes); err != nil {
			return nil, fmt.Errorf("server: replay %s to version %d: %w", c.name, targetVersion, err)
		}
		if w.HashedVersion().Compare(rec.ResultingVersion) != 0 {
			return nil, fmt.Errorf("server: replay hash mismatch at version %d for %s",
				rec.ResultingVersion.Version(), c.name)
		}
	}
	if w == nil || w.HashedVersion().Version() != targetVersion {
		return nil, fmt.Errorf("%w %d for %s", ErrNoVersion, targetVersion, c.name)
	}
	return w, nil
}

// Submit validates, transforms-to-head, applies, hashes, and persists a client
// delta, returning the result. It is safe for concurrent callers (serialized).
//
// Errors carry a cc.ResponseCode (VersionError / InvalidOperation / ...). The
// caller (frontend) maps them to the wire response.
func (c *WaveletContainer) Submit(delta waveop.WaveletDelta) (SubmitResult, error) {
	return c.submitExcluding(delta, "", nil)
}

// SubmitFrom is Submit for a connection-originated delta. nonce is the submitter's
// opaque per-submission tag, retained with the applied delta so the submitter can
// recognize it in a later resync tail. exclude is the submitter's own subscription,
// suppressed from the resulting fan-out (self-suppression) so an optimistic
// submitter sees only other participants' deltas and learns its own outcome from
// the returned result / ack. exclude == nil and nonce == "" behave like Submit.
func (c *WaveletContainer) SubmitFrom(delta waveop.WaveletDelta, nonce string, exclude *Subscription) (SubmitResult, error) {
	return c.submitExcluding(delta, nonce, exclude)
}

func (c *WaveletContainer) submitExcluding(delta waveop.WaveletDelta, nonce string, exclude *Subscription) (SubmitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submitLocked(delta, nonce, exclude)
}

// SeedIfEmpty atomically seeds a never-yet-created wavelet with ops authored by
// author against version zero, returning whether it seeded. It is a no-op
// (false, nil) if the wavelet already exists. The existence check and the seed
// apply happen under one lock acquisition, so two connections racing the first
// Open of the same wavelet cannot both seed — exactly one wins and the other
// observes the wavelet already exists. This is the server-side replacement for
// the client cold-start bootstrap; ops should include AddParticipant(author) so
// the seeder becomes the first participant (see conv.SeedConversation).
func (c *WaveletContainer) SeedIfEmpty(author id.ParticipantID, ops []waveop.Operation) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wavelet != nil {
		return false, nil // already created: nothing to seed
	}
	delta := waveop.NewWaveletDelta(author, c.zero, ops)
	if _, err := c.submitLocked(delta, "", nil); err != nil {
		return false, err
	}
	return true, nil
}

// submitLocked is the body of the submit pipeline; the caller must hold c.mu. It
// is shared by submitExcluding (lock + submit) and SeedIfEmpty (lock + check +
// seed) so the seed's existence check and apply are atomic.
func (c *WaveletContainer) submitLocked(delta waveop.WaveletDelta, nonce string, exclude *Subscription) (SubmitResult, error) {
	if c.corrupted {
		return SubmitResult{}, &cc.Error{Code: cc.InternalError, Msg: "wavelet corrupted; reload required"}
	}

	// Double-submit dedup (spec 03 §"Double-submit / ghost delta"): a delta
	// resent after a reconnect targets the version its original was applied at.
	// If the delta already applied there matches by author + ops (ignoring
	// re-stamped context), this is a duplicate — return the original result
	// idempotently rather than applying it again.
	if len(delta.Ops()) > 0 {
		if prior, ok := c.history.DeltaStartingAt(delta.TargetVersion().Version()); ok &&
			prior.Author == delta.Author() && waveop.EqualOps(prior.Ops, delta.Ops()) {
			return SubmitResult{OpsApplied: 0, ResultingVersion: prior.ResultingVersion, Timestamp: prior.Timestamp}, nil
		}
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

	// Reject a content-mismatched (or malformed) operation BEFORE applying it: apply
	// is Compose, which cancels deletions by length only, so an op whose deletes /
	// replaces disagree with the document would otherwise be applied silently and
	// corrupt the blip (and a bad updateAttributes would panic in compose). Validate
	// the transformed ops against current state; replay never runs this.
	if err := c.wavelet.ValidateDelta(transformed); err != nil {
		if createdNow {
			c.wavelet = nil // undo the phantom: nothing was applied or persisted
		}
		return SubmitResult{}, &cc.Error{Code: cc.InvalidOperation, Msg: "invalid operation", Err: err}
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
		Nonce:            nonce,
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
		Author: rec.Author, ResultingVersion: resulting, Timestamp: ts, Ops: rec.Ops, Nonce: nonce,
	}
	c.history.Append(applied)
	c.applied = append(c.applied, applied)
	// Fan out to subscribers in version order (still under the lock, so concurrent
	// submits deliver their deltas in order). exclude (the submitter's own
	// subscription, when self-suppression is on) is skipped.
	c.publish(WaveletUpdate{Delta: applied, ResultingVersion: resulting}, exclude)
	c.maybeSnapshot()
	if c.indexer != nil {
		c.indexer.OnCommit(c.name, c.wavelet, applied)
	}
	return SubmitResult{OpsApplied: len(rec.Ops), ResultingVersion: resulting, Timestamp: ts}, nil
}

// maybeSnapshot writes a snapshot when enabled and the tail since the last one
// has grown past the threshold. Best-effort: a snapshot is a derivable cache, so
// a write failure is swallowed (a later submit or a full replay covers it). Must
// be called with c.mu held.
func (c *WaveletContainer) maybeSnapshot() {
	if c.snapshots == nil || c.snapEvery <= 0 || c.wavelet == nil {
		return
	}
	v := c.wavelet.HashedVersion().Version()
	if v < c.lastSnap+uint64(c.snapEvery) {
		return
	}
	// NOTE: this persists synchronously while holding c.mu, so every Nth submit
	// blocks the wavelet's submit serialization on a SQLite fsync. Fine at
	// single-machine scale (sub-ms appends); if submit-latency spikes at the
	// snapshot cadence ever matter, snapshot State() under the lock and
	// encode+persist off-lock.
	if err := c.snapshots.PutSnapshot(c.name, v, snapshot.Encode(c.wavelet.State())); err != nil {
		return // best-effort
	}
	c.lastSnap = v
}
