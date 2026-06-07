// <wave-blip> — one blip in a conversation: its editable content plus a "Reply"
// affordance and its nested reply threads. The content is a controlled
// <blip-view>; this component wraps the view's `edit` op-stream into a request on
// the ConvController (which submits it through the OptimisticClient), and renders
// each reply thread as a nested <wave-thread> — the recursion that makes the
// conversation a tree.
//
// Non-decorator Lit + light DOM (so the nested contenteditable's selection is
// reachable, matching <blip-view>).

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import type { Component } from "../wave/types.ts";
import type { Blip } from "../wave/conversation.ts";
import type { ConvController } from "./controller.ts";
import "./blip-view.ts";
import "./wave-thread.ts";

export class WaveBlip extends LitElement {
  static override properties = {
    blip: { attribute: false },
    controller: { attribute: false },
  };

  declare blip: Blip;
  declare controller: ConvController;

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  // onEdit forwards a blip content op (from the controlled <blip-view>) to the
  // controller, tagged with this blip's id. stopPropagation keeps the event from
  // bubbling past us (each blip handles only its own view's edits).
  private onEdit = (e: Event): void => {
    e.stopPropagation();
    const ops = (e as CustomEvent<Component[]>).detail;
    this.controller.editBlip(this.blip.id, ops);
  };

  private onReply = (): void => {
    this.controller.replyToBlip(this.blip.id, false);
  };

  // onReplyInline anchors a reply within the parent blip's text, at the line the
  // caret is in (or the end of the blip if the caret is elsewhere).
  private onReplyInline = (): void => {
    const view = this.querySelector("blip-view") as
      | (HTMLElement & { caretLineEndOffset(): number | null })
      | null;
    let offset = view?.caretLineEndOffset() ?? null;
    if (offset === null) {
      const len = this.controller.blipContent(this.blip.id).documentLength();
      offset = Math.max(0, len - 1); // before </body>
    }
    this.controller.replyToBlip(this.blip.id, true, offset);
  };

  protected override render(): TemplateResult {
    const content = this.controller.blipContent(this.blip.id);
    return html`
      <div class="wave-blip">
        <blip-view
          .content=${content}
          .selfAddress=${this.controller.user}
          @edit=${this.onEdit}
        ></blip-view>
        <div class="blip-actions">
          <button class="reply-btn" @click=${this.onReply}>Reply</button>
          <button class="reply-inline-btn" @click=${this.onReplyInline}>Reply inline</button>
        </div>
        ${this.blip.threads.map(
          (t) => html`<wave-thread .thread=${t} .controller=${this.controller}></wave-thread>`,
        )}
      </div>
    `;
  }
}

customElements.define("wave-blip", WaveBlip);
