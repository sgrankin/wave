// The conversation's editing surface, shared down the component tree.
//
// <wave-conversation> owns the OptimisticClient; the thread/blip components are
// pure views that read optimistic blip content and *request* edits/replies
// through this controller. Keeping the client behind an interface means the
// recursive components never touch the connection — they just call methods — and
// it makes them trivially testable with a fake controller.

import { CONTRIBUTOR_ADD } from "../wave/types.ts";
import type { Component, DocOp, Operation, Participant } from "../wave/types.ts";

// MANIFEST_ID is the conversation manifest document id (== conv.ManifestDocumentID).
export const MANIFEST_ID = "conversation";

// ROOT_BLIP_ID is the deterministic id of the conversation's first (root) blip,
// created at bootstrap. A fixed id keeps a cold-start race (two clients opening
// the same never-before-seen wavelet at once) from producing two different root
// blips; it does not fully prevent a double-manifest race — see
// <wave-conversation> bootstrap notes.
export const ROOT_BLIP_ID = "b+root";

// ConvController is what the thread/blip components see of the conversation.
export interface ConvController {
  // The authoring participant.
  readonly user: Participant;
  // Optimistic content of a blip, or an empty DocOp if it does not exist yet.
  blipContent(blipId: string): DocOp;
  // Apply+submit a content op (from <blip-view>) against an existing blip.
  editBlip(blipId: string, ops: Component[]): void;
  // Append a new blip to a thread (threadId "" selects the root thread) and
  // create that blip's content, in one delta.
  continueThread(threadId: string): void;
  // Create a new reply thread under a blip (and its first blip), in one delta.
  // When inline and anchorOffset is given, also place a <reply> anchor in the
  // parent blip body at that offset (a line boundary) so the reply is anchored
  // within the text.
  replyToBlip(parentBlipId: string, inline: boolean, anchorOffset?: number): void;
  // The current optimistic participant roster (may change on each re-render).
  participants(): Participant[];
  // Submit an addParticipant op for addr. Throws if addr is not a valid participant address.
  addParticipant(addr: string): void;
  // Upload file as an attachment and insert an inline <image> referencing it into
  // blipId at the given doc offset (a line boundary). Best-effort (no-op on failure).
  attachImage(blipId: string, file: File, offset: number): void;
}

// blipContentOp wraps a content DocOp as a wavelet blip operation authored by
// user. A content op that is an initialization, submitted for a blip id that does
// not exist yet, creates the blip; a mutation op edits an existing blip.
export function blipContentOp(user: Participant, blipId: string, contentOp: DocOp): Operation {
  return {
    kind: "blip",
    blipId,
    op: {
      ctx: { creator: user, timestamp: Date.now(), versionIncrement: 1, hashedVersion: null },
      contentOp,
      method: CONTRIBUTOR_ADD,
    },
  };
}

// addParticipantOp builds an addParticipant wavelet operation authored by user.
export function addParticipantOp(user: Participant, p: Participant): Operation {
  return {
    kind: "addParticipant",
    ctx: { creator: user, timestamp: Date.now(), versionIncrement: 1, hashedVersion: null },
    participant: p,
  };
}
