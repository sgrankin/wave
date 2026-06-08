// <comment-sheet> — the overlay that shows one inline comment thread, opened by tapping
// a 💬 anchor / comment pill (or auto-opened when a comment is created). Inline comment
// threads are NOT rendered in the document flow; they live here.
//
// Layout follows the document-editor consensus for mobile comment threads (Google Docs,
// Notion, Quip, Word — a focused near-full-height surface, NOT a shallow peek sheet):
//   - TOUCH (phone): a FULL-HEIGHT sheet covering the visual viewport — header (grabber
//     + title) / scrolling body (the thread) / footer (Done). Going full-height kills
//     the three bottom-sheet bugs structurally: there is no exposed backdrop for touch
//     scroll to leak into, and no gap between the sheet bottom and the keyboard top to
//     jitter. It is anchored to the VISUAL VIEWPORT via a `translateY(offsetTop)`
//     transform + a `height` from visualViewport (composited, smooth, keyboard-aware),
//     not a recomputed `bottom` (which jitters because offsetTop mixes page-scroll into
//     the keyboard offset). The background scroll is locked while open.
//   - MOUSE (desktop): a centered bottom card over a dim backdrop.
//
// It is a viewport-fixed overlay, never interleaved with the contenteditable text, so it
// cannot perturb the rune-offset caret mapping. Rendered by <wave-conversation> (not a
// document-root singleton) so it re-renders live with the conversation tree.

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
    coarse: { state: true },
  };

  declare thread: Thread | null;
  declare controller: ConvController | null;
  declare autoFocus: boolean;
  declare private coarse: boolean; // touch → full-height sheet; mouse → centered card

  onClose: (() => void) | null = null;

  // Auto-focus once per open (the parent re-supplies a fresh thread object each render).
  private didAutoFocus = false;
  private focusPending = false;
  private focusRaf = 0;
  private focusTries = 0;
  // Background scroll-lock: the scrollable ancestor whose overflow we pin while open.
  private lockedScroller: HTMLElement | null = null;
  private prevOverflow = "";

  constructor() {
    super();
    this.thread = null;
    this.controller = null;
    this.autoFocus = false;
    this.coarse = (navigator.maxTouchPoints ?? 0) > 0 || window.matchMedia("(any-pointer: coarse)").matches;
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
    this.lockBackgroundScroll();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    document.removeEventListener("keydown", this.onKeydown);
    window.visualViewport?.removeEventListener("resize", this.onViewportChange);
    window.visualViewport?.removeEventListener("scroll", this.onViewportChange);
    if (this.focusRaf !== 0) cancelAnimationFrame(this.focusRaf);
    this.unlockBackgroundScroll();
  }

  private onKeydown = (e: KeyboardEvent): void => {
    if (e.key === "Escape") this.close();
  };

  private onViewportChange = (): void => this.reposition();

  // lockBackgroundScroll pins the nearest scrollable ancestor (the conversation pane)
  // so the page behind the sheet cannot scroll — combined with overscroll-behavior on
  // the sheet body, this stops touch scroll from leaking out behind the sheet.
  private lockBackgroundScroll(): void {
    let el: HTMLElement | null = this.parentElement;
    while (el !== null) {
      const oy = getComputedStyle(el).overflowY;
      if (oy === "auto" || oy === "scroll") {
        this.lockedScroller = el;
        break;
      }
      el = el.parentElement;
    }
    if (this.lockedScroller !== null) {
      this.prevOverflow = this.lockedScroller.style.overflow;
      this.lockedScroller.style.overflow = "hidden";
    }
  }

  private unlockBackgroundScroll(): void {
    if (this.lockedScroller !== null) {
      this.lockedScroller.style.overflow = this.prevOverflow;
      this.lockedScroller = null;
    }
  }

  private close = (): void => this.onClose?.();

  private onBackdrop = (e: MouseEvent): void => {
    if (e.target === e.currentTarget) this.close();
  };

  protected override updated(): void {
    this.reposition();
    // Auto-focus the reply input ONCE on open (a created comment). The nested
    // wave-thread→wave-blip→blip-view renders in later frames, so retry until it exists.
    if (!this.didAutoFocus && !this.focusPending && this.autoFocus && this.thread !== null) {
      this.focusPending = true;
      this.focusTries = 0;
      this.tryAutoFocus();
    }
  }

  // reposition anchors the TOUCH sheet to the visual viewport: full visible height, with
  // a translateY(offsetTop) so it tracks the keyboard/viewport without jitter. (Desktop
  // is centered by CSS; clear any inline geometry there.)
  private reposition(): void {
    const panel = this.querySelector<HTMLElement>(".cs-panel");
    if (panel === null) return;
    if (!this.coarse) {
      panel.style.height = "";
      panel.style.transform = "";
      return;
    }
    const vv = window.visualViewport;
    if (vv === null) return;
    panel.style.height = `${Math.round(vv.height)}px`;
    panel.style.transform = `translateY(${Math.round(vv.offsetTop)}px)`;
  }

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
      this.focusPending = false;
    }
  }

  protected override render(): TemplateResult {
    if (this.thread === null || this.controller === null) return html``;
    const panel = html`
      <div class="cs-panel" role="dialog" aria-modal="true" aria-label="Comment thread">
        <div class="cs-head">
          ${this.coarse ? html`<span class="cs-grabber" aria-hidden="true"></span>` : html``}
          <span class="cs-title">💬 Comment</span>
        </div>
        <div class="cs-body">
          <wave-thread .thread=${this.thread} .controller=${this.controller}></wave-thread>
        </div>
        <div class="cs-foot">
          <button class="cs-done" @click=${this.close}>Done</button>
        </div>
      </div>
    `;
    return html`
      ${STYLES}
      <div class=${"cs-backdrop" + (this.coarse ? " coarse" : "")} @mousedown=${this.onBackdrop}>${panel}</div>
    `;
  }
}

