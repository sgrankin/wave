package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/wavelet"
)

// The gateway bridges one (wave, agent) to an EXTERNAL harness with no OT or Go
// knowledge: it streams semantic events out and accepts reply intents in, as
// newline-delimited JSON, over any io pair (a spawned child's stdio, a WebSocket,
// an SSE+POST pair). The harness sends "here is my reply" and the gateway turns it
// into a correct, versioned OT submit. This is the contract to code an external
// agent against; the in-process Runtime is the same loop with a Go Harness instead.

// wireBlip is one blip in a wave.opened snapshot.
type wireBlip struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// wireEvent is an event sent to the harness (type:"event"). Fields are populated
// per Kind; wave.opened carries the connect-time snapshot (Participants + Blips).
type wireEvent struct {
	Type         string     `json:"type"` // always "event"
	Kind         string     `json:"kind"`
	Version      uint64     `json:"version,omitempty"`
	Author       string     `json:"author,omitempty"`
	BlipID       string     `json:"blipId,omitempty"`
	Text         string     `json:"text,omitempty"`
	Participant  string     `json:"participant,omitempty"`
	Target       string     `json:"target,omitempty"`
	Participants []string   `json:"participants,omitempty"` // wave.opened
	Blips        []wireBlip `json:"blips,omitempty"`        // wave.opened
}

// wireIntent is an intent received from the harness (type:"intent").
type wireIntent struct {
	Type        string `json:"type"` // expected "intent"
	Kind        string `json:"kind"`
	ThreadID    string `json:"threadId,omitempty"`
	BlipID      string `json:"blipId,omitempty"`
	Text        string `json:"text,omitempty"`
	Participant string `json:"participant,omitempty"`
}

// KindWaveOpened is the wire-only event kind for the connect-time snapshot (it has
// no Go Event counterpart — it is assembled from the current wavelet state).
const KindWaveOpened = "wave.opened"

// Gateway runs the bridge for one opened LocalClient.
type Gateway struct {
	client *LocalClient
	self   id.ParticipantID
	logger *slog.Logger
}

// NewGateway builds a gateway over an already-opened client. A nil logger uses
// slog.Default().
func NewGateway(client *LocalClient, self id.ParticipantID, logger *slog.Logger) *Gateway {
	return &Gateway{client: client, self: self, logger: logger}
}

func (g *Gateway) log() *slog.Logger {
	if g.logger != nil {
		return g.logger
	}
	return slog.Default()
}

// Run bridges the wave to the harness: it first sends a wave.opened snapshot, then
// streams live events to eventsOut and submits intents read from intentsIn, both as
// newline-delimited JSON. It returns when ctx is cancelled, the wavelet
// subscription closes, or the intent reader reaches EOF — whichever comes first.
func (g *Gateway) Run(ctx context.Context, eventsOut io.Writer, intentsIn io.Reader) error {
	enc := json.NewEncoder(eventsOut)
	if err := enc.Encode(g.snapshot()); err != nil {
		return fmt.Errorf("agent gateway: write snapshot: %w", err)
	}

	// Intents in: each line is one intent; submit it. EOF ends this direction.
	intentErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(intentsIn)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20) // allow long lines, bound them
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var wi wireIntent
			if err := json.Unmarshal(line, &wi); err != nil {
				g.log().Warn("agent gateway: bad intent json", "err", err)
				continue
			}
			if err := g.client.SubmitIntent(intentOf(wi)); err != nil {
				g.log().Warn("agent gateway: submit intent", "kind", wi.Kind, "err", err)
			}
		}
		intentErr <- sc.Err()
	}()

	// Events out: stream applied deltas as events until the subscription closes.
	updates := g.client.Updates()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-intentErr:
			return err // the harness closed its intent stream
		case u, ok := <-updates:
			if !ok {
				return nil // subscription ended
			}
			for _, ev := range g.client.Events(g.self, u) {
				if err := enc.Encode(wireEventFrom(ev)); err != nil {
					return fmt.Errorf("agent gateway: write event: %w", err)
				}
			}
		}
	}
}

// snapshot builds the wave.opened message from the current wavelet state, giving
// the harness its starting context (participants + each blip's text).
func (g *Gateway) snapshot() wireEvent {
	ev := wireEvent{Type: "event", Kind: KindWaveOpened, Participants: []string{}, Blips: []wireBlip{}}
	g.client.read(func(w *wavelet.Data) {
		if w == nil {
			return
		}
		ev.Version = w.HashedVersion().Version()
		for _, p := range w.Participants() {
			ev.Participants = append(ev.Participants, p.Address())
		}
		ids := w.BlipIDs()
		sort.Strings(ids)
		for _, id := range ids {
			if id == conv.ManifestDocumentID {
				continue
			}
			b, ok := w.Blip(id)
			if !ok {
				continue
			}
			text, _ := doc.PlainText(b.Content())
			ev.Blips = append(ev.Blips, wireBlip{ID: id, Author: b.Author().Address(), Text: text})
		}
	})
	return ev
}

// wireEventFrom maps a Go Event to its wire form.
func wireEventFrom(ev Event) wireEvent {
	w := wireEvent{Type: "event", Kind: string(ev.Kind), Version: ev.Version, Author: ev.Author.Address(), BlipID: ev.BlipID, Text: ev.Text, Target: ev.Target}
	if ev.Participant != (id.ParticipantID{}) {
		w.Participant = ev.Participant.Address()
	}
	return w
}

// intentOf maps a wire intent to a Go Intent (kind passed through; validation
// happens in Translate at submit time).
func intentOf(wi wireIntent) Intent {
	return Intent{Kind: IntentKind(wi.Kind), ThreadID: wi.ThreadID, BlipID: wi.BlipID, Text: wi.Text, Participant: wi.Participant}
}
