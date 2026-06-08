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
import type { Blip, Thread } from "../wave/conversation.ts";
import type { ConvController } from "./controller.ts";
import { paragraphText, project } from "./blipdoc.ts";
import { avatar } from "./participant.ts";
import { displayNameFor, profiles } from "../wave/profiles.ts";
import "./blip-view.ts";
import "./wave-thread.ts";

// PILL_SNIPPET_MAX: characters of a comment shown on its collapsed pill.
const PILL_SNIPPET_MAX = 48;

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

  // onUndo routes a Cmd-Z / Cmd-Shift-Z request from this blip's view to the
  // controller's per-blip undo manager (redo when detail.redo).
  private onUndo = (e: Event): void => {
    e.stopPropagation();
    const { redo } = (e as CustomEvent<{ redo: boolean }>).detail;
    if (redo) this.controller.redo?.(this.blip.id);
    else this.controller.undo?.(this.blip.id);
  };

  // onCaret relays this blip's local caret/selection (the view reports doc-item offsets;
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

  // onDelete logically deletes this blip (after a confirm). The blip becomes a
  // "message deleted" tombstone; its reply threads remain.
  private onDelete = (): void => {
    if (this.controller.deleteBlip === undefined) return;
    if (!window.confirm("Delete this message? Its text will be removed.")) return;
    this.controller.deleteBlip(this.blip.id);
  };

  // onReplyInline anchors a reply within the parent blip's text, at the exact caret
  // offset (or the end of the blip if the caret is elsewhere).
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

  // renderDeleted is the tombstone view for a logically-deleted blip: a placeholder
  // in place of the editor, but its reply threads remain (a deleted blip stays a
  // parent for its non-inline replies).
  private renderDeleted(): TemplateResult {
    return html`
      <div class="wave-blip deleted">
        <div class="blip-deleted" role="note">🗑️ message deleted</div>
        ${this.blip.threads
          .filter((t) => !t.inline)
          .map(
            (t) =>
              html`<wave-thread data-thread-id=${t.id} .thread=${t} .controller=${this.controller}></wave-thread>`,
          )}
      </div>
    `;
  }

  protected override render(): TemplateResult {
    if (this.blip.deleted) return this.renderDeleted();
    const content = this.controller.blipContent(this.blip.id);
    return html`
      <div class="wave-blip">
        ${this.renderByline()}
        <blip-view
          .content=${content}
          .selfAddress=${this.controller.user}
          .remoteCarets=${this.controller.remoteCaretsFor?.(this.blip.id) ?? []}
          @edit=${this.onEdit}
          @caret=${this.onCaret}
          @undo=${this.onUndo}
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
          ${this.controller.deleteBlip === undefined
            ? html``
            : html`<button
                class="delete-btn"
                @mousedown=${(e: MouseEvent) => e.preventDefault()}
                @click=${this.onDelete}
              >
                Delete
              </button>`}
        </div>
        ${this.renderCommentPills()}
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

  // renderByline shows who wrote this blip: a small avatar + display name above the
  // content. Empty when the author is unknown (e.g. a snapshot open).
  private renderByline(): TemplateResult {
    const author = this.controller.blipAuthor?.(this.blip.id);
    if (author === undefined || author === "") return html``;
    profiles.ensure([author]);
    const profile = profiles.get(author);
    return html`<div class="blip-byline" title=${author}>
      ${avatar(author, profile, 18)}<span class="byline-name">${displayNameFor(author, profile)}</span>
    </div>`;
  }

  // renderCommentPills shows the blip's inline comments as a compact, scannable strip
  // of collapsed pills (snippet + reply count) — so comments are VISIBLE, not hidden
  // behind the in-text 💬 anchor. Tapping a pill (or the anchor) opens that thread in
  // the comment sheet. Orphaned comments (their anchor text deleted) still appear here.
  private renderCommentPills(): TemplateResult {
    const inline = this.blip.threads.filter((t) => t.inline);
    if (inline.length === 0) return html``;
    return html`<div class="comment-pills">
      ${inline.map((t) => {
        const snippet = this.threadSnippet(t);
        const count = t.blips.length;
        const first = t.blips[0];
        const author = first !== undefined ? this.controller.blipAuthor?.(first.id) : undefined;
        if (author !== undefined && author !== "") profiles.ensure([author]);
        return html`<button
          class="comment-pill"
          title=${snippet === "" ? "Comment" : snippet}
          @click=${() => this.openComment(t.id)}
        >
          <span class="cp-glyph" aria-hidden="true"
            >${author !== undefined && author !== "" ? avatar(author, profiles.get(author), 16) : "💬"}</span
          >
          <span class="cp-text">${snippet === "" ? "Comment" : truncate(snippet, PILL_SNIPPET_MAX)}</span>
          ${count > 1 ? html`<span class="cp-count">${count}</span>` : ""}
        </button>`;
      })}
    </div>`;
  }

  // threadSnippet is the plain text of a comment thread's first blip, for its pill.
  private threadSnippet(t: Thread): string {
    const first = t.blips[0];
    if (first === undefined) return "";
    const proj = project(this.controller.blipContent(first.id));
    return proj.paragraphs.map(paragraphText).join(" ").trim();
  }

  // openComment opens a thread's comment sheet (same path as tapping its 💬 anchor).
  private openComment(id: string): void {
    this.dispatchEvent(
      new CustomEvent<{ id: string; focus: boolean }>("anchor-activate", {
        detail: { id, focus: false },
        bubbles: true,
        composed: true,
      }),
    );
  }
}

// truncate shortens s to max runes, appending an ellipsis when cut.
function truncate(s: string, max: number): string {
  const runes = [...s];
  return runes.length <= max ? s : runes.slice(0, max).join("") + "…";
}

customElements.define("wave-blip", WaveBlip);
