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
// composition, cross-paragraph selection edits. A formatting toolbar is provided
// (bold/italic + line-type buttons) and operates via the same emit() path.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import { Attributes, DocOp, runeCount } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import {
  clearStyleRange,
  deleteLineMarker,
  project,
  rangeStyle,
  replaceText,
  setLineType,
  setStyleRange,
  splitLineAt,
} from "./blipdoc.ts";
import type { BlipProjection, Paragraph, Span } from "./blipdoc.ts";

// A saved selection range (both endpoints as doc offsets).
interface SavedSelection {
  anchor: number;
  focus: number;
}

export class BlipView extends LitElement {
  static override properties = {
    content: { attribute: false },
    selfAddress: { attribute: false },
  };

  // `declare` (no field initializer) + assign in the constructor: a reactive
  // property with a class-field initializer shadows Lit's accessor under
  // useDefineForClassFields, silently breaking reactivity (the view would never
  // re-render when content changes).
  declare content: DocOp;
  // The signed-in participant's address, so @mentions of them render emphasized.
  declare selfAddress: string;

  private proj: BlipProjection = project(DocOp.empty());
  private pendingCaret: number | null = null; // caret an edit wants after re-render
  private savedCaret: number | null = null; // caret captured before a non-edit re-render
  // When a formatting command preserves a selection (B/I over a range), this
  // holds the range to restore after re-render instead of a collapsed caret.
  private pendingSelection: SavedSelection | null = null;
  private hasFocus = false; // tracks focusin/focusout to show/hide toolbar

  constructor() {
    super();
    this.content = DocOp.empty();
    this.selfAddress = "";
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

  // emitWithSelection is like emit but requests that a range (not a collapsed
  // caret) be restored after the re-render — used by B/I so the selection stays
  // on the formatted text and a second format key can be applied immediately.
  private emitWithSelection(ops: Component[], anchor: number, focus: number): void {
    if (ops.length === 0) return;
    this.pendingCaret = null;
    this.pendingSelection = { anchor, focus };
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
      // Cmd/Ctrl+B and Cmd/Ctrl+I arrive as formatBold/formatItalic beforeinput
      // events in a contenteditable; we model bold/italic as style annotations
      // (not <b>/<i> tags), so we preventDefault (above) and toggle the range.
      case "formatBold":
        this.toggleStyle(lo, hi, "fontWeight", "bold");
        break;
      case "formatItalic":
        this.toggleStyle(lo, hi, "fontStyle", "italic");
        break;
      default:
        break; // unmodeled input: swallow, keep DOM == model
    }
  };

  // toggleStyle flips a character style over the selection [lo, hi): if the whole
  // range already carries prop=value it is cleared, otherwise it is set. For a
  // non-collapsed selection the selection is preserved after re-render (pendingSelection).
  // A collapsed range (caret-only) is a no-op.
  private toggleStyle(lo: number, hi: number, prop: string, value: string): void {
    if (hi <= lo) return;
    const cur = rangeStyle(this.content, lo, hi, prop);
    const build =
      cur === value
        ? (): Component[] => clearStyleRange(this.content, lo, hi, prop)
        : (): Component[] => setStyleRange(this.content, lo, hi, prop, value);
    const ops = (() => { try { return build(); } catch { return []; } })();
    if (ops.length === 0) return;
    this.emitWithSelection(ops, lo, hi);
  }

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

  // --- toolbar commands ---

  // toolbarToggleStyle is the toolbar entry-point for B/I. It reads the current
  // selection (at mousedown time the selection is still intact because we
  // preventDefault'd mousedown), calls toggleStyle, and keeps focus in the editor.
  private toolbarToggleStyle(prop: string, value: string): void {
    const range = currentRange(this);
    if (range === null) return;
    const a = this.domToOffset(range.startContainer, range.startOffset);
    const b = range.collapsed ? a : this.domToOffset(range.endContainer, range.endOffset);
    if (a === null || b === null) return;
    this.toggleStyle(Math.min(a, b), Math.max(a, b), prop, value);
  }

