package agentgw_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sgrankin/wave/internal/agent"
	"github.com/sgrankin/wave/internal/agentgw"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/search"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/version"
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

type gwEvent struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// postAs submits a post.blip intent as author against the shared container (a normal
// fanned-out submit), so the agent's subscription observes it.
func postAs(t *testing.T, c *server.WaveletContainer, author id.ParticipantID, text, blipID string) {
	t.Helper()
	var (
		ops    []waveop.Operation
		target version.HashedVersion
		terr   error
	)
	c.Read(func(w *wavelet.Data) {
		target = w.HashedVersion()
		reader := func(id string) (op.DocOp, bool) {
			b, ok := w.Blip(id)
			if !ok {
				return op.DocOp{}, false
			}
			return b.Content(), true
		}
		ops, terr = agent.Translate(agent.Intent{Kind: agent.IntentPostBlip, Text: text}, author, 2000, reader, func() string { return blipID })
	})
	if terr != nil {
		t.Fatal(terr)
	}
	if _, err := c.Submit(waveop.NewWaveletDelta(author, target, ops)); err != nil {
		t.Fatal(err)
	}
}

// TestAgentGatewayWebSocket drives the agent channel over a real WebSocket: an
// external "harness" connects as the agent with a bearer token, receives the
// wave.opened snapshot then a mention event, sends a reply intent, and the reply
// lands in the wavelet as a real OT submit.
func TestAgentGatewayWebSocket(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
	wid, _ := id.NewWaveID("example.com", "w+gw")
	wlid, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(wid, wlid)
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}

	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")
	seedOps, _ := conv.SeedConversation(alice, 1000)
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}
	addCtx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}
	if _, err := c.Submit(waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{
		waveop.AddParticipant{Ctx: addCtx, Participant: bot},
	})); err != nil {
		t.Fatal(err)
	}

	h := agentgw.New(wm, agentgw.StaticAuth{"s3cret": bot},
		transport.MembershipChecker{WaveMap: wm}, clock.NewFixed(time.UnixMilli(3000)), nil)
	hs := httptest.NewServer(h)
	defer hs.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/agent/socket?wave=" + url.QueryEscape(name.Serialize())
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer s3cret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	nc := websocket.NetConn(ctx, conn, websocket.MessageText)
	dec := json.NewDecoder(nc)

	// 1. wave.opened snapshot.
	var snap gwEvent
	if err := dec.Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Kind != agent.KindWaveOpened {
		t.Fatalf("first event = %q, want %q", snap.Kind, agent.KindWaveOpened)
	}

	// 2. alice mentions the agent → a mention event reaches the harness.
	postAs(t, c, alice, "hey @assistant@example.com", "b+alice")
	mentioned := false
	for i := 0; i < 4 && !mentioned; i++ {
		var ev gwEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if ev.Kind == string(agent.Mention) {
			if ev.Target != "assistant@example.com" {
				t.Fatalf("mention target = %q", ev.Target)
			}
			mentioned = true
		}
	}
	if !mentioned {
		t.Fatal("never received a mention event over the socket")
	}

	// 3. The harness replies with an intent; the gateway turns it into an OT submit.
	if _, err := nc.Write([]byte(`{"type":"intent","kind":"post.blip","text":"reply via ws"}` + "\n")); err != nil {
		t.Fatal(err)
	}

	// 4. The reply lands in the wavelet (find it by text — the agent's blip id is random).
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		c.Read(func(w *wavelet.Data) {
			for _, bid := range w.BlipIDs() {
				if bid == conv.ManifestDocumentID {
					continue
				}
				b, _ := w.Blip(bid)
				if txt, _ := doc.PlainText(b.Content()); txt == "reply via ws" {
					found = true
				}
			}
		})
		if !found {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("agent's reply did not land in the wavelet")
	}
}

