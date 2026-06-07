// <wave-conversation> — the top-level editor: it owns the OptimisticClient for
// one wavelet, reads the conversation manifest, and renders the root thread as a
// recursive <wave-thread>/<wave-blip> tree. It also provides the ConvController
// the tree edits through (content edits, continue-thread, reply-to-blip), and
// bootstraps an empty wavelet into a one-blip conversation.
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
import { compose } from "../wave/compose.ts";
import { debugEnabled } from "../wave/debug.ts";
import {
  appendBlipToRootThread,
  appendBlipToThread,
  emptyManifest,
  initialBlipContent,
  newBlipID,
  readManifest,
  replyToBlip as buildReplyOp,
} from "../wave/conversation.ts";
import { MANIFEST_ID, ROOT_BLIP_ID, addParticipantOp, blipContentOp } from "./controller.ts";
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

  declare status: string;
  declare rev: number; // bumped on every client change to force a re-render

  private client: OptimisticClient | null = null;
  private author: Participant = "";
  private controller: ConvController | null = null;
  private bootstrapAttempted = false;

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
    this.author = participant(this.user);
    const sep = this.url.includes("?") ? "&" : "?";
    const url = `${this.url}${sep}user=${encodeURIComponent(this.user)}`;
    const client = new OptimisticClient(url, name, this.author);
    this.client = client;
    this.controller = this.makeController(client);
    if (debugEnabled()) {
      // Expose for console poking: window.__wave.debugState(), .version(), etc.
      (globalThis as unknown as { __wave?: OptimisticClient }).__wave = client;
    }
    client.onChange(() => {
      this.rev++;
      this.maybeBootstrap();
    });
    try {
      await client.open();
      this.status = `connected as ${this.user}`;
      this.rev++;
      this.maybeBootstrap();
    } catch (e) {
      this.status = `error: ${String(e)}`;
    }
  }

  // maybeBootstrap creates the conversation manifest + a root blip if the wavelet
  // has none yet. It attempts this at most once per client. NOTE: this is not
  // safe against two clients cold-starting the same empty wavelet simultaneously
  // (both would create a manifest, producing a malformed two-root document). The
  // realistic flow is sequential (one client creates, others join); a robust fix
  // is server-side seeding of the conversation, deferred to the auth/access work.
  private maybeBootstrap(): void {
    if (this.bootstrapAttempted) return;
    const client = this.client;
    if (client === null) return;
    if (client.blipContent(MANIFEST_ID) !== undefined) {
      this.bootstrapAttempted = true; // already exists; nothing to do
      return;
    }
    this.bootstrapAttempted = true;
    const manifestInit = compose(emptyManifest(), appendBlipToRootThread(emptyManifest(), ROOT_BLIP_ID));
    void client.submit([
      addParticipantOp(this.author, this.author),
      blipContentOp(this.author, MANIFEST_ID, manifestInit),
      blipContentOp(this.author, ROOT_BLIP_ID, initialBlipContent()),
    ]);
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
      replyToBlip: (parentId, inline) => {
        void client.submitWith((blip) => {
          const manifest = blip(MANIFEST_ID);
          if (manifest === undefined) return [];
          const id = newBlipID();
          return [
            blipContentOp(author, MANIFEST_ID, buildReplyOp(manifest, parentId, id, inline)),
            blipContentOp(author, id, initialBlipContent()),
          ];
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
    wave-conversation .continue-btn {
      font: 11px system-ui, sans-serif;
      color: #4060c0;
      background: none;
      border: none;
      padding: 2px 4px;
      cursor: pointer;
    }
    wave-conversation .reply-btn:hover,
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
