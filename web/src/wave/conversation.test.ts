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
  emptyManifest,
  initialBlipContent,
  readManifest,
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
