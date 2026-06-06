import { strict as assert } from "node:assert";
import test from "node:test";

import { componentEqual, docOpEqual, invert, normalize } from "./docop.ts";
import { AnnotationBoundaryMap, Attributes, AttributesUpdate, DocOp } from "./types.ts";
import type { Component } from "./types.ts";

// A mixed op exercising every merge/coalesce path of the builder.
function sampleOp(): DocOp {
  return new DocOp([
    // adjacent retains -> merge
    { kind: "retain", count: 2 },
    { kind: "retain", count: 3 },
    // adjacent characters -> merge
    { kind: "characters", text: "ab" },
    { kind: "characters", text: "c" },
    // zero-width pieces -> dropped
    { kind: "retain", count: 0 },
    { kind: "characters", text: "" },
    { kind: "deleteCharacters", text: "" },
    // consecutive annotation boundaries -> coalesce
    { kind: "annotationBoundary", boundary: AnnotationBoundaryMap.of([], [{ key: "b", oldValue: null, newValue: "1" }]) },
    { kind: "annotationBoundary", boundary: AnnotationBoundaryMap.of([], [{ key: "a", oldValue: null, newValue: "2" }]) },
    // an item-bearing component forces the boundary to flush before it
    { kind: "elementStart", type: "p", attributes: Attributes.of({ x: "y" }) },
    { kind: "elementEnd" },
    // adjacent deleteCharacters -> merge
    { kind: "deleteCharacters", text: "de" },
    { kind: "deleteCharacters", text: "f" },
    { kind: "updateAttributes", update: AttributesUpdate.of([{ name: "k", oldValue: "o", newValue: "n" }]) },
    { kind: "replaceAttributes", oldAttributes: Attributes.of({ a: "1" }), newAttributes: Attributes.of({ a: "2" }) },
  ]);
}

test("normalize is idempotent", () => {
  const d = sampleOp();
  const once = normalize(d);
  const twice = normalize(once);
  // normalize(d) and normalize(normalize(d)) must be identical components.
  assert.equal(once.components.length, twice.components.length);
  for (let i = 0; i < once.components.length; i++) {
    assert.ok(componentEqual(once.components[i]!, twice.components[i]!), `component ${i} differs`);
  }
  assert.ok(docOpEqual(once, twice));
});

test("normalize merges and drops zero-width pieces", () => {
  const n = normalize(sampleOp());
  // No zero-width components survive, and no two adjacent same-kind mergeables.
  for (const c of n.components) {
    if (c.kind === "retain") assert.ok(c.count > 0);
    if (c.kind === "characters") assert.ok(c.text !== "");
    if (c.kind === "deleteCharacters") assert.ok(c.text !== "");
  }
  // The two annotation boundaries coalesced into exactly one.
  const boundaries = n.components.filter((c: Component) => c.kind === "annotationBoundary");
  assert.equal(boundaries.length, 1);
  // The merged retain at the head equals 5, merged characters "abc".
  assert.ok(componentEqual(n.components[0]!, { kind: "retain", count: 5 }));
  assert.ok(componentEqual(n.components[1]!, { kind: "characters", text: "abc" }));
});

test("invert(invert(d)) is equivalent to d", () => {
  const d = sampleOp();
  const back = invert(invert(d));
  assert.ok(docOpEqual(back, d), "double inversion should equal the original");
});

test("invert swaps insert/delete and attribute directions", () => {
  const d = new DocOp([
    { kind: "characters", text: "hi" },
    { kind: "elementStart", type: "p", attributes: Attributes.empty() },
    { kind: "elementEnd" },
    { kind: "replaceAttributes", oldAttributes: Attributes.of({ a: "1" }), newAttributes: Attributes.of({ a: "2" }) },
    { kind: "updateAttributes", update: AttributesUpdate.of([{ name: "k", oldValue: "o", newValue: "n" }]) },
    { kind: "annotationBoundary", boundary: AnnotationBoundaryMap.of([], [{ key: "x", oldValue: "o", newValue: "n" }]) },
  ]);
  const inv = invert(d);
  assert.ok(componentEqual(inv.components[0]!, { kind: "deleteCharacters", text: "hi" }));
  assert.ok(componentEqual(inv.components[1]!, { kind: "deleteElementStart", type: "p", attributes: Attributes.empty() }));
  assert.ok(componentEqual(inv.components[2]!, { kind: "deleteElementEnd" }));
  assert.ok(
    componentEqual(inv.components[3]!, {
      kind: "replaceAttributes",
      oldAttributes: Attributes.of({ a: "2" }),
      newAttributes: Attributes.of({ a: "1" }),
    }),
  );
  assert.ok(
    componentEqual(inv.components[4]!, {
      kind: "updateAttributes",
      update: AttributesUpdate.of([{ name: "k", oldValue: "n", newValue: "o" }]),
    }),
  );
  assert.ok(
    componentEqual(inv.components[5]!, {
      kind: "annotationBoundary",
      boundary: AnnotationBoundaryMap.of([], [{ key: "x", oldValue: "n", newValue: "o" }]),
    }),
  );
});

test("docOpEqual normalizes both sides before comparing", () => {
  const a = new DocOp([
    { kind: "retain", count: 2 },
    { kind: "retain", count: 3 },
  ]);
  const b = new DocOp([{ kind: "retain", count: 5 }]);
  assert.ok(docOpEqual(a, b));
  assert.ok(!docOpEqual(a, new DocOp([{ kind: "retain", count: 4 }])));
});
