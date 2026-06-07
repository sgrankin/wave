// <wave-identity> — the signed-in user's identity widget: their avatar + display
// name with an inline editor to set the name (POSTed to the profile API via the
// shared cache). Lives in the app-shell left pane so it is always visible, even
// before any wave is open. Subscribes to the profile cache so the name appears
// once it resolves and updates immediately after an edit.
//
// Non-decorator Lit + light DOM, matching the rest of the client.

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";

import { displayNameFor, profiles } from "../wave/profiles.ts";
import { avatar } from "./participant.ts";

export class WaveIdentity extends LitElement {
  static override properties = {
    address: {},
    editing: { state: true },
    saveError: { state: true },
    rev: { state: true },
  };

  declare address: string;
  declare editing: boolean;
  declare saveError: boolean; // last save failed; keep the editor open with a note
  declare rev: number; // bumped on profile-cache changes to force a re-render

  private unsub: (() => void) | null = null;

  constructor() {
    super();
    this.address = "";
    this.editing = false;
    this.saveError = false;
    this.rev = 0;
  }

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  override connectedCallback(): void {
    super.connectedCallback();
    this.unsub = profiles.onChange(() => this.rev++);
    if (this.address !== "") profiles.ensure([this.address]);
  }

  override disconnectedCallback(): void {
    super.disconnectedCallback();
    this.unsub?.();
    this.unsub = null;
  }

  protected override updated(changed: Map<string, unknown>): void {
    if (changed.has("address") && this.address !== "") profiles.ensure([this.address]);
  }

  private startEdit = (): void => {
    this.saveError = false;
    this.editing = true;
  };

  private cancel = (): void => {
    this.saveError = false;
    this.editing = false;
  };

  private save = (e: Event): void => {
    e.preventDefault();
    const form = e.currentTarget as HTMLFormElement;
    const input = form.querySelector<HTMLInputElement>(".identity-input");
    if (input === null) return;
    const name = input.value.trim();
    this.saveError = false;
    // Leave edit mode only if the POST succeeds; on failure keep the editor open
    // (the typed name is preserved — the input is uncontrolled) and flag the error.
    void profiles.setOwn(this.address, name).then((ok) => {
      if (ok) this.editing = false;
      else this.saveError = true;
    });
  };

  protected override render(): TemplateResult {
    void this.rev;
    if (this.address === "") return html``;
    const profile = profiles.get(this.address);

    if (this.editing) {
      // Uncontrolled input: the `value` attribute seeds it once; we never write the
      // `.value` property on re-render, so a profile-cache "change" (rev++) mid-edit
      // cannot clobber what the user is typing. save() reads the live value.
      return html`
        ${STYLES}
        <form class="wave-identity editing" @submit=${this.save}>
          <input
            class="identity-input"
            type="text"
            value=${profile?.displayName ?? ""}
            placeholder="Your name"
            autocomplete="off"
            autofocus
          />
          <button type="submit" class="identity-save">Save</button>
          <button type="button" class="identity-cancel" @click=${this.cancel}>Cancel</button>
          ${this.saveError ? html`<span class="identity-error">couldn't save</span>` : html``}
        </form>
      `;
    }

    return html`
      ${STYLES}
      <div class="wave-identity" title=${this.address}>
        ${avatar(this.address, profile, 24)}
        <span class="identity-name">${displayNameFor(this.address, profile)}</span>
        <button type="button" class="identity-edit" @click=${this.startEdit} title="Set your display name">
          edit
        </button>
      </div>
    `;
  }
}

customElements.define("wave-identity", WaveIdentity);

const STYLES = html`
  <style>
    wave-identity {
      display: block;
      border-bottom: 1px solid #e0e0e0;
    }
    wave-identity .wave-identity {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 10px 12px;
      font: 13px system-ui, sans-serif;
    }
    wave-identity .identity-name {
      flex: 1;
      font-weight: 600;
      color: #222;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    wave-identity .identity-edit,
    wave-identity .identity-save,
    wave-identity .identity-cancel {
      font: 11px system-ui, sans-serif;
      color: #4060c0;
      background: none;
      border: none;
      padding: 2px 4px;
      cursor: pointer;
    }
    wave-identity .identity-edit:hover,
    wave-identity .identity-save:hover,
    wave-identity .identity-cancel:hover {
      text-decoration: underline;
    }
    wave-identity .wave-identity.editing {
      display: flex;
      align-items: center;
      gap: 4px;
      padding: 8px 12px;
    }
    wave-identity .identity-input {
      flex: 1;
      font: 13px system-ui, sans-serif;
      border: 1px solid #ccc;
      border-radius: 4px;
      padding: 3px 6px;
    }
    wave-identity .identity-error {
      color: #c62828;
      font: 11px system-ui, sans-serif;
    }
  </style>
`;
