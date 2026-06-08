package search_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/search"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func waveletName(t *testing.T, wave string) id.WaveletName {
	t.Helper()
	w, err := id.NewWaveID("example.com", wave)
	if err != nil {
		t.Fatal(err)
	}
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	return id.NewWaveletName(w, wl)
}

func chars(s string) op.DocOp { return op.NewDocOp([]op.Component{op.Characters{Text: s}}) }

// addParticipantDelta adds p (and writes a blip so the wavelet is created).
func addParticipantDelta(author, p id.ParticipantID, target version.HashedVersion) waveop.WaveletDelta {
	c := waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}
	return waveop.NewWaveletDelta(author, target, []waveop.Operation{
		waveop.AddParticipant{Ctx: c, Participant: p},
	})
}

func inboxSet(t *testing.T, idx *search.Index, p id.ParticipantID) map[string]bool {
	t.Helper()
	names, err := idx.Inbox(p)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, n := range names {
		set[n.String()] = true
	}
	return set
}

func TestInboxTracksParticipants(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))

	name := waveletName(t, "w+a")
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	c, _ := wm.Container(name)

	// First delta adds alice (creator) and creates the wavelet.
	if _, err := c.Submit(addParticipantDelta(alice, alice, version.Zero(name))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := inboxSet(t, idx, alice); !got[name.String()] {
		t.Errorf("alice inbox = %v, want it to contain %s", got, name)
	}
	if got := inboxSet(t, idx, bob); got[name.String()] {
		t.Errorf("bob should not be in the wavelet yet")
	}

	// Add bob → appears in his inbox.
	if _, err := c.Submit(addParticipantDelta(alice, bob, c.Version())); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	if got := inboxSet(t, idx, bob); !got[name.String()] {
		t.Errorf("bob inbox = %v, want it to contain %s after add", got, name)
	}

	// Remove bob → drops from his inbox; alice remains.
	rm := waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{
		waveop.RemoveParticipant{Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}, Participant: bob},
	})
	if _, err := c.Submit(rm); err != nil {
		t.Fatalf("remove bob: %v", err)
	}
	if got := inboxSet(t, idx, bob); got[name.String()] {
		t.Errorf("bob inbox = %v, want empty after removal", got)
	}
	if got := inboxSet(t, idx, alice); !got[name.String()] {
		t.Errorf("alice should still be a participant")
	}
}

// createWithText adds the author as a participant and writes a blip with text.
func createWithText(author id.ParticipantID, target version.HashedVersion, blipID, text string) waveop.WaveletDelta {
	c := waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}
	return waveop.NewWaveletDelta(author, target, []waveop.Operation{
		waveop.AddParticipant{Ctx: c, Participant: author},
		waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: chars(text)}},
	})
}

func searchSet(t *testing.T, idx *search.Index, who id.ParticipantID, query string) map[string]bool {
	t.Helper()
	results, err := idx.Search(who, query, 0)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	set := map[string]bool{}
	for _, r := range results {
		set[r.Wavelet.String()] = true
	}
	return set
}

func TestFullTextSearch(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))

	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	wa := waveletName(t, "w+a")
	wb := waveletName(t, "w+b")

	ca, _ := wm.Container(wa)
	if _, err := ca.Submit(createWithText(alice, version.Zero(wa), "b", "the quick brown fox")); err != nil {
		t.Fatal(err)
	}
	cb, _ := wm.Container(wb)
	if _, err := cb.Submit(createWithText(alice, version.Zero(wb), "b", "the lazy dog")); err != nil {
		t.Fatal(err)
	}

	// Free-text matches the right wavelet.
	if got := searchSet(t, idx, alice, "quick"); len(got) != 1 || !got[wa.String()] {
		t.Errorf("search 'quick' = %v, want {w+a}", got)
	}
	if got := searchSet(t, idx, alice, "dog"); len(got) != 1 || !got[wb.String()] {
		t.Errorf("search 'dog' = %v, want {w+b}", got)
	}
	// Terms are ANDed: no single wavelet has both "fox" and "dog".
	if got := searchSet(t, idx, alice, "fox dog"); len(got) != 0 {
		t.Errorf("search 'fox dog' = %v, want empty (AND)", got)
	}

	// Inbox-scoped: bob is not a participant, so he finds nothing.
	if got := searchSet(t, idx, bob, "quick"); len(got) != 0 {
		t.Errorf("bob (non-participant) search = %v, want empty", got)
	}
	// Add bob to w+a → he can now find it.
	if _, err := ca.Submit(addParticipantDelta(alice, bob, ca.Version())); err != nil {
		t.Fatal(err)
	}
	if got := searchSet(t, idx, bob, "quick"); !got[wa.String()] {
		t.Errorf("bob search after add = %v, want to contain w+a", got)
	}

	// creator: filter — alice created both.
	if got := searchSet(t, idx, alice, "creator:alice@example.com the"); len(got) != 2 {
		t.Errorf("creator:alice = %v, want both wavelets", got)
	}
	if got := searchSet(t, idx, alice, "creator:bob@example.com the"); len(got) != 0 {
		t.Errorf("creator:bob = %v, want empty (bob created none)", got)
	}
	// with: filter — only w+a also has bob.
	if got := searchSet(t, idx, alice, "with:bob@example.com the"); len(got) != 1 || !got[wa.String()] {
		t.Errorf("with:bob = %v, want {w+a}", got)
	}
}

