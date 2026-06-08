// Convergence tests for the client concurrency-control state machine. They drive
// CC instances against a tiny in-memory simulated server that serializes submits,
// transforms each to head, applies it, and broadcasts the result (suppressing the
// submitter — the optimistic protocol), then assert every client converges to the
// same blip content.
//
// Mirrors internal/clientcc/clientcc_test.go (the simulated server + the
// deterministic ack-race / resync / merge cases + the randomized fuzz).

import { test } from "node:test";
import assert from "node:assert/strict";

import { CC } from "./clientcc.ts";
import type { Outgoing } from "./clientcc.ts";
import { transformOps } from "./waveop.ts";
import { compose } from "./compose.ts";
import { DocOp, HashedVersion, WaveletName, newWaveletDelta, participant } from "./types.ts";
import type { Component, Context, Operation, Participant, WaveletDelta } from "./types.ts";

// --- fixtures ---

function mkName(): WaveletName {
  return new WaveletName("example.com", "w+conv", "example.com", "conv+root");
}

function mkPID(addr: string): Participant {
  return participant(addr);
}

function opCtx(a: Participant): Context {
  return { creator: a, timestamp: 1000, versionIncrement: 1, hashedVersion: null };
}

function blipContentOp(a: Participant, blipId: string, content: DocOp): Operation {
  return {
    kind: "blip",
    blipId,
    op: { ctx: opCtx(a), contentOp: content, method: 0 /* CONTRIBUTOR_ADD */ },
  };
}

function docInsert(s: string): DocOp {
  return new DocOp([{ kind: "characters", text: s }]);
}

// insertCharOp builds a blip "b" content op inserting one char at pos against a
// document of the given length.
function insertCharOp(a: Participant, length: number, pos: number, ch: string): Operation[] {
  const comps: Component[] = [];
  if (pos > 0) comps.push({ kind: "retain", count: pos });
  comps.push({ kind: "characters", text: ch });
  if (length - pos > 0) comps.push({ kind: "retain", count: length - pos });
  return [blipContentOp(a, "b", new DocOp(comps))];
}

// docOpEqual compares two DocOps structurally. The DocOps we compare here are
// composed blip contents — pure insertion-only initializations produced by the
// deterministic composer — so they are already in canonical form and a direct
// component-wise comparison suffices (mirrors op.DocOp.Equal's component check;
// no normalization needed for converged inits).
function docOpEqual(a: DocOp, b: DocOp): boolean {
  if (a.components.length !== b.components.length) return false;
  for (let i = 0; i < a.components.length; i++) {
    if (!componentEqual(a.components[i]!, b.components[i]!)) return false;
  }
  return true;
}

function componentEqual(a: Component, b: Component): boolean {
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "retain":
      return a.count === (b as { count: number }).count;
    case "characters":
    case "deleteCharacters":
      return a.text === (b as { text: string }).text;
    case "elementStart":
    case "deleteElementStart": {
      const bb = b as { type: string; attributes: { equal(o: unknown): boolean } };
      return a.type === bb.type && a.attributes.equal(bb.attributes as never);
    }
    case "elementEnd":
    case "deleteElementEnd":
      return true;
    case "replaceAttributes": {
      const bb = b as { oldAttributes: { equal(o: unknown): boolean }; newAttributes: { equal(o: unknown): boolean } };
      return a.oldAttributes.equal(bb.oldAttributes as never) && a.newAttributes.equal(bb.newAttributes as never);
    }
    case "updateAttributes":
      return a.update.equal((b as { update: typeof a.update }).update);
    case "annotationBoundary":
      return a.boundary.equal((b as { boundary: typeof a.boundary }).boundary);
  }
}

// blipDoc returns a client's optimistic content for a blip (asserting it exists).
function blipDoc(c: CC, blipId: string): DocOp {
  const d = c.blip(blipId);
  assert.ok(d !== undefined, `no blip ${blipId}`);
  return d;
}

// --- simulated server ---
//
// A minimal stand-in for cc.MemoryHistory + cc.TransformToHead + the test
// simServer: it serializes submits, transforms each client delta to head past
// the deltas applied after its target, applies the result to its own blip map,
// and records the applied (transformed) ops with the submitter's nonce so a
// resync tail can carry it.

interface HistEntry {
  appliedAt: number; // version this delta applied at
  ops: Operation[];
  resulting: HashedVersion;
}

// synthVersion builds a deterministic HashedVersion for a version number. The
// client never validates the hash chain; it only needs the hash to be stable per
// version so HashedVersion.equal/compare behave (two clients echo the same hash
// for the same version).
function synthVersion(v: number): HashedVersion {
  const hash = new Uint8Array(4);
  new DataView(hash.buffer).setUint32(0, v, false);
  return new HashedVersion(v, hash);
}