// TestAgentGatewayReplyIntent drives the reply.blip intent (inline) over the real
// WebSocket: the harness replies to a specific blip and the gateway creates an
// inline reply thread under it — proving the inline JSON field survives the actual
// transport, not just the in-memory gateway.
func TestAgentGatewayReplyIntent(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
	wid, _ := id.NewWaveID("example.com", "w+reply")
	wlid, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(wid, wlid)
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}

	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")
	seedOps, _ := conv.SeedConversation(alice, 1000)
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}
	addCtx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}
	if _, err := c.Submit(waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{
		waveop.AddParticipant{Ctx: addCtx, Participant: bot},
	})); err != nil {
		t.Fatal(err)
	}

	h := agentgw.New(wm, agentgw.StaticAuth{"s3cret": bot},
		transport.MembershipChecker{WaveMap: wm}, clock.NewFixed(time.UnixMilli(3000)), nil)
	hs := httptest.NewServer(h)
	defer hs.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/agent/socket?wave=" + url.QueryEscape(name.Serialize())
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer s3cret"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	nc := websocket.NetConn(ctx, conn, websocket.MessageText)

	// Drain the snapshot so the gateway proceeds.
	var snap gwEvent
	if err := json.NewDecoder(nc).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}

	// Reply inline to the seeded root blip.
	if _, err := nc.Write([]byte(`{"type":"intent","kind":"reply.blip","blipId":"b+root","text":"inline reply over ws","inline":true}` + "\n")); err != nil {
		t.Fatal(err)
	}

	// A new reply blip carries the text, and b+root gains a matching inline anchor.
	deadline := time.Now().Add(3 * time.Second)
	var replyText, anchorThread string
	for time.Now().Before(deadline) && !(replyText != "" && anchorThread != "") {
		c.Read(func(w *wavelet.Data) {
			rb, ok := w.Blip("b+root")
			if !ok {
				return
			}
			anchors := conv.ReadReplyAnchors(rb.Content())
			if len(anchors) == 0 {
				return
			}
			anchorThread = anchors[0].ThreadID
			if b, ok := w.Blip(anchorThread); ok {
				replyText, _ = doc.PlainText(b.Content())
			}
		})
		if !(replyText != "" && anchorThread != "") {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if anchorThread == "" {
		t.Fatal("parent blip b+root never gained an inline reply anchor")
	}
	if replyText != "inline reply over ws" {
		t.Errorf("reply blip text = %q, want %q", replyText, "inline reply over ws")
	}
}

// TestAgentGatewayForbidsNonMember confirms a valid token cannot drive a wave the
// agent is not a participant of (StrictMembershipChecker, no open-or-create).
func TestAgentGatewayForbidsNonMember(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
	wid, _ := id.NewWaveID("example.com", "w+priv")
	wlid, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(wid, wlid)
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")
	// Seed with alice only — the bot is NOT added.
	seedOps, _ := conv.SeedConversation(alice, 1000)
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}

	// Strict checker (as wired in waved) — no open-or-create for agents.
	h := agentgw.New(wm, agentgw.StaticAuth{"s3cret": bot},
		transport.StrictMembershipChecker{WaveMap: wm}, clock.System{}, nil)
	hs := httptest.NewServer(h)
	defer hs.Close()

	req, _ := http.NewRequest("GET", hs.URL+"/agent/socket?wave="+url.QueryEscape(name.Serialize()), nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (non-member agent)", resp.StatusCode)
	}
}

