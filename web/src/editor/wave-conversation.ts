// <wave-conversation> — the top-level editor: it owns the OptimisticClient for
// one wavelet, reads the conversation manifest, and renders the root thread as a
// recursive <wave-thread>/<wave-blip> tree. It also provides the ConvController
// the tree edits through (content edits, continue-thread, reply-to-blip). The
// conversation is seeded server-side at first open (no client-side bootstrap).
//
// Re-render model: the manifest and every blip's content live in the client's
// optimistic replica, not in Lit reactive state. We subscribe to the client's
// change notifications and bump a `rev` counter; that re-renders the whole light-
// DOM tree, which re-reads the manifest and each blip's content fresh. The tree
// is small, and <blip-view> preserves the caret across re-renders, so a full
// re-render per change is fine.
//
// Non-decorator Lit + light DOM (so nested contenteditable selection is reachable).

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import { DocOp, WaveletName, participant } from "../wave/types.ts";
import type { Participant } from "../wave/types.ts";
import { OptimisticClient } from "../wave/transport.ts";
import { debugEnabled } from "../wave/debug.ts";
import {
  appendBlipToThread,
  initialBlipContent,
  insertReplyAnchor,
  newBlipID,
  readManifest,
  replyToBlip as buildReplyOp,
} from "../wave/conversation.ts";
import { MANIFEST_ID, addParticipantOp, blipContentOp } from "./controller.ts";
import type { ConvController } from "./controller.ts";
import "./wave-thread.ts";

export class WaveConversation extends LitElement {
  static override properties = {
    status: { state: true },
    rev: { state: true },
  };

  url = "";
  wave = "";
  user = "";
  // Optional hook the app shell sets to learn when this wave's replica changes
  // (e.g. to refresh the inbox digest). Read lazily on each replica change (which
  // only fires after open() resolves), so binding order vs. connect does not matter.
  onChange: (() => void) | null = null;

  declare status: string;
  declare rev: number; // bumped on every client change to force a re-render

  private client: OptimisticClient | null = null;
  private author: Participant = "";
  private controller: ConvController | null = null;

  constructor() {
    super();
    this.status = "connecting…";
    this.rev = 0;
  }

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  /** The OptimisticClient, or null until connected. For debug tooling only. */
  getClient(): OptimisticClient | null {
    return this.client;
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
      name = WaveletName.parse(this.wave);
    } catch (e) {
      this.status = `bad wavelet name: ${String(e)}`;
      return;
    }
    try {
      this.author = participant(this.user); // throws on an invalid/empty address
    } catch (e) {
      this.status = `bad identity: ${String(e)}`;
      return;
    }
    // Identity rides the session cookie on the WebSocket handshake (same origin),
    // so the URL carries no ?user= — the server resolves the participant from the
    // cookie. The conversation is seeded server-side at first open, so there is no
    // client-side bootstrap (and no cold-start double-manifest race).
    const client = new OptimisticClient(this.url, name, this.author);
    this.client = client;
    this.controller = this.makeController(client);
    if (debugEnabled()) {
      // Expose for console poking: window.__wave.debugState(), .version(), etc.
      (globalThis as unknown as { __wave?: OptimisticClient }).__wave = client;
    }
    client.onChange(() => {
      this.rev++;
      this.onChange?.();
    });
    try {
      await client.open();
      this.status = `connected as ${this.user}`;
      this.rev++;
    } catch (e) {
      this.status = `error: ${String(e)}`;
    }
  }

  private makeController(client: OptimisticClient): ConvController {
    const author = this.author;
    return {
      user: author,
      blipContent: (id) => client.blipContent(id) ?? DocOp.empty(),
      editBlip: (id, ops) => {
        void client.submit([blipContentOp(author, id, new DocOp(ops))]);
      },
      continueThread: (threadId) => {
        void client.submitWith((blip) => {
          const manifest = blip(MANIFEST_ID);
          if (manifest === undefined) return [];
          const id = newBlipID();
          return [
            blipContentOp(author, MANIFEST_ID, appendBlipToThread(manifest, threadId, id)),
            blipContentOp(author, id, initialBlipContent()),
          ];
        });
      },
      replyToBlip: (parentId, inline, anchorOffset) => {
        void client.submitWith((blip) => {
          const manifest = blip(MANIFEST_ID);
          if (manifest === undefined) return [];
          const id = newBlipID();
          const ops = [
            blipContentOp(author, MANIFEST_ID, buildReplyOp(manifest, parentId, id, inline)),
            blipContentOp(author, id, initialBlipContent()),
          ];
          // For an inline reply, also anchor it in the parent blip body at the
          // requested line boundary (clamped to the current content length, since
          // submitWith re-reads the live blip at submit time).
          if (inline && anchorOffset !== undefined) {
            const parentBody = blip(parentId);
            if (parentBody !== undefined) {
              // Clamp to before the final </body> so the anchor stays inside the
              // body (documentLength() itself is past it).
              const at = Math.max(0, Math.min(anchorOffset, parentBody.documentLength() - 1));
              ops.push(blipContentOp(author, parentId, insertReplyAnchor(parentBody, id, at)));
            }
          }
          return ops;
        });
      },
      participants: () => client.participants(),
      addParticipant: (addr: string) => {
        const p = participant(addr); // throws on invalid address
        void client.submit([addParticipantOp(author, p)]);
      },
    };
  }

  protected override render(): TemplateResult {
    // rev is a reactive dependency: bumping it re-runs this render, which re-reads
    // the manifest and (via the controller) each blip's content from the client.
    void this.rev;
    const manifest = this.client?.blipContent(MANIFEST_ID);
    const controller = this.controller;

    let body: TemplateResult;
    if (manifest === undefined || controller === null) {
      body = html`<p class="conv-empty">No conversation yet…</p>`;
    } else {
      try {
        const m = readManifest(manifest);
        body = html`<wave-thread .thread=${m.rootThread} .controller=${controller}></wave-thread>`;
      } catch (e) {
        body = html`<p class="conv-error">malformed manifest: ${String(e)}</p>`;
      }
    }

    const roster = controller !== null ? this._renderRoster(controller) : html``;

    return html`
      ${STYLES}
      <div class="conv-bar">${this.status}</div>
      ${roster}
      ${body}
    `;
  }

  private _renderRoster(controller: ConvController): TemplateResult {
    const parts = controller.participants().slice().sort();
    const onAdd = (e: Event): void => {
      e.preventDefault();
      const form = e.currentTarget as HTMLFormElement;
      const input = form.querySelector<HTMLInputElement>(".add-participant-input");
      if (input === null) return;
      const val = input.value.trim();
      if (val === "") return;
      try {
        controller.addParticipant(val);
        input.value = "";
      } catch {
        // invalid address — shake the input briefly to signal error without crashing
        input.classList.add("add-participant-error");
        setTimeout(() => input.classList.remove("add-participant-error"), 600);
      }
    };
    return html`
      <div class="conv-roster">
        <span class="roster-label">Participants:</span>
        ${parts.map((p) => html`<span class="roster-chip">${p}</span>`)}
        <form class="add-participant-form" @submit=${onAdd}>
          <input class="add-participant-input" type="text" placeholder="user@domain" autocomplete="off" />
          <button type="submit" class="add-participant-btn">+ Add</button>
        </form>
      </div>
    `;
  }
}

