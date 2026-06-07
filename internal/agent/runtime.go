package agent

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// LocalClient drives one wavelet in-process as an agent participant. It subscribes
// to the container's applied deltas and submits its own with self-suppression (the
// container excludes its subscription from fan-out), so it never observes — and
// thus never reacts to — its own writes. This is the in-process agent path; the
// network path (OptimisticClient) and the out-of-process gateway build on the same
// event/intent layer.
type LocalClient struct {
	container *server.WaveletContainer
	author    id.ParticipantID
	clk       clock.Clock
	newBlipID func() string
	sub       *server.Subscription
	nonce     int
}

// NewLocalClient builds an in-process client for container, acting as author.
// newBlipID mints ids for blips the agent creates (a real generator in production,
// a fixed one in tests).
func NewLocalClient(container *server.WaveletContainer, author id.ParticipantID, clk clock.Clock, newBlipID func() string) *LocalClient {
	return &LocalClient{container: container, author: author, clk: clk, newBlipID: newBlipID}
}

// Open subscribes to the wavelet and returns the deltas applied so far (the join
// history). Subsequent deltas arrive on Updates().
func (lc *LocalClient) Open() []server.WaveletUpdate {
	_, history, sub := lc.container.Open()
	lc.sub = sub
	return history
}

// Updates is the live applied-delta stream (closed when the subscription ends).
func (lc *LocalClient) Updates() <-chan server.WaveletUpdate { return lc.sub.Updates() }

// Close ends the subscription.
func (lc *LocalClient) Close() {
	if lc.sub != nil {
		lc.sub.Close()
	}
}

// read runs fn under the container lock with the live wavelet (nil if uncreated).
func (lc *LocalClient) read(fn func(*wavelet.Data)) { lc.container.Read(fn) }

// SubmitIntent translates intent against the live wavelet and submits the
// resulting delta authored by the agent, suppressed from the agent's own
// subscription. The ops are built against the version captured at read time and
// submitted with that as the target, so the container transforms them to head if a
// concurrent delta landed in between. A translate error (invalid/no-op intent) is
// returned and nothing is submitted.
func (lc *LocalClient) SubmitIntent(intent Intent) error {
	ts := lc.clk.Now().UnixMilli()
	var (
		ops     []waveop.Operation
		target  version.HashedVersion
		created bool
		terr    error
	)
	lc.read(func(w *wavelet.Data) {
		if w == nil {
			return // uncreated: nothing to act on
		}
		created = true
		target = w.HashedVersion()
		reader := func(blipID string) (op.DocOp, bool) {
			b, ok := w.Blip(blipID)
			if !ok {
				return op.DocOp{}, false
			}
			return b.Content(), true
		}
		ops, terr = Translate(intent, lc.author, ts, reader, lc.newBlipID)
	})
	if terr != nil {
		return terr
	}
	if !created || len(ops) == 0 {
		return nil
	}
	lc.nonce++
	nonce := lc.author.Address() + "-" + strconv.Itoa(lc.nonce)
	delta := waveop.NewWaveletDelta(lc.author, target, ops)
	_, err := lc.container.SubmitFrom(delta, nonce, lc.sub)
	return err
}

// Harness is the decision-maker (the LLM, or a rule set). React receives one
// semantic event and returns the intents to act on (nil for none). It must be
// pure-ish: the runtime owns submission, rate-limiting, and loop-safety.
type Harness interface {
	React(ev Event) []Intent
}

// Runtime is the agent loop: it observes applied deltas, derives events, asks the
// harness how to react, and submits the resulting intents. Events authored by the
// agent itself are skipped (defensive — self-suppression already keeps them off
// the subscription).
type Runtime struct {
	client  *LocalClient
	self    id.ParticipantID
	harness Harness
	logger  *slog.Logger
}

// NewRuntime builds a runtime over an opened client. A nil logger uses slog.Default().
func NewRuntime(client *LocalClient, self id.ParticipantID, harness Harness, logger *slog.Logger) *Runtime {
	return &Runtime{client: client, self: self, harness: harness, logger: logger}
}

func (r *Runtime) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// step processes one applied-delta update: extract events, react, submit intents.
func (r *Runtime) step(update server.WaveletUpdate) {
	d := update.Delta
	if d.Author == r.self {
		return // our own delta (shouldn't arrive given self-suppression); never react
	}
	var events []Event
	r.client.read(func(w *wavelet.Data) { events = Extract(d.Author, d.Ops, w) })
	for _, ev := range events {
		for _, intent := range r.harness.React(ev) {
			if err := r.client.SubmitIntent(intent); err != nil {
				r.log().Warn("agent: submit intent", "kind", intent.Kind, "err", err)
			}
		}
	}
}

// Run drives the loop until ctx is cancelled or the subscription closes.
func (r *Runtime) Run(ctx context.Context) {
	updates := r.client.Updates()
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-updates:
			if !ok {
				return
			}
			r.step(u)
		}
	}
}

// EchoHarness is a minimal reference agent: it replies to any @mention of itself
// (by someone else) with an acknowledgement blip in the root thread. It exists to
// prove the event→intent→submit loop end-to-end; a real harness calls an LLM.
type EchoHarness struct{ Self id.ParticipantID }

// React replies to a self-mention.
func (h EchoHarness) React(ev Event) []Intent {
	if ev.Kind != Mention || ev.Author == h.Self || !mentionMatches(ev.Target, h.Self) {
		return nil
	}
	return []Intent{{Kind: IntentPostBlip, Text: "Hi " + ev.Author.Address() + ", I'm here."}}
}

// mentionMatches reports whether an @mention reference targets self — either the
// full address or the bare local part (people often write "@assistant").
func mentionMatches(target string, self id.ParticipantID) bool {
	t := strings.ToLower(target)
	addr := strings.ToLower(self.Address())
	local := addr
	if i := strings.IndexByte(addr, '@'); i >= 0 {
		local = addr[:i]
	}
	return t == addr || t == local
}
