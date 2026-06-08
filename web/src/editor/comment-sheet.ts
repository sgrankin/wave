// <comment-sheet> — a bottom-sheet overlay showing one inline comment thread, opened
// by tapping that thread's 💬 anchor in the blip text (or auto-opened when a comment
// is created). Inline comments are NOT rendered in the document flow; they live here.
//
// Why a sheet: on a phone the primary job is reading + commenting on a (possibly long)
// doc, and the thread must be reachable in context without scrolling to the blip's
// bottom. A sheet is the native-mobile pattern and, crucially, is a viewport-fixed
// overlay that is NEVER interleaved with the contenteditable text — so it cannot
// perturb the rune-offset caret mapping the editor depends on.
//
// Rendered by <wave-conversation> (not a document-root singleton) so it re-renders
// live with the conversation tree: a reply added to the open thread appears at once.
//
// Keyboard-aware: the panel's bottom rides the on-screen keyboard (via the
// VisualViewport API) so its reply input is never hidden behind the keyboard.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import type { Thread } from "../wave/conversation.ts";
import type { ConvController } from "./controller.ts";
import "./wave-thread.ts";

export class CommentSheet extends LitElement {
  static override properties = {
    thread: { attribute: false },
    controller: { attribute: false },
    autoFocus: { attribute: false },
    kbInset: { state: true },
  };

  // The open thread (null ⇒ render nothing). Re-supplied each conversation render, so
  // the sheet stays in sync as the thread gains replies.
  declare thread: Thread | null;
  declare controller: ConvController | null;
  // When true (the sheet was opened by CREATING a comment), focus the reply input on
  // open so the user can type immediately. When false (opened by tapping to READ), do
  // not steal focus / raise the keyboard.
  declare autoFocus: boolean;
  // Pixels the on-screen keyboard occludes at the viewport bottom; the panel sits above it.
  declare private kbInset: number;

  // onClose is set by the parent to dismiss the sheet (clears the open thread).
  onClose: (() => void) | null = null;

  // Auto-focus discipline. We focus the reply input ONCE per open. We must NOT key
  // this off "thread changed": the parent re-supplies a fresh `thread` object every
  // conversation re-render, so that would re-grab focus on every keystroke. The sheet
  // remounts per open, so these instance flags reset correctly each time.
  private didAutoFocus = false; // already focused (true ⇒ never focus again this open)
  private focusPending = false; // a focus-retry loop is in flight
  private focusRaf = 0; // the pending retry frame (cancelled on disconnect)
  private focusTries = 0;

  constructor() {
    super();
    this.thread = null;
    this.controller = null;
    this.autoFocus = false;
    this.kbInset = 0;
  }

  // Light DOM: the nested <blip-view> editors are contenteditable and need the page's
  // selection to reach them (same reason as the rest of the editor tree).
  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    document.addEventListener("keydown", this.onKeydown);
    window.visualViewport?.addEventListener("resize", this.onViewportChange);
    window.visualViewport?.addEventListener("scroll", this.onViewportChange);
    this.measureKeyboard();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    document.removeEventListener("keydown", this.onKeydown);
    window.visualViewport?.removeEventListener("resize", this.onViewportChange);
    window.visualViewport?.removeEventListener("scroll", this.onViewportChange);
    if (this.focusRaf !== 0) cancelAnimationFrame(this.focusRaf);
  }

  private onKeydown = (e: KeyboardEvent): void => {
    if (e.key === "Escape") this.close();
  };

  private onViewportChange = (): void => this.measureKeyboard();

  // measureKeyboard records how much the on-screen keyboard occludes at the bottom
  // (the gap between the layout viewport bottom and the visual viewport bottom).
  private measureKeyboard(): void {
    const vv = window.visualViewport;
    const inset = vv === null ? 0 : Math.max(0, window.innerHeight - (vv.offsetTop + vv.height));
    if (Math.abs(inset - this.kbInset) > 1) this.kbInset = inset;
  }

  private close = (): void => this.onClose?.();

  // onBackdrop closes only when the press is on the backdrop itself, not the panel.
  private onBackdrop = (e: MouseEvent): void => {
    if (e.target === e.currentTarget) this.close();
  };

  protected override updated(): void {
    // On open of a freshly-created comment, focus the reply input so the user can type
    // (which raises the keyboard → measureKeyboard lifts the panel above it). Start the
    // retry loop once; it focuses exactly once (didAutoFocus) and stops.
    if (!this.didAutoFocus && !this.focusPending && this.autoFocus && this.thread !== null) {
      this.focusPending = true;
      this.focusTries = 0;
      this.tryAutoFocus();
    }
  }

  // tryAutoFocus focuses the reply input (the last .blip-doc in the sheet). The nested
  // wave-thread → wave-blip → blip-view chain renders in LATER microtasks/frames, so
  // the input may not exist on the sheet's first updated() — retry across a few frames
  // until it appears (then focus once and stop).
  private tryAutoFocus(): void {
    this.focusRaf = 0;
    if (this.didAutoFocus || !this.isConnected) {
      this.focusPending = false;
      return;
    }
    const docs = this.querySelectorAll<HTMLElement>(".blip-doc");
    const target = docs[docs.length - 1]; // the new (empty) comment blip is last
    if (target !== undefined) {
      this.didAutoFocus = true;
      this.focusPending = false;
      target.focus();
      return;
    }
    if (this.focusTries++ < 15) {
      this.focusRaf = requestAnimationFrame(() => this.tryAutoFocus());
    } else {
      this.focusPending = false; // give up rather than spin forever
    }
  }

  protected override render(): TemplateResult {
    if (this.thread === null || this.controller === null) return html``;
    return html`
      ${STYLES}
      <div class="cs-backdrop" @mousedown=${this.onBackdrop}>
        <div
          class="cs-panel"
          role="dialog"
          aria-modal="true"
          aria-label="Comment thread"
          style=${`bottom:${this.kbInset}px`}
        >
          <div class="cs-head">
            <span class="cs-title">💬 Comment</span>
          </div>
          <div class="cs-body">
            <wave-thread .thread=${this.thread} .controller=${this.controller}></wave-thread>
          </div>
          <div class="cs-foot">
            <button class="cs-done" @click=${this.close}>Done</button>
          </div>
        </div>
      </div>
    `;
  }
}

