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
  setBlipDeleted,
} from "../wave/conversation.ts";
import { invert } from "../wave/docop.ts";
import { MANIFEST_ID, addParticipantOp, blipContentOp, removeParticipantOp } from "./controller.ts";
import type { ConvController } from "./controller.ts";
import { fetchReadState, markBlipRead } from "../wave/api.ts";
import { colorFor, contactSuggestions, displayNameFor, profiles } from "../wave/profiles.ts";
import { avatar, participantChip } from "./participant.ts";
import { PresenceClient } from "../wave/presence.ts";
import type { RemoteCaret } from "./controller.ts";
import type { Thread } from "../wave/conversation.ts";
import "./wave-thread.ts";
import "./comment-sheet.ts";

// TYPING_IDLE_MS: how long after the last edit we keep showing "typing".
const TYPING_IDLE_MS = 2000;

export class WaveConversation extends LitElement {
  static override properties = {
    status: { state: true },
    rev: { state: true },
    commentThreadId: { state: true },
    commentAutoFocus: { state: true },
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
  // The open inline-comment thread id ("" ⇒ none), shown in the <comment-sheet>.
  declare commentThreadId: string;
  // Whether to focus the sheet's reply input on open (true when opened by creating a
  // comment; false when opened by tapping an anchor to read).
  declare commentAutoFocus: boolean;

  private client: OptimisticClient | null = null;
  private author: Participant = "";
  private controller: ConvController | null = null;
  private profilesUnsub: (() => void) | null = null;
  private presence: PresenceClient | null = null;
  private presenceUnsub: (() => void) | null = null;
  private statusUnsub: (() => void) | null = null;
  private typingTimer: ReturnType<typeof setTimeout> | null = null;
  // The signed-in participant's per-blip read versions (blipId → version read
  // through), fetched once when the wave opens and advanced locally as blips are
  // viewed. The authoritative side of the unread comparison is the client's
  // per-blip last-modified version; a blip is unread when last-modified exceeds
  // this. Absent ⇒ never read (version 0).
  private blipReads = new Map<string, number>();
  // Our own presence state, the single source of truth for what we publish: which
  // blip we are focused on, whether we are typing, and our caret/selection offsets.
  // Kept here (not in the timer closures) so the typing-idle timer only flips the
  // typing flag and never resurrects a stale focused blip.
  private pres = { typing: false, blipId: "", anchor: -1, focus: -1 };

  constructor() {
    super();
    this.status = "connecting…";
    this.rev = 0;
    this.commentThreadId = "";
    this.commentAutoFocus = false;
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
    // Inline-comment anchors (the 💬 in blip text) bubble an "anchor-activate" event;
    // open the matching thread in the comment sheet. (Bubbles from any depth.)
    this.addEventListener("anchor-activate", this.onAnchorActivate as EventListener);
    void this.start();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.removeEventListener("anchor-activate", this.onAnchorActivate as EventListener);
    this.profilesUnsub?.();
    this.profilesUnsub = null;
    this.presenceUnsub?.();
    this.presenceUnsub = null;
    this.statusUnsub?.();
    this.statusUnsub = null;
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

  // onAnchorActivate opens an inline-comment thread (the anchor id equals the thread
  // id) in the bottom sheet. detail.focus requests the reply input be focused (set
  // when a comment was just created, so the user can type immediately).
  private onAnchorActivate = (e: CustomEvent<{ id: string; focus?: boolean }>): void => {
    this.commentThreadId = e.detail.id;
    this.commentAutoFocus = e.detail.focus === true;
  };

  private closeComment = (): void => {
    this.commentThreadId = "";
    this.commentAutoFocus = false;
  };

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
    // Reflect the live connection state honestly (connecting / connected /
    // reconnecting / offline) — the transport reconnects silently otherwise.
    this.statusUnsub = client.onStatus(() => this.applyConnStatus());
    this.applyConnStatus();
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
    } catch {
      // A fatal or close-before-open surfaces via the connection-status indicator
      // (applyConnStatus, driven by onStatus); the setup errors above already set
      // this.status. Nothing else to do here.
    }
    this.applyConnStatus();
    this.rev++;
    // Fetch the participant's per-blip read versions so the unread markers paint on
    // load (a blip is unread until its read version catches up to its last-modified
    // version). Best-effort: a failure just leaves everything looking read. Fetched
    // ONCE per open (this element's lifetime): a mid-session resyncReset rebuilds the
    // client's last-modified map from full history but does NOT re-fetch reads — that
    // is intentional, since this session's locally-advanced reads (held in blipReads)
    // already suppress unread for what was viewed, and re-fetching could only lose a
    // mark whose POST had not yet landed.
    void fetchReadState(this.wave)
      .then((reads) => {
        // Merge by max per blip rather than replace: a blip the participant viewed
        // (mark-on-view) before this fetch resolved already advanced its local read
        // version, and the server's older value must not undo that. Read versions
        // only ever advance, so max is the correct reconciliation.
        for (const [blipId, v] of reads) {
          if (v > (this.blipReads.get(blipId) ?? 0)) this.blipReads.set(blipId, v);
        }
        this.rev++;
      })
      .catch(() => {
        /* best-effort: no read markers rather than an error */
      });
  }

