// Read-side document tree reader conformance, mirroring internal/doc/reader_test.go.

import { test } from "node:test";
import assert from "node:assert/strict";

import { AnnotationBoundaryMap, Attributes, DocOp } from "./types.ts";
import type { Component } from "./types.ts";
import { attr, childElements, elementText, read, root } from "./doc.ts";

function attrs(m: Record<string, string> | null): Attributes {
  return m === null ? Attributes.empty() : Attributes.of(m);
}

// manifestDoc builds <conversation><blip id="b+1"><thread id="b+2"><blip id="b+3">
// </blip></thread></blip><blip id="b+4"></blip></conversation> as a DocInitialization.
function manifestDoc(): DocOp {
  const none = attrs(null);
  return new DocOp([
    { kind: "elementStart", type: "conversation", attributes: none },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+1" }) },
    { kind: "elementStart", type: "thread", attributes: attrs({ id: "b+2" }) },
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+3" }) },
    { kind: "elementEnd" }, // blip b+3
    { kind: "elementEnd" }, // thread
    { kind: "elementEnd" }, // blip b+1
    { kind: "elementStart", type: "blip", attributes: attrs({ id: "b+4" }) },
    { kind: "elementEnd" }, // blip b+4
    { kind: "elementEnd" }, // conversation
  ]);
}

test("read structure", () => {
  const r = root(manifestDoc());
  assert.equal(r.type, "conversation");

  const topBlips = childElements(r);
  assert.equal(topBlips.length, 2, "root thread should have 2 blips");
  assert.equal(attr(topBlips[0]!, "id"), "b+1");
  assert.equal(attr(topBlips[1]!, "id"), "b+4");

  // b+1 contains a thread with one blip b+3.
  const threads = childElements(topBlips[0]!);
  assert.equal(threads.length, 1);
  assert.equal(threads[0]!.type, "thread");
  assert.equal(attr(threads[0]!, "id"), "b+2");

  const replyBlips = childElements(threads[0]!);
  assert.equal(replyBlips.length, 1);
  assert.equal(attr(replyBlips[0]!, "id"), "b+3");
});

test("read text", () => {
  const none = attrs(null);
  const d = new DocOp([
    { kind: "elementStart", type: "body", attributes: none },
    { kind: "elementStart", type: "line", attributes: none },
    { kind: "elementEnd" },
    { kind: "characters", text: "hello world" },
    { kind: "elementEnd" },
  ]);
  const body = root(d);
  assert.equal(elementText(body), "hello world");
  // The <line/> is an element child; "hello world" is a text child.
  assert.equal(childElements(body).length, 1, "body should have one element child (line)");
});

test("read ignores annotations", () => {
  const none = attrs(null);
  const bold = AnnotationBoundaryMap.of([], [{ key: "style/bold", oldValue: null, newValue: "true" }]);
  const endBold = AnnotationBoundaryMap.of(["style/bold"], []);
  const d = new DocOp([
    { kind: "elementStart", type: "p", attributes: none },
    { kind: "annotationBoundary", boundary: bold },
    { kind: "characters", text: "hi" },
    { kind: "annotationBoundary", boundary: endBold },
    { kind: "elementEnd" },
  ]);
  const p = root(d);
  assert.equal(elementText(p), "hi", "annotations ignored by structural read");
});

test("read rejects non-initialization", () => {
  const bad = new DocOp([{ kind: "retain", count: 3 }]);
  assert.throws(() => read(bad), "read should reject an op with retains");
});

test("read rejects unbalanced (unclosed element)", () => {
  const none = attrs(null);
  const bad = new DocOp([{ kind: "elementStart", type: "a", attributes: none }]);
  assert.throws(() => read(bad), "read should reject an unclosed element");
});

test("read rejects unbalanced (stray element end)", () => {
  const bad = new DocOp([{ kind: "elementEnd" }]);
  assert.throws(() => read(bad), "read should reject an unbalanced element end");
});

test("root rejects multiple top-level nodes", () => {
  const none = attrs(null);
  const d = new DocOp([
    { kind: "elementStart", type: "a", attributes: none },
    { kind: "elementEnd" },
    { kind: "elementStart", type: "b", attributes: none },
    { kind: "elementEnd" },
  ]);
  assert.throws(() => root(d), "root should reject more than one top-level node");
});

test("root rejects a top-level text node", () => {
  const d = new DocOp([{ kind: "characters", text: "loose text" } satisfies Component]);
  assert.throws(() => root(d), "root should reject a text root");
});

test("read returns a forest of top-level nodes", () => {
  const none = attrs(null);
  const nodes = read(
    new DocOp([
      { kind: "elementStart", type: "a", attributes: none },
      { kind: "elementEnd" },
      { kind: "characters", text: "x" },
      { kind: "elementStart", type: "b", attributes: none },
      { kind: "elementEnd" },
    ]),
  );
  assert.equal(nodes.length, 3);
  assert.equal(nodes[0]!.kind, "element");
  assert.equal(nodes[1]!.kind, "text");
  assert.equal(nodes[2]!.kind, "element");
});
