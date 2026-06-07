// Browser component tests for <blip-view> formatting toolbar.
//
// Covers:
//  1. Toolbar renders when blip-view has focus (focusin).
//  2. H1 button click emits a setLineType op for the caret paragraph.
//  3. Bold button click over a selection emits a setStyleRange op.
//  4. Pressing Bold a second time (toggle) emits a clearStyleRange op.
//  5. Line-type toggle (H1 → plain) emits setLineType(null).
//
// Run via: npm run test:web  (from web/)
// This file uses no node built-ins; the web runner picks it up automatically.

import { html } from "lit";
import type { T } from "../../testing/harness.ts";
import { eq, render } from "../../testing/harness.ts";

import { AnnotationBoundaryMap, Attributes, DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";

// Import so the custom element is registered.
import "./blip-view.ts";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Build a structured body: <body><line [t=lineType]/>text</body> */
function makeContent(lineType: string | null, text: string): DocOp {
  const lineAttrs = lineType !== null ? Attributes.of({ t: lineType }) : Attributes.empty();
  const comps: Component[] = [
    { kind: "elementStart", type: "body", attributes: Attributes.empty() },
    { kind: "elementStart", type: "line", attributes: lineAttrs },
    { kind: "elementEnd" },
  ];
  if (text !== "") comps.push({ kind: "characters", text });
  comps.push({ kind: "elementEnd" }); // </body>
  return new DocOp(comps);
}

/** Wait for all pending Lit updates to settle. */
async function waitForUpdate(el: HTMLElement): Promise<void> {
  if ("updateComplete" in el) await (el as { updateComplete: Promise<unknown> }).updateComplete;
  // Extra tick for Lit child-element updates triggered by the first.
  await new Promise<void>((r) => setTimeout(r, 0));
  if ("updateComplete" in el) await (el as { updateComplete: Promise<unknown> }).updateComplete;
}

/** Collect edit CustomEvents dispatched from a blip-view. */
function collectEdits(el: HTMLElement): Component[][] {
  const edits: Component[][] = [];
  el.addEventListener("edit", (e) => {
    edits.push((e as CustomEvent<Component[]>).detail);
  });
  return edits;
}

/** Fire a mousedown on a toolbar button by data-cmd name. */
function toolbarMousedown(toolbar: HTMLElement, cmd: string): void {
  const btn = toolbar.querySelector<HTMLElement>(`[data-cmd="${cmd}"]`);
  if (btn === null) throw new Error(`No toolbar button for cmd="${cmd}"`);
  const ev = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
  btn.dispatchEvent(ev);
}

/** Find the .blip-toolbar element inside a blip-view (light DOM). */
function findToolbar(bv: HTMLElement): HTMLElement {
  const tb = bv.querySelector<HTMLElement>(".blip-toolbar");
  if (tb === null) throw new Error("blip-view has no .blip-toolbar");
  return tb;
}

/** Find the .blip-doc contenteditable inside a blip-view. */
function findDoc(bv: HTMLElement): HTMLElement {
  const doc = bv.querySelector<HTMLElement>(".blip-doc");
  if (doc === null) throw new Error("blip-view has no .blip-doc");
  return doc;
}

/** Find the first Text node inside el (depth-first). */
function firstTextNode(el: Element): Text | null {
  for (const c of el.childNodes) {
    if (c.nodeType === Node.TEXT_NODE && (c as Text).length > 0) return c as Text;
    if (c.nodeType === Node.ELEMENT_NODE) {
      const found = firstTextNode(c as Element);
      if (found !== null) return found;
    }
  }
  return null;
}

/** Place a collapsed selection at the start of the first text node in a paragraph. */
function placeCaretInPara(para: HTMLElement): void {
  const textNode = firstTextNode(para);
  const sel = window.getSelection()!;
  const range = document.createRange();
  if (textNode !== null) {
    range.setStart(textNode, 0);
  } else {
    // Empty paragraph — use the element itself.
    range.setStart(para, 0);
  }
  range.collapse(true);
  sel.removeAllRanges();
  sel.addRange(range);
}

/** Place a selection spanning [start, end) within the first text node of a paragraph. */
function selectInPara(para: HTMLElement, start: number, end: number): boolean {
  const textNode = firstTextNode(para);
  if (textNode === null) return false;
  const sel = window.getSelection()!;
  const range = document.createRange();
  range.setStart(textNode, start);
  range.setEnd(textNode, end);
  sel.removeAllRanges();
  sel.addRange(range);
  return true;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

export async function testToolbarPresent(t: T): Promise<void> {
  const content = makeContent(null, "hello");
  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);

  const toolbar = findToolbar(el);
  eq(toolbar !== null, true, "toolbar in DOM");
  // Initially hidden (no focus yet).
  eq(toolbar.classList.contains("visible"), false, "toolbar hidden before focus");
}

export async function testToolbarVisibleOnFocus(t: T): Promise<void> {
  const content = makeContent(null, "hello");
  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);

  const toolbar = findToolbar(el);
  // Dispatch focusin from the contenteditable to simulate the blip receiving focus.
  const doc = findDoc(el);
  doc.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
  await waitForUpdate(el);

  eq(toolbar.classList.contains("visible"), true, "toolbar visible after focusin");
}

