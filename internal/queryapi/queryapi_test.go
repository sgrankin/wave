package queryapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/queryapi"
	"github.com/sgrankin/wave/internal/search"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/wavelet"
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

func waveName(t *testing.T, local string) id.WaveletName {
	t.Helper()
	w, err := id.NewWaveID("example.com", local)
	if err != nil {
		t.Fatal(err)
	}
	wl, err := id.NewWaveletID("example.com", "conv+root")
	if err != nil {
		t.Fatal(err)
	}
	return id.NewWaveletName(w, wl)
}

// seedWave creates a conversation wavelet authored by author and types text into
// its root blip, so the digest has a non-empty title/snippet and FTS has content.
func seedWave(t *testing.T, wm *server.WaveMap, name id.WaveletName, author id.ParticipantID, text string) {
	t.Helper()
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := conv.SeedConversation(author, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeedIfEmpty(author, ops); err != nil {
		t.Fatal(err)
	}
	if text == "" {
		return
	}
	// Root blip content after seeding is <body><line/></body> (4 items); insert the
	// text after the line marker (retain 3, insert, retain the closing </body>).
	edit := op.NewDocOp([]op.Component{op.Retain{Count: 3}, op.Characters{Text: text}, op.Retain{Count: 1}})
	blipOp := waveop.WaveletBlipOperation{BlipID: conv.RootBlipID, BlipOp: waveop.BlipContentOperation{
		Ctx: waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}, ContentOp: edit}}
	if _, err := c.Submit(waveop.NewWaveletDelta(author, c.Version(), []waveop.Operation{blipOp})); err != nil {
		t.Fatalf("edit root blip: %v", err)
	}
}

type digestResp struct {
	Waves []queryapi.Digest `json:"waves"`
}

func get(t *testing.T, base, path string, who id.ParticipantID, index *search.Index, wm *server.WaveMap, reads queryapi.ReadState) digestResp {
	t.Helper()
	h := queryapi.New(index, queryapi.NewWaveMapReader(wm), reads,
		func(*http.Request) (id.ParticipantID, bool) { return who, true }, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: status %d", base, path, resp.StatusCode)
	}
	var dr digestResp
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		t.Fatal(err)
	}
	return dr
}

func TestInboxAndSearch(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))

	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	w1 := waveName(t, "w+hello")
	w2 := waveName(t, "w+bye")
	w3 := waveName(t, "w+bobonly")
	seedWave(t, wm, w1, alice, "Hello world")
	seedWave(t, wm, w2, alice, "Goodbye moon")
	seedWave(t, wm, w3, bob, "Bob's private wave")

	// Inbox: alice sees her two waves, not bob's.
	inbox := get(t, "inbox", "/api/inbox", alice, idx, wm, store)
	if len(inbox.Waves) != 2 {
		t.Fatalf("alice inbox = %d waves, want 2: %+v", len(inbox.Waves), inbox.Waves)
	}
	titles := map[string]bool{}
	for _, d := range inbox.Waves {
		titles[d.Title] = true
		if d.Creator != "alice@example.com" {
			t.Errorf("digest creator = %q, want alice", d.Creator)
		}
		if len(d.Participants) != 1 || d.Participants[0] != "alice@example.com" {
			t.Errorf("digest participants = %v, want [alice]", d.Participants)
		}
	}
	if !titles["Hello world"] || !titles["Goodbye moon"] {
		t.Errorf("inbox titles = %v, want Hello world + Goodbye moon", titles)
	}

	// Bob's inbox is just his wave.
	bobInbox := get(t, "inbox", "/api/inbox", bob, idx, wm, store)
	if len(bobInbox.Waves) != 1 || bobInbox.Waves[0].Title != "Bob's private wave" {
		t.Fatalf("bob inbox = %+v, want one wave 'Bob's private wave'", bobInbox.Waves)
	}

	// Search scoped to alice: "Hello" matches only w1, with title + snippet.
	res := get(t, "search", "/api/search?q=Hello", alice, idx, wm, store)
	if len(res.Waves) != 1 || res.Waves[0].Title != "Hello world" {
		t.Fatalf("search Hello = %+v, want one wave 'Hello world'", res.Waves)
	}
	if res.Waves[0].Snippet != "Hello world" {
		t.Errorf("snippet = %q, want %q", res.Waves[0].Snippet, "Hello world")
	}

	// Search never crosses inbox boundaries: alice searching bob's text finds nothing.
	none := get(t, "search", "/api/search?q=private", alice, idx, wm, store)
	if len(none.Waves) != 0 {
		t.Errorf("alice search 'private' = %+v, want none (bob's wave is out of her inbox)", none.Waves)
	}
}

func TestUnauthorized(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))
	h := queryapi.New(idx, queryapi.NewWaveMapReader(wm), store,
		func(*http.Request) (id.ParticipantID, bool) { return id.ParticipantID{}, false }, nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/inbox")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated inbox status = %d, want 401", resp.StatusCode)
	}
}

// stubIndex / stubWaves drive the Handler without a real store, for the digest
// edge cases (uncreated wavelet, load failure) that are awkward to provoke live.
type stubIndex struct{ names []id.WaveletName }

