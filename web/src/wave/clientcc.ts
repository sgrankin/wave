// Client-side optimistic concurrency control for one wavelet: the Jupiter-style
// state machine a collaborative client runs to edit locally without waiting for
// the server, while staying convergent with everyone else.
//
// It is PURE: no timers, no I/O, no clock. A transport adapter drives it with
// wire events — server deltas, submit acks — and sends the deltas it emits.
// Keeping the OT bookkeeping side-effect-free makes it deterministically
// testable (drive it against a simulated server and assert convergence) and lets
// the adapter own timing/reconnection separately.
//
// # Model
//
// The client holds at most one in-flight delta (submitted, awaiting ack) plus a
// queue of locally-authored ops not yet sent. Local edits apply to an optimistic
// replica immediately. Incoming server deltas (from OTHER participants — the
// server suppresses this connection's own deltas) are transformed past the
// unacknowledged ops before being applied, and the unacknowledged ops are
// transformed past them in step, so they always apply on top of the latest
// confirmed server version.
//
// # Suppression and the ack/delta race
//
// Because the server suppresses the client's own delta, the client never receives
// it back; the submit ack is the sole signal of its outcome. The ack can arrive
// before the server deltas that preceded the in-flight delta (they race on the
// connection). The client tolerates this: it settles the in-flight delta only
// once it is both acked AND the client has received every delta the server
// applied before it (recv has reached the in-flight delta's applied-at version).
// A post-in-flight delta arriving first reveals the in-flight delta's slot as a
// version gap and settles it without needing the ack.
//
// The hash chain is the server's concern; this client tracks only the confirmed
// server HashedVersion (for targeting submits and, later, resync).
//
// The core is single-threaded; the caller serializes calls. It also assumes the
// transport delivers a wavelet's server deltas in version order (the gap/settle
// logic relies on it); the adapter must preserve that.
//
// Port of internal/clientcc/clientcc.go.

import { compose } from "./compose.ts";
import { UndoManager } from "./undo.ts";
import { transformOps } from "./waveop.ts";
import { CONTRIBUTOR_ADD, DocOp, HashedVersion, newWaveletDelta } from "./types.ts";
import type { Operation, Participant, WaveletDelta, WaveletName } from "./types.ts";

// Outgoing is a delta the state machine wants submitted, with the per-submission
// nonce to tag it with on the wire (so the client can later recognize its own
// delta in a resync tail).
export interface Outgoing {
  delta: WaveletDelta;
  nonce: string;
}

// pending is the single in-flight delta. ops is kept transformed to apply on top
// of recv. versionSpan is the op count of the delta as sent (= len at send); the
// OT transform is one-to-one on ops, so this is also the count the server applies
// (except a deduped resend, which applies zero — a resync-era case). It locates
// the delta's slot in the version stream when a post-in-flight delta reveals it
// as a gap before the ack arrives. Once acked, ackedApplied carries the server's
// authoritative applied op count, which drives settling. nonce tags the
// submission so it can be recognized in a resync tail.
interface Pending {
  ops: Operation[];
  sentTarget: HashedVersion;
  versionSpan: number;
  nonce: string;
  acked: boolean;
  ackedVer: HashedVersion;
  ackedApplied: number;
}

// CC is the client concurrency-control state machine for one wavelet.
export class CC {
  private readonly name: WaveletName;
  private readonly author: Participant;
  private readonly sessionId: string; // per-session unique nonce prefix
  private nonceSeq = 0; // per-session nonce counter

  // recv is the latest confirmed server version: the version on top of which the
  // unacknowledged ops (inflight, then queue) currently apply. Advanced by
  // received deltas and by settling the in-flight delta.
  private recv: HashedVersion;

  private inflight: Pending | null = null; // the one delta awaiting ack, or null
  private queue: Operation[] = []; // local ops not yet sent, kept transformed onto recv (after inflight)

