// Conversation manifest model conformance, mirroring internal/conv/manifest_test.go.
// Authoring ops are verified by composing them onto a base document (the Go tests
// use op.Apply; compose(manifest, op) is the TS equivalent) and re-reading.

import { test } from "node:test";
import assert from "node:assert/strict";

import { Attributes, DocOp } from "./types.ts";
import { compose } from "./compose.ts";
import { childElements, root } from "./doc.ts";
import {
  appendBlipToRootThread,
  appendBlipToThread,
  emptyManifest,
  initialBlipContent,
  insertReplyAnchor,
  newBlipID,
  readManifest,
  readReplyAnchors,
  replyToBlip,
} from "./conversation.ts";

function attrs(m: Record<string, string> | null): Attributes {
  return m === null ? Attributes.empty() : Attributes.of(m);
}

test("empty manifest round trips", () => {
  const m = readManifest(emptyManifest());
  assert.equal(m.rootThread.blips.length, 0, "empty manifest root thread should have 0 blips");
  assert.equal(m.anchorWavelet, "");
  assert.equal(m.anchorBlip, "");
});

test("append blips round trips in order", () => {
  let manifest = emptyManifest();
  for (const id of ["b+1", "b+2", "b+3"]) {
    manifest = compose(manifest, appendBlipToRootThread(manifest, id));
  }
  const m = readManifest(manifest);
  const got = m.rootThread.blips.map((b) => b.id);
  assert.deepEqual(got, ["b+1", "b+2", "b+3"], "append order preserved");
});

// Appending must skip past a nested reply-thread subtree under the last root blip
// and land the new blip as a root-thread sibling (not inside the thread).
test("append blip past nested thread", () => {
  const none = attrs(null);
  const manifest = new DocOp([
    { kind: "elementStart", type: "conversation", attributes: none },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+1" }) },
    { kind: "elementStart", type: "thread", attributes: attrs({ id: "b+2" }) },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+3" }) },
    { kind: "elementEnd" }, // b+3
    { kind: "elementEnd" }, // thread
    { kind: "elementEnd" }, // b+1
    { kind: "elementEnd" }, // conversation
  ]);
  const next = compose(manifest, appendBlipToRootThread(manifest, "b+new"));
  const m = readManifest(next);

  assert.equal(m.rootThread.blips.length, 2, "root thread should have 2 blips");
  assert.equal(m.rootThread.blips[0]!.id, "b+1");
  assert.equal(m.rootThread.blips[1]!.id, "b+new");

  // The pre-existing nested reply thread under b+1 must be intact.
  const b1 = m.rootThread.blips[0]!;
  assert.equal(b1.threads.length, 1);
  assert.equal(b1.threads[0]!.id, "b+2");
  assert.equal(b1.threads[0]!.blips.length, 1);
  assert.equal(b1.threads[0]!.blips[0]!.id, "b+3");

  // The new blip is a leaf at root level (no threads).
  assert.equal(m.rootThread.blips[1]!.threads.length, 0, "appended blip should have no threads");
});

test("read reply thread", () => {
  const none = attrs(null);
  const manifest = new DocOp([
    { kind: "elementStart", type: "conversation", attributes: none },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+1" }) },
    { kind: "elementStart", type: "thread", attributes: attrs({ id: "b+2", inline: "true" }) },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+3" }) },
    { kind: "elementEnd" }, // b+3
    { kind: "elementEnd" }, // thread
    { kind: "elementEnd" }, // b+1
    { kind: "elementEnd" }, // conversation
  ]);
  const m = readManifest(manifest);

  assert.equal(m.rootThread.blips.length, 1);
  const b = m.rootThread.blips[0]!;
  assert.equal(b.id, "b+1");
  assert.equal(b.threads.length, 1);

  const th = b.threads[0]!;
  assert.equal(th.id, "b+2");
  assert.equal(th.inline, true, "thread should be inline");
  assert.equal(th.blips.length, 1);
  assert.equal(th.blips[0]!.id, "b+3");
});

