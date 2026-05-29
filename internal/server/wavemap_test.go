package server_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
)

func newWaveMap(t *testing.T) *server.WaveMap {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
}

func TestWaveMapCachesContainers(t *testing.T) {
	m := newWaveMap(t)
	name := waveletName(t)
	c1, err := m.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := m.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Error("Container should return the same instance for the same name")
	}
	// A different wave maps to a different container.
	w2, _ := id.NewWaveID("example.com", "w+other")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	other, err := m.Container(id.NewWaveletName(w2, wl))
	if err != nil {
		t.Fatal(err)
	}
	if other == c1 {
		t.Error("different wavelet names should map to different containers")
	}
}

func TestFanoutToSubscribers(t *testing.T) {
	m := newWaveMap(t)
	name := waveletName(t)
	c, _ := m.Container(name)
	alice := pid(t, "alice@example.com")

	s1 := c.Subscribe()
	defer s1.Close()
	s2 := c.Subscribe()
	defer s2.Close()

	res, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	for i, sub := range []*server.Subscription{s1, s2} {
		select {
		case u := <-sub.Updates():
			if u.ResultingVersion.Compare(res.ResultingVersion) != 0 {
				t.Errorf("sub %d: update version v%d, want v%d", i, u.ResultingVersion.Version(), res.ResultingVersion.Version())
			}
			if len(u.Delta.Ops) != 2 {
				t.Errorf("sub %d: update has %d ops, want 2", i, len(u.Delta.Ops))
			}
		default:
			t.Errorf("sub %d received no update", i)
		}
	}
}

func TestFanoutInOrder(t *testing.T) {
	m := newWaveMap(t)
	name := waveletName(t)
	c, _ := m.Container(name)
	alice := pid(t, "alice@example.com")

	sub := c.Subscribe()
	defer sub.Close()

	if _, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatal(err)
	}
	v2 := c.Version()
	if _, err := c.Submit(blipDelta(alice, v2, "b", appendText(2, "!"))); err != nil {
		t.Fatal(err)
	}

	first := <-sub.Updates()
	second := <-sub.Updates()
	if first.ResultingVersion.Version() != 2 || second.ResultingVersion.Version() != 3 {
		t.Errorf("delivery order = v%d, v%d; want v2, v3",
			first.ResultingVersion.Version(), second.ResultingVersion.Version())
	}
}

func TestSubscriptionClose(t *testing.T) {
	m := newWaveMap(t)
	c, _ := m.Container(waveletName(t))
	sub := c.Subscribe()
	sub.Close()
	if _, open := <-sub.Updates(); open {
		t.Error("closed subscription channel should be closed (not deliver)")
	}
}

// A subscriber that never drains is dropped once its buffer overflows: the
// channel closes, and submits keep succeeding (never blocked by the slow reader).
func TestFanoutOverflowDropsSubscriber(t *testing.T) {
	m := newWaveMap(t)
	name := waveletName(t)
	c, _ := m.Container(name)
	alice := pid(t, "alice@example.com")

	sub := c.Subscribe() // intentionally never drained

	// Creation delta (1 update) + enough edits to overflow the buffer by one.
	if _, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatal(err)
	}
	docLen := 2 // "hi"
	for i := 0; i < server.DefaultSubBuffer; i++ {
		if _, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(docLen, "!"))); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		docLen++
	}
	// Drain: exactly DefaultSubBuffer updates buffered, then closed (the overflow
	// update dropped the subscriber).
	got := 0
	for range sub.Updates() {
		got++
	}
	if got != server.DefaultSubBuffer {
		t.Errorf("buffered %d updates before drop, want %d", got, server.DefaultSubBuffer)
	}
}

// Open is the join flow: a client joining after some edits gets the full,
// contiguous history, then subsequent deltas on a live stream with no gap or
// overlap.
func TestOpenJoinThenFollow(t *testing.T) {
	m := newWaveMap(t)
	name := waveletName(t)
	c, _ := m.Container(name)
	alice := pid(t, "alice@example.com")

	// A creates (v2) and edits (v3) before B joins.
	if _, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(2, "!"))); err != nil {
		t.Fatal(err)
	}

	// B joins.
	_, history, sub := c.Open()
	defer sub.Close()
	if len(history) != 2 {
		t.Fatalf("history length %d, want 2", len(history))
	}
	if history[0].ResultingVersion.Version() != 2 || history[1].ResultingVersion.Version() != 3 {
		t.Errorf("history versions = %d,%d; want 2,3",
			history[0].ResultingVersion.Version(), history[1].ResultingVersion.Version())
	}

	// A edits again -> v4 -> arrives on B's live stream, contiguous with history.
	if _, err := c.Submit(blipDelta(alice, c.Version(), "b", appendText(3, "?"))); err != nil {
		t.Fatal(err)
	}
	u := <-sub.Updates()
	if u.ResultingVersion.Version() != 4 {
		t.Errorf("live update version %d, want 4", u.ResultingVersion.Version())
	}
	// No overlap: the live stream carries only the post-Open delta, not v2/v3.
	select {
	case extra := <-sub.Updates():
		t.Errorf("unexpected extra update v%d on the live stream", extra.ResultingVersion.Version())
	default:
	}
}

// Concurrent subscribe/close churn against an active submit loop must not race
// (run under -race -count=N to explore interleavings).
func TestFanoutConcurrent(t *testing.T) {
	m := newWaveMap(t)
	name := waveletName(t)
	c, _ := m.Container(name)
	alice := pid(t, "alice@example.com")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s := c.Subscribe()
				done := make(chan struct{})
				go func() {
					for range s.Updates() {
					}
					close(done)
				}()
				s.Close()
				<-done
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := c.Submit(creationDelta(alice, version.Zero(name), "b", chars("hi"))); err != nil {
			return
		}
		docLen := 2
		for j := 0; j < 200; j++ {
			c.Submit(blipDelta(alice, c.Version(), "b", appendText(docLen, "!")))
			docLen++
		}
	}()
	wg.Wait()
}
