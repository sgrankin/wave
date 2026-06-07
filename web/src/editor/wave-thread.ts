// <wave-thread> — a sequence of blips: the root thread (blips directly in the
// conversation) or a reply thread under a blip. It renders each blip as a
// <wave-blip> and offers a button to add a blip to the END of this thread
// (continue it). Reply *threads* are created on a blip (see <wave-blip>); this
// button continues an existing thread.
//
// Non-decorator Lit + light DOM, matching the rest of the editor tree.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import type { Thread } from "../wave/conversation.ts";
import type { ConvController } from "./controller.ts";
import "./wave-blip.ts";

export class WaveThread extends LitElement {
  static override properties = {
    thread: { attribute: false },
    controller: { attribute: false },
  };

  declare thread: Thread;
  declare controller: ConvController;

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  private onContinue = (): void => {
    this.controller.continueThread(this.thread.id);
  };

  protected override render(): TemplateResult {
    const isRoot = this.thread.id === "";
    const cls = ["wave-thread", isRoot ? "root" : "reply", this.thread.inline ? "inline" : ""]
      .filter((s) => s !== "")
      .join(" ");
    return html`
      <div class=${cls}>
        ${this.thread.blips.map(
          (b) => html`<wave-blip .blip=${b} .controller=${this.controller}></wave-blip>`,
        )}
        <div class="thread-actions">
          <button class="continue-btn" @click=${this.onContinue}>
            ${isRoot ? "+ New message" : "+ Continue thread"}
          </button>
        </div>
      </div>
    `;
  }
}

customElements.define("wave-thread", WaveThread);
