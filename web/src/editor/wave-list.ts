// <wave-list> — the left pane: a search box, a "New wave" button, and the list of
// waves (inbox or search results). It is a pure view: it renders the `waves` it is
// given and reports user intent through callback props (onSearch / onSelect /
// onNew) that the <wave-app> shell wires to the API + navigation. Highlights the
// active wave.
//
// Non-decorator Lit + light DOM, matching the rest of the client.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import type { WaveDigest } from "../wave/api.ts";
import { displayNameFor, profiles } from "../wave/profiles.ts";

export class WaveList extends LitElement {
  static override properties = {
    waves: { attribute: false },
    selected: {},
    query: {},
  };

  declare waves: WaveDigest[];
  declare selected: string; // serialized name of the active wave, or ""
  declare query: string;

  // Wired by <wave-app>.
  onSearch: (query: string) => void = () => {};
  onSelect: (wave: string) => void = () => {};
  onNew: () => void = () => {};

  private unsub: (() => void) | null = null;

  constructor() {
    super();
    this.waves = [];
    this.selected = "";
    this.query = "";
  }

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    // Re-render when participant display names resolve so the meta line humanizes.
    this.unsub = profiles.onChange(() => this.requestUpdate());
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.unsub?.();
    this.unsub = null;
  }

  private onInput = (e: Event): void => {
    this.query = (e.target as HTMLInputElement).value;
    this.onSearch(this.query);
  };

  protected override render(): TemplateResult {
    return html`
      ${STYLES}
      <div class="wl-head">
        <button class="wl-new" @click=${() => this.onNew()}>✎ New wave</button>
        <input
          class="wl-search"
          type="search"
          placeholder="Search waves…"
          .value=${this.query}
          @input=${this.onInput}
        />
      </div>
      <div class="wl-items">${this.renderItems()}</div>
    `;
  }

  private renderItems(): TemplateResult {
    if (this.waves.length === 0) {
      const msg = this.query.trim() !== "" ? "No matching waves" : "No waves yet — create one";
      return html`<div class="wl-empty">${msg}</div>`;
    }
    // Resolve display names for everyone shown (one batched fetch); names that
    // are not cached yet fall back to the address until "change" re-renders.
    profiles.ensure(this.waves.flatMap((w) => [w.creator, ...w.participants]));
    return html`${this.waves.map((w) => this.renderItem(w))}`;
  }

  private renderItem(w: WaveDigest): TemplateResult {
    const cls =
      "wl-item" + (w.wave === this.selected ? " selected" : "") + (w.unread ? " unread" : "");
    const title = w.title.trim() !== "" ? w.title : "(untitled wave)";
    const others = w.participants.map((a) => displayNameFor(a, profiles.get(a))).join(", ");
    return html`
      <div class=${cls} @click=${() => this.onSelect(w.wave)} title=${w.wave}>
        <div class="wl-title">${w.unread ? html`<span class="wl-dot"></span>` : html``}${title}</div>
        ${w.snippet.trim() !== "" && w.snippet !== title
          ? html`<div class="wl-snippet">${w.snippet}</div>`
          : html``}
        <div class="wl-meta">${others}</div>
      </div>
    `;
  }
}

customElements.define("wave-list", WaveList);

const STYLES = html`
  <style>
    wave-list .wl-head {
      display: flex;
      flex-direction: column;
      gap: 6px;
      padding: 10px;
      border-bottom: 1px solid #e0e0e0;
    }
    wave-list .wl-new {
      font: 13px system-ui, sans-serif;
      padding: 6px 10px;
      border: 1px solid #4060c0;
      color: #4060c0;
      background: #fff;
      border-radius: 6px;
      cursor: pointer;
    }
    wave-list .wl-new:hover {
      background: #e8eeff;
    }
    wave-list .wl-search {
      font: 13px system-ui, sans-serif;
      padding: 6px 8px;
      border: 1px solid #ccc;
      border-radius: 6px;
    }
    wave-list .wl-items {
      overflow-y: auto;
      flex: 1;
    }
    wave-list .wl-empty {
      padding: 16px 12px;
      color: #999;
      font: 13px system-ui, sans-serif;
    }
    wave-list .wl-item {
      padding: 10px 12px;
      border-bottom: 1px solid #f0f0f0;
      cursor: pointer;
    }
    wave-list .wl-item:hover {
      background: #f7f9ff;
    }
    wave-list .wl-item.selected {
      background: #e8eeff;
      border-left: 3px solid #4060c0;
      padding-left: 9px;
    }
    wave-list .wl-title {
      font: 400 13px system-ui, sans-serif;
      color: #444;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    wave-list .wl-item.unread .wl-title {
      font-weight: 700;
      color: #111;
    }
    wave-list .wl-dot {
      display: inline-block;
      width: 7px;
      height: 7px;
      border-radius: 50%;
      background: #4060c0;
      margin-right: 6px;
      vertical-align: middle;
    }
    wave-list .wl-snippet {
      font: 12px system-ui, sans-serif;
      color: #666;
      margin-top: 2px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    wave-list .wl-meta {
      font: 11px system-ui, sans-serif;
      color: #999;
      margin-top: 3px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
  </style>
`;