customElements.define("comment-sheet", CommentSheet);

// Light-DOM styles scoped by tag name.
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
      touch-action: none; /* swallow drags on the backdrop so the page can't scroll */
    }
    comment-sheet .cs-panel {
      position: relative;
      background: #fff;
      display: flex;
      flex-direction: column;
      min-height: 0;
      max-height: 70vh;
      border-radius: 14px 14px 0 0;
      box-shadow: 0 -6px 24px rgba(0, 0, 0, 0.25);
      animation: cs-rise 0.18s ease-out;
    }
    @keyframes cs-rise {
      from {
        opacity: 0;
      }
      to {
        opacity: 1;
      }
    }
    /* Desktop: a centered bottom card. */
    @media (min-width: 640px) {
      comment-sheet .cs-backdrop:not(.coarse) {
        align-items: center;
      }
      comment-sheet .cs-backdrop:not(.coarse) .cs-panel {
        width: 560px;
        max-width: calc(100vw - 32px);
        border-radius: 14px;
        margin-bottom: 24px;
      }
    }
    /* TOUCH: a full-height sheet covering the visual viewport (height + translateY set in
       JS). No exposed backdrop → no scroll leak; no gap to the keyboard → no jitter. */
    comment-sheet .cs-backdrop.coarse {
      background: rgba(0, 0, 0, 0.12);
    }
    /* Absolutely anchored to the viewport top; JS sets height = visualViewport.height and
       transform = translateY(offsetTop) so the panel fills exactly the visible area above
       the keyboard, tracking it smoothly (composited transform, no bottom-offset jitter). */
    comment-sheet .cs-backdrop.coarse .cs-panel {
      position: absolute;
      top: 0;
      left: 0;
      width: 100%;
      max-height: none;
      border-radius: 0;
    }
    comment-sheet .cs-head {
      position: relative;
      flex: none;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 10px 14px;
      border-bottom: 1px solid #eee;
    }
    comment-sheet .cs-grabber {
      position: absolute;
      top: 6px;
      left: 50%;
      transform: translateX(-50%);
      width: 36px;
      height: 4px;
      border-radius: 2px;
      background: #d0d0d0;
    }
    comment-sheet .cs-title {
      font: 600 14px system-ui, sans-serif;
      color: #333;
    }
    comment-sheet .cs-body {
      flex: 1 1 auto;
      min-height: 0;
      padding: 8px 14px 14px;
      overflow-y: auto;
      overscroll-behavior: contain; /* don't chain scroll out to the page (Safari 16+) */
      -webkit-overflow-scrolling: touch;
      scroll-padding-bottom: 64px; /* keep a focused reply visible above the footer */
    }
    /* Sticky footer (outside the scroll area) so "Done" is always reachable, even for a
       long thread. Done closes the sheet — the comment is saved live as you type. */
    comment-sheet .cs-foot {
      flex: none;
      display: flex;
      justify-content: flex-end;
      gap: 8px;
      padding: 10px 14px calc(10px + env(safe-area-inset-bottom));
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
    /* The thread inside the sheet is already the comment context; strip its in-flow
       left rule/indent. */
    comment-sheet .cs-body .wave-thread.reply {
      margin-left: 0;
      border-left: none;
      padding-left: 0;
      background: none;
    }
  </style>
`;
