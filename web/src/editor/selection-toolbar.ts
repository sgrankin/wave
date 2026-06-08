// <selection-toolbar> — a single floating formatting + comment bar that appears
// over the current text selection inside any <blip-view>, anywhere in the editor.
//
// Why a global singleton instead of a per-blip toolbar: the old in-flow toolbar sat
// at the top of each blip, so on a long blip it scrolled out of reach, and it
// reserved space / cluttered every blip. This one is `position: fixed`, mounted once
// at the document root, and self-driving: it watches the document selection and pops
// up at the selection (Medium/Docs style). It carries NO editable content and is not
// a descendant of any `.blip-doc`, so it cannot perturb the caret↔offset mapping the
// controlled editor depends on.
//
// It reaches the right editor by climbing the DOM from the selection node (the whole
// tree is light DOM): format commands go to the enclosing <blip-view> via its public
// applyCommand(); "Comment" goes to the enclosing <wave-blip> via commentInline().
// Buttons preventDefault their mousedown/pointerdown so tapping them never blurs the
// editor or collapses the selection (touchstart is deliberately NOT used — see
// keepSelection).
//
// Touch vs. mouse: on a coarse pointer the bar docks to the bottom of the viewport
// instead of floating at the selection — a floating bar there collides with the
// native selection callout (Cut/Copy/Paste) and is awkward to hit; a bottom bar is
// the familiar mobile pattern and stays clear of the callout.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import type { BlipView } from "./blip-view.ts";
import type { WaveBlip } from "./wave-blip.ts";

// Gap (px) between the selection and the floating bar.
const GAP = 8;
// Viewport edge padding (px) so the bar never touches the window edge.
const EDGE = 8;

// isTouchPrimary reports whether this is a touch device (→ bottom-docked bar). maxTouchPoints
// is the reliable discriminator (Mac trackpad = 0; iPhone/iPad > 0, even in iPad desktop-mode
// where matchMedia lies); any-pointer:coarse is the live fallback / paired-mouse signal.
function isTouchPrimary(mql: MediaQueryList): boolean {
  return (navigator.maxTouchPoints ?? 0) > 0 || mql.matches;
}

export class SelectionToolbar extends LitElement {
  static override properties = {
    visible: { state: true },
    states: { state: true },
    coarse: { state: true },
    collapsed: { state: true },
  };

  declare private visible: boolean;
  declare private states: { bold: boolean; italic: boolean; lineType: string | null };
  declare private coarse: boolean; // touch layout (bottom-docked) vs. floating
  // Whether the current selection is collapsed (a caret, no range). The bar still
  // shows — H1/H2/H3/list and Comment act on the caret's line — but B/I (which need a
  // range) are disabled.
  declare private collapsed: boolean;

  // The editor the current selection lives in, resolved on each refresh. Commands
  // dispatch to these directly (the bar is fixed at the document root, so events
  // from it would not bubble into the editor).
  private blipView: BlipView | null = null;
  private waveBlip: WaveBlip | null = null;
  // The selection's viewport rect, captured at refresh and used to position the bar
  // in updated() (after the bar has rendered, so its own size is measurable).
  private selRect: DOMRect | null = null;
  // The host blip-view's rect; for a collapsed caret the bar is centered over the blip
  // (stable) rather than over the caret (which would jitter as you type).
  private hostRect: DOMRect | null = null;
  private raf = 0;
  private mql: MediaQueryList | null = null;

  constructor() {
    super();
    this.visible = false;
    this.states = { bold: false, italic: false, lineType: null };
    this.coarse = false;
    this.collapsed = false;
  }