test("read manifest anchor and sort", () => {
  const manifest = new DocOp([
    {
      kind: "elementStart",
      type: "conversation",
      attributes: attrs({
        anchorWavelet: "example.com!conv+root",
        anchorBlip: "b+parent",
        sort: "m",
      }),
    },
    { kind: "elementEnd" },
  ]);
  const m = readManifest(manifest);
  assert.equal(m.anchorWavelet, "example.com!conv+root");
  assert.equal(m.anchorBlip, "b+parent");
  assert.equal(m.sort, "m");
});

test("initial blip content is <body><line/></body>", () => {
  const body = root(initialBlipContent());
  assert.equal(body.type, "body");
  const els = childElements(body);
  assert.equal(els.length, 1, "body should have a single child");
  assert.equal(els[0]!.type, "line");
  // No <head> is ever emitted (spec §3.3 implementation note).
  assert.notEqual(body.type, "head");
});

test("readManifest rejects a non-<conversation> root", () => {
  const none = attrs(null);
  const notManifest = new DocOp([
    { kind: "elementStart", type: "body", attributes: none },
    { kind: "elementEnd" },
  ]);
  assert.throws(() => readManifest(notManifest), "readManifest should reject a non-conversation root");
});

test("readManifest reads deleted blips", () => {
  const none = attrs(null);
  const manifest = new DocOp([
    { kind: "elementStart", type: "conversation", attributes: none },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+1", deleted: "true" }) },
    { kind: "elementEnd" },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+2" }) },
    { kind: "elementEnd" },
    { kind: "elementEnd" }, // conversation
  ]);
  const m = readManifest(manifest);
  assert.equal(m.rootThread.blips.length, 2);
  assert.equal(m.rootThread.blips[0]!.deleted, true);
  assert.equal(m.rootThread.blips[1]!.deleted, false);
});

// --- general thread authoring (mirrors the Go TestReply* / TestAppend* tests) ---

function append(manifest: DocOp, threadID: string, blipID: string): DocOp {
  return compose(manifest, appendBlipToThread(manifest, threadID, blipID));
}
function reply(manifest: DocOp, parentBlipID: string, newBlipID: string, inline: boolean): DocOp {
  return compose(manifest, replyToBlip(manifest, parentBlipID, newBlipID, inline));
}

test("appendBlipToThread with empty threadID matches appendBlipToRootThread", () => {
  const manifest = emptyManifest();
  const viaGeneral = compose(manifest, appendBlipToThread(manifest, "", "b+1"));
  const viaRoot = compose(manifest, appendBlipToRootThread(manifest, "b+1"));
  assert.deepEqual(
    readManifest(viaGeneral).rootThread.blips.map((b) => b.id),
    readManifest(viaRoot).rootThread.blips.map((b) => b.id),
  );
});

test("reply creates a thread (id == new blip id) and continue appends to it", () => {
  let manifest = emptyManifest();
  manifest = append(manifest, "", "b+1"); // root blip
  manifest = reply(manifest, "b+1", "b+2", false); // reply thread b+2 under b+1
  manifest = append(manifest, "b+2", "b+3"); // continue thread b+2

  const m = readManifest(manifest);
  assert.deepEqual(m.rootThread.blips.map((b) => b.id), ["b+1"]);
  const b1 = m.rootThread.blips[0]!;
  assert.equal(b1.threads.length, 1);
  const th = b1.threads[0]!;
  assert.equal(th.id, "b+2");
  assert.equal(th.inline, false);
  assert.deepEqual(th.blips.map((b) => b.id), ["b+2", "b+3"]);
});

test("inline reply marks the thread inline", () => {
  let manifest = append(emptyManifest(), "", "b+1");
  manifest = reply(manifest, "b+1", "b+2", true);
  const th = readManifest(manifest).rootThread.blips[0]!.threads[0]!;
  assert.equal(th.inline, true);
});

