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
  insertImage,
  insertReplyAnchor,
  newBlipID,
  readManifest,
  replyToBlip as buildReplyOp,
} from "../wave/conversation.ts";
import { MANIFEST_ID, addParticipantOp, blipContentOp } from "./controller.ts";
import type { ConvController } from "./controller.ts";
import { colorFor, contactSuggestions, displayNameFor, profiles } from "../wave/profiles.ts";
import { avatar, participantChip } from "./participant.ts";
import { PresenceClient } from "../wave/presence.ts";
import type { RemoteCaret } from "./controller.ts";
import "./wave-thread.ts";

// TYPING_IDLE_MS: how long after the last edit we keep showing "typing".
const TYPING_IDLE_MS = 2000;

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
  private profilesUnsub: (() => void) | null = null;
  private presence: PresenceClient | null = null;
  private presenceUnsub: (() => void) | null = null;
  private typingTimer: ReturnType<typeof setTimeout> | null = null;
  // Our own presence state, the single source of truth for what we publish: which
  // blip we are focused on, whether we are typing, and our caret/selection offsets.
  // Kept here (not in the timer closures) so the typing-idle timer only flips the
  // typing flag and never resurrects a stale focused blip.
  private pres = { typing: false, blipId: "", anchor: -1, focus: -1 };

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
    // Re-render the whole tree when display names resolve, so the roster chips and
    // any @-mention tooltips humanize without waiting for the next edit.
    this.profilesUnsub = profiles.onChange(() => this.rev++);
    void this.start();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.profilesUnsub?.();
    this.profilesUnsub = null;
    this.presenceUnsub?.();
    this.presenceUnsub = null;
    this.presence?.close();
    this.presence = null;
    if (this.typingTimer !== null) clearTimeout(this.typingTimer);
    this.client?.close();
    this.client = null;
  }

  // publishPresence sends our current presence state (throttled by the client).
  private publishPresence(): void {
    this.presence?.setLocal(this.pres.typing, this.pres.blipId, this.pres.anchor, this.pres.focus);
  }

  // markTyping flags "typing in blipId" and arms a timer to clear the typing flag
  // after a short idle. The timer only flips the flag — the focused blip + caret are
  // whatever they currently are, so a focus change during the window is not undone.
  private markTyping(blipId: string): void {
    this.pres.typing = true;
    this.pres.blipId = blipId;
    this.publishPresence();
    if (this.typingTimer !== null) clearTimeout(this.typingTimer);
    this.typingTimer = setTimeout(() => {
      this.pres.typing = false;
      this.publishPresence();
    }, TYPING_IDLE_MS);
  }

  // setCaret records our caret/selection in blipId (from the focused <blip-view>) and
  // publishes it so peers can render it.
  private setCaret(blipId: string, anchor: number, focus: number): void {
    this.pres.blipId = blipId;
    this.pres.anchor = anchor;
    this.pres.focus = focus;
    this.publishPresence();
  }

  // remoteCaretsFor returns the peers currently caretted in blipId, ready to render
  // (offsets + the peer's avatar color and display name).
  private remoteCaretsFor(blipId: string): RemoteCaret[] {
    const peers = this.presence?.remotes() ?? [];
    return peers
      .filter((p) => p.blipId === blipId && p.focus >= 0)
      .map((p) => ({
        participant: p.participant,
        anchor: p.anchor,
        focus: p.focus,
        color: colorFor(p.participant),
        name: displayNameFor(p.participant, profiles.get(p.participant)),
      }));
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
    this.controller = this.makeController(client, name);
    if (debugEnabled()) {
      // Expose for console poking: window.__wave.debugState(), .version(), etc.
      (globalThis as unknown as { __wave?: OptimisticClient }).__wave = client;
    }
    client.onChange(() => {
      this.rev++;
      this.onChange?.();
    });
    // Presence rides a separate transient socket (/presence) — derived from the OT
    // socket URL — so it never perturbs the delta channel. It re-renders the tree on
    // remote changes (typing/joining/leaving).
    try {
      const pu = new URL(this.url);
      pu.pathname = "/presence";
      pu.search = `?wave=${encodeURIComponent(this.wave)}`;
      this.presence = new PresenceClient(pu.toString());
      this.presenceUnsub = this.presence.onChange(() => this.rev++);
      this.presence.connect();
    } catch {
      this.presence = null; // bad URL: presence is best-effort, the editor still works
    }
    try {
      await client.open();
      // Who you are is shown by the shell's identity widget; the bar carries only
      // connection state.
      this.status = "connected";
      this.rev++;
    } catch (e) {
      this.status = `error: ${String(e)}`;
    }
  }

  private makeController(client: OptimisticClient, name: WaveletName): ConvController {
    const author = this.author;
    return {
      user: author,
      blipContent: (id) => client.blipContent(id) ?? DocOp.empty(),
      editBlip: (id, ops) => {
        this.markTyping(id); // publish "typing in this blip" to the presence channel
        void client.submit([blipContentOp(author, id, new DocOp(ops))]);
      },
      setCaret: (blipId, anchor, focus) => this.setCaret(blipId, anchor, focus),
      remoteCaretsFor: (blipId) => this.remoteCaretsFor(blipId),
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
      attachImage: (blipId, file, offset) => {
        const wave = `${name.waveDomain}/${name.waveId}`;
        const wavelet = `${name.waveletDomain}/${name.waveletId}`;
        void (async () => {
          let id: string;
          try {
            const q = new URLSearchParams({
              wave,
              wavelet,
              filename: file.name,
              mime: file.type !== "" ? file.type : "application/octet-stream",
            });
            const resp = await fetch(`/attachments?${q.toString()}`, {
              method: "POST",
              body: file,
              credentials: "same-origin",
            });
            if (!resp.ok) return; // best-effort
            const body = (await resp.json()) as { id?: string };
            if (typeof body.id !== "string" || body.id === "") return;
            id = body.id;
          } catch {
            return; // best-effort
          }
          // Insert the inline image at the requested line boundary, clamped to
          // before the final </body> (submitWith re-reads the live blip).
          void client.submitWith((blip) => {
            const content = blip(blipId);
            if (content === undefined) return [];
            const at = Math.max(0, Math.min(offset, content.documentLength() - 1));
            return [blipContentOp(author, blipId, insertImage(content, id, at))];
          });
        })();
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
      <div class=${"conv-bar" + (/^(error|bad)/.test(this.status) ? " error" : "")}>${this.status}</div>
      ${roster}
      ${this._renderPresence()}
      ${body}
    `;
  }

  // _renderPresence shows the transient awareness line: an avatar per online peer
  // (dimmed unless typing) and a "… is typing" note. Best-effort — empty when no
  // peers are present. Names/avatars reuse the profile cache.
  private _renderPresence(): TemplateResult {
    const peers = this.presence?.remotes() ?? [];
    if (peers.length === 0) return html``;
    profiles.ensure(peers.map((p) => p.participant));
    const typing = peers
      .filter((p) => p.typing)
      .map((p) => displayNameFor(p.participant, profiles.get(p.participant)));
    return html`
      <div class="conv-presence" title="Who else is here">
        ${peers.map(
          (p) =>
            html`<span class="presence-peer ${p.typing ? "typing" : ""}" title=${p.participant}
              >${avatar(p.participant, profiles.get(p.participant), 16)}</span
            >`,
        )}
        ${typing.length > 0
          ? html`<span class="presence-typing"
              >${typing.join(", ")} ${typing.length === 1 ? "is" : "are"} typing…</span
            >`
          : html``}
      </div>
    `;
  }

  private _renderRoster(controller: ConvController): TemplateResult {
    const parts = controller.participants().slice().sort();
    // Resolve display names for the roster (one batched fetch); chips fall back to
    // the address until the cache "change" re-renders.
    profiles.ensure(parts);
    // Contact suggestions for the add box: known names minus the current
    // participants (shared helper, also unit-tested).
    const suggestions = contactSuggestions(profiles, parts);

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
        ${parts.map(
          (p) => html`<span class="roster-chip">${participantChip(p, profiles.get(p))}</span>`,
        )}
        <form class="add-participant-form" @submit=${onAdd}>
          <input
            class="add-participant-input"
            type="text"
            list="roster-contacts"
            aria-label="Add participant by address"
            placeholder="user@domain"
            autocomplete="off"
          />
          <datalist id="roster-contacts">
            ${suggestions.map(
              (p) =>
                html`<option value=${p.address} label=${displayNameFor(p.address, p)}></option>`,
            )}
          </datalist>
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
    /* The editor tree is custom elements, which default to display:inline and so
       shrink-wrap their content instead of filling the conversation pane (B2: the
       blip box "stayed tiny" and did not grow with the window). Make the structural
       elements block so each fills the available width down to the blip card. */
    wave-conversation,
    wave-conversation wave-thread,
    wave-conversation wave-blip {
      display: block;
    }
    /* The shell is full-window, but the conversation is a reading surface — cap it at
       a comfortable measure (long lines hurt readability; still ~2x the old fixed
       width). Left-aligned in the pane, after the nav, with the slack on the right —
       centering it would leave an awkward gap between the nav and the column. */
    wave-conversation {
      max-width: 820px;
    }
    wave-conversation .conv-bar {
      font: 12px system-ui, sans-serif;
      color: #555;
      margin-bottom: 10px;
      overflow-wrap: break-word;
    }
    wave-conversation .conv-bar.error {
      color: #c62828;
    }
    wave-conversation .conv-empty {
      font: 13px system-ui, sans-serif;
      color: #6a6a6a;
    }
    wave-conversation .conv-error {
      font: 13px system-ui, sans-serif;
      color: #c62828;
    }
    wave-conversation .conv-presence {
      display: flex;
      align-items: center;
      gap: 4px;
      min-height: 20px;
      margin-bottom: 10px;
      font: 12px system-ui, sans-serif;
      color: #4060c0;
    }
    wave-conversation .presence-peer {
      opacity: 0.45;
      transition: opacity 0.15s;
    }
    wave-conversation .presence-peer.typing {
      opacity: 1;
    }
    wave-conversation .presence-typing {
      font-style: italic;
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
      position: relative; /* containing block for the remote-caret overlay */
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
    wave-conversation .attach-btn,
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
    wave-conversation .attach-btn:hover,
    wave-conversation .continue-btn:hover {
      text-decoration: underline;
    }
    /* Borderless link-style buttons get a deliberate ring so keyboard focus is
       visible (the faint UA outline barely shows on a button with no border). */
    wave-conversation .reply-btn:focus-visible,
    wave-conversation .reply-inline-btn:focus-visible,
    wave-conversation .attach-btn:focus-visible,
    wave-conversation .continue-btn:focus-visible,
    wave-conversation .add-participant-btn:focus-visible {
      outline: 2px solid #4060c0;
      outline-offset: 1px;
      border-radius: 4px;
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
      display: inline-flex;
      align-items: center;
      background: #e8eaf6;
      color: #3949ab;
      border-radius: 12px;
      padding: 2px 8px 2px 3px;
      font-size: 11px;
      max-width: 200px;
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
      border-radius: 6px;
      padding: 2px 6px;
      width: 140px;
    }
    wave-conversation .add-participant-input:focus-visible {
      outline: none;
      border-color: #4060c0;
      box-shadow: 0 0 0 2px rgba(64, 96, 192, 0.18);
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
      border-radius: 6px;
      padding: 1px 6px;
      cursor: pointer;
    }
    wave-conversation .add-participant-btn:hover {
      background: #e8eeff;
    }
    wave-conversation .add-participant-btn:active {
      background: #d9e3ff;
    }
    /* Touch devices: give the small text-link actions a comfortable tap area without
       changing the desktop (mouse) look. */
    @media (pointer: coarse) {
      wave-conversation .reply-btn,
      wave-conversation .reply-inline-btn,
      wave-conversation .attach-btn,
      wave-conversation .continue-btn,
      wave-conversation .add-participant-btn {
        padding: 8px 10px;
      }
      wave-conversation .add-participant-input {
        padding: 7px 8px;
      }
    }
  </style>
`;
