// Lit render helpers for humanized participant display: a colored initials
// avatar and a chip (avatar + display name) with the raw address as a tooltip.
// Pure functions — pass the resolved Profile (or undefined) from the shared
// profile cache. Styling is inline so the helpers carry no cross-component CSS
// dependency and can be dropped into any light-DOM host.

import { html } from "lit";
import type { TemplateResult } from "lit";

import { colorFor, displayNameFor, initialsFor } from "../wave/profiles.ts";
import type { Profile } from "../wave/profiles.ts";

// avatarStyle builds the inline style for a circular initials avatar of the given
// pixel size, colored deterministically from the address.
function avatarStyle(address: string, size: number): string {
  return [
    `background:${colorFor(address)}`,
    "display:inline-flex",
    "align-items:center",
    "justify-content:center",
    `width:${size}px`,
    `height:${size}px`,
    "border-radius:50%",
    "color:#fff",
    `font:600 ${Math.round(size * 0.46)}px system-ui, sans-serif`,
    "line-height:1",
    "flex:none",
    "user-select:none",
  ].join(";");
}

/** A colored circular initials avatar for an address (default 18px). */
export function avatar(address: string, profile?: Profile, size = 18): TemplateResult {
  return html`<span class="wave-avatar" style=${avatarStyle(address, size)} aria-hidden="true"
    >${initialsFor(address, profile)}</span
  >`;
}

/** A participant chip: avatar + display name, with the address as a hover
 *  tooltip. extraClass is appended to the wrapper (e.g. to mark the signed-in
 *  user). */
export function participantChip(address: string, profile?: Profile, extraClass = ""): TemplateResult {
  const cls = "wave-participant" + (extraClass !== "" ? " " + extraClass : "");
  return html`<span
    class=${cls}
    title=${address}
    style="display:inline-flex;align-items:center;gap:5px;max-width:100%;"
    >${avatar(address, profile)}<span
      class="wave-participant-name"
      style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;"
      >${displayNameFor(address, profile)}</span
    ></span
  >`;
}
