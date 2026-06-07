// Browser component tests for <wave-identity> — the signed-in user's avatar +
// display-name widget with an inline name editor.
//
// We render the real element. With no profile cached the name falls back to the
// address; clicking "edit" swaps in the input. The full set-name round trip
// (POST → cache → re-render) is covered end-to-end by the browser e2e against a
// real server, so here we stay at the static-render + edit-toggle level.
//
// Run via: npm run test:web  (from web/)

import { html } from "lit";
import type { T } from "../../testing/harness.ts";
import { eq, render } from "../../testing/harness.ts";

import "./wave-identity.ts";
import type { WaveIdentity } from "./wave-identity.ts";

async function renderIdentity(address: string): Promise<WaveIdentity> {
  const el = (await render(html`<wave-identity .address=${address}></wave-identity>`)) as WaveIdentity;
  await el.updateComplete;
  return el;
}

export async function testIdentityRendersAvatarAndAddressFallback(t: T): Promise<void> {
  void t;
  const el = await renderIdentity("dana@example.com");

  eq(el.querySelector(".wave-avatar") !== null, true, "avatar present");
  const name = el.querySelector(".identity-name");
  eq(name !== null, true, "name present");
  // No profile cached for this fresh address ⇒ falls back to the address.
  eq(name!.textContent!.trim(), "dana@example.com", "name falls back to address");
  eq(el.querySelector(".identity-edit") !== null, true, "edit button present");
}

export async function testIdentityEditToggle(t: T): Promise<void> {
  void t;
  const el = await renderIdentity("erin@example.com");

  // Not editing initially.
  eq(el.querySelector(".identity-input"), null, "no input before edit");

  const editBtn = el.querySelector<HTMLButtonElement>(".identity-edit");
  eq(editBtn !== null, true, "edit button present");
  editBtn!.click();
  await el.updateComplete;

  const input = el.querySelector<HTMLInputElement>(".identity-input");
  eq(input !== null, true, "input appears in edit mode");
  eq(el.querySelector(".identity-save") !== null, true, "save button present");

  // Cancel returns to the static view.
  el.querySelector<HTMLButtonElement>(".identity-cancel")!.click();
  await el.updateComplete;
  eq(el.querySelector(".identity-input"), null, "input gone after cancel");
  eq(el.querySelector(".identity-name") !== null, true, "name shown again after cancel");
}

export async function testIdentityEmptyAddressRendersNothing(t: T): Promise<void> {
  void t;
  const el = await renderIdentity("");
  eq(el.querySelector(".wave-identity"), null, "nothing rendered for empty address");
}