class SimServer {
  private hist: HistEntry[] = [];
  private head: HashedVersion;
  readonly blips = new Map<string, DocOp>();

  constructor(name: WaveletName) {
    // version 0; the synthetic hash is what every client seeds from.
    this.head = synthVersion(0);
  }

  currentVersion(): HashedVersion {
    return this.head;
  }

  // submit transforms a client delta to head, applies it to the server document
  // and history (recording the submitter's nonce), and returns the resulting
  // version plus the applied (transformed) ops.
  submit(d: WaveletDelta, nonce: string): { resulting: HashedVersion; ops: Operation[] } {
    let ops = [...d.ops];
    const targetV = d.targetVersion.version;
    assert.ok(targetV <= this.head.version, "delta targets a future version");
    // Transform past every delta applied at or after the target version.
    for (const entry of this.hist) {
      if (entry.appliedAt < targetV) continue;
      const [clientPrime] = transformOps(ops, entry.ops);
      ops = clientPrime;
    }
    assert.ok(ops.length > 0, "unexpected no-op submit (these tests use inserts only)");
    const appliedAt = this.head.version;
    const resulting = synthVersion(appliedAt + ops.length);
    this.applyServerDoc(ops);
    this.hist.push({ appliedAt, ops, resulting });
    this.head = resulting;
    return { resulting, ops };
  }

  private applyServerDoc(ops: Operation[]): void {
    for (const o of ops) {
      if (o.kind !== "blip") continue;
      const cur = this.blips.get(o.blipId) ?? DocOp.empty();
      this.blips.set(o.blipId, compose(cur, o.op.contentOp));
    }
  }

  blipDoc(blipId: string): DocOp {
    const d = this.blips.get(blipId);
    assert.ok(d !== undefined, `server has no blip ${blipId}`);
    return d;
  }

  // seed creates blip "b" with the given content and returns the seeding ops and
  // resulting version so clients can initialize from it via onServerDelta.
  seed(author: Participant, content: string): { resulting: HashedVersion; ops: Operation[] } {
    const d = newWaveletDelta(author, this.head, [
      { kind: "addParticipant", ctx: opCtx(author), participant: author },
      blipContentOp(author, "b", docInsert(content)),
    ]);
    return this.submit(d, "");
  }
}

// TestAckRaceHoldsThenSettles drives the option-1 case: a submit ack is delivered
// before the concurrent server delta that preceded the in-flight delta. The
// client must hold the ack, apply the delta, then settle — and converge.
test("ack race holds then settles", () => {
  const name = mkName();
  const alice = mkPID("alice@example.com");
  const bob = mkPID("bob@example.com");

  const srv = new SimServer(name);
  const seed = srv.seed(alice, "X");

  const a = new CC(name, alice, synthVersion(0), "sessA");
  const b = new CC(name, bob, synthVersion(0), "sessB");
  for (const c of [a, b]) {
    assert.equal(c.onServerDelta(seed.ops, seed.resulting, ""), null);
  }

  // Both edit concurrently against the seed: alice inserts "A" at 0, bob "B" at end.
  const aOut = a.edit(insertCharOp(alice, 1, 0, "A"));
  assert.ok(aOut !== null, "alice edit produced no delta");
  const bOut = b.edit(insertCharOp(bob, 1, 1, "B"));
  assert.ok(bOut !== null, "bob edit produced no delta");

  // Server applies bob first, then alice (transformed past bob).
  const bSub = srv.submit(bOut.delta, bOut.nonce);
  const aSub = srv.submit(aOut.delta, aOut.nonce);

  // Alice: deliver her ack BEFORE bob's preceding delta (the race). She must hold.
  assert.equal(a.onAck(aSub.resulting, aSub.ops.length), null, "alice sent a delta before settling");
  assert.equal(a.onServerDelta(bSub.ops, bSub.resulting, bOut.nonce), null);

  // Bob: ack then alice's delta, normal order.
  assert.equal(b.onAck(bSub.resulting, bSub.ops.length), null, "bob unexpectedly sent");
  assert.equal(b.onServerDelta(aSub.ops, aSub.resulting, aOut.nonce), null);

  const want = srv.blipDoc("b");
  assert.deepEqual([...want.components], [{ kind: "characters", text: "AXB" }]);
  assert.ok(docOpEqual(blipDoc(a, "b"), want), "alice did not converge");
  assert.ok(docOpEqual(blipDoc(b, "b"), want), "bob did not converge");
  assert.equal(a.serverVersion().compare(aSub.resulting), 0, "alice not at head");
  assert.equal(b.serverVersion().compare(aSub.resulting), 0, "bob not at head");
});

