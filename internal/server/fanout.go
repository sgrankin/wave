package server

import (
	"github.com/sgrankin/wave/internal/cc"
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

// Open is the join flow: it atomically returns the wavelet's applied-delta
// history (so a joining client can build its view) and a live subscription for
// subsequent deltas, with no gap or overlap between them. A delta either appears
// in history (it was applied before Open) or arrives on the subscription (after),
// never both — because both Open and Submit hold the lock.
func (c *WaveletContainer) Open() (history []WaveletUpdate, sub *Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	history = make([]WaveletUpdate, len(c.applied))
	for i, d := range c.applied {
		history[i] = WaveletUpdate{Delta: d, ResultingVersion: d.ResultingVersion}
	}
	return history, c.subscribeLocked()
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

// publish fans an update out to all subscribers. It must be called with c.mu
// held (delivery order == submit order). A full subscriber queue means the
// subscriber fell behind: it is dropped (channel closed) so it resyncs, rather
// than receiving a gapped stream or blocking the submit path.
func (c *WaveletContainer) publish(u WaveletUpdate) {
	for s := range c.subs {
		select {
		case s.ch <- u:
		default:
			delete(c.subs, s)
			close(s.ch)
		}
	}
}