  // Light DOM (matches the rest of the editor; styles are scoped by tag name).
  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    // Touch detection: navigator.maxTouchPoints is the reliable signal (a Mac trackpad
    // is 0; iPhone/iPad are >0), since matchMedia("(pointer: coarse)") is stale/unreliable
    // on iOS (esp. iPad desktop-mode). any-pointer:coarse is the live fallback (and lets a
    // paired mouse flip it). Re-evaluated on the mql change event below.
    this.mql = window.matchMedia("(any-pointer: coarse)");
    this.coarse = isTouchPrimary(this.mql);
    this.mql.addEventListener("change", this.onPointerKindChange);
    document.addEventListener("selectionchange", this.onSelectionChange);
    // Focus changes show/hide the bar: focusin into an editor reveals it for a caret;
    // focusout hides it (a blur may not fire selectionchange, so we can't rely on that).
    document.addEventListener("focusin", this.onSelectionChange);
    document.addEventListener("focusout", this.onSelectionChange);
    // Capture: catch the conversation pane's scroll (which does not bubble) so the
    // floating bar tracks the selection as the user scrolls.
    window.addEventListener("scroll", this.onViewportChange, true);
    window.addEventListener("resize", this.onViewportChange);
    // The on-screen keyboard shrinks the VISUAL viewport (not the layout viewport):
    // its resize/scroll events fire when the keyboard shows/hides, so the touch bar
    // can re-anchor just above the keyboard instead of hiding behind it.
    window.visualViewport?.addEventListener("resize", this.onViewportChange);
    window.visualViewport?.addEventListener("scroll", this.onViewportChange);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.mql?.removeEventListener("change", this.onPointerKindChange);
    document.removeEventListener("selectionchange", this.onSelectionChange);
    document.removeEventListener("focusin", this.onSelectionChange);
    document.removeEventListener("focusout", this.onSelectionChange);
    window.removeEventListener("scroll", this.onViewportChange, true);
    window.removeEventListener("resize", this.onViewportChange);
    window.visualViewport?.removeEventListener("resize", this.onViewportChange);
    window.visualViewport?.removeEventListener("scroll", this.onViewportChange);
    if (this.raf !== 0) cancelAnimationFrame(this.raf);
  }

  private onPointerKindChange = (e: MediaQueryListEvent): void => {
    this.coarse = (navigator.maxTouchPoints ?? 0) > 0 || e.matches;
  };

  private onSelectionChange = (): void => this.schedule();
  private onViewportChange = (): void => {
    if (this.visible) this.schedule();
  };

  // schedule coalesces selection/scroll/resize bursts into one refresh per frame.
  private schedule(): void {
    if (this.raf !== 0) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      this.refresh();
      // Reposition directly too: a pure viewport change (scroll, or the keyboard
      // opening) may not alter any reactive state, so updated() would not fire — but
      // the bar still needs to track the new geometry.
      this.reposition();
    });
  }

  // refresh reads the current selection, decides whether to show the bar, and (when
  // shown) resolves the host editor and captures the selection rect for positioning.
  private refresh(): void {
    const sel = window.getSelection();
    if (sel === null || sel.rangeCount === 0) {
      this.hide();
      return;
    }
    const range = sel.getRangeAt(0);
    const startEl =
      range.startContainer.nodeType === Node.TEXT_NODE
        ? range.startContainer.parentElement
        : (range.startContainer as Element);
    const blipDoc = startEl?.closest<HTMLElement>(".blip-doc") ?? null;
    if (blipDoc === null) {
      this.hide(); // selection is not inside an editor
      return;
    }
    // Only a selection wholly within one blip is actionable (no cross-blip ranges).
    if (!blipDoc.contains(range.endContainer)) {
      this.hide();
      return;
    }
    const collapsed = sel.isCollapsed;
    // Only while the editor is actually focused — otherwise a lingering selection or
    // caret (after the editor blurred) would keep the bar up. (contains() is true for
    // the element itself, so a focused .blip-doc counts.)
    if (!blipDoc.contains(document.activeElement)) {
      this.hide();
      return;
    }
    const blipView = startEl?.closest<HTMLElement>("blip-view") as BlipView | null;
    const waveBlip = startEl?.closest<HTMLElement>("wave-blip") as WaveBlip | null;
    if (blipView === null) {
      this.hide();
      return;
    }
    const rect = range.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) {
      this.hide(); // a zero-size rect (e.g. selection across a hidden node): nothing to anchor to
      return;
    }
    this.blipView = blipView;
    this.waveBlip = waveBlip;
    this.selRect = rect;
    this.hostRect = blipView.getBoundingClientRect();
    this.collapsed = collapsed;
    this.states = blipView.commandStates();
    this.visible = true;
  }

  private hide(): void {
    if (!this.visible) return;
    this.visible = false;
    this.blipView = null;
    this.waveBlip = null;
    this.selRect = null;
  }

  // run applies a format command (or creates a comment) against the resolved editor.
  // Called from a button click; the button already preventDefaulted mousedown/
  // pointerdown, so the selection is intact. applyCommand restores the selection
  // through the blip-view's own re-render path (so the bar stays put for a follow-up
  // command); creating a comment leaves the selection alone too.
  private run(cmd: string): void {
    if (cmd === "comment") {
      this.waveBlip?.commentInline();
      return;
    }
    this.blipView?.applyCommand(cmd);
  }

  // keepSelection stops a button press from blurring the editor / collapsing the
  // selection. Bound to mousedown AND pointerdown — NOT touchstart: preventDefault on
  // touchstart suppresses the whole emulated mouse sequence INCLUDING the click, which
  // would make every button dead on touch. preventDefault on mousedown (desktop, plus
  // iOS's emulated mousedown) and pointerdown (pen/touch) preserves the selection
  // without canceling the click that actually runs the command.
  private keepSelection = (e: Event): void => {
    e.preventDefault();
  };

  protected override updated(): void {
    this.reposition();
  }

  // reposition places the (host) bar. Coarse/touch: a full-width bar pinned to the TOP
  // of the visual viewport. The top is the one uncrowded zone on iOS — the selection
  // callout (Copy/Look Up/…) sits at the selection mid-screen, and the keyboard + its
  // native accessory/format bars own the bottom; a top bar competes with neither (and
  // is stable whether or not the keyboard is up, since the keyboard shrinks the bottom).
  // Fine/mouse: float centered above the selection, flipping below if there's no room.
  private reposition(): void {
    if (!this.visible) {
      this.style.left = "";
      this.style.top = "";
      this.style.width = "";
      return;
    }
    if (this.coarse) {
      const vv = window.visualViewport;
      this.style.left = `${Math.round(vv ? vv.offsetLeft : 0)}px`;
      this.style.width = `${Math.round(vv ? vv.width : window.innerWidth)}px`;
      this.style.top = `${Math.round(vv ? vv.offsetTop : 0)}px`; // top of the visible viewport
      return;
    }
    this.style.width = ""; // auto-width for the floating bubble
    if (this.selRect === null) {
      this.style.left = "";
      this.style.top = "";
      return;
    }
    // Float above the selection, clamped to the viewport; flip below if there's no
    // room above. Measured after render so the bar's own size is known. For a collapsed
    // caret, center the bar over the BLIP (stable) rather than over the caret — else it
    // would jump horizontally as you type.
    const tb = this.getBoundingClientRect();
    const sr = this.selRect;
    const centerX =
      this.collapsed && this.hostRect !== null
        ? this.hostRect.left + this.hostRect.width / 2
        : sr.left + sr.width / 2;
    let left = centerX - tb.width / 2;
    left = Math.max(EDGE, Math.min(left, window.innerWidth - tb.width - EDGE));
    let top = sr.top - tb.height - GAP;
    if (top < EDGE) top = sr.bottom + GAP; // no room above → below
    this.style.left = `${Math.round(left)}px`;
    this.style.top = `${Math.round(top)}px`;
  }

  protected override render(): TemplateResult {
    const s = this.states;
    const btn = (
      cmd: string,
      label: string,
      pressed: boolean,
      content: TemplateResult | string,
      disabled = false,
    ): TemplateResult =>
      html`<button
        data-cmd=${cmd}
        aria-label=${label}
        aria-pressed=${pressed ? "true" : "false"}
        ?disabled=${disabled}
        class=${pressed ? "pressed" : ""}
        @click=${() => this.run(cmd)}
      >
        ${content}
      </button>`;
    return html`
      ${STYLES}
      <div
        class=${"sel-toolbar" + (this.visible ? " visible" : "") + (this.coarse ? " coarse" : "")}
        role="toolbar"
        aria-label="Text formatting"
        @mousedown=${this.keepSelection}
        @pointerdown=${this.keepSelection}
      >
        ${btn("bold", "Bold", s.bold, html`<b>B</b>`, this.collapsed)}
        ${btn("italic", "Italic", s.italic, html`<i>I</i>`, this.collapsed)}
        ${btn("h1", "Heading 1", s.lineType === "h1", "H1")}
        ${btn("h2", "Heading 2", s.lineType === "h2", "H2")}
        ${btn("h3", "Heading 3", s.lineType === "h3", "H3")}
        ${btn("li", "Bullet list", s.lineType === "li", "•")}
        <span class="sep" aria-hidden="true"></span>
        ${btn("comment", "Comment", false, html`<span class="cmt">💬 Comment</span>`)}
      </div>
    `;
  }
}

