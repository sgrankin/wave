// <wave-editor> — a minimal collaborative flat-text editor element. It owns an
// OptimisticClient for one wavelet, renders one blip's text in a contenteditable
// region, turns local edits into ops (diff-based), and reconciles remote edits
// back into the DOM (preserving the caret heuristically). First cut: flat text,
// one blip; rich structure/formatting and multiple blips come later.
//
// Uses Lit's NON-decorator API (static properties + customElements.define) so the
// source stays erasable-syntax-only (no experimental decorators). Renders into the
// light DOM (createRenderRoot returns this) so window.getSelection() works for the
// contenteditable caret without shadow-DOM selection quirks.

import { LitElement, html } from "lit";
import type { PropertyValues } from "lit";

import { CONTRIBUTOR_ADD, DocOp, WaveletName, participant } from "../wave/types.ts";
import type { Component, Operation } from "../wave/types.ts";
import { OptimisticClient } from "../wave/transport.ts";
import { blipText, diffToContentOp, shiftCursor } from "./doctext.ts";

const BLIP_ID = "main"; // single-blip MVP

export class WaveEditor extends LitElement {
  // Only `status` is reactive (drives re-render). url/wave/user are set once by
  // main.ts before the element is connected, so they are plain fields.
  static override properties = {
    status: { state: true },
  };

  url = "";
  wave = "";
  user = "";

  declare status: string;

  private client: OptimisticClient | null = null;
  private lastText = ""; // the text the DOM currently shows (== optimistic blip text)
  private applying = false; // true while we mutate the DOM, to ignore the resulting input event

  constructor() {
    super();
    this.status = "connecting…";
  }

  // Render into the light DOM so the contenteditable's selection is reachable via
  // window.getSelection() (shadow-DOM selection is non-portable).
  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    void this.start();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.client?.close();
    this.client = null;
  }

  private async start(): Promise<void> {
    let name: WaveletName;
    try {
      name = WaveletName.parse(this.wave); // 4-token serialized wavelet name
    } catch (e) {
      this.status = `bad wavelet name: ${String(e)}`;
      return;
    }
    const sep = this.url.includes("?") ? "&" : "?";
    const url = `${this.url}${sep}user=${encodeURIComponent(this.user)}`;
    const client = new OptimisticClient(url, name, participant(this.user));
    this.client = client;
    client.onChange(() => this.reconcile());
    try {
      await client.open();
      this.status = `connected as ${this.user}`;
      this.reconcile();
    } catch (e) {
      this.status = `error: ${String(e)}`;
    }
  }

  // reconcile pulls the optimistic blip text into the DOM if it differs from what
  // is shown, preserving the caret across the change.
  private reconcile(): void {
    if (this.client === null) return;
    const content = this.client.blipContent(BLIP_ID);
    const text = content === undefined ? "" : blipText(content);
    if (text === this.lastText) return;
    const doc = this.docEl();
    if (doc === null) {
      this.lastText = text;
      return;
    }
    const caret = this.getCaret(doc);
    const newCaret = caret === null ? null : shiftCursor(caret, this.lastText, text);
    this.applying = true;
    doc.textContent = text;
    this.lastText = text;
    if (newCaret !== null) this.setCaret(doc, newCaret);
    this.applying = false;
  }

  // onInput turns the current DOM text into an op diffed against the last known
  // text and submits it. Programmatic updates set `applying` and are ignored.
  private onInput = (): void => {
    if (this.applying || this.client === null) return;
    const doc = this.docEl();
    if (doc === null) return;
    const text = doc.textContent ?? "";
    const comps = diffToContentOp(this.lastText, text);
    if (comps.length === 0) return;
    this.lastText = text;
    void this.client.submit(this.blipOp(comps));
  };

  private blipOp(comps: Component[]): Operation[] {
    return [
      {
        kind: "blip",
        blipId: BLIP_ID,
        op: {
          ctx: { creator: participant(this.user), timestamp: Date.now(), versionIncrement: 1, hashedVersion: null },
          contentOp: new DocOp(comps),
          method: CONTRIBUTOR_ADD,
        },
      },
    ];
  }

  private docEl(): HTMLElement | null {
    return this.querySelector<HTMLElement>(".doc");
  }

  // --- caret helpers (single text node; offsets in UTF-16 code units, which match
  // runes for the common case; surrogate-pair caret positioning is approximate) ---

  private getCaret(doc: HTMLElement): number | null {
    const s = window.getSelection();
    if (s === null || s.rangeCount === 0) return null;
    const r = s.getRangeAt(0);
    if (!doc.contains(r.startContainer)) return null;
    return r.startOffset;
  }

  private setCaret(doc: HTMLElement, offset: number): void {
    const node: Node = doc.firstChild ?? doc;
    const max = node.textContent?.length ?? 0;
    const pos = Math.max(0, Math.min(offset, max));
    const r = document.createRange();
    try {
      r.setStart(node, pos);
    } catch {
      return;
    }
    r.collapse(true);
    const s = window.getSelection();
    if (s !== null) {
      s.removeAllRanges();
      s.addRange(r);
    }
  }

  protected override firstUpdated(_changed: PropertyValues): void {
    const doc = this.docEl();
    if (doc !== null) doc.addEventListener("input", this.onInput);
  }

  protected override render(): unknown {
    return html`
      <style>
        wave-editor .bar { font: 12px system-ui, sans-serif; color: #555; margin-bottom: 6px; }
        wave-editor .doc {
          min-height: 8em; border: 1px solid #bbb; border-radius: 6px; padding: 10px;
          font: 15px/1.5 ui-monospace, monospace; white-space: pre-wrap; outline: none;
        }
        wave-editor .doc:focus { border-color: #4a90d9; }
      </style>
      <div class="bar">${this.status}</div>
      <div class="doc" contenteditable="true" spellcheck="false"></div>
    `;
  }
}

customElements.define("wave-editor", WaveEditor);
