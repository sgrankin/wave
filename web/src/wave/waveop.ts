// Wavelet-level operational transform: a faithful TypeScript port of the Go
// waveop.Transform (single op pair) and cc.TransformOps (op-list cross-transform).
//
// A wavelet operation either mutates a blip's document (kind "blip", whose
// inner DocOp is transformed by the DocOp transform in ./transform.ts), changes
// the participant set (addParticipant / removeParticipant), or does nothing
// (noOp). The transform reconciles a concurrent client and server operation into
// (clientOp', serverOp') such that applying server then clientOp' converges with
// applying client then serverOp'.
//
// Go references: internal/waveop/transform.go, internal/cc/transform.go.

import { CONTRIBUTOR_ADD, opContext } from "./types.ts";
import type { DocOp, Operation, Participant } from "./types.ts";
import { transform } from "./transform.ts";

// ---------------------------------------------------------------------------
// transformOp — single op-pair transform (ports waveop.Transform).
// ---------------------------------------------------------------------------

/**
 * Reconciles a concurrent client and server wavelet operation into
 * (clientOp', serverOp') such that applying server then clientOp' converges with
 * applying client then serverOp' (ports waveop.Transform). It dispatches on
 * operation kind: same-blip operations recurse into the DocOp transform;
 * different blips and unrelated participant operations are identity; concurrent
 * idempotent participant operations collapse to NoOp; and concurrent add+remove
 * or author-removal throw.
 */
export function transformOp(clientOp: Operation, serverOp: Operation): [Operation, Operation] {
  if (clientOp.kind === "blip" && serverOp.kind === "blip") {
    if (clientOp.blipId === serverOp.blipId) {
      const [ct, st] = transformBlip(clientOp, serverOp);
      return [ct, st];
    }
    // Different blips never conflict.
    return [clientOp, serverOp];
  }

  // At least one side is a participant or no-op operation.
  //
  // The author-removal check is intentionally SERVER-SIDE ONLY: it fires only
  // when the server removes the client op's author, never the reverse (Java
  // parity — checkParticipantRemoval is invoked solely from the server-remove
  // branch). It must also run BEFORE the same-participant collapse below, so
  // that removing one's own creator still throws RemovedAuthorError.
  if (serverOp.kind === "removeParticipant") {
    checkParticipantRemoval(serverOp.participant, clientOp);
    if (clientOp.kind === "removeParticipant") {
      if (clientOp.participant === serverOp.participant) {
        return [{ kind: "noOp", ctx: clientOp.ctx }, { kind: "noOp", ctx: serverOp.ctx }];
      }
    } else if (clientOp.kind === "addParticipant") {
      checkParticipantRemovalAndAddition(serverOp.participant, clientOp.participant);
    }
  } else if (serverOp.kind === "addParticipant") {
    if (clientOp.kind === "addParticipant") {
      if (clientOp.participant === serverOp.participant) {
        return [{ kind: "noOp", ctx: clientOp.ctx }, { kind: "noOp", ctx: serverOp.ctx }];
      }
    } else if (clientOp.kind === "removeParticipant") {
      checkParticipantRemovalAndAddition(clientOp.participant, serverOp.participant);
    }
  }
  // Identity transform by default.
  return [clientOp, serverOp];
}

// transformBlip transforms two blip operations on the same blip. Both carry an
// inner DocOp (content op), so the result feeds them to the DocOp transform.
//
// Java rewraps via the two-arg BlipContentOperation constructor, which resets
// the contributor-update method to ADD (discarding the original). We match that
// — the original is the behavioral source of truth — rather than preserving the
// client/server method.
function transformBlip(
  clientOp: Extract<Operation, { kind: "blip" }>,
  serverOp: Extract<Operation, { kind: "blip" }>,
): [Operation, Operation] {
  let ct: DocOp;
  let st: DocOp;
  try {
    [ct, st] = transform(clientOp.op.contentOp, serverOp.op.contentOp);
  } catch (err) {
    throw new Error("waveop: blip content transform failed: " + errMsg(err));
  }
  return [
    {
      kind: "blip",
      blipId: clientOp.blipId,
      op: { ctx: clientOp.op.ctx, contentOp: ct, method: CONTRIBUTOR_ADD },
    },
    {
      kind: "blip",
      blipId: serverOp.blipId,
      op: { ctx: serverOp.op.ctx, contentOp: st, method: CONTRIBUTOR_ADD },
    },
  ];
}

// checkParticipantRemoval throws if the server removes the participant who
// authored the client operation (ports RemovedAuthorException).
function checkParticipantRemoval(removed: Participant, other: Operation): void {
  if (removed === opContext(other).creator) {
    throw new Error("waveop: operation author concurrently removed: " + removed);
  }
}

// checkParticipantRemovalAndAddition throws if the same participant is
// concurrently added and removed.
function checkParticipantRemovalAndAddition(removed: Participant, added: Participant): void {
  if (removed === added) {
    throw new Error("waveop: concurrent add and remove of participant " + removed);
  }
}

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// ---------------------------------------------------------------------------
// transformOps — op-list cross-transform (ports cc.TransformOps /
// transformOpLists).
// ---------------------------------------------------------------------------

/**
 * Transforms a client op list against a concurrent server op list, returning
 * both transformed lists (the DeltaPair transform; ports cc.TransformOps).
 *
 * Each client op is transformed past every server op left-to-right, and
 * symmetrically each server op past every client op. Throws on transform
 * failure (where the Go returns a non-nil error).
 */
export function transformOps(client: Operation[], server: Operation[]): [Operation[], Operation[]] {
  let serverOps = server.slice();
  const clientPrime: Operation[] = [];
  for (const c of client) {
    let ci = c;
    const next: Operation[] = new Array(serverOps.length);
    for (let j = 0; j < serverOps.length; j++) {
      const [cPrime, sPrime] = transformOp(ci, serverOps[j]!);
      ci = cPrime;
      next[j] = sPrime;
    }
    clientPrime.push(ci);
    serverOps = next;
  }
  return [clientPrime, serverOps];
}