  // optimistic replica: blip contents (kept as composed DocOps) and the participant
  // set. Enough to read back the document.
  private blips = new Map<string, DocOp>();
  private parts = new Set<Participant>();
  // Per-blip authorship, derived from the creator of each blip op applied: author is
  // the creator of the FIRST op for a blip (its creation); contributors accumulate
  // every creator. Recoverable because opens replay history in order (the creation op
  // is always seen first); a snapshot open would not carry it (reset in loadSnapshot).
  private blipMeta = new Map<string, { author: Participant; contributors: Set<Participant> }>();

  // Per-blip undo managers (local edits are undoable; remote edits are fed as
  // non-undoable so undo transforms past them). Fed the SAME applied op stream as
  // the optimistic replica, in the same order, so they stay in lockstep with the
  // blip contents. Cleared on a snapshot/resync reset (the history is then stale).
  private undoMgrs = new Map<string, UndoManager>();

  // New creates a client state machine for wavelet name authored by author,
  // starting from the given confirmed version (e.g. version zero for a fresh
  // open; the snapshot/history version for a resync). sessionId is a per-session
  // unique token used to prefix submission nonces so a client recognizes only its
  // own deltas (distinct even across two sessions of the same participant). The
  // caller then feeds any initial history via onServerDelta.
  constructor(name: WaveletName, author: Participant, start: HashedVersion, sessionId: string) {
    this.name = name;
    this.author = author;
    this.sessionId = sessionId;
    this.recv = start;
  }

  // loadSnapshot seeds the optimistic replica from a current-state snapshot — the
  // blip contents by id, the participant set, and the version it represents — for
  // a snapshot-based open or a resync reset. It must be called on a fresh CC,
  // before any edit or server delta (it replaces all state).
  loadSnapshot(at: HashedVersion, blips: Map<string, DocOp>, parts: Participant[]): void {
    this.recv = at;
    this.inflight = null;
    this.queue = [];
    this.blips = new Map(blips);
    this.parts = new Set(parts);
    this.blipMeta = new Map(); // a snapshot carries content, not per-blip authorship
    this.undoMgrs.clear(); // undo history does not survive a reset (it targets old state)
  }

  // serverVersion returns the latest confirmed server version (what a fresh idle
  // submit targets, and the resync point).
  serverVersion(): HashedVersion {
    return this.recv;
  }

  // blipIds returns the ids of all blips in the optimistic replica, unsorted.
  blipIds(): string[] {
    return [...this.blips.keys()];
  }

  // blip returns the optimistic content of a blip, or undefined if absent.
  blip(blipId: string): DocOp | undefined {
    return this.blips.get(blipId);
  }

  // blipAuthor returns the participant who created a blip (the creator of its first
  // op), or undefined if unknown (absent blip, or a snapshot open that carried no
  // authorship).
  blipAuthor(blipId: string): Participant | undefined {
    return this.blipMeta.get(blipId)?.author;
  }

  // blipContributors returns every participant who has authored an op on a blip, in
  // first-seen order (the author first), or [] if unknown.
  blipContributors(blipId: string): Participant[] {
    const m = this.blipMeta.get(blipId);
    return m === undefined ? [] : [...m.contributors];
  }

  // inflightActive reports whether a delta is currently in flight (submitted,
  // awaiting ack). Read-only introspection for debug tooling.
  inflightActive(): boolean {
    return this.inflight !== null;
  }

  // queueLength is the number of locally-authored ops queued but not yet sent
  // (waiting for the in-flight slot). Read-only introspection for debug tooling.
  queueLength(): number {
    return this.queue.length;
  }

  // hasParticipant reports whether p is in the optimistic participant set.
  hasParticipant(p: Participant): boolean {
    return this.parts.has(p);
  }

  // participants returns the optimistic participant set as an array (unordered).
  participants(): Participant[] {
    return [...this.parts];
  }

