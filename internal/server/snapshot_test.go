package server_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
)

// TestSnapshotLoadMatchesFullReplay: a wavelet built with snapshots enabled
// reloads via snapshot+tail to the same state (version, hash, content) as a
// full replay.
func TestSnapshotLoadMatchesFullReplay(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clk := clock.NewFixed(time.UnixMilli(1000))
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	// Build with snapshots enabled (snapshot every 2 ops).
	wm := server.NewWaveMap(store, clk, server.WithSnapshots(store, 2))
	c, _ := wm.Container(name)
	if _, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatalf("create: %v", err) // -> v2 (AddParticipant + blip)
	}
	if _, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(2, "!"))); err != nil {
		t.Fatalf("edit1: %v", err) // -> v3
	}
	if _, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(3, "?"))); err != nil {
		t.Fatalf("edit2: %v", err) // -> v4
	}
	want := c.Version()

	if _, _, ok, _ := store.GetLatestSnapshot(name); !ok {
		t.Fatal("expected a snapshot to have been written")
	}

	// Fresh snapshot-aware load and fresh full replay (separate WaveMaps so neither
	// hits the in-memory container cache).
	c2, err := server.NewWaveMap(store, clk, server.WithSnapshots(store, 2)).Container(name)
	if err != nil {
		t.Fatalf("snapshot load: %v", err)
	}
	c3, err := server.NewWaveMap(store, clk).Container(name)
	if err != nil {
		t.Fatalf("full replay: %v", err)
	}

	// Identical version + hash, and identical content (bit-identical state).
	if c2.Version().Compare(want) != 0 {
		t.Errorf("snapshot load v%d, want v%d", c2.Version().Version(), want.Version())
	}
	if c2.Version().Compare(c3.Version()) != 0 {
		t.Errorf("snapshot vs full-replay version/hash differ (v%d vs v%d)",
			c2.Version().Version(), c3.Version().Version())
	}
	b2, _ := c2.Wavelet().Blip("b")
	b3, _ := c3.Wavelet().Blip("b")
	if !b2.Content().Equal(b3.Content()) || !b2.Content().Equal(chars("hi!?")) {
		t.Errorf("content: snapshot %v, full %v, want hi!?", b2.Content().Components(), b3.Content().Components())
	}
}

// TestCorruptSnapshotFallsBack: a corrupt snapshot must not break loading — the
// container falls back to full replay (the delta log is authoritative).
func TestCorruptSnapshotFallsBack(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clk := clock.NewFixed(time.UnixMilli(1000))
	name := waveletName(t)
	alice := pid(t, "alice@example.com")

	// Build the log with snapshots off (so the only snapshot is the garbage below).
	wm := server.NewWaveMap(store, clk)
	c, _ := wm.Container(name)
	if _, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(2, "!"))); err != nil {
		t.Fatal(err)
	}
	want := c.Version()

	// Plant a corrupt snapshot, then load with snapshots enabled.
	if err := store.PutSnapshot(name, 2, []byte("garbage-not-a-snapshot")); err != nil {
		t.Fatal(err)
	}
	c2, err := server.NewWaveMap(store, clk, server.WithSnapshots(store, 100)).Container(name)
	if err != nil {
		t.Fatalf("load with corrupt snapshot should fall back, not error: %v", err)
	}
	if c2.Version().Compare(want) != 0 {
		t.Errorf("fell back to v%d, want v%d", c2.Version().Version(), want.Version())
	}
	if b, _ := c2.Wavelet().Blip("b"); !b.Content().Equal(chars("hi!")) {
		t.Errorf("content after fallback = %v, want hi!", b.Content().Components())
	}
}
