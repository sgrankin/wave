package server

import (
	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/snapshot"
	"github.com/sgrankin/wave/internal/version"
)

// DefaultSubBuffer is the per-subscriber update queue depth. A subscriber that
// falls further behind than this is dropped (its channel closed) and must
// resync via a fresh Open — delivering a partial/gapped stream would break
// version continuity. Resync is a later increment; for now the buffer is
// generous.
const DefaultSubBuffer = 256

// WaveletUpdate is a fan-out event: a delta applied to the wavelet, with the
// version reached after it.
type WaveletUpdate struct {
	Delta            cc.TransformedWaveletDelta
	ResultingVersion version.HashedVersion
}

// Subscription is a live stream of a wavelet's applied deltas. The channel is
// closed when the subscription ends — either via Close, or because the
// subscriber fell too far behind (a gap; resync with a fresh Open).
type Subscription struct {
	c  *WaveletContainer
	ch chan WaveletUpdate
}

// Updates returns the channel of applied-delta events. A closed channel means
// the subscription ended (explicit close or a dropped/gapped subscriber).
func (s *Subscription) Updates() <-chan WaveletUpdate { return s.ch }

// Close ends the subscription.
func (s *Subscription) Close() { s.c.removeSub(s) }

// Subscribe registers a subscriber for the wavelet's applied deltas. The caller
// reads Updates() and must keep up (see DefaultSubBuffer) or be dropped.
func (c *WaveletContainer) Subscribe() *Subscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscribeLocked()
}

func (c *WaveletContainer) subscribeLocked() *Subscription {
	s := &Subscription{c: c, ch: make(chan WaveletUpdate, DefaultSubBuffer)}
	c.subs[s] = struct{}{}
	return s
}

// Open is the join flow: it atomically returns a starting view of the wavelet
// plus a live subscription for subsequent deltas, with no gap or overlap (both
// Open and Submit hold the lock, so a delta is either in the starting view or on
// the subscription, never both).
//
// The starting view is either a current-state snapshot (when snapshots are
// enabled — the client reconstructs state and follows live deltas from the
// snapshot version) or the full applied-delta history from version zero (the
// client replays it). snapshotBlob is non-nil in the former case and history is
// then empty; in the latter snapshotBlob is nil.
func (c *WaveletContainer) Open() (snapshotBlob []byte, history []WaveletUpdate, sub *Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshots != nil && c.wavelet != nil {
		return snapshot.Encode(c.wavelet.State()), nil, c.subscribeLocked()
	}
	history = make([]WaveletUpdate, len(c.applied))
	for i, d := range c.applied {
		history[i] = WaveletUpdate{Delta: d, ResultingVersion: d.ResultingVersion}
	}
	return nil, history, c.subscribeLocked()
}

// OpenAt is the incremental (resync) join for a reconnecting client that already
// holds state through (knownVersion, knownHash). On success (ok == true) it
// returns the applied deltas after knownVersion plus a live subscription that
// continues from there, with no gap or overlap (both selected under the lock,
// like Open). ok == false means the known point is not a current signature — a
// fork (hash mismatch) or pruned below the snapshot floor — and the caller must
// fall back to a full Open (a reset): the client's local state is unrecoverable
// incrementally and must be rebuilt.
func (c *WaveletContainer) OpenAt(knownVersion uint64, knownHash []byte) (tail []WaveletUpdate, sub *Subscription, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wavelet == nil {
		return nil, nil, false // nothing applied yet: nothing to resync onto
	}
	if !c.history.HasSignature(version.NewHashedVersion(knownVersion, knownHash)) {
		return nil, nil, false // fork or pruned: reset
	}
	for _, d := range c.applied {
		if d.ResultingVersion.Version() > knownVersion {
			tail = append(tail, WaveletUpdate{Delta: d, ResultingVersion: d.ResultingVersion})
		}
	}
	return tail, c.subscribeLocked(), true
}

// removeSub closes and deregisters a subscription if still present (idempotent).
func (c *WaveletContainer) removeSub(s *Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.subs[s]; ok {
		delete(c.subs, s)
		close(s.ch)
	}
}

// publish fans an update out to all subscribers except exclude (the submitter's
// own subscription under self-suppression; nil to fan out to everyone). It must
// be called with c.mu held (delivery order == submit order). A full subscriber
// queue means the subscriber fell behind: it is dropped (channel closed) so it
// resyncs, rather than receiving a gapped stream or blocking the submit path.
func (c *WaveletContainer) publish(u WaveletUpdate, exclude *Subscription) {
	for s := range c.subs {
		if s == exclude {
			continue
		}
		select {
		case s.ch <- u:
		default:
			delete(c.subs, s)
			close(s.ch)
		}
	}
}