customElements.define("wave-conversation", WaveConversation);

// Light-DOM styles for the whole tree. Scoped by element/class names since there
// is no shadow root. Threads indent their replies; blips are bordered cards.
const STYLES = html`
  <style>
    wave-conversation .conv-bar {
      font: 12px system-ui, sans-serif;
      color: #555;
      margin-bottom: 10px;
    }
    wave-conversation .conv-empty,
    wave-conversation .conv-error {
      font: 13px system-ui, sans-serif;
      color: #888;
    }
    wave-conversation .wave-thread.reply {
      margin-left: 20px;
      border-left: 2px solid #e0e0e0;
      padding-left: 12px;
    }
    wave-conversation .wave-thread.inline {
      border-left-color: #4060c0;
      background: #f7f9ff;
      border-radius: 0 6px 6px 0;
    }
    wave-conversation .wave-blip {
      margin: 6px 0;
    }
    wave-conversation blip-view {
      display: block;
      border: 1px solid #ddd;
      border-radius: 6px;
      padding: 8px 10px;
      background: #fff;
    }
    wave-conversation .blip-actions {
      margin: 2px 0 0;
    }
    wave-conversation .reply-btn,
    wave-conversation .reply-inline-btn,
    wave-conversation .continue-btn {
      font: 11px system-ui, sans-serif;
      color: #4060c0;
      background: none;
      border: none;
      padding: 2px 4px;
      cursor: pointer;
    }
    wave-conversation .reply-btn:hover,
    wave-conversation .reply-inline-btn:hover,
    wave-conversation .continue-btn:hover {
      text-decoration: underline;
    }
    wave-conversation .thread-actions {
      margin: 4px 0 8px;
    }
    wave-conversation .conv-roster {
      display: flex;
      align-items: center;
      flex-wrap: wrap;
      gap: 4px;
      margin-bottom: 10px;
      font: 12px system-ui, sans-serif;
    }
    wave-conversation .roster-label {
      color: #555;
      margin-right: 2px;
    }
    wave-conversation .roster-chip {
      background: #e8eaf6;
      color: #3949ab;
      border-radius: 12px;
      padding: 1px 8px;
      font-size: 11px;
    }
    wave-conversation .add-participant-form {
      display: inline-flex;
      align-items: center;
      gap: 2px;
      margin-left: 4px;
    }
    wave-conversation .add-participant-input {
      font: 11px system-ui, sans-serif;
      border: 1px solid #ccc;
      border-radius: 4px;
      padding: 1px 6px;
      width: 140px;
    }
    wave-conversation .add-participant-input.add-participant-error {
      border-color: #c62828;
      background: #fff8f8;
    }
    wave-conversation .add-participant-btn {
      font: 11px system-ui, sans-serif;
      color: #4060c0;
      background: none;
      border: 1px solid #4060c0;
      border-radius: 4px;
      padding: 1px 6px;
      cursor: pointer;
    }
    wave-conversation .add-participant-btn:hover {
      background: #e8eeff;
    }
  </style>
`;