// TestVersionIncrementIgnored locks the op-count version basis: an op carrying a
// non-unit versionIncrement must advance the version by ONE (op count).
test("version increment ignored (op-count basis)", () => {
  const name = mkName();
  const alice = mkPID("alice@example.com");
  const srv = new SimServer(name);
  const seed = srv.seed(alice, "X");

  const c = new CC(name, alice, synthVersion(0), "sessA");
  assert.equal(c.onServerDelta(seed.ops, seed.resulting, ""), null);
  const before = c.serverVersion().version;

  const ctx: Context = { creator: alice, timestamp: 1000, versionIncrement: 5, hashedVersion: null };
  const ops: Operation[] = [
    {
      kind: "blip",
      blipId: "b",
      op: {
        ctx,
        contentOp: new DocOp([{ kind: "characters", text: "Y" }, { kind: "retain", count: 1 }]),
        method: 0,
      },
    },
  ];
  const out = c.edit(ops);
  assert.ok(out !== null, "edit produced no delta");
  const sub = srv.submit(out.delta, out.nonce);
  assert.equal(sub.resulting.version, before + 1, "server must advance by op count, not versionIncrement");
  assert.equal(c.onAck(sub.resulting, sub.ops.length), null, "unexpected resend");
  assert.equal(c.serverVersion().version, before + 1, "client must count ops, not sum versionIncrement");
});

// TestOpsAppliedZeroSettlesInPlace locks the zero-op ack: a deduped or fully
// transformed-away submit (opsApplied==0) settles the in-flight delta in place
// rather than underflowing and wedging the slot.
test("opsApplied==0 settles in place", () => {
  const name = mkName();
  const alice = mkPID("alice@example.com");
  const srv = new SimServer(name);
  const seed = srv.seed(alice, "X");

  const c = new CC(name, alice, synthVersion(0), "sessA");
  assert.equal(c.onServerDelta(seed.ops, seed.resulting, ""), null);
  const v = c.serverVersion();

  const out = c.edit(insertCharOp(alice, 1, 0, "Z"));
  assert.ok(out !== null, "edit produced no delta");
  assert.equal(c.onAck(v, 0), null, "unexpected resend after no-op ack");
  assert.equal(c.serverVersion().compare(v), 0, "version advanced on a zero-op ack");

  const out2 = c.edit(insertCharOp(alice, 2, 2, "W"));
  assert.ok(out2 !== null, "in-flight slot still wedged after a zero-op ack");
});

// TestResyncRecognizesOwnDelta: an in-flight delta that committed while the
// client was disconnected appears in the resync tail (no longer suppressed); the
// client recognizes it by nonce and settles it without re-applying.
test("resync recognizes own delta", () => {
  const name = mkName();
  const alice = mkPID("alice@example.com");
  const srv = new SimServer(name);
  const seed = srv.seed(alice, "X");

  const c = new CC(name, alice, synthVersion(0), "sessA");
  assert.equal(c.onServerDelta(seed.ops, seed.resulting, ""), null);

  const out = c.edit(insertCharOp(alice, 1, 0, "Z")); // "ZX" optimistically; in-flight
  assert.ok(out !== null, "edit produced no delta");
  // The server applied it (the ack was lost to a disconnect).
  const sub = srv.submit(out.delta, out.nonce);

  // On resync, the tail carries the client's OWN delta with its nonce. The client
  // recognizes and settles it — no re-apply, no transform-against-self.
  const send = c.onServerDelta(sub.ops, sub.resulting, out.nonce);
  assert.equal(send, null, "unexpected send (queue empty)");
  assert.equal(c.serverVersion().compare(sub.resulting), 0, "client not at resync version");
  assert.ok(
    docOpEqual(blipDoc(c, "b"), new DocOp([{ kind: "characters", text: "ZX" }])),
    "content not ZX (optimistic edit confirmed, not doubled)",
  );
  assert.equal(c.afterResync(), null, "afterResync wanted nothing to resend");
});