func (s stubIndex) Inbox(id.ParticipantID) ([]id.WaveletName, error) { return s.names, nil }
func (s stubIndex) Search(id.ParticipantID, string, int) ([]storage.SearchResult, error) {
	res := make([]storage.SearchResult, len(s.names))
	for i, n := range s.names {
		res[i] = storage.SearchResult{Wavelet: n}
	}
	return res, nil
}

type stubWaves struct {
	fn func(name id.WaveletName, f func(*wavelet.Data)) error
}

func (s stubWaves) Read(name id.WaveletName, f func(*wavelet.Data)) error { return s.fn(name, f) }

// stubReads is a no-op ReadState (everything read) for tests that don't exercise
// the unread indicator.
type stubReads struct{}

func (stubReads) ReadVersions(id.ParticipantID) (map[string]uint64, error) {
	return map[string]uint64{}, nil
}
func (stubReads) SetReadVersion(id.ParticipantID, id.WaveletName, uint64) error { return nil }

func getJSON(t *testing.T, h *queryapi.Handler, path string) digestResp {
	t.Helper()
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: status %d", path, resp.StatusCode)
	}
	var dr digestResp
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		t.Fatal(err)
	}
	return dr
}

func alwaysAlice(t *testing.T) func(*http.Request) (id.ParticipantID, bool) {
	a := pid(t, "alice@example.com")
	return func(*http.Request) (id.ParticipantID, bool) { return a, true }
}

// TestDigestSkipsUncreated: a name the index returns but whose wavelet has no
// state yet (Read yields nil) is silently omitted — not a 500, not a phantom row.
func TestDigestSkipsUncreated(t *testing.T) {
	name := waveName(t, "w+ghost")
	h := queryapi.New(
		stubIndex{names: []id.WaveletName{name}},
		stubWaves{fn: func(_ id.WaveletName, fn func(*wavelet.Data)) error { fn(nil); return nil }},
		stubReads{}, alwaysAlice(t), nil)
	dr := getJSON(t, h, "/api/inbox")
	if len(dr.Waves) != 0 {
		t.Errorf("uncreated wavelet should be omitted, got %+v", dr.Waves)
	}
}

// TestDigestLogsLoadFailure: a wavelet that fails to load is omitted AND logged
// (so index/store drift or corruption is observable, not silently missing).
func TestDigestLogsLoadFailure(t *testing.T) {
	name := waveName(t, "w+broken")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := queryapi.New(
		stubIndex{names: []id.WaveletName{name}},
		stubWaves{fn: func(_ id.WaveletName, _ func(*wavelet.Data)) error { return errors.New("boom") }},
		stubReads{}, alwaysAlice(t), logger)
	dr := getJSON(t, h, "/api/inbox")
	if len(dr.Waves) != 0 {
		t.Errorf("load-failed wavelet should be omitted, got %+v", dr.Waves)
	}
	if !strings.Contains(buf.String(), "failed to load") {
		t.Errorf("expected a warn log about the failed load, got %q", buf.String())
	}
}

// TestSnippetTruncation: a long root blip yields a snippet capped to the rune
// limit with a trailing ellipsis (the doc.Snippet contract).
func TestSnippetTruncation(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))
	alice := pid(t, "alice@example.com")
	seedWave(t, wm, waveName(t, "w+long"), alice, strings.Repeat("a", 200))

	res := get(t, "inbox", "/api/inbox", alice, idx, wm, store)
	if len(res.Waves) != 1 {
		t.Fatalf("inbox = %d, want 1", len(res.Waves))
	}
	snip := res.Waves[0].Snippet
	if !strings.HasSuffix(snip, "…") {
		t.Errorf("snippet should end with an ellipsis when truncated, got %q", snip)
	}
	if n := len([]rune(snip)); n > 141 { // 140 + the ellipsis rune
		t.Errorf("snippet = %d runes, want <= 141", n)
	}
}

// TestUnreadAndMarkRead: a seeded wave is unread until /api/read records the
// participant's read version, after which the inbox reports it read.
func TestUnreadAndMarkRead(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))
	alice := pid(t, "alice@example.com")
	w := waveName(t, "w+unread")
	seedWave(t, wm, w, alice, "hi")

	h := queryapi.New(idx, queryapi.NewWaveMapReader(wm), store, alwaysAlice(t), nil)
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	inbox := func() queryapi.Digest {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/inbox")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var dr digestResp
		if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
			t.Fatal(err)
		}
		if len(dr.Waves) != 1 {
			t.Fatalf("inbox = %d waves, want 1", len(dr.Waves))
		}
		return dr.Waves[0]
	}

	d := inbox()
	if !d.Unread {
		t.Errorf("freshly seeded wave should be unread")
	}

	// Mark read at the wave's current version.
	markURL := fmt.Sprintf("%s/api/read?wave=%s&version=%d", srv.URL, url.QueryEscape(w.Serialize()), d.Version)
	resp, err := http.Post(markURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("mark read status = %d, want 204", resp.StatusCode)
	}

	if inbox().Unread {
		t.Errorf("wave should be read after /api/read")
	}
}
