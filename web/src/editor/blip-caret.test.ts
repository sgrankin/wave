// Browser component tests for <blip-view> caret/offset mapping — the invariant the
// whole controlled editor rests on. These are the regression guards for the
// demo-found defects:
//   B1 — the first line of text sat partway down the box (a leading whitespace text
//        node from template indentation rendered as visible space under
//        white-space:pre-wrap).
//   B3 — typing on a later line landed at the start of line 1, because a caret parked
//        outside any .para (on the editable root, or in a stray whitespace/comment
//        node) was mis-mapped to doc offset 0 / the wrong paragraph.
//
// The mapping is exercised end-to-end: place a real DOM selection, dispatch a real
// `beforeinput` insertText, and read back the doc offset the editor chose (the
// leading retain of the emitted content op). That is exactly the path a keystroke
// takes, so it tests domToOffset without reaching into private state.
//
// Run via: npm run test:web  (from web/)

import { html } from "lit";
import type { T } from "../../testing/harness.ts";
import { eq, render } from "../../testing/harness.ts";

import { Attributes, DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";

// Import so the custom element is registered.
import "./blip-view.ts";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

interface Line {
  type?: string | null;
  text: string;
}

/**
 * makeBody builds a structured blip body <body>(<line [t]/>text)*</body>. The doc
 * offsets it produces (each char = 1 item, each <line> start/end = 1 item) are what
 * the tests assert against. For lines [{text:"a"},{text:"b"},{text:"c"}]:
 *   body(0) line(1) /line(2) a(3) line(4) /line(5) b(6) line(7) /line(8) c(9) /body(10)
 * so paragraph textStarts are 3, 6, 9.
 */
function makeBody(lines: Line[]): DocOp {
  const comps: Component[] = [{ kind: "elementStart", type: "body", attributes: Attributes.empty() }];
  for (const ln of lines) {
    const attrs = ln.type ? Attributes.of({ t: ln.type }) : Attributes.empty();
    comps.push({ kind: "elementStart", type: "line", attributes: attrs });
    comps.push({ kind: "elementEnd" });
    if (ln.text !== "") comps.push({ kind: "characters", text: ln.text });
  }
  comps.push({ kind: "elementEnd" }); // </body>
  return new DocOp(comps);
}

async function renderBlip(content: DocOp): Promise<HTMLElement> {
  return render(html`<blip-view .content=${content}></blip-view>`);
}

function blipDoc(el: HTMLElement): HTMLElement {
  const d = el.querySelector<HTMLElement>(".blip-doc");
  if (d === null) throw new Error("blip-view has no .blip-doc");
  return d;
}

function paras(el: HTMLElement): HTMLElement[] {
  return Array.from(blipDoc(el).querySelectorAll<HTMLElement>(".para"));
}

function setCaret(node: Node, offset: number): void {
  const sel = window.getSelection();
  if (sel === null) throw new Error("no selection");
  const r = document.createRange();
  r.setStart(node, offset);
  r.collapse(true);
  sel.removeAllRanges();
  sel.addRange(r);
}

/**
 * insertOffsetAfterTyping dispatches a real `beforeinput` insertText at the current
 * selection and returns the doc offset the editor inserted at — the leading retain
 * count of the emitted content op (0 if there is no leading retain). Returns null if
 * the editor emitted nothing (a mapping miss → swallowed keystroke).
 */
function insertOffsetAfterTyping(el: HTMLElement, ch: string): number | null {
  let captured: Component[] | null = null;
  const onEdit = (e: Event): void => {
    captured = (e as CustomEvent<Component[]>).detail;
  };
  el.addEventListener("edit", onEdit);
  blipDoc(el).dispatchEvent(
    new InputEvent("beforeinput", { inputType: "insertText", data: ch, bubbles: true, cancelable: true }),
  );
  el.removeEventListener("edit", onEdit);
  const op = captured as Component[] | null;
  if (op === null) return null;
  const first = op[0];
  return first !== undefined && first.kind === "retain" ? first.count : 0;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// B1: the first line must sit at the top of the editor. A leading whitespace text
// node (from template indentation around the paragraph map) rendered as visible
// space under white-space:pre-wrap, pushing line 1 down ~one line height.
export async function testNoLeadingGapAboveFirstLine(t: T): Promise<void> {
  const el = await renderBlip(makeBody([{ text: "hi" }]));
  const gap = paras(el)[0]!.getBoundingClientRect().top - blipDoc(el).getBoundingClientRect().top;
  eq(gap < 6, true, `first line should sit at the top of the editor, but gap=${Math.round(gap)}px`);
}

// B3 (the latent core): a caret parked on the editable root carries a CHILD-NODE
// index, not a paragraph index. With 3 paragraphs the old clamp mis-mapped a caret
// before line 2 to the last line.
export async function testRootCaretBeforeSecondOfThreeLines(t: T): Promise<void> {
  const el = await renderBlip(makeBody([{ text: "a" }, { text: "b" }, { text: "c" }]));
  const d = blipDoc(el);
  const p2 = paras(el)[1]!; // the "b" line
  const childIdx = Array.from(d.childNodes).indexOf(p2);
  eq(childIdx >= 0, true, "second paragraph is a direct child of .blip-doc");
  setCaret(d, childIdx); // caret parked on the editable root, before the 2nd paragraph
  eq(insertOffsetAfterTyping(el, "X"), 6, "typing maps to the start of line 2 (offset 6), not another line");
}

// B3: a caret parked on the root PAST the last child belongs at the end of the
// document (end of the last line), not at the start of some earlier line.
export async function testRootCaretPastEndMapsToDocEnd(t: T): Promise<void> {
  const el = await renderBlip(makeBody([{ text: "a" }, { text: "b" }]));
  const d = blipDoc(el);
  setCaret(d, d.childNodes.length); // caret on root, after the last child
  eq(insertOffsetAfterTyping(el, "X"), 7, "caret past all paragraphs maps to the end of line 2 (offset 7)");
}

// B3 (the reported gesture): clicking into an empty later line must type there, not
// at the start of line 1. Browsers park such a click at (para div, 1) — after the
// paragraph's leading marker comment / before its <br>.
export async function testCaretInEmptySecondLineTypesThere(t: T): Promise<void> {
  const el = await renderBlip(makeBody([{ text: "alpha" }, { text: "" }]));
  setCaret(paras(el)[1]!, 1);
  eq(insertOffsetAfterTyping(el, "X"), 10, "typing in the empty second line maps to its start (offset 10)");
}

// Guard: a caret parked on the root before a Lit marker COMMENT between paragraphs
// (not a .para and not a text node) still resolves to the following paragraph's
// start. Locks in correct handling if template churn changes the inter-paragraph
// child structure.
export async function testRootCaretAtCommentBetweenLines(t: T): Promise<void> {
  const el = await renderBlip(makeBody([{ text: "a" }, { text: "b" }, { text: "c" }]));
  const d = blipDoc(el);
  const kids = Array.from(d.childNodes);
  const i0 = kids.indexOf(paras(el)[0]!);
  const i1 = kids.indexOf(paras(el)[1]!);
  // A comment node strictly between line 1 and line 2.
  const commentIdx = kids.findIndex((n, i) => i > i0 && i < i1 && n.nodeType === Node.COMMENT_NODE);
  eq(commentIdx >= 0, true, "a marker comment sits between line 1 and line 2");
  setCaret(d, commentIdx);
  eq(insertOffsetAfterTyping(el, "X"), 6, "caret at the comment before line 2 maps to line 2's start (offset 6)");
}

// Guard: a caret at the very start of the editable maps to the start of line 1's
// text (offset 3 = just after the <line> marker), the only place a caret can be.
export async function testRootCaretAtStartMapsToFirstLine(t: T): Promise<void> {
  const el = await renderBlip(makeBody([{ text: "a" }, { text: "b" }]));
  setCaret(blipDoc(el), 0);
  eq(insertOffsetAfterTyping(el, "X"), 3, "caret at the very start maps to the start of line 1 (offset 3)");
}

// Guard: a normal caret inside line 2's text maps within that line, unaffected by
// the fixes above.
export async function testCaretInsideSecondLineText(t: T): Promise<void> {
  const el = await renderBlip(makeBody([{ text: "ab" }, { text: "cd" }]));
  // line 2 text "cd": body0 line1 /line2 a3 b4 line5 /line6 c7 d8 /body9 → textStart 7.
  const tn = firstText(paras(el)[1]!);
  if (tn === null) throw new Error("line 2 has no text node");
  // Place the caret between c and d (rune offset 1 within the text node).
  setCaret(tn, 1);
  eq(insertOffsetAfterTyping(el, "X"), 8, "typing between c and d maps to offset 8");
}

function firstText(el: Element): Text | null {
  for (const c of el.childNodes) {
    if (c.nodeType === Node.TEXT_NODE && (c as Text).length > 0) return c as Text;
    if (c.nodeType === Node.ELEMENT_NODE) {
      const f = firstText(c as Element);
      if (f !== null) return f;
    }
  }
  return null;
}