// TestResyncResubmitsUncommitted: an in-flight delta the server never received
// (disconnect before it arrived) is NOT in the resync tail; afterResync
// re-submits it, re-targeted to the post-resync version, with its original nonce.
test("resync resubmits uncommitted in-flight delta", () => {
  const name = mkName();
  const alice = mkPID("alice@example.com");
  const bob = mkPID("bob@example.com");
  const srv = new SimServer(name);
  const seed = srv.seed(alice, "X");

  const c = new CC(name, alice, synthVersion(0), "sessA");
  assert.equal(c.onServerDelta(seed.ops, seed.resulting, ""), null);

  const out = c.edit(insertCharOp(alice, 1, 0, "Z")); // in-flight, but server never got it
  assert.ok(out !== null, "edit produced no delta");

  // Meanwhile another participant committed a delta; the resync tail carries it
  // (a foreign delta), not the client's own. Bob inserts "Q" at end of "X".
  const bobDelta = newWaveletDelta(bob, seed.resulting, [
    blipContentOp(bob, "b", new DocOp([{ kind: "retain", count: 1 }, { kind: "characters", text: "Q" }])),
  ]);
  const qSub = srv.submit(bobDelta, "sessB.1");
  assert.doesNotThrow(() => c.onServerDelta(qSub.ops, qSub.resulting, "sessB.1"));

  // afterResync must re-submit the still-unsettled in-flight delta, re-targeted to
  // the post-resync version, keeping its nonce.
  const rs = c.afterResync();
  assert.ok(rs !== null, "afterResync did not re-submit the uncommitted in-flight delta");
  assert.equal(rs.nonce, out.nonce, "resubmit nonce should equal the original");
  assert.equal(
    rs.delta.targetVersion.compare(c.serverVersion()),
    0,
    "resubmit must target the current version",
  );

  // And it applies cleanly at head, converging with the foreign edit.
  const rSub = srv.submit(rs.delta, rs.nonce);
  assert.equal(c.onAck(rSub.resulting, rs.delta.ops.length), null, "unexpected send after resubmit ack");
  assert.ok(docOpEqual(blipDoc(c, "b"), srv.blipDoc("b")), "client diverged from server after resubmit");
});

// TestQueueMergesConsecutiveBlipOps: edits queued behind an in-flight delta (a run
// of same-blip inserts) merge into a single op when sent, and still converge.
test("queue merges consecutive blip ops", () => {
  const name = mkName();
  const alice = mkPID("alice@example.com");
  const srv = new SimServer(name);
  const seed = srv.seed(alice, "X");

  const c = new CC(name, alice, synthVersion(0), "sessA");
  assert.equal(c.onServerDelta(seed.ops, seed.resulting, ""), null);

  // First edit goes in-flight (one op).
  const out1 = c.edit(insertCharOp(alice, 1, 0, "A")); // "AX"
  assert.ok(out1 !== null, "edit A produced no delta");
  assert.equal(out1.delta.ops.length, 1, "first delta should have 1 op");

  // Two more edits queue behind it (same blip, same contributor method).
  assert.equal(c.edit(insertCharOp(alice, 2, 2, "B")), null, "edit B should queue"); // "AXB"
  assert.equal(c.edit(insertCharOp(alice, 3, 3, "C")), null, "edit C should queue"); // "AXBC"

  // Acking the in-flight sends the queue — merged from two ops into one.
  const sub1 = srv.submit(out1.delta, out1.nonce);
  const out2 = c.onAck(sub1.resulting, sub1.ops.length);
  assert.ok(out2 !== null, "queue not sent after ack");
  assert.equal(out2.delta.ops.length, 1, "merged queue delta should have 1 op (B,C composed)");

  // It applies and converges.
  const sub2 = srv.submit(out2.delta, out2.nonce);
  assert.equal(c.onAck(sub2.resulting, sub2.ops.length), null, "unexpected send");
  const want = new DocOp([{ kind: "characters", text: "AXBC" }]);
  assert.ok(docOpEqual(blipDoc(c, "b"), want), "client content not AXBC");
  assert.ok(docOpEqual(srv.blipDoc("b"), want), "server content not AXBC");
});

// --- randomized convergence fuzz ---
//
// node is one client plus its in-order inbox of server deltas and a floatable
// pending ack (the simulated network).

interface ServerDelta {
  ops: Operation[];
  ver: HashedVersion;
  nonce: string;
}

interface PendingAck {
  ver: HashedVersion;
  opsApplied: number;
}

interface Node {
  cc: CC;
  author: Participant;
  inbox: ServerDelta[];
  ackPend: PendingAck | null;
}

