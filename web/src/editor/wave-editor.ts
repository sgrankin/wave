// <wave-editor> — owns the OptimisticClient for one wavelet and hosts a <blip-view>
// for its (single, for now) blip. It is the connection + submit shell: it feeds the
// optimistic blip content into the view, and wraps the view's content-op `edit`
// events in a wavelet blip operation submitted through the client. The editing
// itself (rendering + command generation) lives in <blip-view>.
//
// Non-decorator Lit + light-DOM render so the nested contenteditable's selection is
// reachable. Single blip ("main"); the conversation/threading view comes next.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import { CONTRIBUTOR_ADD, DocOp, WaveletName, participant } from "../wave/types.ts";
import type { Component, Operation } from "../wave/types.ts";
import { OptimisticClient } from "../wave/transport.ts";
import "./blip-view.ts";

const BLIP_ID = "main";

export class WaveEditor extends LitElement {
  static override properties = {
    status: { state: true },
    blip: { state: true },
  };

  url = "";
  wave = "";
  user = "";

  declare status: string;
  declare blip: DocOp;

  private client: OptimisticClient | null = null;

  constructor() {
    super();
    this.status = "connecting…";
    this.blip = DocOp.empty();
  }

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
      name = WaveletName.parse(this.wave);
    } catch (e) {
      this.status = `bad wavelet name: ${String(e)}`;
      return;
    }
    const sep = this.url.includes("?") ? "&" : "?";
    const url = `${this.url}${sep}user=${encodeURIComponent(this.user)}`;
    const client = new OptimisticClient(url, name, participant(this.user));
    this.client = client;
    client.onChange(() => this.refresh());
    try {
      await client.open();
      this.status = `connected as ${this.user}`;
      this.refresh();
    } catch (e) {
      this.status = `error: ${String(e)}`;
    }
  }

  private refresh(): void {
    this.blip = this.client?.blipContent(BLIP_ID) ?? DocOp.empty();
  }

  // onEdit wraps a blip content op (from <blip-view>) in a wavelet blip operation
  // and submits it; the optimistic apply feeds the result back via refresh().
  private onEdit = (e: Event): void => {
    const client = this.client;
    if (client === null) return;
    const ops = (e as CustomEvent<Component[]>).detail;
    const op: Operation = {
      kind: "blip",
      blipId: BLIP_ID,
      op: {
        ctx: { creator: participant(this.user), timestamp: Date.now(), versionIncrement: 1, hashedVersion: null },
        contentOp: new DocOp(ops),
        method: CONTRIBUTOR_ADD,
      },
    };
    void client.submit([op]);
  };

  protected override render(): TemplateResult {
    return html`
      <style>
        wave-editor .bar { font: 12px system-ui, sans-serif; color: #555; margin-bottom: 6px; }
        wave-editor blip-view {
          display: block; border: 1px solid #bbb; border-radius: 6px; padding: 10px; min-height: 8em;
        }
      </style>
      <div class="bar">${this.status}</div>
      <blip-view .content=${this.blip} @edit=${this.onEdit}></blip-view>
    `;
  }
}

customElements.define("wave-editor", WaveEditor);
