// <wave-app> — the application shell: a two-pane layout with the wave list /
// search on the left and the active <wave-conversation> on the right, plus
// new-wave creation and URL-based navigation between waves. It owns the inbox/
// search data (via the query API) and the active-wave selection; the conversation
// component owns its own OT client. Switching waves recreates the conversation
// (keyed on the wave name) so it reconnects cleanly.
//
// Non-decorator Lit + light DOM, matching the rest of the client.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";
import { keyed } from "lit/directives/keyed.js";

import { fetchInbox, searchWaves } from "../wave/api.ts";
import type { WaveDigest } from "../wave/api.ts";
import { domainOf, newConversationWave } from "../wave/waveid.ts";
import type { OptimisticClient } from "../wave/transport.ts";
import "./wave-conversation.ts";
import type { WaveConversation } from "./wave-conversation.ts";
import "./wave-list.ts";

const SEARCH_DEBOUNCE_MS = 200;

export class WaveApp extends LitElement {
  static override properties = {
    activeWave: { state: true },
    waves: { state: true },
    query: { state: true },
  };

  wsUrl = "";
  user = "";

  declare activeWave: string; // serialized WaveletName, or "" for none
  declare waves: WaveDigest[];
  declare query: string;

  private searchTimer: ReturnType<typeof setTimeout> | null = null;
  private refreshTimer: ReturnType<typeof setTimeout> | null = null;
  // Monotonic id for list fetches; a response is applied only if it is still the
  // latest request (drops out-of-order inbox/search responses).
  private listSeq = 0;

  constructor() {
    super();
    this.activeWave = "";
    this.waves = [];
    this.query = "";
  }

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    this.activeWave = waveFromURL();
    window.addEventListener("popstate", this.onPopState);
    void this.loadInbox();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    window.removeEventListener("popstate", this.onPopState);
    if (this.searchTimer !== null) clearTimeout(this.searchTimer);
    if (this.refreshTimer !== null) clearTimeout(this.refreshTimer);
  }

  /** The active conversation's OT client, or null. For debug tooling. */
  getActiveClient(): OptimisticClient | null {
    const conv = this.querySelector<WaveConversation>("wave-conversation");
    return conv?.getClient() ?? null;
  }

  private onPopState = (): void => {
    this.activeWave = waveFromURL();
  };

  private async loadInbox(): Promise<void> {
    const seq = ++this.listSeq;
    let waves: WaveDigest[];
    try {
      waves = await fetchInbox();
    } catch {
      waves = [];
    }
    if (seq === this.listSeq) this.waves = waves; // drop a stale (out-of-order) response
  }

  private async runSearch(q: string): Promise<void> {
    const seq = ++this.listSeq;
    let waves: WaveDigest[];
    try {
      waves = await searchWaves(q);
    } catch {
      waves = [];
    }
    if (seq === this.listSeq) this.waves = waves; // drop a stale (out-of-order) response
  }

  // rerun loads the inbox (empty query) or runs the search. loadInbox/runSearch
  // each bump listSeq and apply only the latest request's response, so out-of-
  // order fetches (typing faster than the network, or a refresh racing a search)
  // never show results for a stale query.
  private rerun(q: string): void {
    void (q === "" ? this.loadInbox() : this.runSearch(q));
  }

  // refreshList re-runs the current view so the list reflects new/changed waves.
  // It uses its own timer so a background refresh never cancels the user's
  // in-flight keystroke debounce. Debounced.
  private refreshList(): void {
    if (this.refreshTimer !== null) clearTimeout(this.refreshTimer);
    this.refreshTimer = setTimeout(() => this.rerun(this.query.trim()), SEARCH_DEBOUNCE_MS);
  }

  private handleSearch = (query: string): void => {
    this.query = query;
    if (this.searchTimer !== null) clearTimeout(this.searchTimer);
    this.searchTimer = setTimeout(() => this.rerun(query.trim()), SEARCH_DEBOUNCE_MS);
  };

  private handleSelect = (wave: string): void => {
    if (wave === this.activeWave) return;
    this.activeWave = wave;
    history.pushState({ wave }, "", `?wave=${encodeURIComponent(wave)}`);
  };

  private handleNew = (): void => {
    const name = newConversationWave(domainOf(this.user)).serialize();
    this.handleSelect(name);
    // The server seeds the conversation on open; refresh once it has landed.
    this.refreshList();
  };

  // handleConvChange fires when the active conversation's replica changes (e.g.
  // after seeding or an edit); refresh the list so titles/order stay current.
  private handleConvChange = (): void => {
    this.refreshList();
  };

  protected override render(): TemplateResult {
    return html`
      ${STYLES}
      <div class="app">
        <div class="app-left">
          <wave-list
            .waves=${this.waves}
            .selected=${this.activeWave}
            .query=${this.query}
            .onSearch=${this.handleSearch}
            .onSelect=${this.handleSelect}
            .onNew=${this.handleNew}
          ></wave-list>
        </div>
        <div class="app-right">
          ${this.activeWave === ""
            ? html`<div class="app-placeholder">Select a wave, or create a new one.</div>`
            : keyed(
                this.activeWave,
                html`<wave-conversation
                  .url=${this.wsUrl}
                  .wave=${this.activeWave}
                  .user=${this.user}
                  .onChange=${this.handleConvChange}
                ></wave-conversation>`,
              )}
        </div>
      </div>
    `;
  }
}

customElements.define("wave-app", WaveApp);

// waveFromURL reads the active wave (serialized name) from the ?wave= query param.
function waveFromURL(): string {
  return new URLSearchParams(location.search).get("wave") ?? "";
}

const STYLES = html`
  <style>
    wave-app {
      display: block;
      height: 100vh;
    }
    wave-app .app {
      display: flex;
      height: 100%;
    }
    wave-app .app-left {
      width: 300px;
      min-width: 240px;
      border-right: 1px solid #e0e0e0;
      display: flex;
      flex-direction: column;
      background: #fafbfc;
    }
    wave-app wave-list {
      display: flex;
      flex-direction: column;
      height: 100%;
    }
    wave-app .app-right {
      flex: 1;
      overflow-y: auto;
      padding: 16px 20px;
    }
    wave-app .app-placeholder {
      color: #999;
      font: 14px system-ui, sans-serif;
      margin-top: 40px;
      text-align: center;
    }
  </style>
`;
