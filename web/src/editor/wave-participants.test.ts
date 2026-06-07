// Browser component tests for the participants roster in <wave-conversation>.
//
// We can't instantiate <wave-conversation> itself in component tests (it needs a
// live WebSocket), so we test the roster UI by directly calling the private
// _renderRoster method via a minimal stub component that mirrors the same render
// logic. The fake controller pattern mirrors wave-thread.test.ts.
//
// Covers:
//  1. Roster renders the participant list from controller.participants().
//  2. Clicking "Add" with a valid address calls controller.addParticipant.
//  3. Clicking "Add" with an invalid address does NOT call addParticipant.
//
// Run via: npm run test:web  (from web/)

import { LitElement, html } from "lit";
import type { TemplateResult } from "lit";
import type { T } from "../../testing/harness.ts";
import { eq, render } from "../../testing/harness.ts";

import type { ConvController } from "./controller.ts";
import { DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import { contactSuggestions, displayNameFor, profiles } from "../wave/profiles.ts";
import { participantChip } from "./participant.ts";

// ---------------------------------------------------------------------------
// Fake ConvController for roster tests
// ---------------------------------------------------------------------------

function fakeController(participants: string[] = []): ConvController & {
  addCalls: string[];
  removeCalls: string[];
} {
  const addCalls: string[] = [];
  const removeCalls: string[] = [];
  return {
    user: "creator@example.com",
    blipContent(_blipId: string): DocOp {
      return DocOp.empty();
    },
    editBlip(_blipId: string, _ops: Component[]): void {},
    continueThread(_threadId: string): void {},
    replyToBlip(_parentBlipId: string, _inline: boolean): string {
      return "";
    },
    participants(): string[] {
      return participants;
    },
    addParticipant(addr: string): void {
      // mirror the real implementation: validate by checking for '@'
      if (!addr.includes("@") || addr.endsWith("@") || (addr.match(/@/g) ?? []).length !== 1) {
        throw new Error(`id: invalid participant address ${addr}`);
      }
      addCalls.push(addr.toLowerCase());
    },
    removeParticipant(addr: string): void {
      removeCalls.push(addr);
    },
    attachImage(_blipId: string, _file: File, _offset: number): void {
      // no-op in participant-roster tests
    },
    addCalls,
    removeCalls,
  };
}

// ---------------------------------------------------------------------------
// Minimal test host that exposes _renderRoster as a Lit template
// ---------------------------------------------------------------------------

class RosterHost extends LitElement {
  static override properties = { controller: { state: true } };
  declare controller: ConvController | null;

  constructor() {
    super();
    this.controller = null;
  }

  protected override createRenderRoot(): HTMLElement {
    return this;
  }

  // Mirrors WaveConversation._renderRoster: humanized chips + a contact-picker
  // datalist + the add form. Uses the real participantChip helper so the chip
  // rendering under test is the production code.
  private _renderRoster(controller: ConvController): TemplateResult {
    const parts = controller.participants().slice().sort();
    profiles.ensure(parts);
    const suggestions = contactSuggestions(profiles, parts);

    const onAdd = (e: Event): void => {
      e.preventDefault();
      const form = e.currentTarget as HTMLFormElement;
      const input = form.querySelector<HTMLInputElement>(".add-participant-input");
      if (input === null) return;
      const val = input.value.trim();
      if (val === "") return;
      try {
        controller.addParticipant(val);
        input.value = "";
      } catch {
        input.classList.add("add-participant-error");
        setTimeout(() => input.classList.remove("add-participant-error"), 600);
      }
    };
    return html`
      <div class="conv-roster">
        <span class="roster-label">Participants:</span>
        ${parts.map((p) => html`<span class="roster-chip">${participantChip(p, profiles.get(p))}</span>`)}
        <form class="add-participant-form" @submit=${onAdd}>
          <input
            class="add-participant-input"
            type="text"
            list="roster-contacts"
            placeholder="user@domain"
            autocomplete="off"
          />
          <datalist id="roster-contacts">
            ${suggestions.map(
              (p) => html`<option value=${p.address} label=${displayNameFor(p.address, p)}></option>`,
            )}
          </datalist>
          <button type="submit" class="add-participant-btn">+ Add</button>
        </form>
      </div>
    `;
  }

  protected override render(): TemplateResult {
    if (this.controller === null) return html``;
    return this._renderRoster(this.controller);
  }
}
customElements.define("x-roster-host", RosterHost);

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function renderRoster(ctrl: ConvController): Promise<HTMLElement> {
  const el = await render(html`<x-roster-host .controller=${ctrl}></x-roster-host>`);
  if ("updateComplete" in el) await (el as { updateComplete: Promise<unknown> }).updateComplete;
  return el;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// chipName reads the display-name text of the nth roster chip (the chip also
// holds an avatar glyph, so we target the name span, not the whole chip text).
function chipName(el: HTMLElement, nth: number): string {
  return el.querySelectorAll(".roster-chip .wave-participant-name")[nth]?.textContent?.trim() ?? "";
}

export async function testRosterRendersParticipants(t: T): Promise<void> {
  void t;
  const ctrl = fakeController(["alice@example.com", "bob@example.com"]);
  const el = await renderRoster(ctrl);

  const chips = el.querySelectorAll(".roster-chip");
  eq(chips.length, 2, "two participant chips");
  // Each chip carries a colored initials avatar plus the name (no profile cached
  // ⇒ the name falls back to the address). Sorted alphabetically.
  eq(el.querySelectorAll(".roster-chip .wave-avatar").length, 2, "every chip has an avatar");
  eq(chipName(el, 0), "alice@example.com", "first chip name");
  eq(chipName(el, 1), "bob@example.com", "second chip name");
  // The contact-picker datalist is present for the add box.
  eq(el.querySelector("datalist#roster-contacts") !== null, true, "contact datalist present");
}

export async function testRosterRendersEmptyList(t: T): Promise<void> {
  void t;
  const ctrl = fakeController([]);
  const el = await renderRoster(ctrl);

  const chips = el.querySelectorAll(".roster-chip");
  eq(chips.length, 0, "no chips when empty");

  // The add form should still be present.
  const form = el.querySelector(".add-participant-form");
  eq(form !== null, true, "add form present");
}

export async function testAddValidParticipant(t: T): Promise<void> {
  void t;
  const ctrl = fakeController([]);
  const el = await renderRoster(ctrl);

  const input = el.querySelector<HTMLInputElement>(".add-participant-input");
  const btn = el.querySelector<HTMLButtonElement>(".add-participant-btn");
  eq(input !== null, true, "input present");
  eq(btn !== null, true, "add button present");

  // Type a valid address and submit.
  input!.value = "newuser@example.com";
  btn!.click(); // submits the form

  // Wait for async event propagation.
  await new Promise<void>((r) => setTimeout(r, 0));

  eq((ctrl as ReturnType<typeof fakeController>).addCalls.length, 1, "addParticipant called once");
  eq(
    (ctrl as ReturnType<typeof fakeController>).addCalls[0],
    "newuser@example.com",
    "addParticipant called with correct address",
  );
  // Input should be cleared on success.
  eq(input!.value, "", "input cleared after successful add");
}

export async function testAddInvalidParticipantDoesNotCrash(t: T): Promise<void> {
  void t;
  const ctrl = fakeController([]);
  const el = await renderRoster(ctrl);

  const input = el.querySelector<HTMLInputElement>(".add-participant-input");
  const btn = el.querySelector<HTMLButtonElement>(".add-participant-btn");

  // Attempt to add an invalid address (no '@').
  input!.value = "notavalid";
  btn!.click();
  await new Promise<void>((r) => setTimeout(r, 0));

  // addParticipant must not have been called.
  eq((ctrl as ReturnType<typeof fakeController>).addCalls.length, 0, "addParticipant not called for invalid address");
  // Input should NOT be cleared (keeping user's input for correction).
  eq(input!.value, "notavalid", "input preserved for invalid address");
  // Error class added.
  eq(input!.classList.contains("add-participant-error"), true, "error class set");
}

export async function testAddEmptyInputDoesNothing(t: T): Promise<void> {
  void t;
  const ctrl = fakeController([]);
  const el = await renderRoster(ctrl);

  const input = el.querySelector<HTMLInputElement>(".add-participant-input");
  const btn = el.querySelector<HTMLButtonElement>(".add-participant-btn");

  input!.value = "  "; // whitespace only
  btn!.click();
  await new Promise<void>((r) => setTimeout(r, 0));

  eq((ctrl as ReturnType<typeof fakeController>).addCalls.length, 0, "addParticipant not called for empty input");
}

export async function testRosterSortedAlphabetically(t: T): Promise<void> {
  void t;
  // Provide participants in reverse order — they should be sorted.
  const ctrl = fakeController(["zoe@example.com", "alice@example.com", "mike@example.com"]);
  const el = await renderRoster(ctrl);

  const chips = el.querySelectorAll(".roster-chip");
  eq(chips.length, 3, "three chips");
  eq(chipName(el, 0), "alice@example.com");
  eq(chipName(el, 1), "mike@example.com");
  eq(chipName(el, 2), "zoe@example.com");
}
