package conv

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

// RootBlipID is the deterministic id of a seeded conversation's first (root)
// blip. A fixed id (rather than a random one) means a second opener racing the
// creation would target the same root blip, not a divergent one. It mirrors the
// TypeScript client's ROOT_BLIP_ID constant.
const RootBlipID = "b+root"

// SeedConversation returns the operations that initialise a brand-new wavelet as
// a one-blip conversation, authored by creator at timestamp ts (ms since epoch):
//
//  1. AddParticipant(creator) — the creator becomes the first participant.
//  2. the conversation manifest document (id "conversation") holding a single
//     root blip: <conversation><blip id="b+root"/></conversation>.
//  3. the root blip's initial content: <body><line/></body>.
//  4. an empty structured-state document (id "state"): <state></state>. Seeding it
//     here — created exactly once, atomically, at wavelet creation — is what makes
//     set.state safe under concurrent agents: every later write is an insert/update
//     against this single root, so two agents first-writing state converge to sibling
//     entries rather than racing to emit two competing <state> initializations.
//
// These are exactly the operations the browser client used to submit on cold
// start (see web/src/editor/wave-conversation.ts maybeBootstrap); authoring them
// server-side at first Open makes wavelet creation atomic (no two-client
// double-manifest race) and removes the client bootstrap. The caller wraps them
// in a delta targeting version zero and applies it (see
// server.WaveletContainer.SeedIfEmpty).
func SeedConversation(creator id.ParticipantID, ts int64) ([]waveop.Operation, error) {
	manifest, err := op.Apply(EmptyManifest(), AppendBlipToRootThread(EmptyManifest(), RootBlipID))
	if err != nil {
		return nil, fmt.Errorf("conv: build seed manifest: %w", err)
	}
	ctx := waveop.Context{Creator: creator, Timestamp: ts, VersionIncrement: 1}
	return []waveop.Operation{
		waveop.AddParticipant{Ctx: ctx, Participant: creator},
		waveop.WaveletBlipOperation{
			BlipID: ManifestDocumentID,
			BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: manifest, Method: waveop.ContributorAdd},
		},
		waveop.WaveletBlipOperation{
			BlipID: RootBlipID,
			BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: InitialBlipContent(), Method: waveop.ContributorAdd},
		},
		waveop.WaveletBlipOperation{
			BlipID: StateDocumentID,
			BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: EmptyState(), Method: waveop.ContributorAdd},
		},
	}, nil
}
