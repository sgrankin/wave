// <wave-playback> — a read-only history scrubber for one wave. It loads the delta
// timeline (GET /api/playback/deltas), shows a slider over it, and renders the
// conversation as it stood at the selected version (GET /api/playback/state) as
// plain-text blips. It never submits — playback is a separate read-only mode from
// the live <wave-conversation> editor.
//
// Non-decorator Lit + light DOM, matching the rest of the client.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import { fetchPlaybackDeltas, fetchPlaybackState } from "../wave/playback.ts";
import type { ConversationView, DeltaDigest } from "../wave/playback.ts";
import { displayNameFor, profiles } from "../wave/profiles.ts";
import { participantChip } from "./participant.ts";

export class WavePlayback extends LitElement {
  static override properties = {
    wave: {},
    deltas: { state: true },
    idx: { state: true },
    view: { state: true },
    status: { state: true },
  };

  declare wave: string;
  declare deltas: DeltaDigest[];
  declare idx: number; // selected index into deltas
  declare view: ConversationView | null;
  declare status: string;

  private profilesUnsub: (() => void) | null = null;
  // Monotonic id for state fetches; a response applies only if still the latest
  // (drops out-of-order responses when scrubbing fast).
  private loadSeq = 0;

  constructor() {
    super();
    this.wave = "";
    this.deltas = [];
    this.idx = 0;
    this.view = null;
    this.status = "loading…";
  }

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    this.profilesUnsub = profiles.onChange(() => this.requestUpdate());
    void this.loadTimeline();
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.profilesUnsub?.();
    this.profilesUnsub = null;
  }

  protected override updated(changed: Map<string, unknown>): void {
    if (changed.has("wave")) void this.loadTimeline();
  }

  private async loadTimeline(): Promise<void> {
    this.status = "loading…";
    this.view = null;
    let deltas: DeltaDigest[];
    try {
      deltas = await fetchPlaybackDeltas(this.wave);
    } catch (e) {
      this.deltas = [];
      this.status = `error: ${String(e)}`;
      return;
    }
    this.deltas = deltas;
    if (deltas.length === 0) {
      this.status = "no history yet";
      return;
    }
    profiles.ensure(deltas.map((d) => d.author));
    this.idx = deltas.length - 1; // default to the latest version
    await this.loadState();
  }

  private async loadState(): Promise<void> {
    const d = this.deltas[this.idx];
    if (d === undefined) return;
    const seq = ++this.loadSeq;
    try {
      const view = await fetchPlaybackState(this.wave, d.version);
      if (seq !== this.loadSeq) return; // a newer scrub superseded this fetch
      this.view = view;
      this.status = "";
      profiles.ensure([...view.participants, ...view.blips.map((b) => b.author)]);
    } catch (e) {
      if (seq === this.loadSeq) this.status = `error: ${String(e)}`;
    }
  }

  private onScrub = (e: Event): void => {
    this.idx = Number((e.target as HTMLInputElement).value);
    void this.loadState();
  };

  protected override render(): TemplateResult {
    if (this.deltas.length === 0) {
      return html`${STYLES}<div class="pb-status">${this.status}</div>`;
    }
    const d = this.deltas[this.idx]!;
    return html`
      ${STYLES}
      <div class="pb-bar">
        <input
          class="pb-slider"
          type="range"
          min="0"
          max=${this.deltas.length - 1}
          .value=${String(this.idx)}
          aria-label="History version"
          aria-valuetext=${`version ${d.version}, ${this.idx + 1} of ${this.deltas.length}`}
          @input=${this.onScrub}
        />
        <span class="pb-pos">v${d.version} · ${this.idx + 1}/${this.deltas.length}</span>
        <span class="pb-meta"
          >${displayNameFor(d.author, profiles.get(d.author))} ·
          ${new Date(d.timestamp).toLocaleString()}</span
        >
      </div>
      ${this.renderView()}
    `;
  }

  private renderView(): TemplateResult {
    if (this.status !== "") return html`<div class="pb-status">${this.status}</div>`;
    if (this.view === null) return html``;
    return html`
      <div class="pb-roster">
        ${this.view.participants.map(
          (p) => html`<span class="roster-chip">${participantChip(p, profiles.get(p))}</span>`,
        )}
      </div>
      <div class="pb-blips">
        ${this.view.blips.length === 0
          ? html`<div class="pb-status">(empty at this version)</div>`
          : this.view.blips.map(
              (b) => html`
                <div class="pb-blip">
                  <div class="pb-blip-author">${participantChip(b.author, profiles.get(b.author))}</div>
                  <div class="pb-blip-text">${b.text}</div>
                </div>
              `,
            )}
      </div>
    `;
  }
}

customElements.define("wave-playback", WavePlayback);

const STYLES = html`
  <style>
    wave-playback {
      display: block;
      max-width: 820px;
      font: 14px system-ui, sans-serif;
    }
    wave-playback .pb-bar {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 8px 0 12px;
      border-bottom: 1px solid #eee;
      margin-bottom: 12px;
    }
    wave-playback .pb-slider {
      flex: 1;
      max-width: 360px;
      accent-color: #4060c0;
    }
    wave-playback .pb-slider:focus-visible {
      outline: 2px solid #4060c0;
      outline-offset: 4px;
      border-radius: 4px;
    }
    wave-playback .pb-pos {
      font: 12px ui-monospace, monospace;
      color: #555;
      white-space: nowrap;
      margin-left: 4px;
    }
    wave-playback .pb-meta {
      font: 12px system-ui, sans-serif;
      color: #6a6a6a;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      min-width: 0;
      flex: 1 1 auto;
    }
    wave-playback .pb-roster {
      display: flex;
      flex-wrap: wrap;
      gap: 4px;
      margin-bottom: 12px;
    }
    wave-playback .roster-chip {
      display: inline-flex;
      align-items: center;
      background: #e8eaf6;
      color: #3949ab;
      border-radius: 12px;
      padding: 2px 8px 2px 3px;
      font-size: 11px;
      max-width: 200px;
    }
    wave-playback .pb-blip {
      border: 1px solid #ddd;
      border-radius: 6px;
      padding: 8px 10px;
      margin: 6px 0;
      background: #fafafa;
    }
    wave-playback .pb-blip-author {
      font-size: 11px;
      color: #666;
      margin-bottom: 4px;
    }
    wave-playback .pb-blip-text {
      white-space: pre-wrap;
      color: #222;
    }
    wave-playback .pb-status {
      color: #6a6a6a;
      font: 13px system-ui, sans-serif;
      padding: 12px 0;
    }
  </style>
`;
