// Package agent is the agent channel: the bridge that lets an LLM agent
// participate in a wave as an ordinary participant over the OT client. This file
// is the semantic event layer — turning one applied delta into typed conversation
// events a harness can reason about (a new blip, an edit, a participant change, an
// @mention), so the harness never has to walk raw operations.
//
// See docs/architecture/06-agent-channel-and-playback.md (Part A). Events are
// agent-local and derived: the op log is canonical, so a wrong extraction can never
// corrupt a wave.
package agent

import (
	"regexp"
	"strings"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// EventKind names a semantic conversation event.
type EventKind string

const (
	// BlipAdded: a blip was created (its content op is an initialization).
	BlipAdded EventKind = "blip.added"
	// BlipEdited: an existing blip's content changed.
	BlipEdited EventKind = "blip.edited"
	// ParticipantAdded: a participant joined the wavelet.
	ParticipantAdded EventKind = "participant.added"
	// ParticipantRemoved: a participant left/was removed.
	ParticipantRemoved EventKind = "participant.removed"
	// Mention: an @address token appeared in newly inserted blip text.
	Mention EventKind = "mention"
)

// Event is one semantic conversation event derived from a delta. Fields are
// populated per Kind (see each constant); the rest are zero.
type Event struct {
	Kind    EventKind
	Version uint64           // the wavelet version after the delta that produced this
	Author  id.ParticipantID // who caused the change (the delta author)

	BlipID string // blip.added / blip.edited / mention
	Text   string // blip.added / blip.edited: the blip's full plain text after the change

	Participant id.ParticipantID // participant.added / participant.removed
	Target      string           // mention: the mentioned reference (the text after '@')
}

// mentionRE matches an @-mention: a local part, optionally @domain. Mirrors the
// browser client's inline decorator so detection is consistent across both.
var mentionRE = regexp.MustCompile(`@[A-Za-z0-9._%+\-]+(?:@[A-Za-z0-9.\-]+)?`)

// Extract derives semantic events from one applied delta: its author, its
// operations, and the resulting wavelet state (after the delta applied). It is
// stateless — blip creation versus edit is distinguished by whether the content op
// is an initialization, so no prior state is threaded through. Edits to the
// conversation manifest document are structural and emit no events (a blip's
// arrival is observed from its own content op). state == nil yields no events.
func Extract(author id.ParticipantID, ops []waveop.Operation, state *wavelet.Data) []Event {
	if state == nil {
		return nil
	}
	version := state.Version()
	var events []Event
	for _, o := range ops {
		switch wo := o.(type) {
		case waveop.AddParticipant:
			events = append(events, Event{Kind: ParticipantAdded, Version: version, Author: author, Participant: wo.Participant})
		case waveop.RemoveParticipant:
			events = append(events, Event{Kind: ParticipantRemoved, Version: version, Author: author, Participant: wo.Participant})
		case waveop.WaveletBlipOperation:
			if wo.BlipID == conv.ManifestDocumentID {
				continue // structural; blip arrival is seen via the content blip's own op
			}
			bc, ok := wo.BlipOp.(waveop.BlipContentOperation)
			if !ok {
				continue
			}
			text := ""
			if b, ok := state.Blip(wo.BlipID); ok {
				if t, err := doc.PlainText(b.Content()); err == nil {
					text = t
				}
			}
			kind := BlipEdited
			if bc.ContentOp.IsInitialization() {
				kind = BlipAdded
			}
			events = append(events, Event{Kind: kind, Version: version, Author: author, BlipID: wo.BlipID, Text: text})
			// Mentions are taken from the text inserted by THIS op (not the whole
			// blip), so an agent is notified once when addressed, not on every later edit.
			for _, ref := range mentionsIn(bc.ContentOp) {
				events = append(events, Event{Kind: Mention, Version: version, Author: author, BlipID: wo.BlipID, Target: ref})
			}
		}
	}
	return events
}

// mentionsIn returns the @-mention references (text after '@') inserted by a
// content op, in order, scanning only its inserted characters.
func mentionsIn(d op.DocOp) []string {
	var inserted strings.Builder
	for _, c := range d.Components() {
		if ch, ok := c.(op.Characters); ok {
			inserted.WriteString(ch.Text)
		}
	}
	matches := mentionRE.FindAllString(inserted.String(), -1)
	refs := make([]string, 0, len(matches))
	for _, m := range matches {
		refs = append(refs, strings.TrimPrefix(m, "@"))
	}
	return refs
}