customElements.define("comment-sheet", CommentSheet);

// Light-DOM styles scoped by tag name. The backdrop is a viewport-fixed dim layer; the
// panel is bottom-anchored (lifted above the keyboard via the inline `bottom`).
const STYLES = html`
  <style>
    comment-sheet .cs-backdrop {
      position: fixed;
      inset: 0;
      z-index: 1100;
      background: rgba(0, 0, 0, 0.32);
      display: flex;
      flex-direction: column;
      justify-content: flex-end;
    }
    comment-sheet .cs-panel {
      position: relative;
      background: #fff;
      border-radius: 14px 14px 0 0;
      box-shadow: 0 -6px 24px rgba(0, 0, 0, 0.25);
      max-height: 70vh;
      display: flex;
      flex-direction: column;
      animation: cs-slide-up 0.18s ease-out;
      padding-bottom: env(safe-area-inset-bottom);
    }
    /* On a wide screen, cap the width and center it (a centered bottom card). */
    @media (min-width: 640px) {
      comment-sheet .cs-backdrop {
        align-items: center;
      }
      comment-sheet .cs-panel {
        width: 560px;
        max-width: calc(100vw - 32px);
        border-radius: 14px;
        margin-bottom: 24px;
      }
    }
    @keyframes cs-slide-up {
      from {
        transform: translateY(12px);
        opacity: 0;
      }
      to {
        transform: translateY(0);
        opacity: 1;
      }
    }
    comment-sheet .cs-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 10px 14px;
      border-bottom: 1px solid #eee;
      flex: none;
    }
    comment-sheet .cs-title {
      font: 600 14px system-ui, sans-serif;
      color: #333;
    }
    comment-sheet .cs-body {
      padding: 8px 14px 14px;
      overflow-y: auto;
      -webkit-overflow-scrolling: touch;
    }
    /* Sticky footer (outside the scroll area) so "Done" is always reachable, even for a
       long thread. "Done" closes the sheet — the comment is already saved live (edits
       sync as you type), so there is no separate "save" step. */
    comment-sheet .cs-foot {
      flex: none;
      display: flex;
      justify-content: flex-end;
      gap: 8px;
      padding: 10px 14px;
      border-top: 1px solid #eee;
    }
    comment-sheet .cs-done {
      font: 600 14px system-ui, sans-serif;
      color: #fff;
      background: #4060c0;
      border: none;
      border-radius: 8px;
      padding: 9px 20px;
      cursor: pointer;
    }
    comment-sheet .cs-done:hover {
      background: #36509c;
    }
    comment-sheet .cs-done:focus-visible {
      outline: 2px solid #9db4ff;
      outline-offset: 2px;
    }
    /* The thread inside the sheet is already the comment context; its left rule/indent
       (from the in-flow .reply styling) is redundant here. */
    comment-sheet .cs-body .wave-thread.reply {
      margin-left: 0;
      border-left: none;
      padding-left: 0;
      background: none;
    }
  </style>
`;
