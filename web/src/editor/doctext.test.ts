import { test } from "node:test";
import assert from "node:assert/strict";

import { DocOp } from "../wave/types.ts";
import { compose } from "../wave/compose.ts";
import { docOpEqual } from "../wave/docop.ts";
import { blipText, diffToContentOp, shiftCursor } from "./doctext.ts";

function chars(s: string): DocOp {
  return s === "" ? DocOp.empty() : new DocOp([{ kind: "characters", text: s }]);
}

// The core invariant: diffToContentOp(old,new) composes onto a flat-text blip of
// `old` to yield `new`. Verified through the real ported composer.
const cases: Array<[string, string]> = [
  ["", "hello"], // create
  ["hi", "hi!"], // append
  ["world", "_world"], // prepend
  ["abc", "ac"], // delete middle
  ["hello", "help"], // replace suffix
  ["café", "cafés"], // multi-byte rune append
  ["😀x", "x"], // delete a surrogate-pair char
  ["abcdef", "abXYef"], // replace inner run
  ["same", "same"], // no change
];

for (const [oldText, newText] of cases) {
  test(`diffToContentOp ${JSON.stringify(oldText)} -> ${JSON.stringify(newText)} composes correctly`, () => {
    const comps = diffToContentOp(oldText, newText);
    if (oldText === newText) {
      assert.equal(comps.length, 0, "unchanged text must yield no op");
      return;
    }
    const op = new DocOp(comps);
    const result = compose(chars(oldText), op);
    assert.ok(docOpEqual(result, chars(newText)), `got ${blipText(result)}, want ${newText}`);
    assert.equal(blipText(result), newText);
  });
}

test("blipText extracts characters", () => {
  assert.equal(blipText(new DocOp([{ kind: "characters", text: "ab" }, { kind: "characters", text: "c" }])), "abc");
});

test("shiftCursor moves a caret across a change", () => {
  // "hi" -> "Xhi": caret at 1 (after 'h') shifts right by the prepend.
  assert.equal(shiftCursor(1, "hi", "Xhi"), 2);
  // caret before the change is unaffected: "hi" -> "hi!" caret at 1 stays.
  assert.equal(shiftCursor(1, "hi", "hi!"), 1);
  // caret after a deletion shifts left: "abc" -> "ac" caret at 3 -> 2.
  assert.equal(shiftCursor(3, "abc", "ac"), 2);
});