  // edit applies locally-authored ops to the optimistic replica and queues them
  // for submission, returning a delta to send now or null if one is already in
  // flight (the ops wait in the queue). ops must be authored against the current
  // optimistic document and carry a per-op versionIncrement (normally 1).
  edit(ops: Operation[]): Outgoing | null {
    this.apply(ops);
    // Record each blip content op as undoable, one undo unit per edit() call (per
    // input event). A later remote op on the same blip is fed as non-undoable in
    // onServerDelta, so undo transforms past it.
    for (const o of ops) {
      if (o.kind === "blip") {
        const m = this.undoFor(o.blipId);
        m.undoableOp(o.op.contentOp);
        m.checkpoint();
      }
    }
    this.queue.push(...ops);
    return this.trySend();
  }

  // undoFor lazily creates the undo manager for a blip.
  private undoFor(blipId: string): UndoManager {
    let m = this.undoMgrs.get(blipId);
    if (m === undefined) {
      m = new UndoManager();
      this.undoMgrs.set(blipId, m);
    }
    return m;
  }

  // onServerDelta incorporates a delta the server applied. ops are its
  // operations, resulting the version reached after it, and nonce the submitting
  // client's tag (empty for server-internal or pre-nonce deltas). If the nonce
  // matches this client's own in-flight delta — which happens in a resync tail,
  // where the delta is no longer suppressed — it settles the in-flight delta
  // without re-applying it (already applied optimistically). Otherwise the delta
  // is transformed past the unacknowledged ops and applied to the replica, and
  // the unacknowledged ops are transformed past it. May settle the in-flight
  // delta and return a newly-sendable delta (else null).
  onServerDelta(ops: Operation[], resulting: HashedVersion, nonce: string): Outgoing | null {
    const span = versionSpan(ops);
    const appliedAt = resulting.version - span;

    // Our own delta, recognized by nonce in a resync tail (not suppressed there).
    // It is confirmed at `resulting`; settle without re-applying. All deltas the
    // server applied before it have already been fed (recv reached its
    // applied-at).
    if (this.inflight !== null && nonce !== "" && nonce === this.inflight.nonce) {
      if (appliedAt !== this.recv.version) {
        throw new Error(
          `clientcc: own delta in resync tail applies at ${appliedAt}, client at ${this.recv.version}`,
        );
      }
      this.recv = resulting;
      this.inflight = null;
      return this.trySend();
    }

    // A gap (the delta applies beyond recv) is our own suppressed in-flight
    // delta: everything up to here was concurrent with it; from here on follows
    // it. Settle the in-flight delta into the confirmed sequence — its resulting
    // version is this delta's applied-at version — without needing the ack. We
    // can't set recv to that version (we lack its hash until the ack), but this
    // delta follows it and carries a real signature, so recv advances to this
    // delta's resulting version below.
    let gapSettled = false;
    if (this.inflight !== null && appliedAt > this.recv.version) {
      if (appliedAt - this.recv.version !== this.inflight.versionSpan) {
        throw new Error(
          `clientcc: stream gap ${this.recv.version}..${appliedAt} does not match in-flight span ${this.inflight.versionSpan}`,
        );
      }
      this.inflight = null;
      gapSettled = true;
    }
    if (!gapSettled && appliedAt !== this.recv.version) {
      throw new Error(
        `clientcc: out-of-order delta: applies at ${appliedAt}, client at ${this.recv.version}`,
      );
    }

    let d = ops;
    if (this.inflight !== null) {
      // Concurrent with the in-flight delta: transform past it.
      const [inflightPrime, dPrime] = transformOps(this.inflight.ops, d);
      this.inflight.ops = inflightPrime;
      d = dPrime;
    }
    const [queuePrime, dPrime] = transformOps(this.queue, d);
    this.queue = queuePrime;
    d = dPrime;

    this.apply(d);
    // Remote edits on a blip are non-undoable: feed them so a later undo of a
    // local edit transforms past them. (Our own deltas are recognized by nonce and
    // settled above without reaching here, so d is purely others' ops.)
    for (const o of d) {
      if (o.kind === "blip") {
        this.undoFor(o.blipId).nonUndoableOp(o.op.contentOp);
      }
    }
    this.recv = resulting;
    return this.settleAndSend();
  }

