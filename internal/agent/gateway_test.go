package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
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
	Intent       string `json:"intent"`
	ID           string `json:"id"`
	Error        string `json:"error"`
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
	var mentioned, sawBlipForMention bool
	for i := 0; i < 4 && !mentioned; i++ {
		ev := readEvent()
		switch ev.Kind {
		case string(agent.BlipAdded), string(agent.BlipEdited):
			sawBlipForMention = true
		case string(agent.Mention):
			if ev.Target != "assistant@example.com" {
				t.Fatalf("mention target = %q", ev.Target)
			}
			// Coalescing must never let a mention precede the blip event that carries the
			// text it refers to: the blip.added/edited for this blip must arrive first.
			if !sawBlipForMention {
				t.Fatal("mention arrived before any blip event for the mentioned blip")
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

// TestGatewayCoalescesBlipEditBurst drives the blip.edited debounce end-to-end: a
// burst of rapid edit.blip deltas to the SAME blip from alice collapses into ONE
// blip.edited carrying the FINAL text, forced out by an immediate event
// (add.participant). The exact wall-clock timing is not asserted — the burst-
// collapse is triggered by the immediate-event flush, not by waiting out the timer
// — so the test is deterministic.
func TestGatewayCoalescesBlipEditBurst(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	var snap struct{ Kind string }
	if err := dec.Decode(&snap); err != nil {
		t.Fatal(err)
	}

	// alice creates a blip, then appends to it many times in rapid succession. Each
	// postAs is a distinct OT submit → a distinct delta → a distinct update fed to
	// the gateway, so without coalescing the harness would see one blip.edited per
	// append.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "x"}, "b+burst")
	for i := 0; i < 5; i++ {
		postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+burst", Text: "y"}, "b+ignored")
	}
	// An immediate event (add.participant) forces every pending edit to flush ahead
	// of it, so the burst is observable now without waiting for the debounce timer.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentAddParticipant, Participant: "carol@example.com"}, "b+ignored")

	// Read until the participant.added arrives, counting blip.edited lines for
	// b+burst and capturing the latest edited text along the way.
	editCount := 0
	var lastEditText string
	var sawParticipant bool
	deadline := time.Now().Add(2 * time.Second)
	for !sawParticipant && time.Now().Before(deadline) {
		var ev struct {
			Kind, BlipID, Text, Participant string
		}
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		switch ev.Kind {
		case string(agent.BlipEdited):
			if ev.BlipID == "b+burst" {
				editCount++
				lastEditText = ev.Text
			}
		case string(agent.ParticipantAdded):
			sawParticipant = true
		}
	}
	if !sawParticipant {
		t.Fatal("never received the participant.added that forces the flush")
	}
	if editCount != 1 {
		t.Fatalf("blip.edited count for b+burst = %d, want exactly 1 (coalesced)", editCount)
	}
	// The surviving edit carries the FINAL text (the body started "x" then got five
	// "y" appends).
	if want := "xyyyyy"; lastEditText != want {
		t.Fatalf("coalesced blip.edited text = %q, want %q (latest-wins)", lastEditText, want)
	}

	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Run did not return after cancel")
	}
}

// TestGatewayCoalesceTimerFlush proves the debounce-on-quiescence path itself: a
// burst of edits to the same blip, then NO further activity (no immediate event,
// no shutdown). Run's wall-clock timer must fire after the quiescence window and
// emit exactly one blip.edited carrying the final text. The coalescer's time base
// is real wall time (not the injected clock), so this is reliable under the fixed
// test clock; the window is 400ms, so a 3s read deadline is generous.
func TestGatewayCoalesceTimerFlush(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	var snap struct{ Kind string }
	if err := dec.Decode(&snap); err != nil {
		t.Fatal(err)
	}

	// alice creates a blip then appends to it several times, then goes quiet. The
	// quiescence timer (not an immediate event, not shutdown) must flush the burst.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "x"}, "b+burst")
	for i := 0; i < 4; i++ {
		postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+burst", Text: "y"}, "b+ignored")
	}

	// Read events for up to 3s; count blip.edited for b+burst and capture the final
	// text. blip.added arrives immediately (it is an immediate kind); the edits are
	// buffered and arrive coalesced after the ~400ms quiescence window.
	type ev struct{ Kind, BlipID, Text string }
	got := make(chan ev, 16)
	go func() {
		for {
			var e ev
			if err := dec.Decode(&e); err != nil {
				return
			}
			got <- e
		}
	}()
	editCount := 0
	var lastEditText string
	deadline := time.After(3 * time.Second)
	// Read until we have seen at least one coalesced edit and a short settle window
	// passes with no further edits (proving collapse, not just first-arrival).
	settle := time.NewTimer(time.Hour)
	settle.Stop()
loop:
	for {
		select {
		case e := <-got:
			if e.Kind == string(agent.BlipEdited) && e.BlipID == "b+burst" {
				editCount++
				lastEditText = e.Text
				if !settle.Stop() {
					select {
					case <-settle.C:
					default:
					}
				}
				settle.Reset(500 * time.Millisecond)
			}
		case <-settle.C:
			break loop
		case <-deadline:
			break loop
		}
	}
	if editCount != 1 {
		t.Fatalf("blip.edited count for b+burst = %d, want exactly 1 (timer-coalesced)", editCount)
	}
	if want := "xyyyy"; lastEditText != want {
		t.Fatalf("timer-coalesced blip.edited text = %q, want %q (latest-wins)", lastEditText, want)
	}

	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Run did not return after cancel")
	}
}

// TestGatewayShutdownFlushesPending proves a still-pending coalesced edit is
// flushed on a terminal path: alice edits a blip (buffered, debounce not yet
// elapsed), then the harness closes its intent stream (EOF) — Run must emit the
// final blip.edited before returning.
func TestGatewayShutdownFlushesPending(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	var snap struct{ Kind string }
	if err := dec.Decode(&snap); err != nil {
		t.Fatal(err)
	}

	// alice creates then edits b+root → the edit is buffered (debounce 400ms not yet
	// elapsed, and no immediate event follows). b+root already exists from the seed.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+root", Text: " edited"}, "b+ignored")

	// Drain any blip.added/edited from prior deltas; we just need to confirm a final
	// edit for b+root is delivered after the EOF-triggered flush. Close the intent
	// writer to trigger the guaranteed-deliverable flush path.
	_ = intentsW.Close()

	sawEdit := make(chan struct{})
	go func() {
		for {
			var ev struct{ Kind, BlipID string }
			if err := dec.Decode(&ev); err != nil {
				return
			}
			if ev.Kind == string(agent.BlipEdited) && ev.BlipID == "b+root" {
				close(sawEdit)
				return
			}
		}
	}()
	select {
	case <-sawEdit:
	case <-time.After(2 * time.Second):
		t.Fatal("pending blip.edited for b+root was not flushed on EOF shutdown")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Run did not return after intent EOF")
	}
	// Unblock the reader goroutine parked in Decode (Run returns without closing
	// eventsW, so close the read side to let it exit).
	_ = eventsR.Close()
}

// TestGatewayIntentKindsAndResilience exercises the other intent kinds and the
// malformed-line resilience over the gateway: a bad JSON line is skipped (not
// fatal), then an add.participant and an edit.blip both apply.
func TestGatewayIntentKindsAndResilience(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	// Read (and discard) the snapshot so the gateway proceeds.
	var snap struct{ Kind string }
	if err := json.NewDecoder(eventsR).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	// Drain remaining event output so the gateway's event-writer never blocks while
	// the intent goroutine works.
	go func() { _, _ = io.Copy(io.Discard, eventsR) }()

	// A malformed line (must be skipped, not fatal), then two valid intents.
	lines := "this is not json\n" +
		`{"type":"intent","kind":"add.participant","participant":"carol@example.com"}` + "\n" +
		`{"type":"intent","kind":"edit.blip","blipId":"b+root","text":"appended by agent"}` + "\n"
	if _, err := intentsW.Write([]byte(lines)); err != nil {
		t.Fatal(err)
	}

	// Both intents take effect (the malformed line between/before them did not abort
	// the stream): carol is a participant and b+root gained the appended text.
	deadline := time.Now().Add(3 * time.Second)
	var carolIn, appended bool
	for time.Now().Before(deadline) && !(carolIn && appended) {
		c.Read(func(w *wavelet.Data) {
			carolIn = false
			for _, p := range w.Participants() {
				if p == pid(t, "carol@example.com") {
					carolIn = true
				}
			}
			if b, ok := w.Blip("b+root"); ok {
				if txt, _ := doc.PlainText(b.Content()); strings.Contains(txt, "appended by agent") {
					appended = true
				}
			}
		})
		if !(carolIn && appended) {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if !carolIn {
		t.Error("add.participant intent did not take effect")
	}
	if !appended {
		t.Error("edit.blip intent did not take effect (or malformed line aborted the stream)")
	}

	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	<-done
}

// TestGatewayReplyIntent drives the reply.blip intent over the wire (including the
// inline flag): the harness replies to a specific blip, and the gateway turns it
// into an OT submit that creates an inline reply thread under that blip — proving
// the inline JSON field flows end-to-end through the gateway schema.
func TestGatewayReplyIntent(t *testing.T) {
	c, _ := newContainer(t)
	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")
	seedOps, _ := conv.SeedConversation(alice, 1000)
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}

	lc := agent.NewLocalClient(c, bot, clock.NewFixed(time.UnixMilli(3000)), func() string { return "b+reply" })
	lc.Open()
	defer lc.Close()

	eventsR, eventsW := io.Pipe()
	intentsR, intentsW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	var snap struct{ Kind string }
	if err := json.NewDecoder(eventsR).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, eventsR) }()

	// Reply inline to the seeded root blip.
	line := `{"type":"intent","kind":"reply.blip","blipId":"b+root","text":"inline reply via gateway","inline":true}` + "\n"
	if _, err := intentsW.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}

	// The reply lands as a new blip b+reply with the text, and the parent body
	// gains an inline <reply id="b+reply"/> anchor.
	deadline := time.Now().Add(3 * time.Second)
	var replyText string
	var anchored bool
	for time.Now().Before(deadline) && !(replyText != "" && anchored) {
		c.Read(func(w *wavelet.Data) {
			if b, ok := w.Blip("b+reply"); ok {
				replyText, _ = doc.PlainText(b.Content())
			}
			if b, ok := w.Blip("b+root"); ok {
				for _, a := range conv.ReadReplyAnchors(b.Content()) {
					if a.ThreadID == "b+reply" {
						anchored = true
					}
				}
			}
		})
		if !(replyText != "" && anchored) {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if replyText != "inline reply via gateway" {
		t.Errorf("reply blip text = %q, want %q", replyText, "inline reply via gateway")
	}
	if !anchored {
		t.Error("parent blip b+root did not gain an inline reply anchor for b+reply")
	}

	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	<-done
}

// TestGatewayReportsIntentError: a failed intent (here a reply to a nonexistent
// blip) is reported back as an operation.error event carrying the failed intent's
// kind, its echoed id, and the reason — so an LLM harness has an in-band failure
// signal to retry or correct, rather than fire-and-forget.
func TestGatewayReportsIntentError(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	var snap gwEvent
	if err := dec.Decode(&snap); err != nil { // the connect-time snapshot
		t.Fatalf("decode snapshot: %v", err)
	}

	// Reply to a blip that does not exist → the intent fails.
	line := `{"type":"intent","kind":"reply.blip","id":"req-7","blipId":"b+nope","text":"hi"}` + "\n"
	if _, err := intentsW.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}

	var errEv gwEvent
	for i := 0; i < 6; i++ {
		var ev gwEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if ev.Kind == agent.KindOperationError {
			errEv = ev
			break
		}
	}
	if errEv.Kind != agent.KindOperationError {
		t.Fatal("never received an operation.error event for the failed intent")
	}
	if errEv.Intent != "reply.blip" {
		t.Errorf("operation.error intent = %q, want reply.blip", errEv.Intent)
	}
	if errEv.ID != "req-7" {
		t.Errorf("operation.error id = %q, want the echoed req-7", errEv.ID)
	}
	if errEv.Error == "" {
		t.Error("operation.error missing the failure reason")
	}
}

