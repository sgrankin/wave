// <wave-debug> — a tiny dev-only overlay showing the OptimisticClient's live
// state (connection, confirmed version, in-flight/queue, blip ids + lengths). It
// is the steady-state companion to the gated console delta-trace (debug.ts):
// together they replace the ad-hoc instrumentation that diagnosed the controlled-
// editor submit bug. Mounted only for `?debug=1` (see main.ts).
//
// It polls the client (rather than subscribing) to stay decoupled — the client is
// created asynchronously inside <wave-conversation>, and a 250ms poll is plenty
// for a human-read panel.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import type { OptimisticClient } from "../wave/transport.ts";

export class WaveDebug extends LitElement {
  static override properties = {
    tick: { state: true },
  };

  // Supplies the client to introspect (null until connected). Set by main.ts.
  provider: () => OptimisticClient | null = () => null;

  declare tick: number;
  private timer: ReturnType<typeof setInterval> | undefined;

  constructor() {
    super();
    this.tick = 0;
  }

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    this.timer = setInterval(() => {
      this.tick++;
    }, 250);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    if (this.timer !== undefined) clearInterval(this.timer);
  }

  protected override render(): TemplateResult {
    void this.tick; // reactive dep: re-render on each poll
    const client = this.provider();
    const s = client?.debugState();
    const rows = s
      ? [
          ["opened", String(s.opened)],
          ["version", String(s.version)],
          ["inflight", String(s.inflight)],
          ["queue", String(s.queueLength)],
          ["fatal", s.fatal ?? "—"],
          ["blips", Object.entries(s.blips).map(([id, n]) => `${id}:${n}`).join("  ") || "—"],
        ]
      : [["status", "no client"]];
    return html`
      <style>
        wave-debug {
          position: fixed;
          right: 8px;
          bottom: 8px;
          z-index: 9999;
          font: 11px/1.5 ui-monospace, Menlo, monospace;
          background: #111;
          color: #cfe;
          padding: 8px 10px;
          border-radius: 6px;
          opacity: 0.9;
          max-width: 360px;
          overflow: hidden;
        }
        wave-debug .k {
          color: #89a;
          display: inline-block;
          width: 64px;
        }
        wave-debug .row {
          white-space: nowrap;
          overflow: hidden;
          text-overflow: ellipsis;
        }
        wave-debug .title {
          color: #6c8;
          font-weight: 600;
          margin-bottom: 2px;
        }
      </style>
      <div class="title">wave debug</div>
      ${rows.map(([k, v]) => html`<div class="row"><span class="k">${k}</span>${v}</div>`)}
    `;
  }
}

customElements.define("wave-debug", WaveDebug);
