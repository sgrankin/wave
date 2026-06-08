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

  // onCaret relays this blip's local caret/selection (the view reports rune offsets;
  // we tag them with this blip's id) to the controller, which publishes it on the
  // presence channel so peers can render it.
  private onCaret = (e: Event): void => {
    e.stopPropagation();
    const { anchor, focus } = (e as CustomEvent<{ anchor: number; focus: number }>).detail;
    this.controller.setCaret?.(this.blip.id, anchor, focus);
  };

  private onReply = (): void => {
    this.controller.replyToBlip(this.blip.id, false);
  };

  // onReplyInline anchors a reply within the parent blip's text, at the line the
  // caret is in (or the end of the blip if the caret is elsewhere).
  private onReplyInline = (): void => {
    this.commentInline();
  };

  // commentInline creates an inline reply anchored at the current selection's line and
  // opens it in the comment sheet, focused for immediate typing. Public so the floating
  // <selection-toolbar>'s "Comment" button can drive it from outside the component tree
  // (it resolves this <wave-blip> by climbing the DOM).
  commentInline(): void {
    const id = this.controller.replyToBlip(this.blip.id, true, this.anchorOffset());
    // Bubble to <wave-conversation>, which opens the sheet for this thread once the
    // optimistic create settles (focus:true so the reply input is ready to type).
    this.dispatchEvent(
      new CustomEvent<{ id: string; focus: boolean }>("anchor-activate", {
        detail: { id, focus: true },
        bubbles: true,
        composed: true,
      }),
    );
  }

  // anchorOffset returns the doc offset where an inline element should attach — the
  // EXACT caret offset (the selection's low end), or the end of the blip if the caret
  // is elsewhere. The caret mapping counts inline elements as their doc items, so a
  // mid-text anchor is caret-safe.
  private anchorOffset(): number {
    const view = this.querySelector("blip-view") as
      | (HTMLElement & { caretAnchorOffset(): number | null })
      | null;
    const off = view?.caretAnchorOffset() ?? null;
    if (off !== null) return off;
    const len = this.controller.blipContent(this.blip.id).documentLength();
    return Math.max(0, len - 1); // before </body>
  }

  private onAttachClick = (): void => {
    this.querySelector<HTMLInputElement>(".attach-input")?.click();
  };

  private onAttachFile = (e: Event): void => {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = ""; // allow re-picking the same file
    if (file === undefined) return;
    this.controller.attachImage(this.blip.id, file, this.anchorOffset());
  };

  protected override render(): TemplateResult {
    const content = this.controller.blipContent(this.blip.id);
    return html`
      <div class="wave-blip">
        <blip-view
          .content=${content}
          .selfAddress=${this.controller.user}
          .remoteCarets=${this.controller.remoteCaretsFor?.(this.blip.id) ?? []}
          @edit=${this.onEdit}
          @caret=${this.onCaret}
        ></blip-view>
        <div class="blip-actions">
          <button class="reply-btn" @click=${this.onReply}>Reply</button>
          <!-- preventDefault on mousedown so clicking the button does NOT blur the
               editor: the caret stays, so the inline reply / attachment anchors at the
               caret's line (anchorOffset reads it) instead of falling back to the end. -->
          <button
            class="reply-inline-btn"
            @mousedown=${(e: MouseEvent) => e.preventDefault()}
            @click=${this.onReplyInline}
          >
            Reply inline
          </button>
          <button
            class="attach-btn"
            @mousedown=${(e: MouseEvent) => e.preventDefault()}
            @click=${this.onAttachClick}
          >
            Attach
          </button>
          <input
            class="attach-input"
            type="file"
            accept="image/*"
            style="display:none"
            @change=${this.onAttachFile}
          />
        </div>
        ${this.blip.threads
          .filter((t) => !t.inline)
          .map(
            (t) =>
              html`<wave-thread
                data-thread-id=${t.id}
                .thread=${t}
                .controller=${this.controller}
              ></wave-thread>`,
          )}
      </div>
    `;
  }
}

customElements.define("wave-blip", WaveBlip);
