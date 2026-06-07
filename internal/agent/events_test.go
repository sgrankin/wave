package agent_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/agent"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
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

// stateWithBlip builds a wavelet state at the given version holding one blip with
// the given content (used to back the extractor's text lookup).
func stateWithBlip(t *testing.T, version uint64, author, blipID string, content op.DocOp) *wavelet.Data {
	t.Helper()
	w, err := wavelet.FromState(wavelet.SnapshotState{
		WaveID:       "example.com/w+a",
		WaveletID:    "example.com/conv+root",
		Creator:      author,
		Version:      version,
		Participants: []string{author},
		Blips:        []wavelet.BlipSnapshot{{ID: blipID, Author: author, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func blipOp(author id.ParticipantID, blipID string, content op.DocOp) waveop.WaveletBlipOperation {
	return waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{
		Ctx: waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}, ContentOp: content}}
}

func chars(s string) op.DocOp { return op.NewDocOp([]op.Component{op.Characters{Text: s}}) }

func TestExtractBlipAddedWithMention(t *testing.T) {
	alice := pid(t, "alice@example.com")
	content := chars("hi @bob@example.com") // pure insert ⇒ an initialization ⇒ BlipAdded
	state := stateWithBlip(t, 5, "alice@example.com", "b1", content)

	events := agent.Extract(alice, []waveop.Operation{blipOp(alice, "b1", content)}, 5, state)

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (added + mention): %+v", len(events), events)
	}
	added := events[0]
	if added.Kind != agent.BlipAdded || added.BlipID != "b1" || added.Text != "hi @bob@example.com" ||
		added.Author != alice || added.Version != 5 {
		t.Errorf("added = %+v", added)
	}
	mention := events[1]
	if mention.Kind != agent.Mention || mention.Target != "bob@example.com" || mention.BlipID != "b1" {
		t.Errorf("mention = %+v", mention)
	}
}

func TestExtractBlipEdited(t *testing.T) {
	alice := pid(t, "alice@example.com")
	// Retain then insert ⇒ NOT an initialization ⇒ BlipEdited. The inserted text
	// carries a mention.
	edit := op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: " @carol@example.com"}})
	// The resulting blip text (what state holds) is the composed result.
	state := stateWithBlip(t, 9, "alice@example.com", "b1", chars("hi @carol@example.com"))

	events := agent.Extract(alice, []waveop.Operation{blipOp(alice, "b1", edit)}, 9, state)

	if len(events) != 2 || events[0].Kind != agent.BlipEdited || events[1].Kind != agent.Mention {
		t.Fatalf("got %+v, want edited + mention", events)
	}
	if events[0].Text != "hi @carol@example.com" {
		t.Errorf("edited text = %q", events[0].Text)
	}
	if events[1].Target != "carol@example.com" {
		t.Errorf("mention target = %q", events[1].Target)
	}
}

func TestExtractParticipantAddAndRemove(t *testing.T) {
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	ctx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}
	state := stateWithBlip(t, 3, "alice@example.com", "b1", chars("x"))

	events := agent.Extract(alice, []waveop.Operation{
		waveop.AddParticipant{Ctx: ctx, Participant: bob},
		waveop.RemoveParticipant{Ctx: ctx, Participant: bob},
	}, 3, state)

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	if events[0].Kind != agent.ParticipantAdded || events[0].Participant != bob || events[0].Author != alice {
		t.Errorf("added = %+v", events[0])
	}
	if events[1].Kind != agent.ParticipantRemoved || events[1].Participant != bob {
		t.Errorf("removed = %+v", events[1])
	}
}

func TestExtractIgnoresManifestAndNoMention(t *testing.T) {
	alice := pid(t, "alice@example.com")
	// An edit to the manifest document is structural — no event. And a blip edit
	// with no mention yields exactly one BlipEdited (no Mention).
	plain := op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "more"}})
	state := stateWithBlip(t, 4, "alice@example.com", "b1", chars("more"))

	events := agent.Extract(alice, []waveop.Operation{
		blipOp(alice, conv.ManifestDocumentID, plain), // ignored
		blipOp(alice, "b1", plain),                    // one BlipEdited, no mention
	}, 4, state)

	if len(events) != 1 || events[0].Kind != agent.BlipEdited || events[0].BlipID != "b1" {
		t.Fatalf("got %+v, want a single blip.edited for b1", events)
	}
}

func TestExtractNilState(t *testing.T) {
	if got := agent.Extract(pid(t, "a@example.com"), nil, 0, nil); got != nil {
		t.Errorf("Extract(nil state) = %+v, want nil", got)
	}
}
