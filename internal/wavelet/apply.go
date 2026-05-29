package wavelet

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// ApplyDelta applies a delta to the wavelet: it applies each operation in order
// (mutating documents and the participant set), advances the version by one per
// operation, advances the hashed version over the delta's serialized bytes, and
// updates the last-modified time (spec §8.2).
//
// serializedDelta MUST be the canonical serialized ProtocolAppliedWaveletDelta
// (spec invariant #2): the history hash is computed over exactly those bytes.
// Serialization is a later phase; until then callers pass the bytes they will
// persist, and the chain is self-consistent.
//
// ApplyDelta does not verify the delta's target version against the wavelet's
// current version — that check belongs to the concurrency-control layer.
func (w *Data) ApplyDelta(delta waveop.WaveletDelta, serializedDelta []byte) error {
	base := w.hashedVersion.Version()
	running := base
	for i := 0; i < delta.Len(); i++ {
		o := delta.Op(i)
		// Each operation advances the wavelet by exactly one version (spec §8.2:
		// a delta of N operations advances the version by N). Context.VersionIncrement
		// is wire metadata and is NOT used for version arithmetic — doing so would
		// diverge from the op-count basis used by cc, the history, and storage.
		running++
		if err := w.applyOp(o, running); err != nil {
			return err
		}
		if o.Context().HasTimestamp() {
			w.lastModifiedTime = o.Context().Timestamp
		}
	}
	w.hashedVersion = version.Apply(w.hashedVersion, serializedDelta, running-base)
	return nil
}

// applyOp applies a single operation, mutating wavelet state. atVersion is the
// wavelet version reached after this operation (used as a blip's last-modified
// version).
func (w *Data) applyOp(o waveop.Operation, atVersion uint64) error {
	switch v := o.(type) {
	case waveop.WaveletBlipOperation:
		return w.applyBlipOp(v, atVersion)
	case waveop.AddParticipant:
		// Java AddParticipant.doApply throws on a duplicate; match it (defensive
		// against malformed deltas — concurrent duplicate adds are collapsed to
		// NoOp by the wavelet transform before they reach apply).
		if indexOf(w.participants, v.Participant) >= 0 {
			return fmt.Errorf("wavelet: attempt to add duplicate participant %s", v.Participant.Address())
		}
		w.participants = append(w.participants, v.Participant)
	case waveop.RemoveParticipant:
		i := indexOf(w.participants, v.Participant)
		if i < 0 {
			return fmt.Errorf("wavelet: attempt to remove non-existent participant %s", v.Participant.Address())
		}
		w.participants = append(w.participants[:i], w.participants[i+1:]...)
	case waveop.NoOp:
		// no state change
	default:
		return fmt.Errorf("wavelet: unknown operation type %T", o)
	}
	return nil
}

// applyBlipOp applies a blip operation, implicitly creating the target blip (an
// empty document authored by the op's creator) if it does not yet exist
// (WaveletBlipOperation.getTargetBlip).
func (w *Data) applyBlipOp(wbo waveop.WaveletBlipOperation, atVersion uint64) error {
	ctx := wbo.Context()
	blip := w.blips[wbo.BlipID]
	if blip == nil {
		blip = &BlipData{
			id:                  wbo.BlipID,
			author:              ctx.Creator,
			contributors:        []id.ParticipantID{ctx.Creator},
			content:             op.EmptyDoc(),
			lastModifiedTime:    ctx.Timestamp,
			lastModifiedVersion: atVersion,
		}
		w.blips[wbo.BlipID] = blip
	}

	bc, ok := wbo.BlipOp.(waveop.BlipContentOperation)
	if !ok {
		return fmt.Errorf("wavelet: unsupported blip operation %T", wbo.BlipOp)
	}

	newContent, err := op.Compose(blip.content, bc.ContentOp)
	if err != nil {
		return fmt.Errorf("wavelet: applying blip op to %q: %w", wbo.BlipID, err)
	}
	// A document composed with a well-formed op stays insertion-only; assert it,
	// so a latent bug surfaces as an error rather than silent storage corruption.
	if !newContent.IsInitialization() {
		return fmt.Errorf("wavelet: blip op on %q produced non-document content", wbo.BlipID)
	}
	blip.content = newContent

	// Metadata (contributors, last-modified) is updated only for "worthy" edits to
	// "worthy" documents: trivial edits (inline-reply anchors, presence/spell/
	// link/translation/language annotations) and system documents do not count
	// (BlipOperation.update gated on updatesBlipMetadata).
	if bc.UpdatesBlipMetadata(wbo.BlipID) {
		switch bc.Method {
		case waveop.ContributorAdd:
			blip.contributors = addToSet(blip.contributors, ctx.Creator)
		case waveop.ContributorRemove:
			blip.contributors = removeFromSet(blip.contributors, ctx.Creator)
		case waveop.ContributorNone:
			// leave contributors unchanged
		}
		blip.lastModifiedVersion = atVersion
		if ctx.HasTimestamp() {
			blip.lastModifiedTime = ctx.Timestamp
		}
	}
	return nil
}