// TestAgentGatewayRejectsBadToken confirms an unknown bearer token is rejected
// before any upgrade.
func TestAgentGatewayRejectsBadToken(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
	bot := pid(t, "assistant@example.com")
	h := agentgw.New(wm, agentgw.StaticAuth{"good": bot}, transport.MembershipChecker{WaveMap: wm}, clock.System{}, nil)
	hs := httptest.NewServer(h)
	defer hs.Close()

	req, _ := http.NewRequest("GET", hs.URL+"/agent/socket?wave=example.com/w+x/~/conv+root", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestAgentWaveManagement drives the agent wave-management API (create / list /
// leave): an agent creates its own memory wave, finds it via discovery, and leaves it.
func TestAgentWaveManagement(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	idx := search.New(store, nil)
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)), server.WithIndexer(idx))
	bot := pid(t, "assistant@example.com")

	h := agentgw.New(wm, agentgw.StaticAuth{"s3cret": bot},
		transport.StrictMembershipChecker{WaveMap: wm}, clock.NewFixed(time.UnixMilli(3000)), nil).WithIndex(idx)
	hs := httptest.NewServer(h.Routes())
	defer hs.Close()

	do := func(method, path, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, hs.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Unauthorized without a token.
	if resp := do("POST", "/agent/waves", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("create without token = %d, want 401", resp.StatusCode)
	}

	// Create: the agent makes a fresh wave and is its sole participant.
	resp := do("POST", "/agent/waves", "s3cret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d, want 200", resp.StatusCode)
	}
	var created struct {
		Wave string `json:"wave"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	name, err := id.ParseWaveletName(created.Wave)
	if err != nil {
		t.Fatalf("created wave name %q: %v", created.Wave, err)
	}
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	if exists, createdW := c.HasParticipant(bot); !createdW || !exists {
		t.Fatalf("agent should be a participant of the wave it created (exists=%v created=%v)", exists, createdW)
	}

	// Discover: the created wave shows up in the agent's wave list.
	resp = do("GET", "/agent/waves", "s3cret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d, want 200", resp.StatusCode)
	}
	var listed struct {
		Waves []struct {
			Wave string `json:"wave"`
		} `json:"waves"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	found := false
	for _, w := range listed.Waves {
		if w.Wave == created.Wave {
			found = true
		}
	}
	if !found {
		t.Fatalf("created wave %q not in discovery list %+v", created.Wave, listed.Waves)
	}

	// Leave: the agent removes itself; it is no longer a participant.
	resp = do("POST", "/agent/leave?wave="+url.QueryEscape(created.Wave), "s3cret")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("leave = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	if exists, _ := c.HasParticipant(bot); exists {
		t.Fatal("agent should not be a participant after leaving")
	}

	// Leaving a wave the agent is not in is forbidden.
	resp = do("POST", "/agent/leave?wave="+url.QueryEscape(created.Wave), "s3cret")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("leave-again = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestAgentStateMemory proves the structured-state memory primitive end to end: an
// agent writes state with a set.state intent, and a fresh gateway's wave.opened
// snapshot reports it back (write → read-on-connect).
func TestAgentStateMemory(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))
	wid, _ := id.NewWaveID("example.com", "w+state")
	wlid, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(wid, wlid)
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")
	seedOps, _ := conv.SeedConversation(alice, 1000)
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}
	addCtx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}
	if _, err := c.Submit(waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{
		waveop.AddParticipant{Ctx: addCtx, Participant: bot},
	})); err != nil {
		t.Fatal(err)
	}

	fixedID := func() string { return "b+unused" }

	// The agent writes two state keys via intents.
	lc := agent.NewLocalClient(c, bot, clock.NewFixed(time.UnixMilli(2000)), fixedID)
	lc.Open()
	for _, kv := range []struct{ k, v string }{{"status", "ready"}, {"runs", "7"}} {
		if err := lc.SubmitIntent(agent.Intent{Kind: agent.IntentSetState, Key: kv.k, Value: kv.v}); err != nil {
			t.Fatalf("set.state %s: %v", kv.k, err)
		}
	}
	lc.Close()

	// A fresh gateway's wave.opened snapshot reports the state back.
	lc2 := agent.NewLocalClient(c, bot, clock.NewFixed(time.UnixMilli(3000)), fixedID)
	lc2.Open()
	defer lc2.Close()
	gw := agent.NewGateway(lc2, bot, nil)
	var buf bytes.Buffer
	if err := gw.Run(context.Background(), &buf, strings.NewReader("")); err != nil {
		t.Fatalf("gateway run: %v", err)
	}
	var snap struct {
		Kind  string            `json:"kind"`
		State map[string]string `json:"state"`
	}
	if err := json.NewDecoder(&buf).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Kind != agent.KindWaveOpened {
		t.Fatalf("first event = %q, want wave.opened", snap.Kind)
	}
	if snap.State["status"] != "ready" || snap.State["runs"] != "7" || len(snap.State) != 2 {
		t.Fatalf("snapshot state = %v, want {status:ready, runs:7}", snap.State)
	}
}
