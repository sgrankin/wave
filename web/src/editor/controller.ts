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

// RemoteCaret is one peer's caret/selection within a blip, ready to render: doc-item
// offsets (text runes + 2 per inline widget — the same basis as domToOffset/offsetToDom;
// all peers must agree) plus presentation (the peer's avatar color + display name).
// anchor==focus (or anchor<0) is a collapsed caret; anchor!=focus is a selection range.
export interface RemoteCaret {
  participant: string;
  anchor: number;
  focus: number;
  color: string;
  name: string;
}

// ConvController is what the thread/blip components see of the conversation.
export interface ConvController {
  // The authoring participant.
  readonly user: Participant;
  // Optimistic content of a blip, or an empty DocOp if it does not exist yet.
  blipContent(blipId: string): DocOp;
  // Apply+submit a content op (from <blip-view>) against an existing blip.
  editBlip(blipId: string, ops: Component[]): void;
  // Undo / redo the most recent local edit to blipId (Cmd-Z / Cmd-Shift-Z). The
  // undo op is transform-corrected against intervening remote edits. Optional — a
  // fake/headless controller may omit it.
  undo?(blipId: string): void;
  redo?(blipId: string): void;
  // Logically delete a blip (mark it deleted + clear its content), keeping it as a
  // tombstone parent for any reply threads. Optional — a fake controller may omit it.
  deleteBlip?(blipId: string): void;
  // Append a new blip to a thread (threadId "" selects the root thread) and
  // create that blip's content, in one delta.
  continueThread(threadId: string): void;
  // Create a new reply thread under a blip (and its first blip), in one delta.
  // When inline and anchorOffset is given, also place a <reply> anchor in the
  // parent blip body at that offset (the exact caret offset) so the reply is
  // anchored at the selection within the text. Returns the new thread's id (== its first blip's id) so the
  // caller can open it (e.g. the inline-comment sheet auto-opens on creation).
  replyToBlip(parentBlipId: string, inline: boolean, anchorOffset?: number): string;
  // The current optimistic participant roster (may change on each re-render).
  participants(): Participant[];
  // Submit an addParticipant op for addr. Throws if addr is not a valid participant address.
  addParticipant(addr: string): void;
  // Submit a removeParticipant op for addr (removing yourself = leaving the wave).
  removeParticipant(addr: string): void;
  // Upload file as an attachment and insert an inline <image> referencing it into
  // blipId at the given doc offset (the caret offset). Best-effort (no-op on failure).
  attachImage(blipId: string, file: File, offset: number): void;
  // Publish our caret/selection (doc-item offsets) in blipId to the presence channel so
  // peers can render it. Optional — a fake/headless controller may omit it.
  setCaret?(blipId: string, anchor: number, focus: number): void;
  // The peers currently caretted in blipId, ready to render as remote carets.
  // Optional — returns [] (or is absent) when presence is unavailable.
  remoteCaretsFor?(blipId: string): RemoteCaret[];
  // The participant who authored a blip (its creator), or undefined if unknown.
  // Optional — a fake/headless controller may omit it.
  blipAuthor?(blipId: string): Participant | undefined;
  // Every participant who has authored an op on a blip (author first). Optional.
  blipContributors?(blipId: string): Participant[];
  // Whether a blip is unread for the signed-in participant: it was last modified by
  // a REMOTE edit at a version past the participant's stored read version for it.
  // Drives the unread marker. Optional — a fake/headless controller may omit it
  // (then nothing is ever unread).
  isBlipUnread?(blipId: string): boolean;
  // Mark a blip read up to its current last-modified version (clearing its unread
  // marker), called when the participant has viewed it (a dwell after it scrolls
  // into view). A no-op if the blip is already read. Optional.
  markBlipViewed?(blipId: string): void;
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

// removeParticipantOp builds a removeParticipant wavelet operation authored by user.
export function removeParticipantOp(user: Participant, p: Participant): Operation {
  return {
    kind: "removeParticipant",
    ctx: { creator: user, timestamp: Date.now(), versionIncrement: 1, hashedVersion: null },
    participant: p,
  };
}