test("reply targets the right nested blip and leaves siblings intact", () => {
  let manifest = emptyManifest();
  manifest = append(manifest, "", "b+1");
  manifest = append(manifest, "", "b+2"); // two root blips
  manifest = reply(manifest, "b+2", "b+3", false); // reply under the second
  manifest = reply(manifest, "b+3", "b+4", false); // reply under the reply

  const m = readManifest(manifest);
  assert.equal(m.rootThread.blips.length, 2);
  assert.equal(m.rootThread.blips[0]!.threads.length, 0, "b+1 untouched");
  const b2 = m.rootThread.blips[1]!;
  assert.equal(b2.threads.length, 1);
  assert.equal(b2.threads[0]!.id, "b+3");
  const b3 = b2.threads[0]!.blips[0]!;
  assert.equal(b3.id, "b+3");
  assert.equal(b3.threads.length, 1);
  assert.equal(b3.threads[0]!.id, "b+4");
});

test("authoring throws on a missing target", () => {
  const manifest = emptyManifest();
  assert.throws(() => appendBlipToThread(manifest, "no-such-thread", "b+x"));
  assert.throws(() => replyToBlip(manifest, "no-such-blip", "b+x", false));
});

test("newBlipID is unique and well-formed", () => {
  const a = newBlipID();
  const b = newBlipID();
  assert.match(a, /^b\+[0-9a-f]+$/);
  assert.notEqual(a, b);
});

test("readManifest ignores stray non-blip children of the root thread", () => {
  const none = attrs(null);
  // A <thread> directly under <conversation> is schema-invalid; it is silently
  // ignored (permissive parse), and the valid blip is still read.
  const manifest = new DocOp([
    { kind: "elementStart", type: "conversation", attributes: none },
    { kind: "elementStart", type: "thread", attributes: attrs({ id: "stray" }) },
    { kind: "elementEnd" },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+1" }) },
    { kind: "elementEnd" },
    { kind: "elementEnd" }, // conversation
  ]);
  const m = readManifest(manifest);
  assert.equal(m.rootThread.blips.length, 1);
  assert.equal(m.rootThread.blips[0]!.id, "b+1");
});

// --- inline reply anchors (mirror internal/conv/anchor_test.go) ---

function bodyWithText(text: string): DocOp {
  // <body><line/>{text}</body>: insert after the line marker (retain 3, then text).
  return compose(
    initialBlipContent(),
    new DocOp([{ kind: "retain", count: 3 }, { kind: "characters", text }, { kind: "retain", count: 1 }]),
  );
}

test("insert and read an inline reply anchor", () => {
  const body = bodyWithText("hello"); // offset 5 = after "he"
  const withAnchor = compose(body, insertReplyAnchor(body, "b+r1", 5));
  const anchors = readReplyAnchors(withAnchor);
  assert.equal(anchors.length, 1);
  assert.equal(anchors[0]!.threadId, "b+r1");
  assert.equal(anchors[0]!.offset, 5);
});

test("inline reply anchor offset range is validated", () => {
  const body = initialBlipContent();
  const n = body.documentLength();
  assert.throws(() => insertReplyAnchor(body, "b+r", -1));
  assert.throws(() => insertReplyAnchor(body, "b+r", n + 1));
  // offset == len is allowed and applies cleanly.
  const out = compose(body, insertReplyAnchor(body, "b+r", n));
  const anchors = readReplyAnchors(out);
  assert.equal(anchors.length, 1);
  assert.equal(anchors[0]!.offset, n);
});

test("reply anchors are read in document order", () => {
  const body = bodyWithText("abcdef");
  const b1 = compose(body, insertReplyAnchor(body, "b+early", 4));
  const b2 = compose(b1, insertReplyAnchor(b1, "b+late", b1.documentLength() - 1));
  const anchors = readReplyAnchors(b2);
  assert.deepEqual(
    anchors.map((a) => a.threadId),
    ["b+early", "b+late"],
  );
});

test("inline replyToBlip marks the manifest thread inline", () => {
  let manifest = compose(emptyManifest(), appendBlipToRootThread(emptyManifest(), "b+root"));
  manifest = compose(manifest, replyToBlip(manifest, "b+root", "b+r1", true));
  const m = readManifest(manifest);
  const threads = m.rootThread.blips[0]!.threads;
  assert.equal(threads.length, 1);
  assert.equal(threads[0]!.inline, true);
  assert.equal(threads[0]!.id, "b+r1");
});
