package agentgw_test

import (
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