  // canUndo / canRedo report whether the blip has an undoable / redoable edit.
  canUndo(blipId: string): boolean {
    return this.undoMgrs.get(blipId)?.canUndo() ?? false;
  }
  canRedo(blipId: string): boolean {
    return this.undoMgrs.get(blipId)?.canRedo() ?? false;
  }

  // undo reverts the most recent local edit to blipId: it computes the undo op
  // (transformed to apply at the current content), applies it to the optimistic
  // replica, and queues it for submission — returning a delta to send (or null if
  // nothing to undo or a delta is already in flight). The undo op is NOT recorded
  // as a new undoable edit; it is already on the blip's redo stack.
  undo(blipId: string): Outgoing | null {
    return this.applyUndoOp(blipId, this.undoMgrs.get(blipId)?.undo() ?? null);
  }

  // redo re-applies the most recently undone edit to blipId (the mirror of undo).
  redo(blipId: string): Outgoing | null {
    return this.applyUndoOp(blipId, this.undoMgrs.get(blipId)?.redo() ?? null);
  }

  // applyUndoOp submits an undo/redo content op for blipId without re-recording it
  // as undoable (the manager already moved it between the undo/redo stacks).
  private applyUndoOp(blipId: string, contentOp: DocOp | null): Outgoing | null {
    if (contentOp === null) {
      return null;
    }
    const op: Operation = {
      kind: "blip",
      blipId,
      op: {
        ctx: { creator: this.author, timestamp: Date.now(), versionIncrement: 1, hashedVersion: null },
        contentOp,
        method: CONTRIBUTOR_ADD,
      },
    };
    this.apply([op]);
    this.queue.push(op);
    return this.trySend();
  }

  // onAck records that the in-flight delta was accepted, resulting in the given
  // version with opsApplied operations applied by the server (the authoritative
  // count, which the client cannot reliably infer: a deduped resend applies zero,
  // and a transformed-to-noOp delta still applies its op count). It may settle the
  // in-flight delta (once all preceding server deltas have arrived) and return a
  // newly-sendable delta (else null). A late ack for a delta already settled via a
  // version gap is ignored.
  onAck(resulting: HashedVersion, opsApplied: number): Outgoing | null {
    if (this.inflight === null) {
      return null; // already settled (gap-confirmed); the ack is redundant
    }
    this.inflight.acked = true;
    this.inflight.ackedVer = resulting;
    this.inflight.ackedApplied = opsApplied;
    return this.settleAndSend();
  }

  // afterResync is called once the resync tail has been fully fed (via
  // onServerDelta). If the in-flight delta was recognized in the tail it is
  // already settled and this returns the next queued delta (if any). If it was NOT
  // in the tail — the server never received it before the disconnect — it is
  // re-submitted, re-targeted to the now-current version (its ops are already
  // transformed onto recv), with its original nonce so a later resync recognizes
  // it too.
  afterResync(): Outgoing | null {
    if (this.inflight !== null) {
      this.inflight.sentTarget = this.recv;
      return {
        delta: newWaveletDelta(this.author, this.recv, this.inflight.ops),
        nonce: this.inflight.nonce,
      };
    }
    return this.trySend();
  }

  // settleAndSend settles an acked in-flight delta once the client has received
  // every delta the server applied before it, then sends the next queued delta if
  // the path is now clear. The delta's applied-at version is derived from the
  // server's authoritative applied count (so opsApplied==0 — a deduped or fully
  // transformed-away submit — settles in place at the resulting version,
  // advancing nothing, rather than underflowing).
  private settleAndSend(): Outgoing | null {
    if (this.inflight !== null && this.inflight.acked) {
      const appliedAt = this.inflight.ackedVer.version - this.inflight.ackedApplied;
      if (this.recv.version === appliedAt) {
        // All preceding deltas are in; the in-flight delta occupies the next slot.
        this.recv = this.inflight.ackedVer;
        this.inflight = null;
      }
      // recv < appliedAt: still waiting for preceding deltas — hold.
    }
    return this.trySend();
  }