// TestGatewayCoalesceTwoBlipsDistinctEvents proves coalescing is per-blip: editing
// two different blips in one window and then forcing a flush (add.participant)
// yields exactly TWO blip.edited lines, one per blip with its own final text — not
// one merged line and not the raw per-delta count.
func TestGatewayCoalesceTwoBlipsDistinctEvents(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	var snap struct{ Kind string }
	if err := dec.Decode(&snap); err != nil {
		t.Fatal(err)
	}

	// Create blip A and blip B, then burst-edit each. Without coalescing the harness
	// would see one blip.edited per append; with it, exactly one per blip.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "A"}, "b+A")
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "B"}, "b+B")
	for i := 0; i < 3; i++ {
		postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+A", Text: "a"}, "b+ignored")
		postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+B", Text: "b"}, "b+ignored")
	}
	// add.participant flushes every pending edit ahead of it.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentAddParticipant, Participant: "carol@example.com"}, "b+ignored")

	editText := map[string]string{}
	editCount := map[string]int{}
	var sawParticipant bool
	deadline := time.Now().Add(2 * time.Second)
	for !sawParticipant && time.Now().Before(deadline) {
		var ev struct{ Kind, BlipID, Text string }
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		switch ev.Kind {
		case string(agent.BlipEdited):
			editCount[ev.BlipID]++
			editText[ev.BlipID] = ev.Text
		case string(agent.ParticipantAdded):
			sawParticipant = true
		}
	}
	if !sawParticipant {
		t.Fatal("never received participant.added")
	}
	if editCount["b+A"] != 1 || editCount["b+B"] != 1 {
		t.Fatalf("edit counts = A:%d B:%d, want 1 each (per-blip coalesce)", editCount["b+A"], editCount["b+B"])
	}
	if editText["b+A"] != "Aaaa" {
		t.Fatalf("b+A final text = %q, want %q", editText["b+A"], "Aaaa")
	}
	if editText["b+B"] != "Bbbb" {
		t.Fatalf("b+B final text = %q, want %q", editText["b+B"], "Bbbb")
	}

	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Run did not return after cancel")
	}
}

