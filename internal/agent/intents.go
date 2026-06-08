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
	// IntentReplyBlip replies to a specific blip: it creates a new reply thread
	// under that blip and posts a new blip (with text) into it. With Inline set it
	// also anchors the thread in the parent blip body; otherwise it is a sibling
	// (non-inline) reply thread. This is how an agent answers the blip that
	// mentioned it, rather than appending to the root.
	IntentReplyBlip IntentKind = "reply.blip"
	// IntentEditBlip appends text to the end of an existing blip's body.
	IntentEditBlip IntentKind = "edit.blip"
	// IntentAddParticipant adds a participant to the wavelet.
	IntentAddParticipant IntentKind = "add.participant"
	// IntentSetState sets a key→value entry in the wave's structured state document —
	// the agent's shared key/value memory. The state doc is seeded with the wave, so a
	// set normally mutates the existing doc (creating it only on a legacy wave that
	// predates state — the defensive fallback in Translate).
	IntentSetState IntentKind = "set.state"
	// IntentDeleteState removes a key from the structured state document.
	IntentDeleteState IntentKind = "delete.state"
)

// Intent is one high-level action a harness requests. Fields are read per Kind.
type Intent struct {
	Kind        IntentKind
	ThreadID    string // post.blip: target thread; "" selects the root thread
	BlipID      string // edit.blip / reply.blip: the target blip
	Text        string // post.blip / edit.blip / reply.blip
	Participant string // add.participant: the address to add
	Inline      bool   // reply.blip: anchor the reply thread inline in the parent body
	Key         string // set.state / delete.state: the state key
	Value       string // set.state: the (opaque string) value
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

	case IntentReplyBlip:
		manifest, ok := blip(conv.ManifestDocumentID)
		if !ok {
			return nil, fmt.Errorf("agent: reply.blip: no conversation manifest")
		}
		// The reply thread id IS the new blip's id (the Wave convention: a reply
		// thread is identified by its first blip). Mint it once and use it for the
		// thread, the blip content, and the inline anchor so they all agree.
		blipID := newBlipID()
		manifestOp, err := conv.ReplyToBlip(manifest, intent.BlipID, blipID, intent.Inline)
		if err != nil {
			return nil, fmt.Errorf("agent: reply.blip: %w", err)
		}
		ops := []waveop.Operation{
			blipContentOp(author, ts, conv.ManifestDocumentID, manifestOp),
			blipContentOp(author, ts, blipID, conv.BlipContentWithText(intent.Text)),
		}
		// An inline reply also drops a <reply> anchor in the parent body so the
		// thread renders at a position rather than as a sibling. The agent has no
		// caret, so anchor at the end of the body — just before the final </body>,
		// clamping like the web controller's replyToBlip does.
		if intent.Inline {
			parentBody, ok := blip(intent.BlipID)
			if !ok {
				return nil, fmt.Errorf("agent: reply.blip: no such blip %q", intent.BlipID)
			}
			at := parentBody.DocumentLength() - 1
			if at < 0 {
				at = 0
			}
			anchorOp, err := conv.InsertReplyAnchor(parentBody, blipID, at)
			if err != nil {
				return nil, fmt.Errorf("agent: reply.blip: %w", err)
			}
			ops = append(ops, blipContentOp(author, ts, intent.BlipID, anchorOp))
		}
		return ops, nil

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

	case IntentSetState:
		if intent.Key == "" {
			return nil, fmt.Errorf("agent: set.state: empty key")
		}
		cur, exists := blip(conv.StateDocumentID)
		var contentOp op.DocOp
		if exists {
			var err error
			if contentOp, err = conv.SetStateValue(cur, intent.Key, intent.Value); err != nil {
				return nil, fmt.Errorf("agent: set.state: %w", err)
			}
		} else {
			// Fallback for a wave with no state document — DEFENSIVE only: SeedConversation
			// now seeds an empty <state> in every wave (created once, atomically), so the
			// common path is the `exists` branch above. This branch can therefore only run
			// for a legacy wave seeded before state existed; there, two agents first-writing
			// concurrently could each emit a competing <state> initialization (OT concatenates
			// them into two roots). Acceptable as a bounded migration edge — new waves never
			// reach here. The content op is the full initialization, like a new blip's content.
			mut, err := conv.SetStateValue(conv.EmptyState(), intent.Key, intent.Value)
			if err != nil {
				return nil, fmt.Errorf("agent: set.state: %w", err)
			}
			if contentOp, err = op.Apply(conv.EmptyState(), mut); err != nil {
				return nil, fmt.Errorf("agent: set.state: %w", err)
			}
		}
		return []waveop.Operation{blipContentOp(author, ts, conv.StateDocumentID, contentOp)}, nil

	case IntentDeleteState:
		if intent.Key == "" {
			return nil, fmt.Errorf("agent: delete.state: empty key")
		}
		cur, exists := blip(conv.StateDocumentID)
		if !exists {
			return nil, fmt.Errorf("agent: delete.state: no state document")
		}
		contentOp, err := conv.DeleteStateValue(cur, intent.Key)
		if err != nil {
			return nil, fmt.Errorf("agent: delete.state: %w", err)
		}
		return []waveop.Operation{blipContentOp(author, ts, conv.StateDocumentID, contentOp)}, nil

	default:
		return nil, fmt.Errorf("agent: unknown intent kind %q", intent.Kind)
	}
}
