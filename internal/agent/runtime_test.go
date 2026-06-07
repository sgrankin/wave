package agent_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/agent"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

func newContainer(t *testing.T) (*server.WaveletContainer, id.WaveletName) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	w, _ := id.NewWaveID("example.com", "w+agent")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(w, wl)
	access, err := store.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	c, err := server.Load(name, access, clock.NewFixed(time.UnixMilli(1000)))
	if err != nil {
		t.Fatal(err)
	}
	return c, name
}

// postAs builds and submits a delta as author (a normal submit, fanned out to all
// subscribers) by translating intent against the live wavelet.
func postAs(t *testing.T, c *server.WaveletContainer, author id.ParticipantID, intent agent.Intent, blipID string) {
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
		ops, terr = agent.Translate(intent, author, 2000, reader, func() string { return blipID })
	})
	if terr != nil {
		t.Fatalf("translate as %s: %v", author, terr)
	}
	if _, err := c.Submit(waveop.NewWaveletDelta(author, target, ops)); err != nil {
		t.Fatalf("submit as %s: %v", author, err)
	}
}

// TestEchoAgentRepliesToMention drives the whole agent loop against a real
// container: alice mentions the agent, the runtime extracts the mention, the echo
// harness posts a reply, and the reply lands in the wavelet — without the agent
// observing its own delta (self-suppression).
func TestEchoAgentRepliesToMention(t *testing.T) {
	c, _ := newContainer(t)
	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")

	// alice creates the wave and adds the agent.
	seedOps, err := conv.SeedConversation(alice, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}
	addCtx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}
	if _, err := c.Submit(waveop.NewWaveletDelta(alice, c.Version(), []waveop.Operation{
		waveop.AddParticipant{Ctx: addCtx, Participant: bot},
	})); err != nil {
		t.Fatal(err)
	}

	// The agent connects (subscribes) AFTER the setup, so its live stream begins
	// with alice's next delta.
	lc := agent.NewLocalClient(c, bot, clock.NewFixed(time.UnixMilli(3000)), func() string { return "b+echo" })
	lc.Open()
	defer lc.Close()
	rt := agent.NewRuntime(lc, bot, agent.EchoHarness{Self: bot}, nil)

	// alice posts a blip mentioning the agent.
	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "ping @assistant@example.com"}, "b+alice")

	// The agent's subscription receives alice's delta; process it.
	select {
	case u := <-lc.Updates():
		rt.StepForTest(u)
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not receive alice's delta")
	}

	// The echo reply landed in the wavelet.
	var echoText string
	c.Read(func(w *wavelet.Data) {
		if b, ok := w.Blip("b+echo"); ok {
			echoText, _ = doc.PlainText(b.Content())
		}
	})
	if echoText != "Hi alice@example.com, I'm here." {
		t.Fatalf("echo blip text = %q, want the acknowledgement", echoText)
	}

	// Self-suppression: the agent's own reply must NOT have arrived on its stream —
	// and the stream must still be open (a closed channel would also yield ok=false,
	// which must not be mistaken for "suppressed").
	select {
	case u, ok := <-lc.Updates():
		if ok {
			t.Fatalf("agent observed its own delta (self-suppression failed): %+v", u.Delta.Author)
		}
		t.Fatal("agent subscription unexpectedly closed")
	default:
		// good: nothing waiting, channel still open
	}
}

// TestEchoAgentIgnoresNonMentions confirms the agent does not reply to a blip that
// does not mention it.
func TestEchoAgentIgnoresNonMentions(t *testing.T) {
	c, _ := newContainer(t)
	alice := pid(t, "alice@example.com")
	bot := pid(t, "assistant@example.com")
	seedOps, _ := conv.SeedConversation(alice, 1000)
	if _, err := c.SeedIfEmpty(alice, seedOps); err != nil {
		t.Fatal(err)
	}

	lc := agent.NewLocalClient(c, bot, clock.NewFixed(time.UnixMilli(3000)), func() string { return "b+echo" })
	lc.Open()
	defer lc.Close()
	rt := agent.NewRuntime(lc, bot, agent.EchoHarness{Self: bot}, nil)

	postAs(t, c, alice, agent.Intent{Kind: agent.IntentPostBlip, Text: "just chatting, no mention"}, "b+alice")
	select {
	case u := <-lc.Updates():
		rt.StepForTest(u)
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not receive alice's delta")
	}

	var exists bool
	c.Read(func(w *wavelet.Data) { _, exists = w.Blip("b+echo") })
	if exists {
		t.Fatal("agent replied to a non-mention")
	}
}
