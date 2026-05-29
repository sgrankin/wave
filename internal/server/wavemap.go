package server

import (
	"fmt"
	"sync"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// WaveMap holds the loaded wavelet containers for a server, keyed by wavelet
// name, loading them from the delta store on first access. It is the routing
// layer beneath the frontend: Open/Submit for a wavelet name resolve to a
// container here.
type WaveMap struct {
	store storage.DeltaStore
	clk   clock.Clock

	mu         sync.Mutex
	containers map[string]*WaveletContainer
}

// NewWaveMap creates a wave map backed by the given delta store and clock.
func NewWaveMap(store storage.DeltaStore, clk clock.Clock) *WaveMap {
	return &WaveMap{
		store:      store,
		clk:        clk,
		containers: map[string]*WaveletContainer{},
	}
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
	if c, ok := m.containers[key]; ok {
		return c, nil
	}
	access, err := m.store.Open(name)
	if err != nil {
		return nil, fmt.Errorf("server: open wavelet %s: %w", name, err)
	}
	c, err := Load(name, access, m.clk)
	if err != nil {
		return nil, err
	}
	m.containers[key] = c
	return c, nil
}
