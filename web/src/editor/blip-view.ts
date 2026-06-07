// <blip-view> — a controlled contenteditable view of one blip document. It is a
// CONTROLLED editor: it renders the projected model (paragraphs of styled spans),
// intercepts every `beforeinput` (preventDefault), translates the action into a
// blip content op via the pure command builders (blipdoc.ts), and dispatches it as
// an `edit` event. The parent applies the op (optimistically, through the
// OptimisticClient) and feeds the new content back via the `content` property; the
// view re-renders and restores the caret. The DOM is therefore never mutated out of
// band — it is always a render of the model — which is what makes diffing
// unnecessary and convergence with the Go server exact.
//
// First cut: text + <line> paragraph structure + style annotations (rendered).
// Handled inputs: insertText/replace, Enter (split line), Backspace/Delete
// (in-paragraph + line-merge at a boundary), paste-as-text. Not yet: IME
// composition, cross-paragraph selection edits, a formatting toolbar — these are
// swallowed (preventDefault, no-op) so the DOM never diverges from the model.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import { Attributes, DocOp, runeCount } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import {
  deleteLineMarker,
  project,
  replaceText,
  splitLineAt,
} from "./blipdoc.ts";
import type { BlipProjection, Paragraph, Span } from "./blipdoc.ts";

export class BlipView extends LitElement {
  static override properties = {
    content: { attribute: false },
  };

  // `declare` (no field initializer) + assign in the constructor: a reactive
  // property with a class-field initializer shadows Lit's accessor under
  // useDefineForClassFields, silently breaking reactivity (the view would never
  // re-render when content changes).
  declare content: DocOp;

  private proj: BlipProjection = project(DocOp.empty());
  private pendingCaret: number | null = null; // caret an edit wants after re-render
  private savedCaret: number | null = null; // caret captured before a non-edit re-render

  constructor() {
    super();
    this.content = DocOp.empty();
  }

  // Light DOM so window.getSelection() reaches the editable content.
  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  // --- edit emission ---

  // emit dispatches a content op for the parent to apply+submit, and records where
  // the caret should land once the model round-trips back as a re-render.
  private emit(ops: Component[], caretAfter: number): void {
    if (ops.length === 0) return;
    this.pendingCaret = caretAfter;
    this.dispatchEvent(new CustomEvent<Component[]>("edit", { detail: ops, bubbles: true, composed: true }));
  }

  // --- input handling ---

  private onBeforeInput = (e: InputEvent): void => {
    // Controlled editor: the DOM must change ONLY via re-render from the model.
    // preventDefault unconditionally and FIRST, so that even a selection we cannot
    // map (a mapping miss) can never let the browser mutate the DOM out of band —
    // which would silently diverge the model from the view and drop the edit on
    // the floor (it would never be submitted). A miss becomes a dropped keystroke,
    // not a divergence; domToOffset is made robust below so misses are rare.
    e.preventDefault();
    const range = currentRange(this);
    if (range === null) return;
    const a = this.domToOffset(range.startContainer, range.startOffset);
    const b = range.collapsed ? a : this.domToOffset(range.endContainer, range.endOffset);
    if (a === null || b === null) return;
    const lo = Math.min(a, b);
    const hi = Math.max(a, b);

    switch (e.inputType) {
      case "insertText":
      case "insertReplacementText":
        this.tryEdit(() => replaceText(this.content, lo, hi, e.data ?? ""), lo + runeCount(e.data ?? ""));
        break;
      case "insertParagraph":
      case "insertLineBreak":
        this.tryEdit(() => splitLineAt(this.content, lo, hi, Attributes.empty()), lo + 2);
        break;
      case "deleteContentBackward":
      case "deleteWordBackward":
      case "deleteByCut":
        this.deleteBackward(lo, hi);
        break;
      case "deleteContentForward":
      case "deleteWordForward":
        this.deleteForward(lo, hi);
        break;
      default:
        break; // unmodeled input: swallow, keep DOM == model
    }
  };

  private onPaste = (e: ClipboardEvent): void => {
    e.preventDefault();
    const text = e.clipboardData?.getData("text/plain") ?? "";
    if (text === "") return;
    const range = currentRange(this);
    if (range === null) return;
    const a = this.domToOffset(range.startContainer, range.startOffset);
    const b = range.collapsed ? a : this.domToOffset(range.endContainer, range.endOffset);
    if (a === null || b === null) return;
    const lo = Math.min(a, b);
    const hi = Math.max(a, b);
    this.tryEdit(() => replaceText(this.content, lo, hi, text), lo + runeCount(text));
  };