// mulberry32 is a small deterministic PRNG so the fuzz is reproducible per seed
// (matching the Go test's seeded math/rand usage).
function mulberry32(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// TestConvergenceRandom fuzzes random concurrent inserts across three clients
// with random delivery order (acks float relative to each client's in-order delta
// stream, exercising the ack race and version gaps), then asserts every client
// converges to the server document. Repeated over many seeds.
test("randomized convergence across clients", () => {
  for (let seed = 1; seed <= 50; seed++) {
    runConvergence(seed, 600);
  }
});

function runConvergence(seed: number, steps: number): void {
  const name = mkName();
  const authors = [mkPID("alice@example.com"), mkPID("bob@example.com"), mkPID("carol@example.com")];

  const srv = new SimServer(name);
  const seedRes = srv.seed(authors[0]!, "X");

  const nodes: Node[] = authors.map((au, i) => {
    const cc = new CC(name, au, synthVersion(0), `sess${i}`);
    assert.equal(cc.onServerDelta(seedRes.ops, seedRes.resulting, ""), null, `seed client ${i}`);
    return { cc, author: au, inbox: [], ackPend: null };
  });

  const send = (from: Node, o: Outgoing): void => {
    const { resulting, ops } = srv.submit(o.delta, o.nonce);
    from.ackPend = { ver: resulting, opsApplied: ops.length };
    for (const n of nodes) {
      if (n !== from) n.inbox.push({ ops, ver: resulting, nonce: o.nonce });
    }
  };
  const deliverInbox = (n: Node): void => {
    const sd = n.inbox.shift()!;
    const out = n.cc.onServerDelta(sd.ops, sd.ver, sd.nonce);
    if (out !== null) send(n, out);
  };
  const deliverAck = (n: Node): void => {
    const a = n.ackPend!;
    n.ackPend = null;
    const out = n.cc.onAck(a.ver, a.opsApplied);
    if (out !== null) send(n, out);
  };

  const rng = mulberry32(seed);
  const KEDIT = 0;
  const KINBOX = 1;
  const KACK = 2;

  for (let i = 0; i < steps; i++) {
    const acts: { kind: number; n: Node }[] = [];
    for (const n of nodes) {
      acts.push({ kind: KEDIT, n });
      if (n.inbox.length > 0) acts.push({ kind: KINBOX, n });
      if (n.ackPend !== null) acts.push({ kind: KACK, n });
    }
    const a = acts[Math.floor(rng() * acts.length)]!;
    switch (a.kind) {
      case KEDIT: {
        const content = a.n.cc.blip("b");
        const length = content === undefined ? 0 : content.documentLength();
        const pos = Math.floor(rng() * (length + 1));
        const ch = String.fromCharCode("a".charCodeAt(0) + Math.floor(rng() * 26));
        const out = a.n.cc.edit(insertCharOp(a.n.author, length, pos, ch));
        if (out !== null) send(a.n, out);
        break;
      }
      case KINBOX:
        deliverInbox(a.n);
        break;
      case KACK:
        deliverAck(a.n);
        break;
    }
  }

  // Drain to quiescence.
  for (;;) {
    let progressed = false;
    for (const n of nodes) {
      while (n.inbox.length > 0) {
        deliverInbox(n);
        progressed = true;
      }
      if (n.ackPend !== null) {
        deliverAck(n);
        progressed = true;
      }
    }
    if (!progressed) break;
  }

  const want = srv.blipDoc("b");
  const head = srv.currentVersion();
  for (let i = 0; i < nodes.length; i++) {
    const n = nodes[i]!;
    assert.ok(docOpEqual(blipDoc(n.cc, "b"), want), `client ${i} did not converge (seed ${seed})`);
    assert.equal(n.cc.serverVersion().compare(head), 0, `client ${i} not at server head (seed ${seed})`);
  }
}

// --- authorship tracking (the byline / pill-avatar source) ---

test("CC tracks blip authorship: author = first op's creator; contributors accumulate", () => {
  const name = mkName();
  const alice = mkPID("alice@example.com");
  const bob = mkPID("bob@example.com");
  const c = new CC(name, alice, synthVersion(0), "sessAuth");

  // Alice creates blip "b" (its FIRST op → she is the author).
  assert.equal(c.onServerDelta([blipContentOp(alice, "b", docInsert("hello"))], synthVersion(1), ""), null);
  // Bob then edits it (a contributor, not the author).
  assert.equal(c.onServerDelta(insertCharOp(bob, 5, 5, "\!"), synthVersion(2), ""), null);

  assert.equal(c.blipAuthor("b"), alice, "author is the creator of the blip's first op");
  assert.deepEqual(c.blipContributors("b"), [alice, bob], "contributors in first-seen order (author first)");
  assert.equal(c.blipAuthor("missing"), undefined, "unknown blip has no author");
  assert.deepEqual(c.blipContributors("missing"), [], "unknown blip has no contributors");
});
