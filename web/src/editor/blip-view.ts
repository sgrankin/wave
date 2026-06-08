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
// (in-paragraph + line-merge at a boundary), paste-as-text, and IME composition
// (CJK / dictation / autocorrect — see onCompositionStart/End: the browser owns the
// DOM during composition and the model reconciles from the committed string). Not
// yet: cross-paragraph selection edits. A formatting toolbar is provided (bold/italic
// + line-type buttons) and operates via the same emit() path.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";
import { keyed } from "lit/directives/keyed.js";

import { Attributes, DocOp, runeCount } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import {
  clearStyleRange,
  deleteInlineElement,
  deleteLineMarker,
  lineAttributes,
  project,
  rangeStyle,
  replaceText,
  setLineType,
  setStyleRange,
  splitLineAt,
} from "./blipdoc.ts";
import type { BlipProjection, Paragraph, Span } from "./blipdoc.ts";
import type { RemoteCaret } from "./controller.ts";

// A saved selection range (both endpoints as doc offsets).
interface SavedSelection {
  anchor: number;
  focus: number;
}

// HIGHLIGHT_COLOR is the single highlighter color the toolbar toggles (a soft
// yellow). Modeled as a style/backgroundColor annotation, which spanStyle already
// renders, so the toggle reuses the bold/italic machinery.
const HIGHLIGHT_COLOR = "#fff3a0";

export class BlipView extends LitElement {
  static override properties = {
    content: { attribute: false },
    selfAddress: { attribute: false },
    remoteCarets: { attribute: false },
  };

  // `declare` (no field initializer) + assign in the constructor: a reactive
  // property with a class-field initializer shadows Lit's accessor under
  // useDefineForClassFields, silently breaking reactivity (the view would never
  // re-render when content changes).
  declare content: DocOp;
  // The signed-in participant's address, so @mentions of them render emphasized.
  declare selfAddress: string;
  // Other participants' carets/selections in THIS blip (doc-item offsets + color/name),
  // rendered as colored bars/highlights over the text. Set by the parent from the
  // presence channel; empty when nobody else is focused here.
  declare remoteCarets: readonly RemoteCaret[];

  private proj: BlipProjection = project(DocOp.empty());
  private pendingCaret: number | null = null; // caret an edit wants after re-render
  private savedCaret: number | null = null; // caret captured before a non-edit re-render
  // A live non-collapsed selection captured before a non-edit re-render (an ack
  // settling or a peer's delta), restored afterward so the selection — and the
  // floating selection-toolbar anchored to it — survives. Without this, such a
  // re-render would collapse the selection to a caret.
  private savedSelection: SavedSelection | null = null;
  // When a formatting command preserves a selection (B/I over a range), this
  // holds the range to restore after re-render instead of a collapsed caret.
  private pendingSelection: SavedSelection | null = null;
  private hasFocus = false; // tracks focusin/focusout; gates caret preservation in willUpdate
  // IME composition (CJK, mobile dictation, autocorrect): the browser IGNORES
  // preventDefault on composition input, so the controlled-editor trick can't apply
  // there. While `composing` is true the browser owns the DOM — we let it insert the
  // marked text natively, suppress our re-renders (they would destroy the IME), and
  // reconcile the model from the committed string at compositionend. compositionRange
  // is the model offset range the composition replaces, captured at compositionstart
  // BEFORE the browser mutates anything (reads after that are unreliable).
  private composing = false;
  private compositionRange: { lo: number; hi: number } | null = null;
  // The content instance at compositionstart. If `content` is a different instance at
  // compositionend, a remote delta landed mid-composition and the captured offsets are
  // stale — the commit is aborted rather than applied at the wrong place.
  private compositionStartContent: DocOp | null = null;
  // Set for one microtask after compositionend: some browsers fire a trailing
  // insertText for the just-committed text, which would double-insert (compositionend
  // already made it an op); swallow beforeinput during this window.
  private justEndedComposition = false;
  // Bumped to force a from-scratch rebuild of the .blip-doc subtree (it is `keyed` on
  // this): used after a composition that aborted/cancelled, where the browser may have
  // left native nodes that lit-html — diffing against its own template, not the live
  // DOM — would not scrub. A normal edit leaves it unchanged (no extra churn).
  private renderKey = 0;