  private deleteBackward(lo: number, hi: number): void {
    if (hi > lo) {
      this.tryEdit(() => replaceText(this.content, lo, hi, ""), lo);
      return;
    }
    // Collapsed: find the paragraph the caret sits at the start of.
    const para = this.paragraphAtTextStart(lo);
    if (para !== null && para.lineOffset !== null) {
      // At a line's start: merge into the previous paragraph by deleting the marker.
      this.tryEdit(() => deleteLineMarker(this.content, para.lineOffset!, para.lineType, para.indent), para.lineOffset - 0);
      // caret lands where the previous paragraph's text ended (the marker offset).
      this.pendingCaret = para.lineOffset;
      return;
    }
    if (lo > 0) this.tryEdit(() => replaceText(this.content, lo - 1, lo, ""), lo - 1);
  }

  private deleteForward(lo: number, hi: number): void {
    if (hi > lo) {
      this.tryEdit(() => replaceText(this.content, lo, hi, ""), lo);
      return;
    }
    // Collapsed, within text: delete the following rune (line-boundary forward-merge
    // is not handled in this cut).
    if (lo < this.content.documentLength()) {
      this.tryEditAllowFail(() => replaceText(this.content, lo, lo + 1, ""), lo);
    }
  }

  // tryEdit builds an op and emits it; a builder that throws (e.g. a range spanning
  // a non-character item) is a no-op rather than a crash.
  private tryEdit(build: () => Component[], caretAfter: number): void {
    let ops: Component[];
    try {
      ops = build();
    } catch {
      return;
    }
    this.emit(ops, caretAfter);
  }

  private tryEditAllowFail = this.tryEdit;

  // --- projection-aware lookups ---

  private paragraphAtTextStart(offset: number): Paragraph | null {
    for (const p of this.proj.paragraphs) if (p.textStart === offset) return p;
    return null;
  }

  // --- DOM <-> doc offset mapping ---

  // domToOffset maps a DOM (node, offset) selection point to a doc offset, using
  // the paragraph elements' rendered order (Nth .para == Nth projected paragraph).
  private domToOffset(node: Node, domOffset: number): number | null {
    const root = this.renderRoot.querySelector<HTMLElement>(".blip-doc");
    if (root === null) return null;
    const paras = Array.from(root.querySelectorAll<HTMLElement>(".para"));

    // Selection anchored directly on the editable container: domOffset indexes paragraphs.
    if (node === root) {
      const p = this.proj.paragraphs[Math.min(domOffset, this.proj.paragraphs.length - 1)];
      return p === undefined ? 0 : p.textStart;
    }

    let el: Node | null = node;
    while (el !== null && !(el instanceof HTMLElement && el.classList.contains("para"))) el = el.parentNode;
    if (el === null) {
      // The selection is inside .blip-doc but not within any rendered .para — the
      // browser sometimes parks the caret in a stray text node at the editable
      // root, notably right after a freshly-rendered empty paragraph gains focus.
      // Map it to the START of the paragraph nearest the node in document order so
      // the edit still lands in the model (the next re-render drops the stray
      // node). For the common single-empty-paragraph blip this is paragraph 0.
      if (!root.contains(node)) return null;
      const near = this.proj.paragraphs[nearestParagraphIndex(paras, node)];
      return near === undefined ? (this.proj.paragraphs[0]?.textStart ?? 0) : near.textStart;
    }
    const idx = paras.indexOf(el as HTMLElement);
    const para = this.proj.paragraphs[idx];
    if (para === undefined) return null;
    return para.textStart + runeOffsetWithin(el as HTMLElement, node, domOffset);
  }