export async function testH1ButtonEmitsSetLineType(t: T): Promise<void> {
  const content = makeContent(null, "Title");
  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);

  const edits = collectEdits(el);

  // Simulate focus (triggers re-render to show toolbar).
  findDoc(el).dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
  await waitForUpdate(el);

  // Place caret inside the paragraph AFTER re-render (so DOM nodes are stable).
  const para = findDoc(el).querySelector<HTMLElement>(".para");
  if (para === null) throw new Error("no .para");
  placeCaretInPara(para);

  toolbarMousedown(findToolbar(el), "h1");

  eq(edits.length, 1, "one edit dispatched");
  const op = edits[0]!;
  const hasUpdateAttrs = op.some((c) => c.kind === "updateAttributes");
  eq(hasUpdateAttrs, true, "op contains updateAttributes (setLineType)");
}

export async function testBoldButtonEmitsSetStyleRange(t: T): Promise<void> {
  const content = makeContent(null, "hello world");
  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);

  const edits = collectEdits(el);

  findDoc(el).dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
  await waitForUpdate(el);

  // Select "hello" in the paragraph AFTER re-render.
  const para = findDoc(el).querySelector<HTMLElement>(".para");
  if (para === null) throw new Error("no .para");
  const ok = selectInPara(para, 0, 5);
  eq(ok, true, "selection placed");

  toolbarMousedown(findToolbar(el), "bold");

  eq(edits.length, 1, "one edit dispatched");
  const op = edits[0]!;
  const hasBoldAnnotation = op.some(
    (c) =>
      c.kind === "annotationBoundary" &&
      c.boundary.changes.some((ch) => ch.key === "style/fontWeight" && ch.newValue === "bold"),
  );
  eq(hasBoldAnnotation, true, "op sets style/fontWeight=bold");
}

export async function testBoldButtonTogglesOff(t: T): Promise<void> {
  // Plain paragraph, all text bold via annotation.
  const boldComps: Component[] = [
    { kind: "elementStart", type: "body", attributes: Attributes.empty() },
    { kind: "elementStart", type: "line", attributes: Attributes.empty() },
    { kind: "elementEnd" },
    {
      kind: "annotationBoundary",
      boundary: AnnotationBoundaryMap.of([], [{ key: "style/fontWeight", oldValue: null, newValue: "bold" }]),
    },
    { kind: "characters", text: "bold text" },
    {
      kind: "annotationBoundary",
      boundary: AnnotationBoundaryMap.of(["style/fontWeight"], []),
    },
    { kind: "elementEnd" }, // </body>
  ];
  const content = new DocOp(boldComps);

  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);

  const edits = collectEdits(el);

  findDoc(el).dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
  await waitForUpdate(el);

  const para = findDoc(el).querySelector<HTMLElement>(".para");
  if (para === null) throw new Error("no .para");
  // Select all of the bold text.
  const ok = selectInPara(para, 0, 9); // "bold text" is 9 chars
  eq(ok, true, "selection placed");

  toolbarMousedown(findToolbar(el), "bold");

  eq(edits.length, 1, "one edit dispatched (toggle off)");
  const op = edits[0]!;
  // clearStyleRange emits a boundary that sets fontWeight to null (or endKey).
  const hasClearBold = op.some(
    (c) =>
      c.kind === "annotationBoundary" &&
      (c.boundary.changes.some((ch) => ch.key === "style/fontWeight" && ch.newValue === null) ||
        c.boundary.endKeys.includes("style/fontWeight")),
  );
  eq(hasClearBold, true, "op clears style/fontWeight (toggle off)");
}

