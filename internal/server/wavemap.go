package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/wavelet"
)

// WaveMap holds the loaded wavelet containers for a server, keyed by wavelet
// name, loading them from the delta store on first access. It is the routing
// layer beneath the frontend: Open/Submit for a wavelet name resolve to a
// container here.
//
// Without eviction the cache grows for the life of the process — every wavelet
// ever touched stays resident. WithEviction reaps containers that have been idle
// (no access) for idleTTL and have no live subscribers (see cacheEntry / sweep).
type WaveMap struct {
	store     storage.DeltaStore
	clk       clock.Clock
	snapshots storage.SnapshotStore // nil unless WithSnapshots is set
	snapEvery int                   // ops between snapshots (0 = disabled)
	indexer   Indexer               // nil unless WithIndexer is set
	idleTTL   time.Duration         // evict idle, unsubscribed containers after this; 0 = never (WithEviction)

	mu         sync.Mutex
	containers map[string]*cacheEntry
	lastSweep  time.Time // last idle-eviction sweep (gates sweep frequency to ~idleTTL)
}

// cacheEntry is a cached container with the time it was last handed out, so the
// idle-eviction sweep can tell a hot container from one nobody has touched.
type cacheEntry struct {
	c          *WaveletContainer
	lastAccess time.Time
}

// Indexer is notified after each committed delta so it can maintain derived read
// indexes (inbox, search) off the commit path. It is best-effort: indexes are a
// rebuildable cache, so a container never fails a submit because indexing failed.
// OnCommit is called with the container lock held and the post-delta wavelet
// state, so implementations must not block or call back into the container.
type Indexer interface {
	OnCommit(name id.WaveletName, w *wavelet.Data, delta cc.TransformedWaveletDelta)
}

// Option configures a WaveMap.
type Option func(*WaveMap)

// WithIndexer enables derived-index maintenance: the given indexer is notified
// after each committed delta. Disabled by default.
func WithIndexer(indexer Indexer) Option {
	return func(m *WaveMap) { m.indexer = indexer }
}

// WithSnapshots enables the snapshot cache: containers load via the latest
// snapshot + tail replay (falling back to full replay on any inconsistency),
// write a snapshot every `every` ops, and serve joins from a current-state
// snapshot rather than the full delta history. Disabled by default — the
// snapshot-based join requires the snapshot-aware client (which the transport
// client supports), so enable it only against snapshot-aware clients.
func WithSnapshots(store storage.SnapshotStore, every int) Option {
	return func(m *WaveMap) {
		m.snapshots = store
		m.snapEvery = every
	}
}

// WithEviction bounds the container cache: a container that has been idle (no
// Container() access) for idleTTL and has no live subscribers is dropped from the
// cache, reclaiming its in-memory state (it reloads from the durable delta log on
// next access). A non-zero idleTTL is required; the zero value disables eviction
// (the cache grows unbounded, the historical behavior).
//
// Safety: eviction only ever drops an UNSUBSCRIBED container, and only after a full
// idleTTL of no access. A submitter always holds a subscription (it opened the
// wavelet), so it is never evicted out from under an editing session; and the
// brief window between Container() returning and the caller subscribing is
// millisecond-scale, far shorter than any sane idleTTL — so an idle container has
// no in-flight holder that could submit to a stale instance.
func WithEviction(idleTTL time.Duration) Option {
	return func(m *WaveMap) { m.idleTTL = idleTTL }
}

// NewWaveMap creates a wave map backed by the given delta store and clock.
func NewWaveMap(store storage.DeltaStore, clk clock.Clock, opts ...Option) *WaveMap {
	m := &WaveMap{
		store:      store,
		clk:        clk,
		containers: map[string]*cacheEntry{},
	}
	for _, opt := range opts {
		opt(m)
	}
	m.lastSweep = clk.Now()
	return m
}

// Count returns the number of currently loaded (cached) wavelet containers. It
// is an operability gauge, not the number of wavelets in storage.
func (m *WaveMap) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.containers)
}

// Container returns the container for the named wavelet, loading it (by
// replaying its delta log) on first access and caching it. Repeated calls for
// the same name return the same container.
//
// NOTE: the map lock is held across Load (storage read + replay), so a slow
// first-load of one wavelet serializes opens of all others. Fine at
// single-machine scale; revisit with a per-key load lock (singleflight) if
// first-access latency on large wavelets becomes a problem.
func (m *WaveMap) Container(name id.WaveletName) (*WaveletContainer, error) {
	// name.Serialize() is a collision-free key: '/' delimits the four id fields
	// and appears in none of them (domains are RFC-1035; '/' is excluded from
	// local-id characters), and the domain-elision '~' can never be a real domain.
	key := name.Serialize()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clk.Now()
	if e, ok := m.containers[key]; ok {
		e.lastAccess = now // touch: keep a hot container out of the eviction sweep
		return e.c, nil
	}
	access, err := m.store.Open(name)
	if err != nil {
		return nil, fmt.Errorf("server: open wavelet %s: %w", name, err)
	}
	c, err := loadContainer(name, access, m.snapshots, m.snapEvery, m.indexer, m.clk)
	if err != nil {
		return nil, err
	}
	m.containers[key] = &cacheEntry{c: c, lastAccess: now}
	// Opportunistic reaping: piggyback the idle sweep on loads (the only thing that
	// grows the cache), rate-limited to ~idleTTL so it stays cheap. An idle server
	// never sweeps, but it is also not growing, so that is fine.
	m.maybeSweepLocked(now)
	return c, nil
}

// maybeSweepLocked runs an idle-eviction sweep if enabled and at least idleTTL has
// elapsed since the last one. Caller holds m.mu.
func (m *WaveMap) maybeSweepLocked(now time.Time) {
	if m.idleTTL <= 0 || now.Sub(m.lastSweep) < m.idleTTL {
		return
	}
	m.lastSweep = now
	for key, e := range m.containers {
		if now.Sub(e.lastAccess) <= m.idleTTL {
			continue // accessed recently — hot
		}
		if e.c.subscriberCount() != 0 {
			continue // a live session may still submit — must not evict
		}
		delete(m.containers, key)
	}
}