  // applyConnStatus maps the client's live connection state to the status-bar text.
  // (The bad-name / bad-identity setup errors set before the client is created are
  // not overwritten — applyConnStatus only runs once a client exists.)
  private applyConnStatus(): void {
    switch (this.client?.connectionStatus()) {
      case "connecting":
        this.status = "connecting…";
        break;
      case "live":
        this.status = "connected";
        break;
      case "reconnecting":
        this.status = "reconnecting…";
        break;
      case "offline-fatal":
        this.status = "offline — reload to reconnect";
        break;
      // "closed": the component is being torn down; leave the text as-is.
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
      undo: (id) => {
        this.markTyping(id);
        client.undo(id);
      },
      redo: (id) => {
        this.markTyping(id);
        client.redo(id);
      },
      deleteBlip: (id) => {
        // Logically delete the blip: mark deleted="true" in the manifest AND clear
        // its content (so the text is gone and un-indexed), in one delta. The blip
        // remains a tombstone parent for any reply threads. submitWith re-reads the
        // live manifest/content at submit time.
        void client.submitWith((blip) => {
          const manifest = blip(MANIFEST_ID);
          const content = blip(id);
          if (manifest === undefined || content === undefined) return [];
          // Clear to the empty body: delete the current content, then insert a fresh
          // <body><line/></body> so the blip stays a valid (empty) document.
          const clear = new DocOp([...invert(content).components, ...initialBlipContent().components]);
          return [
            blipContentOp(author, MANIFEST_ID, setBlipDeleted(manifest, id)),
            blipContentOp(author, id, clear),
          ];
        });
      },
      setCaret: (blipId, anchor, focus) => this.setCaret(blipId, anchor, focus),
      remoteCaretsFor: (blipId) => this.remoteCaretsFor(blipId),
      blipAuthor: (blipId) => client.blipAuthor(blipId),
      blipContributors: (blipId) => client.blipContributors(blipId),
      isBlipUnread: (blipId) => client.blipLastModifiedVersion(blipId) > (this.blipReads.get(blipId) ?? 0),
      markBlipViewed: (blipId) => {
        const lastMod = client.blipLastModifiedVersion(blipId);
        if (lastMod <= (this.blipReads.get(blipId) ?? 0)) return; // already read
        this.blipReads.set(blipId, lastMod); // clear locally now (no round-trip wait)
        this.rev++; // drop the unread marker on the next render
        void markBlipRead(this.wave, blipId, lastMod); // persist; best-effort
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
        // Mint the new thread/blip id up front so it can be returned (the sheet opens
        // on it). It is just a fresh unique id, independent of live state, so minting
        // it outside submitWith — which re-reads the live blip — is equivalent.
        const id = newBlipID();
        void client.submitWith((blip) => {
          const manifest = blip(MANIFEST_ID);
          if (manifest === undefined) return [];
          const ops = [
            blipContentOp(author, MANIFEST_ID, buildReplyOp(manifest, parentId, id, inline)),
            blipContentOp(author, id, initialBlipContent()),
          ];
          // For an inline reply, also anchor it in the parent blip body at the
          // requested caret offset (clamped to the current content length, since
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
        return id;
      },
      participants: () => client.participants(),
      addParticipant: (addr: string) => {
        const p = participant(addr); // throws on invalid address
        void client.submit([addParticipantOp(author, p)]);
      },
      removeParticipant: (addr: string) => {
        const p = participant(addr); // throws on invalid address
        void client.submit([removeParticipantOp(author, p)]);
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
          // Insert the inline image at the requested caret offset, clamped to
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
    let sheet: TemplateResult = html``;
    let unreadCount = 0;
    if (manifest === undefined || controller === null) {
      body = html`<p class="conv-empty">No conversation yet…</p>`;
    } else {
      try {
        const m = readManifest(manifest);
        body = html`<wave-thread .thread=${m.rootThread} .controller=${controller}></wave-thread>`;
        // Count unread in-flow blips from the model (accurate this render; a DOM
        // query would lag by a frame since this template isn't committed yet).
        unreadCount = countUnreadInThread(m.rootThread, controller);
        // Inline comments are not rendered in flow; when one is open, show it in the
        // sheet. Resolve the thread by id from the live manifest each render, so a new
        // reply (or the just-created thread settling in) appears without reopening.
        if (this.commentThreadId !== "") {
          const t = findThread(m.rootThread, this.commentThreadId);
          if (t !== null) {
            sheet = html`<comment-sheet
              .thread=${t}
              .controller=${controller}
              .autoFocus=${this.commentAutoFocus}
              .onClose=${this.closeComment}
            ></comment-sheet>`;
          }
        }
      } catch (e) {
        body = html`<p class="conv-error" role="alert">malformed manifest: ${String(e)}</p>`;
      }
    }

    const roster = controller !== null ? this._renderRoster(controller) : html``;

    return html`
      ${STYLES}
      <div class=${"conv-bar" + connBarClass(this.status)} role="status" aria-live="polite">${this.status}</div>
      ${roster}
      ${this._renderPresence()}
      ${body}
      ${sheet}
      ${this._renderUnreadNav(unreadCount)}
    `;
  }

  // _renderUnreadNav shows a floating "jump to next unread" pill when in-flow blips
  // have unseen remote changes, with the count. Tapping it scrolls the next unread
  // blip below the fold into view (wrapping to the first) — which, being viewed,
  // then clears via mark-on-view. Inline-comment blips (rendered in the sheet, not
  // in flow) are out of scope for this nav. The count is computed from the model in
  // render() (accurate this frame); the jump reads the committed DOM on click.
  private _renderUnreadNav(n: number): TemplateResult {
    if (n === 0) return html``;
    return html`<button
      class="unread-nav"
      title="Jump to the next unread message"
      @click=${this.onJumpUnread}
    >
      ↓ ${n} unread
    </button>`;
  }

  // inFlowUnread returns the unread blip elements in document order, excluding any
  // inside the comment sheet (those are navigated by opening their thread, not by
  // this scroll affordance).
  private inFlowUnread(): HTMLElement[] {
    return Array.from(this.querySelectorAll<HTMLElement>(".wave-blip.unread")).filter(
      (el) => el.closest("comment-sheet") === null,
    );
  }

  // onJumpUnread scrolls the next unread in-flow blip into view: the first one whose
  // top is below a small margin from the viewport top (i.e. not already at the top),
  // wrapping to the first unread when all are already above. Centering it brings it
  // fully on screen so mark-on-view can then clear it.
  private onJumpUnread = (): void => {
    const els = this.inFlowUnread();
    if (els.length === 0) return;
    const next = els.find((el) => el.getBoundingClientRect().top > 80) ?? els[0]!;
    next.scrollIntoView({ behavior: "smooth", block: "center" });
  };

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
      <div class="conv-presence" title="Who else is here" role="status" aria-live="polite">
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
    // Remove (or leave, if it's you). Confirm first — it's destructive and the op
    // commits immediately. Playwright dialogs default to dismiss, so e2e must accept.
    const onRemove = (p: string): void => {
      const isSelf = p === controller.user;
      const ok = globalThis.confirm?.(isSelf ? "Leave this wave?" : `Remove ${p} from this wave?`) ?? true;
      if (!ok) return;
      try {
        controller.removeParticipant(p);
      } catch {
        /* invalid address — ignore */
      }
    };
    return html`
      <div class="conv-roster">
        <span class="roster-label">Participants:</span>
        ${parts.map(
          (p) => html`<span class="roster-chip"
            >${participantChip(p, profiles.get(p))}<button
              class="roster-remove"
              title=${p === controller.user ? "Leave this wave" : "Remove " + p}
              aria-label=${p === controller.user ? "Leave this wave" : "Remove " + p}
              @click=${() => onRemove(p)}
            >
              ×
            </button></span
          >`,
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

// findThread locates a thread by id anywhere in the conversation tree (the root
// thread or any nested reply/inline thread). A thread's id equals its first blip's id,
// which is what an inline-comment anchor carries. Returns null if not found.
function findThread(thread: Thread, id: string): Thread | null {
  if (thread.id === id) return thread;
  for (const b of thread.blips) {
    for (const t of b.threads) {
      const found = findThread(t, id);
      if (found !== null) return found;
    }
  }
  return null;
}

// countUnreadInThread counts the in-flow unread blips reachable from a thread: each
// non-deleted blip the controller reports unread, recursing into its NON-inline
// reply threads only (inline-comment threads render in the sheet, not in flow, and
// are reached by opening them — out of scope for the scroll nav). Mirrors what
// <wave-blip> renders, so the count matches the .wave-blip.unread elements on screen.
function countUnreadInThread(thread: Thread, controller: ConvController): number {
  let n = 0;
  for (const b of thread.blips) {
    if (!b.deleted && (controller.isBlipUnread?.(b.id) ?? false)) n++;
    for (const t of b.threads) {
      if (!t.inline) n += countUnreadInThread(t, controller);
    }
  }
  return n;
}

// connBarClass picks the status-bar style: red for failure/offline states, amber for
// transient connecting/reconnecting, none (muted grey) for the steady "connected".
function connBarClass(status: string): string {
  if (/^(error|bad|offline)/.test(status)) return " error";
  if (/^(reconnect|connecting)/.test(status)) return " warn";
  return "";
}

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
      font-weight: 600;
    }
    wave-conversation .conv-bar.warn {
      color: #b26a00;
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
    /* On very narrow screens, shrink the per-level reply indent (34px → 18px) so a
       deep reply chain keeps a usable text width when stacked. */
    @media (max-width: 560px) {
      wave-conversation .wave-thread.reply {
        margin-left: 8px;
        padding-left: 8px;
      }
    }
    wave-conversation .wave-blip {
      margin: 6px 0;
    }
    /* Unread: a blip with remote changes the participant hasn't viewed yet. A blue
       left accent on the card + a dot in the byline; cleared after a short dwell in
       view (mark-on-view). Kept subtle so it reads as "new" without shouting. */
    wave-conversation .wave-blip.unread blip-view {
      border-left: 3px solid #4060c0;
      background: #f7f9ff;
    }
    wave-conversation .blip-byline .unread-dot {
      color: #4060c0;
      font-size: 10px;
      line-height: 1;
    }
    /* Floating "jump to next unread" pill, bottom-right. Fixed so it stays reachable
       while scrolling a long conversation; only rendered when something is unread. */
    wave-conversation .unread-nav {
      position: fixed;
      right: 16px;
      bottom: 16px;
      z-index: 30;
      font: 12px system-ui, sans-serif;
      font-weight: 600;
      color: #fff;
      background: #4060c0;
      border: none;
      border-radius: 16px;
      padding: 8px 14px;
      box-shadow: 0 2px 8px rgba(0, 0, 0, 0.25);
      cursor: pointer;
    }
    wave-conversation .unread-nav:hover {
      background: #34509f;
    }
    wave-conversation .unread-nav:focus-visible {
      outline: 2px solid #fff;
      outline-offset: -4px;
    }
    @media (pointer: coarse) {
      wave-conversation .unread-nav {
        padding: 11px 16px;
        font-size: 13px;
      }
    }
    /* Author byline above each blip: who wrote it (avatar + name). */
    wave-conversation .blip-byline {
      display: flex;
      align-items: center;
      gap: 6px;
      margin: 0 0 3px 2px;
      font: 12px system-ui, sans-serif;
      color: #555;
    }
    wave-conversation .blip-byline .byline-name {
      font-weight: 600;
      color: #444;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      max-width: 60%;
    }
    wave-conversation blip-view {
      display: block;
      position: relative; /* containing block for the remote-caret overlay */
      border: 1px solid #ddd;
      border-radius: 6px;
      padding: 8px 10px;
      background: #fff;
    }
    /* The contenteditable sets outline:none, so light the whole card when focus is
       inside it — otherwise an edited blip has no focus indication. */
    wave-conversation blip-view:focus-within {
      border-color: #4060c0;
      box-shadow: 0 0 0 2px rgba(64, 96, 192, 0.15);
    }
    wave-conversation .blip-actions {
      margin: 2px 0 0;
    }
    /* Inline-comment overview: a scannable strip of collapsed pills under the blip, so
       comments are visible (not hidden behind the in-text 💬). Tap a pill → its sheet. */
    wave-conversation .comment-pills {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      margin: 6px 0 2px;
    }
    wave-conversation .comment-pill {
      display: inline-flex;
      align-items: center;
      gap: 5px;
      max-width: 100%;
      font: 12px system-ui, sans-serif;
      color: #3949ab;
      background: #eef1fb;
      border: 1px solid #d6ddf5;
      border-radius: 13px;
      padding: 3px 9px;
      cursor: pointer;
    }
    wave-conversation .comment-pill:hover {
      background: #e2e8fb;
    }
    wave-conversation .comment-pill:focus-visible {
      outline: 2px solid #4060c0;
      outline-offset: 1px;
    }
    wave-conversation .comment-pill .cp-glyph {
      display: inline-flex;
      align-items: center;
      font-size: 0.85em;
    }
    wave-conversation .comment-pill .cp-text {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      max-width: 22em;
    }
    wave-conversation .comment-pill .cp-count {
      font-size: 11px;
      font-weight: 600;
      background: #4060c0;
      color: #fff;
      border-radius: 9px;
      padding: 0 6px;
      line-height: 1.5;
    }
    @media (pointer: coarse) {
      wave-conversation .comment-pill {
        padding: 7px 12px;
        font-size: 13px;
      }
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
    /* Delete: a borderless link-style button like the others, but muted red so it
       reads as a destructive secondary action. */
    wave-conversation .delete-btn {
      font: 11px system-ui, sans-serif;
      color: #b04040;
      background: none;
      border: none;
      padding: 2px 4px;
      cursor: pointer;
    }
    wave-conversation .delete-btn:hover {
      text-decoration: underline;
    }
    wave-conversation .delete-btn:focus-visible {
      outline: 2px solid #b04040;
      outline-offset: 1px;
      border-radius: 4px;
      text-decoration: underline;
    }
    /* Tombstone placeholder shown in place of a logically-deleted blip's editor. */
    wave-conversation .blip-deleted {
      font: italic 13px system-ui, sans-serif;
      color: #999;
      padding: 6px 2px;
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
      padding: 2px 4px 2px 3px;
      font-size: 11px;
      max-width: 200px;
    }
    wave-conversation .roster-remove {
      margin-left: 3px;
      border: none;
      background: none;
      color: #3949ab;
      opacity: 0.5;
      cursor: pointer;
      font-size: 13px;
      line-height: 1;
      padding: 0 3px;
      border-radius: 8px;
    }
    wave-conversation .roster-remove:hover {
      opacity: 1;
      background: rgba(57, 73, 171, 0.14);
    }
    wave-conversation .roster-remove:focus-visible {
      opacity: 1;
      outline: 2px solid #4060c0;
      outline-offset: 1px;
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
      /* The roster × is a small glyph; enlarge its HIT AREA (transparent padding) to a
         comfortable touch target without bloating the chip. */
      wave-conversation .roster-remove {
        min-width: 44px;
        min-height: 36px;
        margin-left: 0;
      }
    }
  </style>
`;
