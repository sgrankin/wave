// Minting fresh wave identifiers client-side. A new conversation wave is a random
// wave id paired with the conventional "conv+root" conversation wavelet; opening
// it triggers the server's open-or-create seeding (the opener becomes the creator
// and first participant). No server round-trip is needed to allocate the id.

import { WaveletName } from "./types.ts";

// newConversationWave returns a fresh, unique conversation wavelet name in domain:
// "w+<random>" / "conv+root". The random token uses the web-safe base64 alphabet
// (A-Za-z0-9-_), all of which are valid wave id characters.
export function newConversationWave(domain: string): WaveletName {
  return new WaveletName(domain, `w+${randomToken(9)}`, domain, "conv+root");
}

// domainOf returns the domain part of a participant address ("a@b.com" → "b.com").
export function domainOf(address: string): string {
  const i = address.indexOf("@");
  return i >= 0 ? address.slice(i + 1) : address;
}

function randomToken(n: number): string {
  const bytes = new Uint8Array(n);
  crypto.getRandomValues(bytes);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