  // offsetToDom maps a doc offset to a DOM (node, offset) for caret placement.
  private offsetToDom(offset: number): { node: Node; offset: number } | null {
    const root = this.renderRoot.querySelector<HTMLElement>(".blip-doc");
    if (root === null) return null;
    const paras = Array.from(root.querySelectorAll<HTMLElement>(".para"));
    if (paras.length === 0) return null;

    let idx = this.proj.paragraphs.length - 1;
    let runeOff = 0;
    for (let i = 0; i < this.proj.paragraphs.length; i++) {
      const p = this.proj.paragraphs[i]!;
      if (offset <= p.textStart + p.textLength) {
        idx = i;
        runeOff = Math.max(0, offset - p.textStart);
        break;
      }
    }
    const paraEl = paras[Math.min(idx, paras.length - 1)];
    if (paraEl === undefined) return null;
    return domAtRuneOffset(paraEl, runeOff);
  }

  // --- lifecycle ---

  // willUpdate captures the caret before a re-render so it survives updates that
  // are not local edits — an ack settling or a remote delta replacing DOM nodes
  // would otherwise drop the caret. A local edit has already set pendingCaret,
  // which takes precedence. Runs while the DOM still matches the current proj.
  protected override willUpdate(): void {
    if (this.pendingCaret !== null) return;
    const range = currentRange(this);
    this.savedCaret = range === null ? null : this.domToOffset(range.startContainer, range.startOffset);
  }

  protected override render(): TemplateResult {
    const raw = project(this.content);
    // An empty document renders one empty paragraph so there is a line to edit;
    // store that effective projection so the DOM↔offset mapping matches the DOM
    // (otherwise domToOffset finds a .para with no projected paragraph, returns
    // null, and the beforeinput handler bails WITHOUT preventDefault — letting the
    // browser edit the DOM natively while the model never updates).
    this.proj = raw.paragraphs.length > 0 ? raw : { paragraphs: [EMPTY_PARAGRAPH], length: raw.length };
    return html`
      <style>
        .blip-doc { outline: none; font: 15px/1.6 system-ui, sans-serif; white-space: pre-wrap; }
        .blip-doc .para { min-height: 1.6em; }
      </style>
      <div
        class="blip-doc"
        contenteditable="true"
        spellcheck="false"
        @beforeinput=${this.onBeforeInput}
        @paste=${this.onPaste}
      >
        ${this.proj.paragraphs.map((p) => renderParagraph(p))}
      </div>
    `;
  }

  protected override updated(): void {
    const offset = this.pendingCaret ?? this.savedCaret;
    this.pendingCaret = null;
    this.savedCaret = null;
    if (offset === null) return;
    const target = this.offsetToDom(offset);
    if (target === null) return;
    const sel = window.getSelection();
    if (sel === null) return;
    const r = document.createRange();
    try {
      r.setStart(target.node, target.offset);
    } catch {
      return;
    }
    r.collapse(true);
    sel.removeAllRanges();
    sel.addRange(r);
  }
}

customElements.define("blip-view", BlipView);

// --- rendering helpers ---

const EMPTY_PARAGRAPH: Paragraph = {
  lineType: null,
  indent: 0,
  lineOffset: null,
  textStart: 0,
  textLength: 0,
  spans: [],
};

function renderParagraph(p: Paragraph): TemplateResult {
  const style = paragraphStyle(p);
  const body = p.spans.length === 0 ? html`<br />` : p.spans.map((s) => renderSpan(s));
  return html`<div class="para" style=${style}>${body}</div>`;
}

function renderSpan(s: Span): TemplateResult {
  const css = spanStyle(s.styles);
  return css === "" ? html`${s.text}` : html`<span style=${css}>${s.text}</span>`;
}

function paragraphStyle(p: Paragraph): string {
  const decls: string[] = [];
  switch (p.lineType) {
    case "h1":
      decls.push("font-size:1.7em", "font-weight:600", "margin:0.2em 0");
      break;
    case "h2":
      decls.push("font-size:1.4em", "font-weight:600", "margin:0.2em 0");
      break;
    case "h3":
      decls.push("font-size:1.2em", "font-weight:600", "margin:0.2em 0");
      break;
    case "li":
      decls.push("list-style:disc", "display:list-item", "margin-left:1.5em");
      break;
    default:
      break;
  }
  if (p.indent > 0) decls.push(`margin-left:${p.indent * 1.5}em`);
  return decls.join(";");
}

function spanStyle(styles: Readonly<Record<string, string>>): string {
  const decls: string[] = [];
  for (const [prop, value] of Object.entries(styles)) decls.push(`${camelToKebab(prop)}:${value}`);
  return decls.join(";");
}