export async function testH1TogglesOffToPlain(t: T): Promise<void> {
  const content = makeContent("h1", "Heading");
  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);

  const edits = collectEdits(el);

  findDoc(el).dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
  await waitForUpdate(el);

  const para = findDoc(el).querySelector<HTMLElement>(".para");
  if (para === null) throw new Error("no .para");
  placeCaretInPara(para);

  // H1 is already the line type → clicking H1 again toggles off to plain (null).
  toolbarMousedown(findToolbar(el), "h1");

  eq(edits.length, 1, "one edit dispatched");
  const op = edits[0]!;
  const clearsType = op.some(
    (c) => c.kind === "updateAttributes" && c.update.updates.some((u) => u.name === "t" && u.newValue === null),
  );
  eq(clearsType, true, "updateAttributes clears t attribute (plain)");
}

export async function testMentionsHighlighted(t: T): Promise<void> {
  const content = makeContent(null, "hi @bob@example.com see @alice@example.com");
  const el = await render(
    html`<blip-view .content=${content} .selfAddress=${"alice@example.com"}></blip-view>`,
  );
  await waitForUpdate(el);
  const doc = findDoc(el);

  // Both @mentions are highlighted; the signed-in user's is emphasized as self.
  const mentions = doc.querySelectorAll(".wave-mention");
  eq(mentions.length, 2, "two @mentions highlighted");
  const selfMentions = doc.querySelectorAll(".wave-mention-self");
  eq(selfMentions.length, 1, "one self-mention emphasized");
  eq(selfMentions[0]!.textContent, "@alice@example.com", "self-mention text is the signed-in user");
  const other = Array.from(mentions).find((m) => !m.classList.contains("wave-mention-self"));
  eq(other?.textContent, "@bob@example.com", "the other mention is bob");

  // The decoration must not change the document text (caret/offset model intact):
  // the paragraph wraps the mentions in spans but its text is exactly the source.
  const para = doc.querySelector<HTMLElement>(".para");
  eq(
    para?.textContent,
    "hi @bob@example.com see @alice@example.com",
    "mention spans wrap text without altering it",
  );
}

export async function testUrlsAutoLinked(t: T): Promise<void> {
  const content = makeContent(null, "see https://example.com/x?a=1 now");
  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);
  const doc = findDoc(el);

  const links = doc.querySelectorAll<HTMLAnchorElement>("a.wave-link");
  eq(links.length, 1, "one auto-linked URL");
  eq(links[0]!.getAttribute("href"), "https://example.com/x?a=1", "href is the URL");
  eq(links[0]!.textContent, "https://example.com/x?a=1", "link text is the URL");
  // Decoration must not change the document text.
  const para = doc.querySelector<HTMLElement>(".para");
  eq(para?.textContent, "see https://example.com/x?a=1 now", "link wraps text without altering it");
}

export async function testInlineImageRenders(t: T): Promise<void> {
  const content = new DocOp([
    { kind: "elementStart", type: "body", attributes: Attributes.empty() },
    { kind: "elementStart", type: "line", attributes: Attributes.empty() },
    { kind: "elementEnd" },
    { kind: "characters", text: "see" },
    { kind: "elementStart", type: "image", attributes: Attributes.of({ attachment: "att-xyz" }) },
    { kind: "elementEnd" },
    { kind: "elementEnd" },
  ]);
  const el = await render(html`<blip-view .content=${content}></blip-view>`);
  await waitForUpdate(el);
  const doc = findDoc(el);

  const img = doc.querySelector<HTMLImageElement>(".wave-image img");
  eq(img !== null, true, "inline image rendered");
  eq(img!.getAttribute("src"), "/attachments/att-xyz", "img src is the attachment download URL");
  // The image marker carries no editable text, so the paragraph text is unchanged.
  eq(doc.querySelector<HTMLElement>(".para")?.textContent, "see", "image adds no editable text");
}
