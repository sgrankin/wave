import test from "node:test";
import assert from "node:assert/strict";

import { compose } from "./compose.ts";
import { docOpEqual } from "./docop.ts";
import { DocOp } from "./types.ts";
import { UndoManager } from "./undo.ts";

// textDoc is the insertion-only DocOp representing a plain-text document.
function textDoc(s: string): DocOp {
  return new DocOp(s === "" ? [] : [{ kind: "characters", text: s }]);
}

// insertAt builds an op that inserts text at pos in a document of length docLen.
function insertAt(docLen: number, pos: number, text: string): DocOp {
  const comps = [];
  if (pos > 0) comps.push({ kind: "retain", count: pos } as const);
  comps.push({ kind: "characters", text } as const);
  if (docLen - pos > 0) comps.push({ kind: "retain", count: docLen - pos } as const);
  return new DocOp(comps);
}

// deleteAt builds an op that deletes text starting at pos in a document of length docLen.
function deleteAt(docLen: number, pos: number, text: string): DocOp {
  const comps = [];
  if (pos > 0) comps.push({ kind: "retain", count: pos } as const);
  comps.push({ kind: "deleteCharacters", text } as const);
  const after = docLen - pos - text.length;
  if (after > 0) comps.push({ kind: "retain", count: after } as const);
  return new DocOp(comps);
}

const apply = (doc: DocOp, op: DocOp): DocOp => compose(doc, op);

test("undo of a single op round-trips the document", () => {
  const um = new UndoManager();
  const doc0 = textDoc("hello");
  const opA = insertAt(5, 5, " world"); // "hello" -> "hello world"
  const doc1 = apply(doc0, opA);

  um.undoableOp(opA);
  um.checkpoint();
  assert.ok(um.canUndo());

  const u = um.undo();
  assert.ok(u !== null, "undo returned an op");
  assert.ok(docOpEqual(apply(doc1, u!), doc0), "undo restored the original document");
  assert.equal(um.canUndo(), false);
  assert.ok(um.canRedo());
});

test("per-op undo reverts in LIFO order with checkpoints", () => {
  const um = new UndoManager();
  const d0 = textDoc("ab"); // len 2
  const opA = insertAt(2, 2, "C"); // "ab" -> "abC"
  const d1 = apply(d0, opA);
  um.undoableOp(opA);
  um.checkpoint();
  const opB = insertAt(3, 3, "D"); // "abC" -> "abCD"
  const d2 = apply(d1, opB);
  um.undoableOp(opB);
  um.checkpoint();

  const u1 = um.undo()!; // reverts B
  assert.ok(docOpEqual(apply(d2, u1), d1), "first undo reverts the last op (B)");
  const u2 = um.undo()!; // reverts A
  assert.ok(docOpEqual(apply(d1, u2), d0), "second undo reverts the earlier op (A)");
  assert.equal(um.undo(), null, "nothing left to undo");
});

test("redo re-applies an undone op", () => {
  const um = new UndoManager();
  const d0 = textDoc("x");
  const opA = insertAt(1, 1, "Y"); // "x" -> "xY"
  const d1 = apply(d0, opA);
  um.undoableOp(opA);
  um.checkpoint();

  const u = um.undo()!;
  const back = apply(d1, u); // "x"
  assert.ok(docOpEqual(back, d0));
  assert.ok(um.canRedo());

  const r = um.redo()!;
  assert.ok(docOpEqual(apply(back, r), d1), "redo reapplies the op");
  assert.equal(um.canRedo(), false);
  assert.ok(um.canUndo());
});

test("a new undoable op clears the redo stack", () => {
  const um = new UndoManager();
  um.undoableOp(insertAt(0, 0, "a"));
  um.checkpoint();
  um.undo();
  assert.ok(um.canRedo());
  um.undoableOp(insertAt(0, 0, "b")); // new edit forks history
  assert.equal(um.canRedo(), false);
});

test("undo transforms past an intervening (remote) op", () => {
  const um = new UndoManager();
  const d0 = textDoc("abc"); // len 3
  const opA = insertAt(3, 1, "X"); // local: "abc" -> "aXbc"
  const d1 = apply(d0, opA);
  um.undoableOp(opA);
  um.checkpoint();

  // A remote op (as it applies to the current doc "aXbc", len 4): append "Y".
  const remote = insertAt(4, 4, "Y"); // "aXbc" -> "aXbcY"
  const d2 = apply(d1, remote);
  um.nonUndoableOp(remote);

  // Undo opA: must remove the X while keeping the remote Y.
  const u = um.undo()!;
  const got = apply(d2, u);
  assert.ok(docOpEqual(got, textDoc("abcY")), `undo past remote op => ${JSON.stringify(got)}`);
});

test("grouped undo (no checkpoint between ops) reverts the whole unit", () => {
  const um = new UndoManager();
  const d0 = textDoc("");
  const opA = insertAt(0, 0, "a"); // "" -> "a"
  const d1 = apply(d0, opA);
  um.undoableOp(opA);
  const opB = insertAt(1, 1, "b"); // "a" -> "ab"
  const d2 = apply(d1, opB);
  um.undoableOp(opB);
  // No checkpoint between A and B: one undo reverts both.

  const u = um.undo()!;
  assert.ok(docOpEqual(apply(d2, u), d0), "grouped undo reverts both ops at once");
  assert.equal(um.undo(), null);
});

test("deep undo: two edits with an intervening remote op revert correctly", () => {
  const um = new UndoManager();
  const d0 = textDoc("abc");
  const opA = insertAt(3, 0, "A"); // "abc" -> "Aabc"
  const d1 = apply(d0, opA);
  um.undoableOp(opA);
  um.checkpoint();

  // A remote op lands between the two local edits: append "R" to "Aabc" (len 4).
  const remote = insertAt(4, 4, "R"); // "Aabc" -> "AabcR"
  const d2 = apply(d1, remote);
  um.nonUndoableOp(remote);

  const opB = insertAt(5, 1, "B"); // "AabcR" -> "ABabcR"
  const d3 = apply(d2, opB);
  um.undoableOp(opB);
  um.checkpoint();

  // Undo B first -> back to "AabcR".
  const u1 = um.undo()!;
  assert.ok(docOpEqual(apply(d3, u1), d2), "undo B");
  // Undo A: the intervening remote R is threaded onto A's entry, so undoing A must
  // remove only A and keep R -> "abcR".
  const u2 = um.undo()!;
  assert.ok(docOpEqual(apply(d2, u2), textDoc("abcR")), "undo A keeps the intervening remote R");
});

test("undo with an empty stack returns null", () => {
  const um = new UndoManager();
  assert.equal(um.undo(), null);
  assert.equal(um.redo(), null);
  assert.equal(um.canUndo(), false);
  assert.equal(um.canRedo(), false);
});