  // trySend promotes the queue to the in-flight slot when it is free, returning
  // the delta to submit (targeting the confirmed version, tagged with a fresh
  // nonce) or null.
  private trySend(): Outgoing | null {
    if (this.inflight !== null || this.queue.length === 0) {
      return null;
    }
    const ops = mergeQueue(this.queue);
    this.queue = [];
    const nonce = this.nextNonce();
    this.inflight = {
      ops,
      sentTarget: this.recv,
      versionSpan: versionSpan(ops),
      nonce,
      acked: false,
      ackedVer: HashedVersion.unsigned(0),
      ackedApplied: 0,
    };
    return { delta: newWaveletDelta(this.author, this.recv, ops), nonce };
  }

  // nextNonce returns a per-session-unique submission nonce.
  private nextNonce(): string {
    this.nonceSeq++;
    return `${this.sessionId}.${this.nonceSeq}`;
  }

  // apply mutates the optimistic replica by the given ops (blip content composes;
  // participant ops mutate the set; noOp does nothing).
  private apply(ops: Operation[]): void {
    for (const o of ops) {
      switch (o.kind) {
        case "blip": {
          const cur = this.blips.get(o.blipId) ?? DocOp.empty();
          const next = compose(cur, o.op.contentOp);
          if (!next.isInitialization()) {
            throw new Error(`blip ${o.blipId}: composed content is not an initialization`);
          }
          this.blips.set(o.blipId, next);
          // Track authorship: the first op's creator is the author; every creator is a
          // contributor. (Both local and remote ops flow through here, so contributors
          // accumulate across all editors.)
          const creator = o.op.ctx.creator;
          let meta = this.blipMeta.get(o.blipId);
          if (meta === undefined) {
            meta = { author: creator, contributors: new Set() };
            this.blipMeta.set(o.blipId, meta);
          }
          meta.contributors.add(creator);
          break;
        }
        case "addParticipant":
          this.parts.add(o.participant);
          break;
        case "removeParticipant":
          this.parts.delete(o.participant);
          break;
        case "noOp":
          // no document or participant change
          break;
        default: {
          // Fail loud rather than diverge silently if a new op kind is added
          // (the server errors on unknown ops too).
          const _exhaustive: never = o;
          throw new Error(`unsupported wavelet op ${JSON.stringify(_exhaustive)}`);
        }
      }
    }
  }
}

// mergeQueue composes consecutive blip-content ops on the same blip with the same
// contributor method into one op, shrinking an outgoing delta — e.g. a run of
// single-character inserts typed while a delta was in flight collapses to one op.
// A differing blip, a participant op, or a differing contributor method is a
// barrier; a compose that doesn't line up is left unmerged (defensive —
// consecutive edits on the client's own optimistic replica always line up).
// Reducing the op count is sound: the server advances the version by the (merged)
// op count and the ack's opsApplied matches, so settling stays consistent.
function mergeQueue(ops: Operation[]): Operation[] {
  const merged: Operation[] = [];
  for (const o of ops) {
    if (o.kind === "blip" && merged.length > 0) {
      const prev = merged[merged.length - 1]!;
      if (prev.kind === "blip" && prev.blipId === o.blipId && prev.op.method === o.op.method) {
        let composed: DocOp | null = null;
        try {
          composed = compose(prev.op.contentOp, o.op.contentOp);
        } catch {
          composed = null; // does not line up; leave unmerged (defensive)
        }
        if (composed !== null) {
          merged[merged.length - 1] = {
            kind: "blip",
            blipId: o.blipId,
            op: { ctx: prev.op.ctx, contentOp: composed, method: prev.op.method },
          };
          continue;
        }
      }
    }
    merged.push(o);
  }
  return merged;
}

// versionSpan is the number of versions a list of ops advances the wavelet by.
// The wavelet model advances by exactly one version per operation — the OP COUNT
// — and deliberately ignores Context.versionIncrement (wire metadata) for version
// arithmetic, matching wavelet apply, cc, the history, and storage. The OT
// transform is one-to-one on operations, so a delta's op count is preserved
// through transform-to-head; the count the client sent equals the count the
// server applies.
function versionSpan(ops: Operation[]): number {
  return ops.length;
}