function camelToKebab(s: string): string {
  return s.replace(/[A-Z]/g, (m) => "-" + m.toLowerCase());
}

// --- DOM caret helpers ---

// nearestParagraphIndex returns the index of the last .para at or before `node`
// in document order (0 if node precedes all paragraphs). Used to localise a caret
// that the browser placed outside any .para (a stray text node at the editable
// root) to a paragraph.
function nearestParagraphIndex(paras: HTMLElement[], node: Node): number {
  let idx = 0;
  for (let i = 0; i < paras.length; i++) {
    const pos = paras[i]!.compareDocumentPosition(node);
    // node follows paras[i], or paras[i] contains node → paras[i] is a candidate.
    if (pos & Node.DOCUMENT_POSITION_FOLLOWING || pos & Node.DOCUMENT_POSITION_CONTAINED_BY || pos === 0) {
      idx = i;
    } else {
      break; // node precedes paras[i]; earlier candidate (idx) is the nearest
    }
  }
  return idx;
}

function currentRange(host: HTMLElement): Range | null {
  const sel = window.getSelection();
  if (sel === null || sel.rangeCount === 0) return null;
  const r = sel.getRangeAt(0);
  // Only honor selections inside this editor.
  if (!host.contains(r.startContainer)) return null;
  return r;
}

// runeOffsetWithin computes the rune offset of (node, domOffset) within a paragraph
// element, summing the text content that precedes the point in document order.
function runeOffsetWithin(para: HTMLElement, node: Node, domOffset: number): number {
  let runes = 0;
  let done = false;

  const countBeforeChildIndex = (el: Node, n: number): void => {
    const kids = Array.from(el.childNodes);
    for (let i = 0; i < n && i < kids.length; i++) runes += textRuneCount(kids[i]!);
  };

  const walk = (n: Node): void => {
    if (done) return;
    if (n === node) {
      if (n.nodeType === Node.TEXT_NODE) {
        runes += runeCount((n.textContent ?? "").slice(0, domOffset));
      } else {
        countBeforeChildIndex(n, domOffset);
      }
      done = true;
      return;
    }
    if (n.nodeType === Node.TEXT_NODE) {
      runes += runeCount(n.textContent ?? "");
      return;
    }
    for (const c of Array.from(n.childNodes)) {
      walk(c);
      if (done) return;
    }
  };

  if (node === para) {
    countBeforeChildIndex(para, domOffset);
    return runes;
  }
  for (const c of Array.from(para.childNodes)) {
    walk(c);
    if (done) break;
  }
  return runes;
}

// domAtRuneOffset finds the DOM (node, offset) at a given rune offset within a
// paragraph element (the inverse of runeOffsetWithin).
function domAtRuneOffset(para: HTMLElement, runeOff: number): { node: Node; offset: number } {
  let remaining = runeOff;
  const texts: Text[] = [];
  collectTextNodes(para, texts);
  for (const t of texts) {
    const len = runeCount(t.data);
    if (remaining <= len) {
      return { node: t, offset: utf16Index(t.data, remaining) };
    }
    remaining -= len;
  }
  // No text node (empty paragraph, just a <br>): caret at the start of the para.
  return { node: para, offset: 0 };
}

function collectTextNodes(n: Node, out: Text[]): void {
  for (const c of Array.from(n.childNodes)) {
    if (c.nodeType === Node.TEXT_NODE) out.push(c as Text);
    else collectTextNodes(c, out);
  }
}

// textRuneCount counts the document text a node contributes: text nodes by their
// data, elements by their (comment-excluding) textContent, and everything else —
// crucially COMMENT nodes, which Lit uses as template markers — as zero. Counting
// a Lit marker comment's data as text was the bug that misplaced the caret.
function textRuneCount(n: Node): number {
  if (n.nodeType === Node.TEXT_NODE) return runeCount(n.textContent ?? "");
  if (n.nodeType === Node.ELEMENT_NODE) return runeCount((n as Element).textContent ?? "");
  return 0;
}

// utf16Index returns the UTF-16 offset corresponding to a rune offset into s.
function utf16Index(s: string, runeOffset: number): number {
  let i = 0;
  let r = 0;
  for (const ch of s) {
    if (r >= runeOffset) break;
    i += ch.length;
    r += 1;
  }
  return i;
}