  constructor() {
    super();
    this.content = DocOp.empty();
    this.selfAddress = "";
    this.remoteCarets = [];
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

  // onKeydown handles undo/redo (Cmd/Ctrl+Z, Cmd/Ctrl+Shift+Z, Cmd/Ctrl+Y). In a
  // fully-controlled contenteditable the browser's native undo stack is empty (every
  // beforeinput is preventDefault'd), so Cmd-Z may not fire a historyUndo
  // beforeinput at all — keydown is the reliable signal. We preventDefault and emit
  // an "undo" request for the host to route to the per-blip undo manager.
  private onKeydown = (e: KeyboardEvent): void => {
    if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
    const k = e.key.toLowerCase();
    let redo: boolean;
    if (k === "z") {
      redo = e.shiftKey;
    } else if (k === "y" && !e.shiftKey) {
      redo = true; // Cmd/Ctrl+Y is redo on Windows-style bindings
    } else {
      return;
    }
    e.preventDefault();
    this.dispatchEvent(
      new CustomEvent<{ redo: boolean }>("undo", { detail: { redo }, bubbles: true, composed: true }),
    );
  };

  private onBeforeInput = (e: InputEvent): void => {
    // IME composition (CJK, dictation, autocorrect): the browser ignores
    // preventDefault on composition input, so intercepting it only diverges the DOM
    // from the model. Let the browser insert the marked text natively and reconcile
    // the model from the committed string at compositionend (onCompositionEnd). The
    // `composing` guard also catches a commit delivered as a trailing insertText
    // while the composition is still officially open.
    if (e.inputType === "insertCompositionText" || this.composing) return;
    // Trailing insertText right after compositionend (browser-dependent): compositionend
    // already committed the text as an op, so swallow this to avoid a double-insert.
    if (this.justEndedComposition) {
      e.preventDefault();
      return;
    }
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
      case "insertLineBreak": {
        // List continuation: pressing Enter inside a list item starts a NEW list
        // item (carry the line type + indent), rather than dropping to a plain line.
        // Headings deliberately do NOT continue — Enter after a heading is a plain
        // line, as in every editor. Enter on an EMPTY list item exits the list.
        const para = this.paragraphAtOffset(lo);
        if (para !== null && para.lineType === "li" && para.lineOffset !== null) {
          // Exit the list only on a TRULY empty item — no text AND no widgets.
          // textLength counts text runes only, so an item holding just an inline
          // image/reply (widgetCount>0, textLength==0) must still SPLIT, not exit.
          if (range.collapsed && para.items.length === 0) {
            this.tryEdit(() => setLineType(this.content, para.lineOffset!, "li", null), para.lineOffset);
            break;
          }
          this.tryEdit(() => splitLineAt(this.content, lo, hi, lineAttributes("li", para.indent)), lo + 2);
          break;
        }
        this.tryEdit(() => splitLineAt(this.content, lo, hi, Attributes.empty()), lo + 2);
        break;
      }
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

  // onCompositionStart marks the start of an IME composition and captures the model
  // range it will replace — the selection at this instant, before the browser inserts
  // any marked text. The commit at compositionend replaces this range with the final
  // string. A selection we cannot map leaves compositionRange null (handled there).
  private onCompositionStart = (): void => {
    this.composing = true;
    // Snapshot the content instance so compositionend can detect a concurrent remote
    // delta (which would invalidate the captured offsets).
    this.compositionStartContent = this.content;
    const range = currentRange(this);
    if (range === null) {
      this.compositionRange = null;
      return;
    }
    const a = this.domToOffset(range.startContainer, range.startOffset);
    const b = range.collapsed ? a : this.domToOffset(range.endContainer, range.endOffset);
    this.compositionRange =
      a === null || b === null ? null : { lo: Math.min(a, b), hi: Math.max(a, b) };
  };

  // onCompositionEnd commits the composition into the model: it turns the committed
  // string into a replaceText op over the captured range, so the composed text is
  // submitted (and converges to other clients) rather than living only as native DOM
  // the next render would drop. The model round-trip re-renders the DOM back into
  // agreement, discarding the browser's composition nodes. An empty commit (a cancel,
  // or composed-to-nothing) emits nothing — the browser has already restored the DOM —
  // and just flushes any render deferred while composing (e.g. a remote delta).
  private onCompositionEnd = (e: CompositionEvent): void => {
    this.composing = false;
    // Guard against a trailing insertText for one microtask (see onBeforeInput).
    this.justEndedComposition = true;
    queueMicrotask(() => {
      this.justEndedComposition = false;
    });
    const range = this.compositionRange;
    const startContent = this.compositionStartContent;
    this.compositionRange = null;
    this.compositionStartContent = null;
    const text = e.data ?? "";
    // Abort the commit if we cannot trust it: the start selection didn't map, the
    // composition was cancelled/empty, or the content changed under us mid-composition
    // (a remote delta — the captured offsets are now stale, so committing would
    // misplace the text). In every abort case we force a from-scratch rebuild of
    // .blip-doc (bump renderKey → `keyed` re-creates it) so any DOM the browser left
    // during composition is discarded and the view re-syncs with the model — a soft
    // requestUpdate alone would not, since lit-html diffs against its own last template
    // (not the live DOM) and would skip an unchanged subtree.
    if (range === null || text === "" || this.content !== startContent) {
      if (range !== null) this.pendingCaret = range.lo; // caret back where composition began
      this.forceReconcile();
      return;
    }
    // Build the model op for the committed text. If it can't be modeled — e.g. the
    // composed range spans an inline widget (replaceText throws on an element item) —
    // we can't emit, but the browser already inserted native nodes, so force a rebuild
    // to scrub them (a silent swallow here would leave the DOM diverged from the model).
    let ops: Component[];
    try {
      ops = replaceText(this.content, range.lo, range.hi, text);
    } catch {
      this.pendingCaret = range.lo;
      this.forceReconcile();
      return;
    }
    // emit dispatches the op; content round-trips back and re-renders the DOM to match,
    // with the caret after the inserted text.
    this.emit(ops, range.lo + runeCount(text));
  };

  // forceReconcile rebuilds the .blip-doc subtree from scratch on the next render
  // (renderKey is `keyed`), discarding any DOM the browser left behind during a
  // composition that did not round-trip through a model op. A soft requestUpdate
  // would not suffice: lit-html diffs against its own last template, not the live
  // DOM, so it skips an unchanged subtree even when the browser mutated it.
  private forceReconcile(): void {
    this.renderKey++;
    this.requestUpdate();
  }

  // shouldUpdate suppresses re-renders while an IME composition is in flight: the
  // browser owns the DOM then, and re-rendering (notably on a remote delta) would
  // destroy the composition mid-flight. onCompositionEnd flushes the deferred update.
  protected override shouldUpdate(): boolean {
    return !this.composing;
  }

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
    // Caret immediately AFTER an inline widget (reply/image): Backspace deletes the
    // widget (its 2 items), not a text rune — otherwise it would be a no-op (replaceText
    // over an element item throws).
    const w = this.widgetToDelete(lo, "before");
    if (w !== null) {
      this.tryEdit(() => deleteInlineElement(this.content, w.offset, w.type, w.attributes), w.offset);
      return;
    }
    if (lo > 0) this.tryEdit(() => replaceText(this.content, lo - 1, lo, ""), lo - 1);
  }

  private deleteForward(lo: number, hi: number): void {
    if (hi > lo) {
      this.tryEdit(() => replaceText(this.content, lo, hi, ""), lo);
      return;
    }
    // Caret immediately BEFORE an inline widget: Delete removes the widget.
    const w = this.widgetToDelete(lo, "after");
    if (w !== null) {
      this.tryEdit(() => deleteInlineElement(this.content, w.offset, w.type, w.attributes), w.offset);
      return;
    }
    // Collapsed, within text: delete the following rune (line-boundary forward-merge
    // is not handled in this cut).
    if (lo < this.content.documentLength()) {
      this.tryEditAllowFail(() => replaceText(this.content, lo, lo + 1, ""), lo);
    }
  }

  // widgetToDelete finds the inline widget (reply/image) immediately adjacent to a
  // collapsed caret at `caret`: side "before" → a widget whose 2 items end at the caret
  // (Backspace target); side "after" → a widget that starts at the caret (Delete target).
  // Returns the element's offset + tag + exact attributes for deleteInlineElement, or null.
  private widgetToDelete(
    caret: number,
    side: "before" | "after",
  ): { offset: number; type: string; attributes: Attributes } | null {
    for (const p of this.proj.paragraphs) {
      for (const it of p.items) {
        if (it.kind !== "reply" && it.kind !== "image") continue;
        const hit = side === "before" ? it.offset + 2 === caret : it.offset === caret;
        if (!hit) continue;
        return it.kind === "reply"
          ? { offset: it.offset, type: "reply", attributes: Attributes.of({ id: it.id }) }
          : { offset: it.offset, type: "image", attributes: Attributes.of({ attachment: it.attachment }) };
      }
    }
    return null;
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

  // Alias of tryEdit (which already swallows a throwing builder), named at the
  // forward-delete call site to signal that a range over a widget is EXPECTED to throw
  // (textBetween rejects element items) → no-op, not a bug.
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

  // applyCommand runs a formatting command (bold/italic/h1/h2/h3/li/plain) against
  // the current selection. It is the public entry point for the floating
  // <selection-toolbar>: the toolbar preventDefaults its own pointerdown so focus
  // and the selection are still intact in this editor when this runs.
  applyCommand(cmd: string): void {
    switch (cmd) {
      case "bold":
        this.toolbarToggleStyle("fontWeight", "bold");
        break;
      case "italic":
        this.toolbarToggleStyle("fontStyle", "italic");
        break;
      case "underline":
        this.toolbarToggleStyle("underline", "true");
        break;
      case "strike":
        this.toolbarToggleStyle("strikethrough", "true");
        break;
      case "highlight":
        this.toolbarToggleStyle("backgroundColor", HIGHLIGHT_COLOR);
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
  }

  // commandStates reports which formatting commands are active for the current
  // selection, so the floating toolbar can show pressed buttons. Reads live from
  // the selection + model (the same queries the toolbar button states used).
  commandStates(): {
    bold: boolean;
    italic: boolean;
    underline: boolean;
    strike: boolean;
    highlight: boolean;
    lineType: string | null;
  } {
    return {
      bold: this.activeCharStyle("fontWeight") === "bold",
      italic: this.activeCharStyle("fontStyle") === "italic",
      underline: this.activeCharStyle("underline") === "true",
      strike: this.activeCharStyle("strikethrough") === "true",
      highlight: this.activeCharStyle("backgroundColor") === HIGHLIGHT_COLOR,
      lineType: this.caretLineType(),
    };
  }

  // --- focus tracking ---

  private onFocusin = (): void => {
    if (this.hasFocus) return;
    this.hasFocus = true;
    // Track the caret only while focused (selectionchange is a document event, so we
    // subscribe per-focus to avoid every blip reacting to every selection move).
    document.addEventListener("selectionchange", this.onSelectionChange);
    this.reportCaret(); // publish the initial caret position
  };

  private onFocusout = (e: FocusEvent): void => {
    // relatedTarget is the element gaining focus; if it's still inside us, ignore.
    if (this.contains(e.relatedTarget as Node | null)) return;
    this.hasFocus = false;
    document.removeEventListener("selectionchange", this.onSelectionChange);
    // Focus left the editor entirely: clear our caret (anchor/focus = -1) so peers
    // stop rendering it. (A blip-to-blip switch instead overwrites it via the new
    // blip's focusin, so the caret moves rather than lingering.)
    this.dispatchEvent(
      new CustomEvent<{ anchor: number; focus: number }>("caret", {
        detail: { anchor: -1, focus: -1 },
        bubbles: true,
        composed: true,
      }),
    );
  };

  private onSelectionChange = (): void => {
    if (this.hasFocus) this.reportCaret();
  };

  // reportCaret publishes the local caret/selection (doc-item offsets within this blip —
  // text runes plus 2 per inline widget, the SAME basis as domToOffset/offsetToDom) to
  // the parent via a "caret" event; <wave-conversation> relays it on the presence channel
  // so peers can render it. All peers must use this item basis. No-op when the selection
  // is not in this editor.
  private reportCaret(): void {
    const sel = window.getSelection();
    if (sel === null || sel.rangeCount === 0 || !this.contains(sel.anchorNode)) return;
    const anchor = this.domToOffset(sel.anchorNode!, sel.anchorOffset) ?? -1;
    const focus =
      sel.focusNode !== null && this.contains(sel.focusNode)
        ? (this.domToOffset(sel.focusNode, sel.focusOffset) ?? anchor)
        : anchor;
    this.dispatchEvent(
      new CustomEvent<{ anchor: number; focus: number }>("caret", {
        detail: { anchor, focus },
        bubbles: true,
        composed: true,
      }),
    );
  }

  // --- projection-aware lookups ---

  private paragraphAtTextStart(offset: number): Paragraph | null {
    for (const p of this.proj.paragraphs) if (p.textStart === offset) return p;
    return null;
  }

  // paragraphAtOffset returns the paragraph whose content range contains `offset`.
  // Uses paragraphEnd (textStart + textLength + 2*widgetCount), NOT textStart+textLength,
  // so a caret after a mid-text/trailing widget resolves to the right paragraph.
  private paragraphAtOffset(offset: number): Paragraph | null {
    for (const p of this.proj.paragraphs) {
      if (offset >= p.textStart && offset <= p.paragraphEnd) return p;
    }
    return this.proj.paragraphs[this.proj.paragraphs.length - 1] ?? null;
  }

  // caretAnchorOffset returns the EXACT doc offset of the caret (the selection's low
  // end), so an inline-reply anchor / image attaches right at the caret, not at the line
  // end. The mapping now counts inline elements as their doc items, so a mid-text anchor
  // is caret-safe. Returns null if there is no caret in this view.
  caretAnchorOffset(): number | null {
    const range = currentRange(this);
    if (range === null) return null;
    const a = this.domToOffset(range.startContainer, range.startOffset);
    if (a === null) return null;
    if (range.collapsed) return a;
    const b = this.domToOffset(range.endContainer, range.endOffset);
    return b === null ? a : Math.min(a, b); // low end of the selection
  }

  // --- DOM <-> doc offset mapping ---

  // domToOffset maps a DOM (node, offset) selection point to a doc offset, using
  // the paragraph elements' rendered order (Nth .para == Nth projected paragraph).
  private domToOffset(node: Node, domOffset: number): number | null {
    const root = this.renderRoot.querySelector<HTMLElement>(".blip-doc");
    if (root === null) return null;
    if (node !== root && !root.contains(node)) return null;
    const paras = Array.from(root.querySelectorAll<HTMLElement>(".para"));

    // Common case: the caret is within a rendered paragraph — map by rune count.
    let el: Node | null = node;
    while (el !== null && !(el instanceof HTMLElement && el.classList.contains("para"))) el = el.parentNode;
    if (el instanceof HTMLElement) {
      const idx = paras.indexOf(el);
      const para = this.proj.paragraphs[idx];
      if (para === undefined) return null;
      return para.textStart + docItemsBefore(el, node, domOffset);
    }

    // The caret is NOT inside any paragraph: the browser parked it on the editable
    // root or in a stray node between/around paragraphs (a whitespace text node or
    // a Lit marker comment). Resolve it to a real paragraph boundary. The reference
    // is the node the caret sits *before*:
    //   - on the root, that is root.childNodes[domOffset] (undefined ⇒ past the end);
    //   - otherwise the stray node itself.
    // A root-anchored domOffset is a CHILD-NODE index (paras interleaved with marker
    // comments), NOT a paragraph index — so it must be resolved positionally, not
    // used to index proj.paragraphs directly.
    const ref: Node | null = node === root ? (root.childNodes[domOffset] ?? null) : node;
    return this.offsetAtParagraphBoundary(paras, ref);
  }

  // offsetAtParagraphBoundary maps a caret that is not inside any paragraph to a doc
  // offset. `ref` is the DOM node the caret sits before (null ⇒ it sits after the
  // last child). The result is the START of the first paragraph at/after `ref` in
  // document order, or — if `ref` follows every paragraph — the END of the last one.
  private offsetAtParagraphBoundary(paras: HTMLElement[], ref: Node | null): number | null {
    const ps = this.proj.paragraphs;
    const lastEnd = (): number | null => {
      const last = ps[ps.length - 1];
      return last === undefined ? null : last.paragraphEnd;
    };
    if (ref === null) return lastEnd();
    for (let i = 0; i < paras.length; i++) {
      if (ref === paras[i]) return ps[i]?.textStart ?? null;
      const pos = paras[i]!.compareDocumentPosition(ref);
      // ref precedes paras[i] (or is contained by it) → caret sits at this para's start.
      if (pos & Node.DOCUMENT_POSITION_PRECEDING || pos & Node.DOCUMENT_POSITION_CONTAINED_BY) {
        return ps[i]?.textStart ?? null;
      }
      // else ref follows paras[i]; keep scanning.
    }
    return lastEnd(); // ref follows every paragraph
  }

  // offsetToDom maps a doc offset to a DOM (node, offset) for caret placement.
  private offsetToDom(offset: number): { node: Node; offset: number } | null {
    const root = this.renderRoot.querySelector<HTMLElement>(".blip-doc");
    if (root === null) return null;
    const paras = Array.from(root.querySelectorAll<HTMLElement>(".para"));
    if (paras.length === 0) return null;

    let idx = -1;
    let itemOff = 0;
    for (let i = 0; i < this.proj.paragraphs.length; i++) {
      const p = this.proj.paragraphs[i]!;
      if (offset <= p.paragraphEnd) {
        idx = i;
        itemOff = Math.max(0, offset - p.textStart); // a doc-ITEM offset within the paragraph
        break;
      }
    }
    if (idx === -1) {
      // offset is past every paragraph (the end-of-doc gap between the last paragraphEnd
      // and proj.length, e.g. a remote focus clamped to proj.length): map to the END of
      // the last paragraph, not its start.
      idx = this.proj.paragraphs.length - 1;
      const last = this.proj.paragraphs[idx];
      itemOff = last === undefined ? 0 : last.paragraphEnd - last.textStart;
    }
    const paraEl = paras[Math.min(idx, paras.length - 1)];
    if (paraEl === undefined) return null;
    return domAtDocOffset(paraEl, itemOff);
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
    // Remote carets are absolutely positioned from live geometry, so they must be
    // repositioned when the page scrolls or resizes (capture catches the scroll of
    // the conversation pane, which does not bubble).
    window.addEventListener("scroll", this.onViewportChange, true);
    window.addEventListener("resize", this.onViewportChange);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.removeEventListener("focusin", this.onFocusin);
    this.removeEventListener("focusout", this.onFocusout);
    document.removeEventListener("selectionchange", this.onSelectionChange);
    window.removeEventListener("scroll", this.onViewportChange, true);
    window.removeEventListener("resize", this.onViewportChange);
    if (this.recaretRaf !== 0) cancelAnimationFrame(this.recaretRaf);
  }

  // onViewportChange repositions remote carets on scroll/resize, rAF-throttled, and
  // only when some are shown (idle blips do no work).
  private recaretRaf = 0;
  private hadRemoteCarets = false; // whether the last render drew any (to clear once)
  private onViewportChange = (): void => {
    if (this.remoteCarets.length === 0 || this.recaretRaf !== 0) return;
    this.recaretRaf = requestAnimationFrame(() => {
      this.recaretRaf = 0;
      this.renderRemoteCarets();
    });
  };

  // renderRemoteCarets (re)builds the colored caret bars + selection highlights for
  // peers focused on THIS blip, positioned from offsetToDom geometry. Offsets are raw
  // (not OT-transformed), so they are clamped to the current doc and may be briefly
  // stale after a local edit until the peer re-publishes — by design (07-presence §1).
  private renderRemoteCarets(): void {
    const overlay = this.renderRoot.querySelector<HTMLElement>(".remote-carets");
    if (overlay === null) return;
    overlay.replaceChildren();
    if (this.remoteCarets.length === 0) return;
    const docLen = this.proj.length;
    const oRect = overlay.getBoundingClientRect();
    const clampedDomPos = (off: number): { node: Node; offset: number } | null =>
      this.offsetToDom(Math.max(0, Math.min(off, docLen)));

    for (const rc of this.remoteCarets) {
      if (rc.focus < 0) continue;
      const fpos = clampedDomPos(rc.focus);
      if (fpos === null) continue;
      const r = document.createRange();
      try {
        r.setStart(fpos.node, fpos.offset);
        r.collapse(true);
      } catch {
        continue;
      }
      let rect = r.getBoundingClientRect();
      if (rect.height === 0) {
        // A collapsed range in an empty line can return a zero-size rect; fall back to
        // the enclosing element's box so the caret still shows on the right line.
        const el = fpos.node.nodeType === Node.ELEMENT_NODE ? (fpos.node as HTMLElement) : fpos.node.parentElement;
        const pr = el?.getBoundingClientRect();
        if (pr !== undefined) rect = pr;
      }

      // Selection highlight when anchor != focus.
      if (rc.anchor >= 0 && rc.anchor !== rc.focus) {
        const lo = clampedDomPos(Math.min(rc.anchor, rc.focus));
        const hi = clampedDomPos(Math.max(rc.anchor, rc.focus));
        if (lo !== null && hi !== null) {
          const selR = document.createRange();
          try {
            selR.setStart(lo.node, lo.offset);
            selR.setEnd(hi.node, hi.offset);
            for (const cr of Array.from(selR.getClientRects())) {
              const hl = document.createElement("div");
              hl.className = "remote-sel";
              hl.style.cssText =
                `top:${cr.top - oRect.top}px;left:${cr.left - oRect.left}px;` +
                `width:${cr.width}px;height:${cr.height}px;background:${rc.color};opacity:0.18;`;
              overlay.appendChild(hl);
            }
          } catch {
            /* a bad range just skips the highlight; the caret bar still renders */
          }
        }
      }

      const bar = document.createElement("div");
      bar.className = "remote-caret";
      bar.style.cssText =
        `top:${rect.top - oRect.top}px;left:${rect.left - oRect.left}px;` +
        `height:${rect.height || 18}px;background:${rc.color};`;
      const flag = document.createElement("div");
      flag.className = "remote-caret-flag";
      flag.textContent = rc.name;
      flag.style.background = rc.color;
      bar.appendChild(flag);
      overlay.appendChild(bar);
    }
  }

  // willUpdate captures the caret before a re-render so it survives updates that
  // are not local edits — an ack settling or a remote delta replacing DOM nodes
  // would otherwise drop the caret. A local edit has already set pendingCaret or
  // pendingSelection, which take precedence. Runs while the DOM still matches the
  // current proj.
  protected override willUpdate(): void {
    if (this.pendingCaret !== null || this.pendingSelection !== null) return;
    // Only preserve the caret across a re-render while we hold focus. Restoring it
    // into an UNFOCUSED editor (updated() calls selection.addRange, which focuses the
    // contenteditable) would re-grab focus the user just gave away — defeating blur
    // (and the focusout caret-clear, which a re-grab would immediately re-publish).
    if (!this.hasFocus) {
      this.savedCaret = null;
      this.savedSelection = null;
      return;
    }
    const range = currentRange(this);
    if (range === null) {
      this.savedCaret = null;
      this.savedSelection = null;
      return;
    }
    // A non-collapsed selection (both ends inside this editor): save the full range so
    // it — and the toolbar tracking it — survives the re-render. Otherwise save the
    // collapsed caret as before.
    const sel = window.getSelection();
    if (
      sel !== null &&
      !sel.isCollapsed &&
      sel.anchorNode !== null &&
      this.contains(sel.anchorNode) &&
      sel.focusNode !== null &&
      this.contains(sel.focusNode)
    ) {
      const a = this.domToOffset(sel.anchorNode, sel.anchorOffset);
      const f = this.domToOffset(sel.focusNode, sel.focusOffset);
      if (a !== null && f !== null) {
        this.savedSelection = { anchor: a, focus: f };
        this.savedCaret = null;
        return;
      }
    }
    this.savedSelection = null;
    this.savedCaret = this.domToOffset(range.startContainer, range.startOffset);
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
        .blip-doc { outline: none; font: 15px/1.6 system-ui, sans-serif; }
        /* pre-wrap lives on the paragraph (where the text is), not the container:
           on the container it made stray whitespace text nodes between block-level
           paragraphs render as a visible leading gap (B1). */
        /* pre-wrap preserves spaces and wraps at spaces; overflow-wrap also breaks a
           single long unbreakable run (a long URL or word) so it never overflows the
           blip width horizontally. */
        .blip-doc .para { min-height: 1.6em; white-space: pre-wrap; overflow-wrap: break-word; }
        .blip-doc .wave-link { color: #1565c0; text-decoration: underline; }
        .blip-doc .wave-mention { color: #3949ab; }
        .blip-doc .wave-mention-self { background: #fff3cd; border-radius: 3px; padding: 0 2px; font-weight: 600; }
        .blip-doc .reply-anchor { user-select: none; cursor: pointer; }
        .blip-doc .reply-anchor::after { content: "💬"; font-size: 0.8em; opacity: 0.55; margin-left: 1px; }
        /* inline-block (not block): a mid-text image stays on its line so the caret
           positions around it correctly; block would split the sentence onto its own line. */
        .blip-doc .wave-image { display: inline-block; margin: 0 2px; vertical-align: bottom; user-select: none; }
        .blip-doc .wave-image img { max-width: 100%; max-height: 320px; border-radius: 6px; vertical-align: bottom; }
        /* Remote-caret overlay: an absolutely-positioned, non-interactive layer over
           the text. It sits OUTSIDE .blip-doc (a stray element inside the contenteditable
           would corrupt the caret↔offset mapping); positions are computed in updated(). */
        .remote-carets { position: absolute; inset: 0; pointer-events: none; overflow: visible; }
        .remote-caret { position: absolute; width: 2px; border-radius: 1px; }
        .remote-caret-flag {
          position: absolute; top: -2px; left: 0; transform: translateY(-100%);
          font: 600 10px system-ui, sans-serif; color: #fff; white-space: nowrap;
          padding: 1px 4px; border-radius: 3px 3px 3px 0; line-height: 1.2;
        }
        .remote-sel { position: absolute; border-radius: 2px; }
      </style>
      ${keyed(
        this.renderKey,
        html`<div
          class="blip-doc"
          contenteditable="true"
          spellcheck="false"
          role="textbox"
          aria-multiline="true"
          aria-label="Blip content"
          @beforeinput=${this.onBeforeInput}
          @keydown=${this.onKeydown}
          @paste=${this.onPaste}
          @compositionstart=${this.onCompositionStart}
          @compositionend=${this.onCompositionEnd}
        >${this.proj.paragraphs.map((p) => renderParagraph(p, this.selfAddress))}</div>`,
      )}
      <div class="remote-carets" aria-hidden="true"></div>
    `;
  }

  protected override updated(): void {
    // Reposition remote carets after every render (the DOM/text just changed), but
    // skip the work for blips nobody is caretted in — which is most of them on each
    // keystroke. Still render once when the last peer leaves, to clear the overlay.
    if (this.remoteCarets.length > 0 || this.hadRemoteCarets) {
      this.renderRemoteCarets();
      this.hadRemoteCarets = this.remoteCarets.length > 0;
    }
    // Restore a non-collapsed selection: pendingSelection (after B/I) takes priority,
    // then savedSelection (a live selection captured across a non-edit re-render).
    const restoreSel = this.pendingSelection ?? this.savedSelection;
    if (restoreSel !== null) {
      this.pendingSelection = null;
      this.savedSelection = null;
      this.pendingCaret = null;
      this.savedCaret = null;
      // Clamp to the (possibly shrunk) doc: a non-edit re-render may be a peer delta
      // that removed text, leaving saved offsets past the end. Unclamped, offsetToDom
      // falls back to the last paragraph's START (offset 0) — silently mis-placing the
      // selection. Clamping maps an out-of-range offset to the true document end.
      const docLen = this.proj.length;
      const sel = {
        anchor: Math.max(0, Math.min(restoreSel.anchor, docLen)),
        focus: Math.max(0, Math.min(restoreSel.focus, docLen)),
      };
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
  paragraphEnd: 0,
  items: [],
  spans: [],
  anchors: [],
  images: [],
};

function renderParagraph(p: Paragraph, selfAddress: string): TemplateResult {
  const style = paragraphStyle(p);
  // Render the paragraph's inline content (text runs + reply/image widgets) in DOCUMENT
  // ORDER, so a widget sits at its true position rather than being appended at the end.
  // An empty paragraph still needs a <br/> so the line has height + a caret target.
  if (p.items.length === 0) return html`<div class="para" style=${style}><br /></div>`;
  const parts = p.items.map((it) => {
    if (it.kind === "text") return renderSpan(it.span, selfAddress);
    if (it.kind === "reply") return renderReplyAnchor(it.id);
    return renderImage(it.attachment);
  });
  return html`<div class="para" style=${style}>${parts}</div>`;
}

// renderReplyAnchor renders the inline-reply 💬 marker: a non-editable span carrying
// NO text node (the glyph is CSS ::after), so it adds zero document text. It declares
// its document weight via data-doc-items="2" (elementStart + elementEnd) — the single
// contract the DOM↔doc caret walk uses to count it, so a mid-text anchor keeps the
// caret↔offset mapping exact. Clicking it opens the comment sheet for its thread.
function renderReplyAnchor(id: string): TemplateResult {
  return html`<span
    class="reply-anchor"
    data-thread-id=${id}
    data-doc-items="2"
    contenteditable="false"
    title="Go to inline reply"
    @mousedown=${(e: MouseEvent) => {
      // preventDefault so clicking the glyph doesn't move the editor caret; dispatch a
      // bubbling event <wave-conversation> turns into opening the comment sheet for this
      // thread. focus:false — tapping to READ should not steal focus / raise the keyboard.
      e.preventDefault();
      (e.currentTarget as HTMLElement).dispatchEvent(
        new CustomEvent<{ id: string; focus: boolean }>("anchor-activate", {
          detail: { id, focus: false },
          bubbles: true,
          composed: true,
        }),
      );
    }}
  ></span>`;
}

// renderImage renders an inline image: a non-editable span (NO text node) wrapping an
// <img> from the attachment URL. Same caret-safety + data-doc-items="2" contract as the
// reply anchor, so it occupies its 2 doc items at its true mid-text position.
function renderImage(attachment: string): TemplateResult {
  return html`<span class="wave-image" data-doc-items="2" contenteditable="false"
    ><img src=${"/attachments/" + attachment} alt="attachment"
  /></span>`;
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
  // underline and strikethrough are modeled as independent boolean annotations so they
  // toggle separately, but both render to the single `text-decoration` CSS property — so
  // collect their tokens and emit one combined declaration.
  const decoration: string[] = [];
  for (const [prop, value] of Object.entries(styles)) {
    if (prop === "underline" && value === "true") decoration.push("underline");
    else if (prop === "strikethrough" && value === "true") decoration.push("line-through");
    else if (prop === "textDecoration") decoration.push(value); // legacy/combined value
    else decls.push(`${camelToKebab(prop)}:${value}`);
  }
  if (decoration.length > 0) decls.push(`text-decoration:${decoration.join(" ")}`);
  return decls.join(";");
}

function camelToKebab(s: string): string {
  return s.replace(/[A-Z]/g, (m) => "-" + m.toLowerCase());
}

// --- DOM caret helpers ---

function currentRange(host: HTMLElement): Range | null {
  const sel = window.getSelection();
  if (sel === null || sel.rangeCount === 0) return null;
  const r = sel.getRangeAt(0);
  // Only honor selections inside this editor.
  if (!host.contains(r.startContainer)) return null;
  return r;
}

// isWidget reports whether a node is an inline-element widget (reply/image) — an
// element that declares its document weight via data-doc-items. Such a widget is
// ATOMIC: the caret walk counts it as its declared item count and never descends into
// it (it carries no document text of its own).
function isWidget(n: Node): n is HTMLElement {
  return n.nodeType === Node.ELEMENT_NODE && (n as HTMLElement).dataset["docItems"] !== undefined;
}

// docItemCount is the number of DOC ITEMS a DOM node contributes: a text node → its
// rune count; a widget element → its declared data-doc-items (e.g. 2 = elementStart +
// elementEnd); a decoration element (styled span / wave-link / wave-mention, no
// data-doc-items) → the rune count of the text it wraps; comments (Lit markers) and
// everything else → 0. (Counting a Lit marker comment as text once misplaced the caret.)
function docItemCount(n: Node): number {
  if (n.nodeType === Node.TEXT_NODE) return runeCount(n.textContent ?? "");
  if (n.nodeType === Node.ELEMENT_NODE) {
    const el = n as HTMLElement;
    const declared = el.dataset["docItems"];
    if (declared !== undefined) return Number.parseInt(declared, 10) || 0;
    return runeCount(el.textContent ?? "");
  }
  return 0;
}

// docItemsBefore computes the doc-item offset of (node, domOffset) WITHIN a paragraph
// element: the sum of doc items (text runes + widget item-counts) that precede the
// point in document order. This is the item-aware successor to a pure rune count — it
// is what lets a mid-text widget shift the caret↔offset mapping by its 2 items.
function docItemsBefore(para: HTMLElement, node: Node, domOffset: number): number {
  let items = 0;
  let done = false;

  const countBeforeChildIndex = (el: Node, n: number): void => {
    const kids = Array.from(el.childNodes);
    for (let i = 0; i < n && i < kids.length; i++) items += docItemCount(kids[i]!);
  };

  const walk = (n: Node): void => {
    if (done) return;
    if (n === node) {
      if (n.nodeType === Node.TEXT_NODE) {
        items += runeCount((n.textContent ?? "").slice(0, domOffset));
      } else if (isWidget(n)) {
        // Caret on the widget element itself: atomic → resolve to BEFORE the widget (add
        // nothing), the same rule as a caret parked on a descendant of the widget below.
        // A real "after the widget" caret lands on .para at the widget's childIndex+1
        // (counted via countBeforeChildIndex), not here.
      } else {
        countBeforeChildIndex(n, domOffset);
      }
      done = true;
      return;
    }
    if (isWidget(n)) {
      // A widget is atomic. If the caret is parked INSIDE it (some browsers do this for
      // a contenteditable=false node), resolve to before the widget (add nothing) and
      // stop — never descend. Otherwise we are passing it: add its declared items.
      if (n.contains(node)) {
        done = true;
        return;
      }
      items += docItemCount(n);
      return;
    }
    if (n.nodeType === Node.TEXT_NODE) {
      items += runeCount(n.textContent ?? "");
      return;
    }
    for (const c of Array.from(n.childNodes)) {
      walk(c);
      if (done) return;
    }
  };

  if (node === para) {
    countBeforeChildIndex(para, domOffset);
    return items;
  }
  for (const c of Array.from(para.childNodes)) {
    walk(c);
    if (done) break;
  }
  return items;
}

// domAtDocOffset finds the DOM (node, offset) at a doc-item offset within a paragraph
// element (the inverse of docItemsBefore). It walks the paragraph's direct children in
// order, accounting for widget items: a target landing before a widget resolves to the
// caret position just before it (on the paragraph, at the widget's child index); after
// it, to the next position; an interior offset (only from a stale remote offset) clamps
// to before. Decoration elements are descended into to reach the exact text node.
function domAtDocOffset(para: HTMLElement, itemOff: number): { node: Node; offset: number } {
  let acc = 0;
  const children = Array.from(para.childNodes);
  for (let i = 0; i < children.length; i++) {
    const child = children[i]!;
    if (child.nodeType === Node.TEXT_NODE) {
      const r = runeCount(child.textContent ?? "");
      if (itemOff <= acc + r) return { node: child, offset: utf16Index(child.textContent ?? "", itemOff - acc) };
      acc += r;
    } else if (isWidget(child)) {
      const n = docItemCount(child);
      if (itemOff < acc + n) return { node: para, offset: i }; // before (or interior → clamp before)
      acc += n;
    } else if (child.nodeType === Node.ELEMENT_NODE) {
      const r = runeCount((child as Element).textContent ?? ""); // decoration: its wrapped text
      if (itemOff <= acc + r) return domAtTextOffset(child, itemOff - acc);
      acc += r;
    }
    // comments / other: 0 items
  }
  return { node: para, offset: children.length }; // exhausted → end of paragraph
}

// domAtTextOffset finds the DOM (text node, utf16 offset) at a rune offset within a
// subtree known to contain only text (no widgets) — used to descend into a decoration
// element. Falls back to the subtree root start if there is no text.
function domAtTextOffset(root: Node, runeOff: number): { node: Node; offset: number } {
  let remaining = runeOff;
  const texts: Text[] = [];
  collectTextNodes(root, texts);
  for (const t of texts) {
    const len = runeCount(t.data);
    if (remaining <= len) return { node: t, offset: utf16Index(t.data, remaining) };
    remaining -= len;
  }
  return { node: root, offset: 0 };
}

function collectTextNodes(n: Node, out: Text[]): void {
  for (const c of Array.from(n.childNodes)) {
    if (c.nodeType === Node.TEXT_NODE) out.push(c as Text);
    else collectTextNodes(c, out);
  }
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