  // toolbarSetLineType changes the paragraph that contains the caret to `newType`.
  private toolbarSetLineType(newType: string | null): void {
    const range = currentRange(this);
    if (range === null) return;
    const a = this.domToOffset(range.startContainer, range.startOffset);
    if (a === null) return;
    const para = this.paragraphAtOffset(a);
    if (para === null || para.lineOffset === null) return;
    const caretOffset = a; // preserve caret position after re-render
    const oldType = para.lineType;
    // If the paragraph already has this type, toggle off to plain.
    const targetType = oldType === newType ? null : newType;
    this.tryEdit(() => setLineType(this.content, para.lineOffset!, oldType, targetType), caretOffset);
  }

  // onToolbarMousedown prevents the toolbar button mousedown from blurring the
  // contenteditable and collapsing the selection — gotcha #1. The command runs
  // here (not on click) because focus/selection are still intact.
  private onToolbarMousedown = (e: MouseEvent): void => {
    // Prevent focus leaving the contenteditable.
    e.preventDefault();
    const btn = (e.target as HTMLElement).closest("[data-cmd]") as HTMLElement | null;
    if (btn === null) return;
    const cmd = btn.dataset["cmd"] ?? "";
    switch (cmd) {
      case "bold":
        this.toolbarToggleStyle("fontWeight", "bold");
        break;
      case "italic":
        this.toolbarToggleStyle("fontStyle", "italic");
        break;
      case "h1":
        this.toolbarSetLineType("h1");
        break;
      case "h2":
        this.toolbarSetLineType("h2");
        break;
      case "h3":
        this.toolbarSetLineType("h3");
        break;
      case "li":
        this.toolbarSetLineType("li");
        break;
      case "plain":
        this.toolbarSetLineType(null);
        break;
    }
  };

  // --- focus tracking ---

  private onFocusin = (): void => {
    if (this.hasFocus) return;
    this.hasFocus = true;
    this.requestUpdate();
  };

  private onFocusout = (e: FocusEvent): void => {
    // relatedTarget is the element gaining focus; if it's still inside us, ignore.
    if (this.contains(e.relatedTarget as Node | null)) return;
    this.hasFocus = false;
    this.requestUpdate();
  };

  // --- projection-aware lookups ---

  private paragraphAtTextStart(offset: number): Paragraph | null {
    for (const p of this.proj.paragraphs) if (p.textStart === offset) return p;
    return null;
  }

  // paragraphAtOffset returns the paragraph whose text range contains `offset`.
  private paragraphAtOffset(offset: number): Paragraph | null {
    for (const p of this.proj.paragraphs) {
      if (offset >= p.textStart && offset <= p.textStart + p.textLength) return p;
    }
    return this.proj.paragraphs[this.proj.paragraphs.length - 1] ?? null;
  }

