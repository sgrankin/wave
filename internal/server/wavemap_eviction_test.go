package server_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
)

// evictingWaveMap builds a WaveMap with idle eviction and returns its (mutable)
// clock so a test can advance time deterministically to provoke a sweep.
func evictingWaveMap(t *testing.T, idleTTL time.Duration) (*server.WaveMap, *clock.Fixed) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	clk := clock.NewFixed(time.UnixMilli(0))
	return server.NewWaveMap(store, clk, server.WithEviction(idleTTL)), clk
}

func namedWavelet(t *testing.T, local string) id.WaveletName {
	t.Helper()
	w, err := id.NewWaveID("example.com", local)
	if err != nil {
		t.Fatal(err)
	}
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	return id.NewWaveletName(w, wl)
}

// TestEvictsIdleUnsubscribedAndReloads: a container idle past the TTL with no
// subscribers is dropped on the next sweep, and re-accessing it loads a fresh
// instance whose durable state (the delta log) is intact.
func TestEvictsIdleUnsubscribedAndReloads(t *testing.T) {
	const ttl = 10 * time.Minute
	m, clk := evictingWaveMap(t, ttl)
	alice := pid(t, "alice@example.com")
	a := namedWavelet(t, "w+a")

	c1, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	// Durable state so the reload has something to restore.
	if _, err := c1.Submit(creationDelta(alice, version.Zero(a), "b", chars("hello"))); err != nil {
		t.Fatalf("submit: %v", err)
	}
	v := c1.Version()

	// Idle past the TTL, then a miss on a DIFFERENT wave triggers the opportunistic
	// sweep (loads grow the cache, so the sweep piggybacks on them).
	clk.Advance(ttl + time.Minute)
	if _, err := m.Container(namedWavelet(t, "w+trigger")); err != nil {
		t.Fatal(err)
	}

	// 'a' was idle + unsubscribed → evicted; re-getting it loads a NEW instance.
	c2, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	if c2 == c1 {
		t.Fatal("idle unsubscribed container should have been evicted (got the same instance back)")
	}
	if c2.Version().Compare(v) != 0 {
		t.Errorf("reloaded version = v%d, want v%d (state lost across eviction)", c2.Version().Version(), v.Version())
	}
}

// TestSubscribedContainerSurvivesSweep: a container with a live subscriber is NEVER
// evicted, even when idle past the TTL — a subscriber means a session that may still
// submit, and a second instance would split-brain the wavelet.
func TestSubscribedContainerSurvivesSweep(t *testing.T) {
	const ttl = 10 * time.Minute
	m, clk := evictingWaveMap(t, ttl)
	a := namedWavelet(t, "w+a")

	c1, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	sub := c1.Subscribe() // a live session holding the wavelet open
	defer sub.Close()

	clk.Advance(ttl + time.Minute)
	if _, err := m.Container(namedWavelet(t, "w+trigger")); err != nil {
		t.Fatal(err)
	}

	c2, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c1 {
		t.Error("a container with a live subscriber must not be evicted")
	}
}

// TestHotContainerNotEvicted: a container accessed within the TTL survives a sweep
// even with no subscribers — the access timestamp refresh keeps a hot wave resident.
func TestHotContainerNotEvicted(t *testing.T) {
	const ttl = 10 * time.Minute
	m, clk := evictingWaveMap(t, ttl)
	a := namedWavelet(t, "w+a")

	c1, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(ttl + time.Minute)
	// Touch 'a' (a cache hit refreshes its access time but does not sweep)...
	if _, err := m.Container(a); err != nil {
		t.Fatal(err)
	}
	// ...then a miss triggers the sweep; 'a' is hot (just touched) → must survive.
	if _, err := m.Container(namedWavelet(t, "w+trigger")); err != nil {
		t.Fatal(err)
	}

	c2, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c1 {
		t.Error("a recently-accessed container must not be evicted")
	}
}

// TestEvictionDisabledByDefault: without WithEviction the cache never evicts, even
// for an idle unsubscribed container (the historical unbounded behavior).
func TestEvictionDisabledByDefault(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	clk := clock.NewFixed(time.UnixMilli(0))
	m := server.NewWaveMap(store, clk) // no WithEviction

	a := namedWavelet(t, "w+a")
	c1, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(365 * 24 * time.Hour) // a year idle
	if _, err := m.Container(namedWavelet(t, "w+trigger")); err != nil {
		t.Fatal(err)
	}
	c2, err := m.Container(a)
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c1 {
		t.Error("with eviction disabled the container must persist (same instance)")
	}
}
