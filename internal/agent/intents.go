package agent

import (
	"fmt"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

// IntentKind names a high-level agent action (the gateway's "intents in"). Each
// is translated to OT operations against the live wavelet and submitted through
// the OT client; the harness never constructs operations itself.
type IntentKind string

const (
	// IntentPostBlip appends a new blip (with text) to a thread.
	IntentPostBlip IntentKind = "post.blip"
	// IntentEditBlip appends text to the end of an existing blip's body.
	IntentEditBlip IntentKind = "edit.blip"
	// IntentAddParticipant adds a participant to the wavelet.
	IntentAddParticipant IntentKind = "add.participant"
)

// Intent is one high-level action a harness requests. Fields are read per Kind.
type Intent struct {
	Kind        IntentKind
	ThreadID    string // post.blip: target thread; "" selects the root thread
	BlipID      string // edit.blip: the blip to append to
	Text        string // post.blip / edit.blip
	Participant string // add.participant: the address to add
}

// blipContentOp boxes a content DocOp as an authored wavelet operation against
// blipID (creating the blip when contentOp is an initialization). Mirrors
// conv.SeedConversation's op shape.
func blipContentOp(author id.ParticipantID, ts int64, blipID string, contentOp op.DocOp) waveop.Operation {
	ctx := waveop.Context{Creator: author, Timestamp: ts, VersionIncrement: 1}
	return waveop.WaveletBlipOperation{
		BlipID: blipID,
		BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: contentOp, Method: waveop.ContributorAdd},
	}
}

// Translate turns an intent into the wavelet operations to submit, reading the
// live manifest/blips via `blip` (as supplied by OptimisticClient.SubmitWith, so
// the ops target the version at submit time). author is the acting agent; ts is
// the op timestamp; newBlipID mints ids for created blips. It returns an error
// (and nil ops) when the intent cannot apply — an unknown manifest, a missing
// edit target, a missing thread, an invalid address, or empty required text — so
// the caller can log and submit nothing.
func Translate(
	intent Intent,
	author id.ParticipantID,
	ts int64,
	blip func(string) (op.DocOp, bool),
	newBlipID func() string,
) ([]waveop.Operation, error) {
	switch intent.Kind {
	case IntentPostBlip:
		manifest, ok := blip(conv.ManifestDocumentID)
		if !ok {
			return nil, fmt.Errorf("agent: post.blip: no conversation manifest")
		}
		blipID := newBlipID()
		var manifestOp op.DocOp
		if intent.ThreadID == "" {
			manifestOp = conv.AppendBlipToRootThread(manifest, blipID)
		} else {
			var err error
			if manifestOp, err = conv.AppendBlipToThread(manifest, intent.ThreadID, blipID); err != nil {
				return nil, fmt.Errorf("agent: post.blip: %w", err)
			}
		}
		return []waveop.Operation{
			blipContentOp(author, ts, conv.ManifestDocumentID, manifestOp),
			blipContentOp(author, ts, blipID, conv.BlipContentWithText(intent.Text)),
		}, nil

	case IntentEditBlip:
		if intent.Text == "" {
			return nil, fmt.Errorf("agent: edit.blip: empty text")
		}
		cur, ok := blip(intent.BlipID)
		if !ok {
			return nil, fmt.Errorf("agent: edit.blip: no such blip %q", intent.BlipID)
		}
		n := cur.DocumentLength()
		if n < 1 {
			return nil, fmt.Errorf("agent: edit.blip: blip %q has no body", intent.BlipID)
		}
		// Append the text just before the final </body>, mirroring the client's
		// insert-before-close pattern.
		editOp := op.NewDocOp([]op.Component{
			op.Retain{Count: n - 1},
			op.Characters{Text: intent.Text},
			op.Retain{Count: 1},
		})
		return []waveop.Operation{blipContentOp(author, ts, intent.BlipID, editOp)}, nil

	case IntentAddParticipant:
		p, err := id.NewParticipantID(intent.Participant)
		if err != nil {
			return nil, fmt.Errorf("agent: add.participant: %w", err)
		}
		ctx := waveop.Context{Creator: author, Timestamp: ts, VersionIncrement: 1}
		return []waveop.Operation{waveop.AddParticipant{Ctx: ctx, Participant: p}}, nil

	default:
		return nil, fmt.Errorf("agent: unknown intent kind %q", intent.Kind)
	}
}