customElements.define("selection-toolbar", SelectionToolbar);

// ensureSelectionToolbar mounts the singleton bar at the document root if it is not
// already present (idempotent). Called once at boot. Mounting on <body> (not inside
// the app subtree) keeps `position: fixed` anchored to the viewport regardless of
// any transformed ancestor.
export function ensureSelectionToolbar(): void {
  if (document.querySelector("selection-toolbar") !== null) return;
  document.body.appendChild(document.createElement("selection-toolbar"));
}

// Light-DOM styles, scoped by tag name (matching wave-conversation's approach). The
// host element itself is the fixed-positioned layer; the inner .sel-toolbar is the
// visible bar.
const STYLES = html`
  <style>
    selection-toolbar {
      position: fixed;
      top: 0;
      left: 0;
      z-index: 1000;
      /* The bar can float over the editor (it shows for a bare caret now), so it must
         NOT intercept clicks meant for the text — only its buttons are interactive.
         pointer-events: none here + auto on the buttons lets a click on the bar's
         background pass through to the editor underneath. */
      pointer-events: none;
    }
    selection-toolbar .sel-toolbar {
      display: none;
      align-items: center;
      gap: 2px;
      padding: 4px;
      background: #2b2f36;
      border-radius: 8px;
      box-shadow: 0 4px 14px rgba(0, 0, 0, 0.28);
      user-select: none;
      -webkit-user-select: none;
      pointer-events: none;
    }
    selection-toolbar .sel-toolbar.visible {
      display: inline-flex;
    }
    selection-toolbar .sel-toolbar button {
      font: 13px system-ui, sans-serif;
      line-height: 1;
      min-width: 28px;
      padding: 6px 8px;
      border: none;
      border-radius: 5px;
      background: transparent;
      color: #f0f2f5;
      cursor: pointer;
      pointer-events: auto; /* the bar is click-through; its buttons are not */
    }
    selection-toolbar .sel-toolbar button:hover {
      background: rgba(255, 255, 255, 0.14);
    }
    selection-toolbar .sel-toolbar button.pressed {
      background: #4060c0;
      color: #fff;
    }
    selection-toolbar .sel-toolbar button:disabled {
      opacity: 0.38;
      cursor: default;
      pointer-events: none; /* don't intercept editor clicks while inert */
    }
    selection-toolbar .sel-toolbar button:disabled:hover {
      background: transparent;
    }
    selection-toolbar .sel-toolbar button:focus-visible {
      outline: 2px solid #9db4ff;
      outline-offset: 1px;
    }
    selection-toolbar .sel-toolbar .cmt {
      white-space: nowrap;
    }
    selection-toolbar .sel-toolbar .sep {
      width: 1px;
      align-self: stretch;
      margin: 2px 3px;
      background: rgba(255, 255, 255, 0.22);
    }
    /* Touch: a full-width bar the host JS-positions at the TOP of the visual viewport
       (clear of the selection callout and the bottom keyboard/accessory bars). The bar
       fills the host's width (set in JS). */
    selection-toolbar .sel-toolbar.coarse.visible {
      display: flex;
      /* One line, never wrapped (user ask). "safe center" centers when it fits and
         left-aligns instead of clipping when it doesn't; overflow-x scrolls as the
         safety valve on a very narrow phone rather than wrapping to two rows. */
      flex-wrap: nowrap;
      justify-content: safe center;
      overflow-x: auto;
    }
    selection-toolbar .sel-toolbar.coarse {
      width: 100%;
      box-sizing: border-box;
      border-radius: 0 0 10px 10px; /* top bar: round the bottom corners */
      box-shadow: 0 4px 14px rgba(0, 0, 0, 0.28);
      padding: calc(7px + env(safe-area-inset-top)) calc(6px + env(safe-area-inset-right)) 7px
        calc(6px + env(safe-area-inset-left));
      gap: 3px;
    }
    selection-toolbar .sel-toolbar.coarse button {
      flex: 0 0 auto;
      min-width: 36px;
      min-height: 38px;
      padding: 6px 9px;
      font-size: 14px;
    }
  </style>
`;
