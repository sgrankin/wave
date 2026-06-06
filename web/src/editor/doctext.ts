// The flat-text document model for the editor: the bridge between a blip's DocOp
// (the OT document) and a plain text string (what a contenteditable region shows
// and edits). This is the minimal first cut — blips are treated as flat text
// (Characters only); line elements, annotations/formatting, and structure come
// later. Diff-based op generation (compare the edited text against the known
// document and emit the operation that turns one into the other) is the classic
// OT-editor approach and keeps us out of per-keystroke interception.
//
// Operates on the op model in ../wave/types.ts; convergence is the Go server's
// (and the ported transform's) job — this module only produces/reads ops.

import { runeCount } from "../wave/types.ts";
import type { Component, DocOp } from "../wave/types.ts";

/** runes splits a string into Unicode code points (matches the op model's rune basis). */
function runes(s: string): string[] {
  return [...s];
}

/** blipText extracts the flat text a blip DocOp renders (its Characters, in order). */
export function blipText(d: DocOp): string {
  let s = "";
  for (const c of d.components) if (c.kind === "characters") s += c.text;
  return s;
}

/**
 * diffToContentOp builds the blip-content DocOp components that turn oldText into
 * newText: a common-prefix/suffix diff with a single replaced middle (correct,
 * though not minimal — fine for typing and paste). The components' input length
 * equals runeCount(oldText), so the op composes onto a flat-text blip of that
 * length; the output length equals runeCount(newText).
 *
 * Returns an empty array when the text is unchanged (the caller should not submit
 * a no-op).
 */
export function diffToContentOp(oldText: string, newText: string): Component[] {
  if (oldText === newText) return [];
  const a = runes(oldText);
  const b = runes(newText);

  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) {
    suf++;
  }

  const delMid = a.slice(pre, a.length - suf).join("");
  const insMid = b.slice(pre, b.length - suf).join("");

  const comps: Component[] = [];
  if (pre > 0) comps.push({ kind: "retain", count: pre });
  if (delMid.length > 0) comps.push({ kind: "deleteCharacters", text: delMid });
  if (insMid.length > 0) comps.push({ kind: "characters", text: insMid });
  if (suf > 0) comps.push({ kind: "retain", count: suf });
  return comps;
}

/**
 * shiftCursor maps a caret offset (in runes) across a text change from oldText to
 * newText, using the same prefix/suffix diff as diffToContentOp. A caret before
 * the changed region is unaffected; within or after it, it moves by the net length
 * delta of the replaced middle (clamped into the new text). This keeps the caret
 * sensible when a remote edit re-renders the region while the user is positioned in
 * it — a heuristic, not a full positional transform, sufficient for flat text.
 */
export function shiftCursor(cursor: number, oldText: string, newText: string): number {
  if (oldText === newText) return cursor;
  const a = runes(oldText);
  const b = runes(newText);
  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) {
    suf++;
  }
  const oldMidEnd = a.length - suf; // end of the replaced region in old coords
  const delta = b.length - a.length;
  if (cursor <= pre) return cursor; // before the change
  if (cursor >= oldMidEnd) return cursor + delta; // after the change
  // Inside the replaced middle: clamp to the end of the new middle.
  return Math.min(b.length - suf, Math.max(pre, cursor + delta));
}
