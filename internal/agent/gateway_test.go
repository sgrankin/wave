package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/agent"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/wavelet"
)

// gwEvent is the test's view of a gateway event line.
type gwEvent struct {
	Type         string `json:"type"`
	Kind         string `json:"kind"`
	Author       string `json:"author"`
	Target       string `json:"target"`
	Participants []string
	Blips        []struct{ ID, Author, Text string }
}

// TestGatewayBridgesEventsAndIntents drives the out-of-process bridge end-to-end
// over in-memory pipes against a real container: the harness receives a
// wave.opened snapshot then a mention event, and an intent it writes back is turned
// into a real OT submit that lands in the wavelet.
func TestGatewayBridgesEventsAndIntents(t *testing.T) {
	c, _ := newContainer(t)
	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")

	seedOps, _ := conv.SeedConversation(alice, 1000)
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}

	lc := agent.NewLocalClient(c, bot, clock.NewFixed(time.UnixMilli(3000)), func() string { return "b+gw" })
	lc.Open()
	defer lc.Close()

	eventsR, eventsW := io.Pipe()
	intentsR, intentsW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gw := agent.NewGateway(lc, bot, nil)
	done := make(chan error, 1)
	go func() { done <- gw.Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	readEvent := func() gwEvent {
		t.Helper()
		var ev gwEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		return ev
	}

	// 1. The connect-time snapshot.
	snap := readEvent()
	if snap.Kind != agent.KindWaveOpened {
		t.Fatalf("first event = %q, want %q", snap.Kind, agent.KindWaveOpened)
	}

	// 2. alice posts a blip mentioning the agent → the harness sees a mention event
	// (the same delta also yields a blip.added; read until the mention arrives).
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "ping @assistant@example.com"}, "b+alice")
	var mentioned bool
	for i := 0; i < 4 && !mentioned; i++ {
		if ev := readEvent(); ev.Kind == string(agent.Mention) {
			if ev.Target != "assistant@example.com" {
				t.Fatalf("mention target = %q", ev.Target)
			}
			mentioned = true
		}
	}
	if !mentioned {
		t.Fatal("never received a mention event")
	}

	// 3. The harness replies with an intent; the gateway turns it into an OT submit.
	if _, err := intentsW.Write([]byte(`{"type":"intent","kind":"post.blip","text":"reply via gateway"}` + "\n")); err != nil {
		t.Fatal(err)
	}

	// 4. The reply lands in the wavelet (the agent's blip id is b+gw).
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		c.Read(func(w *wavelet.Data) {
			if b, ok := w.Blip("b+gw"); ok {
				got, _ = doc.PlainText(b.Content())
			}
		})
		if got != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got != "reply via gateway" {
		t.Fatalf("gateway reply blip text = %q, want %q", got, "reply via gateway")
	}

	// Shut down cleanly.
	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Run did not return after cancel")
	}
}