  // caretLineEndOffset returns the doc offset at the end of the paragraph that
  // contains the caret — the line boundary where an inline-reply anchor attaches —
  // or null if there is no caret in this view. Anchoring at a line boundary (not
  // mid-text) keeps the caret/offset mapping unaffected by the inserted anchor.
  caretLineEndOffset(): number | null {
    const range = currentRange(this);
    if (range === null) return null;
    const off = this.domToOffset(range.startContainer, range.startOffset);
    if (off === null) return null;
    const para = this.paragraphAtOffset(off);
    if (para === null) return null;
    return para.textStart + para.textLength;
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

  // --- toolbar state queries ---

  // activeCharStyle returns the value of `prop` over the current selection (or
  // null/mixed), used to derive button pressed state.
  private activeCharStyle(prop: string): string | null | "mixed" {
    const range = currentRange(this);
    if (range === null) return null;
    const a = this.domToOffset(range.startContainer, range.startOffset);
    const b = range.collapsed ? a : this.domToOffset(range.endContainer, range.endOffset);
    if (a === null || b === null) return null;
    return rangeStyle(this.content, Math.min(a, b), Math.max(a, b), prop);
  }

  // caretLineType returns the lineType of the paragraph holding the caret.
  private caretLineType(): string | null {
    const range = currentRange(this);
    if (range === null) return null;
    const a = this.domToOffset(range.startContainer, range.startOffset);
    if (a === null) return null;
    return this.paragraphAtOffset(a)?.lineType ?? null;
  }

  // --- lifecycle ---

  override connectedCallback(): void {
    super.connectedCallback();
    this.addEventListener("focusin", this.onFocusin);
    this.addEventListener("focusout", this.onFocusout);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.removeEventListener("focusin", this.onFocusin);
    this.removeEventListener("focusout", this.onFocusout);
  }

  // willUpdate captures the caret before a re-render so it survives updates that
  // are not local edits — an ack settling or a remote delta replacing DOM nodes
  // would otherwise drop the caret. A local edit has already set pendingCaret or
  // pendingSelection, which take precedence. Runs while the DOM still matches the
  // current proj.
  protected override willUpdate(): void {
    if (this.pendingCaret !== null || this.pendingSelection !== null) return;
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

    // Toolbar active states (queried from current selection + model).
    const boldActive = this.activeCharStyle("fontWeight") === "bold";
    const italicActive = this.activeCharStyle("fontStyle") === "italic";
    const lineType = this.caretLineType();

    return html`
      <style>
        .blip-doc { outline: none; font: 15px/1.6 system-ui, sans-serif; white-space: pre-wrap; }
        .blip-doc .para { min-height: 1.6em; }
        .blip-doc .wave-link { color: #1565c0; text-decoration: underline; }
        .blip-doc .wave-mention { color: #3949ab; }
        .blip-doc .wave-mention-self { background: #fff3cd; border-radius: 3px; padding: 0 2px; font-weight: 600; }
        .blip-doc .reply-anchor { user-select: none; cursor: default; }
        .blip-doc .reply-anchor::after { content: "💬"; font-size: 0.8em; opacity: 0.55; margin-left: 1px; }
        .blip-toolbar {
          display: flex;
          gap: 2px;
          padding: 2px 0 4px;
          opacity: 0;
          pointer-events: none;
          transition: opacity 0.1s;
          user-select: none;
        }
        .blip-toolbar.visible {
          opacity: 1;
          pointer-events: auto;
        }
        .blip-toolbar button {
          font: inherit;
          font-size: 12px;
          line-height: 1;
          padding: 2px 6px;
          border: 1px solid #bbb;
          border-radius: 3px;
          background: #f5f5f5;
          cursor: pointer;
          min-width: 26px;
        }
        .blip-toolbar button:hover { background: #e8e8e8; }
        .blip-toolbar button[aria-pressed="true"] {
          background: #c8d8f0;
          border-color: #7aa;
          font-weight: 600;
        }
      </style>
      <div
        class="blip-toolbar ${this.hasFocus ? "visible" : ""}"
        @mousedown=${this.onToolbarMousedown}
      >
        <button data-cmd="bold" aria-pressed="${boldActive}" aria-label="Bold"><b>B</b></button>
        <button data-cmd="italic" aria-pressed="${italicActive}" aria-label="Italic"><i>I</i></button>
        <button data-cmd="h1" aria-pressed="${lineType === "h1"}" aria-label="Heading 1">H1</button>
        <button data-cmd="h2" aria-pressed="${lineType === "h2"}" aria-label="Heading 2">H2</button>
        <button data-cmd="h3" aria-pressed="${lineType === "h3"}" aria-label="Heading 3">H3</button>
        <button data-cmd="li" aria-pressed="${lineType === "li"}" aria-label="Bullet list">•</button>
        <button data-cmd="plain" aria-pressed="${lineType === null}" aria-label="Plain paragraph">¶</button>
      </div>
      <div
        class="blip-doc"
        contenteditable="true"
        spellcheck="false"
        @beforeinput=${this.onBeforeInput}
        @paste=${this.onPaste}
      >
        ${this.proj.paragraphs.map((p) => renderParagraph(p, this.selfAddress))}
      </div>
    `;
  }

  protected override updated(): void {
    // pendingSelection takes priority: restore a non-collapsed selection (after B/I).
    if (this.pendingSelection !== null) {
      const sel = this.pendingSelection;
      this.pendingSelection = null;
      this.pendingCaret = null;
      this.savedCaret = null;
      const anchorPos = this.offsetToDom(sel.anchor);
      const focusPos = this.offsetToDom(sel.focus);
      if (anchorPos !== null && focusPos !== null) {
        const selection = window.getSelection();
        if (selection !== null) {
          const r = document.createRange();
          try {
            r.setStart(anchorPos.node, anchorPos.offset);
            r.setEnd(focusPos.node, focusPos.offset);
          } catch {
            // Fall back to collapsed caret at anchor if range fails.
            try { r.setStart(anchorPos.node, anchorPos.offset); r.collapse(true); } catch { return; }
          }
          selection.removeAllRanges();
          selection.addRange(r);
        }
      }
      return;
    }

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
  anchors: [],
};

function renderParagraph(p: Paragraph, selfAddress: string): TemplateResult {
  const style = paragraphStyle(p);
  const body = p.spans.length === 0 ? html`<br />` : p.spans.map((s) => renderSpan(s, selfAddress));
  // Inline-reply anchors render as a non-editable marker (glyph via CSS ::after, no
  // text node) after the paragraph's text, so they do not affect the caret mapping.
  const markers = p.anchors.map(
    (id) => html`<span class="reply-anchor" data-thread-id=${id} contenteditable="false"></span>`,
  );
  return html`<div class="para" style=${style}>${body}${markers}</div>`;
}

function renderSpan(s: Span, selfAddress: string): TemplateResult {
  const css = spanStyle(s.styles);
  const inner = renderInline(s.text, selfAddress);
  return css === "" ? inner : html`<span style=${css}>${inner}</span>`;
}

// INLINE_RE matches either an http(s) URL or an @-mention. Both are render-time
// decorations only — they wrap existing text in spans/anchors that add no editable
// text, so the document model and caret/offset mapping are untouched (the DOM caret
// helpers walk all descendant text nodes by rune count). The URL alternative is
// listed first so a URL containing '@' is linked, not split as a mention.
const INLINE_RE =
  /(?<url>https?:\/\/[^\s<>"')]+)|(?<mention>@[A-Za-z0-9._%+\-]+(?:@[A-Za-z0-9.\-]+)?)/g;

function renderInline(text: string, selfAddress: string): TemplateResult {
  if (!text.includes("@") && !text.includes("http")) return html`${text}`;
  const self = selfAddress.toLowerCase();
  const selfName = self.split("@")[0] ?? "";
  const out: TemplateResult[] = [];
  let last = 0;
  for (const m of text.matchAll(INLINE_RE)) {
    const i = m.index ?? 0;
    if (i > last) out.push(html`${text.slice(last, i)}`);
    const url = m.groups?.url;
    if (url !== undefined) {
      out.push(html`<a class="wave-link" href=${url} target="_blank" rel="noopener noreferrer">${url}</a>`);
    } else {
      const ref = m[0].slice(1).toLowerCase();
      const isSelf = self !== "" && (ref === self || ref === selfName);
      const cls = isSelf ? "wave-mention wave-mention-self" : "wave-mention";
      out.push(html`<span class=${cls}>${m[0]}</span>`);
    }
    last = i + m[0].length;
  }
  if (last < text.length) out.push(html`${text.slice(last)}`);
  return html`${out}`;
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

// runeOffsetWithin computes the rune offset of (node, offset) within a paragraph
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