// TestGatewayOperationErrorFlushesPending proves operation.error is an immediate
// kind that drains the buffer ahead of itself: a pending coalesced edit must be
// emitted BEFORE the operation.error for a subsequently-failed intent.
func TestGatewayOperationErrorFlushesPending(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	var snap struct{ Kind string }
	if err := dec.Decode(&snap); err != nil {
		t.Fatal(err)
	}

	// alice edits b+root → the edit is buffered (no immediate event yet, debounce not
	// elapsed). b+root exists from the seed.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+root", Text: " edited"}, "b+ignored")

	// The harness submits an intent that will fail (reply to a nonexistent blip) →
	// operation.error. Before encoding it, Run must flush the pending b+root edit.
	line := `{"type":"intent","kind":"reply.blip","id":"req-9","blipId":"b+nope","text":"hi"}` + "\n"
	if _, err := intentsW.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}

	// Assert ordering: a blip.edited for b+root arrives BEFORE the operation.error.
	sawEditBeforeError := false
	var gotError bool
	deadline := time.Now().Add(2 * time.Second)
	for !gotError && time.Now().Before(deadline) {
		var ev gwEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		switch ev.Kind {
		case string(agent.BlipEdited):
			sawEditBeforeError = true
		case agent.KindOperationError:
			if ev.ID != "req-9" {
				t.Fatalf("operation.error id = %q, want req-9", ev.ID)
			}
			gotError = true
		}
	}
	if !gotError {
		t.Fatal("never received operation.error")
	}
	if !sawEditBeforeError {
		t.Fatal("operation.error arrived before the pending blip.edited was flushed")
	}

	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Run did not return after cancel")
	}
}