// Editing a blip updates its indexed text (old terms stop matching).
func TestSearchReindexOnEdit(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))
	alice := pid(t, "alice@example.com")
	wa := waveletName(t, "w+a")
	c, _ := wm.Container(wa)

	if _, err := c.Submit(createWithText(alice, version.Zero(wa), "b", "original")); err != nil {
		t.Fatal(err)
	}
	if got := searchSet(t, idx, alice, "original"); !got[wa.String()] {
		t.Fatal("expected to find 'original'")
	}
	// Replace the blip content (full-doc replace: delete 8 chars, insert new).
	replace := op.NewDocOp([]op.Component{op.DeleteCharacters{Text: "original"}, op.Characters{Text: "replacement"}})
	o := waveop.WaveletBlipOperation{BlipID: "b", BlipOp: waveop.BlipContentOperation{
		Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}, ContentOp: replace}}
	if _, err := c.Submit(waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{o})); err != nil {
		t.Fatal(err)
	}
	if got := searchSet(t, idx, alice, "original"); len(got) != 0 {
		t.Errorf("'original' still matches after edit: %v", got)
	}
	if got := searchSet(t, idx, alice, "replacement"); !got[wa.String()] {
		t.Errorf("'replacement' should match after edit")
	}
}

func TestRebuildFromLog(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clk := clock.NewFixed(time.UnixMilli(1000))
	alice := pid(t, "alice@example.com")

	// Build two waves WITHOUT indexing (index starts empty).
	wm := server.NewWaveMap(store, clk)
	for _, wn := range []string{"w+a", "w+b"} {
		name := waveletName(t, wn)
		c, _ := wm.Container(name)
		o := waveop.WaveletBlipOperation{BlipID: "b", BlipOp: waveop.BlipContentOperation{
			Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}, ContentOp: chars("hi")}}
		d := waveop.NewWaveletDelta(alice, version.Zero(name), []waveop.Operation{
			waveop.AddParticipant{Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}, Participant: alice},
			o,
		})
		if _, err := c.Submit(d); err != nil {
			t.Fatalf("submit %s: %v", wn, err)
		}
	}

	// Index is empty before rebuild.
	idx := search.New(store, nil)
	if got := inboxSet(t, idx, alice); len(got) != 0 {
		t.Fatalf("inbox before rebuild = %v, want empty", got)
	}
	// Rebuild from the log → both waves appear.
	if err := search.Rebuild(store, store, clk); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	got := inboxSet(t, idx, alice)
	if !got[waveletName(t, "w+a").String()] || !got[waveletName(t, "w+b").String()] {
		t.Errorf("inbox after rebuild = %v, want both w+a and w+b", got)
	}
}

// TestCanAccessParticipationPredicate exercises the REAL access-control predicate
// (Index.CanAccess → store.IsParticipant) against a real sqlite store. Every consumer
// (attachments, presence, playback, transport) substitutes a fake access checker in its
// own tests, so a bug in the membership WHERE clause would silently mis-authorize with
// no failing test — this is the one place it is exercised end to end.
func TestCanAccessParticipationPredicate(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))

	name := waveletName(t, "w+access")
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	carol := pid(t, "carol@example.com")

	c, _ := wm.Container(name)
	if _, err := c.Submit(addParticipantDelta(alice, alice, version.Zero(name))); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Submit(addParticipantDelta(alice, bob, c.Version())); err != nil {
		t.Fatalf("add bob: %v", err)
	}

	canAccess := func(p id.ParticipantID, n id.WaveletName) bool {
		ok, err := idx.CanAccess(p, n)
		if err != nil {
			t.Fatalf("CanAccess(%s): %v", p, err)
		}
		return ok
	}

	if !canAccess(alice, name) {
		t.Error("alice (creator) should have access")
	}
	if !canAccess(bob, name) {
		t.Error("bob (member) should have access")
	}
	if canAccess(carol, name) {
		t.Error("carol (non-member) must NOT have access")
	}
	if canAccess(alice, waveletName(t, "w+nonexistent")) {
		t.Error("no one should have access to an unknown wavelet")
	}

	// Removing bob revokes his access immediately.
	rm := waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{
		waveop.RemoveParticipant{Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}, Participant: bob},
	})
	if _, err := c.Submit(rm); err != nil {
		t.Fatalf("remove bob: %v", err)
	}
	if canAccess(bob, name) {
		t.Error("bob's access must be revoked after removal")
	}
	if !canAccess(alice, name) {
		t.Error("alice should still have access")
	}
}