// TestGatewayMentionRidesBufferedBurst proves the sharp same-delta path through Run
// with a NON-EMPTY buffer: several edits to blip X are buffered, then ONE delta
// inserts an @mention into X. That delta yields [blip.edited(X, final), mention(X)]
// in Extract order; the mention (immediate) drains the buffer ahead of itself, so
// the harness sees a single coalesced blip.edited carrying the mention text, THEN
// the mention — never a mention about text it has not seen.
func TestGatewayMentionRidesBufferedBurst(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- agent.NewGateway(lc, bot, nil).Run(ctx, eventsW, intentsR) }()

	dec := json.NewDecoder(eventsR)
	var snap struct{ Kind string }
	if err := dec.Decode(&snap); err != nil {
		t.Fatal(err)
	}

	// Create blip X, burst-edit it (all buffered), then a final edit that inserts an
	// @mention — same delta yields blip.edited THEN mention; the mention flushes the
	// buffered burst ahead of itself.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "X"}, "b+X")
	for i := 0; i < 3; i++ {
		postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+X", Text: "z"}, "b+ignored")
	}
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentEditBlip, BlipID: "b+X", Text: " @assistant@example.com"}, "b+ignored")

	editCount := 0
	var sawMention, sawEditBeforeMention bool
	deadline := time.Now().Add(2 * time.Second)
	for !sawMention && time.Now().Before(deadline) {
		var ev gwEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		switch ev.Kind {
		case string(agent.BlipEdited):
			editCount++
			sawEditBeforeMention = true
		case string(agent.Mention):
			if ev.Target != "assistant@example.com" {
				t.Fatalf("mention target = %q", ev.Target)
			}
			sawMention = true
		}
	}
	if !sawMention {
		t.Fatal("never received the mention")
	}
	if !sawEditBeforeMention {
		t.Fatal("mention arrived before any blip.edited for the buffered burst")
	}
	// The burst (b+X created, then 3 'z' appends, then the @mention edit) must collapse
	// to a single coalesced blip.edited (the create is blip.added, immediate).
	if editCount != 1 {
		t.Fatalf("blip.edited count for the burst = %d, want exactly 1 (coalesced ahead of the mention)", editCount)
	}

	cancel()
	_ = eventsR.Close()
	_ = intentsW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway Run did not return after cancel")
	}
}
