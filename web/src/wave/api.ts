// Read-side wave query client: the inbox and search endpoints (served by the Go
// queryapi package). The session cookie authenticates the requests; results are
// already scoped to the signed-in participant's inbox server-side.

import { dlog } from "./debug.ts";

// WaveDigest is one wave's summary for the list view (mirrors queryapi.Digest).
export interface WaveDigest {
  wave: string; // serialized WaveletName
  title: string;
  snippet: string;
  creator: string;
  participants: string[];
  version: number;
  lastModifiedTime: number;
  unread: boolean; // version > the signed-in participant's read version
}

async function fetchWaves(path: string): Promise<WaveDigest[]> {
  const resp = await fetch(path, { credentials: "same-origin" });
  if (!resp.ok) throw new Error(`${path}: ${resp.status}`);
  const body = (await resp.json()) as { waves?: WaveDigest[] };
  return body.waves ?? [];
}

/** The signed-in participant's waves, most-recently-modified first. */
export function fetchInbox(): Promise<WaveDigest[]> {
  return fetchWaves("/api/inbox");
}

/** Waves matching query (Wave operators: with:, creator:, orderby:modified, free text). */
export function searchWaves(query: string): Promise<WaveDigest[]> {
  return fetchWaves(`/api/search?q=${encodeURIComponent(query)}`);
}

/** Mark a wave read through the given version (clears its unread state). Best-effort. */
export async function markRead(wave: string, version: number): Promise<void> {
  await fetch(`/api/read?wave=${encodeURIComponent(wave)}&version=${version}`, {
    method: "POST",
    credentials: "same-origin",
  });
}

/**
 * The participant's per-blip read versions for one wave: blipId → the server
 * version they have read that blip through. A blip absent from the map (or read
 * through version 0) is unread once it has any content. Fetched when a wave opens
 * so the client can paint unread markers before any live delta arrives.
 */
export async function fetchReadState(wave: string): Promise<Map<string, number>> {
  const resp = await fetch(`/api/read?wave=${encodeURIComponent(wave)}`, { credentials: "same-origin" });
  if (!resp.ok) throw new Error(`/api/read: ${resp.status}`);
  const body = (await resp.json()) as { blipReads?: Record<string, number> };
  return new Map(Object.entries(body.blipReads ?? {}));
}

/**
 * Mark a single blip read through the given server version (its last-modified
 * version), clearing that blip's unread marker. The blip id is URL-encoded — it
 * contains a '+' (e.g. "b+root") which a form decoder would otherwise turn into a
 * space. Best-effort; a failed POST just leaves the blip marked unread.
 */
export async function markBlipRead(wave: string, blip: string, version: number): Promise<void> {
  // Best-effort, but not silent: a failed mark leaves the blip looking unread on
  // the next reopen, so trace it under ?debug=1 (matching fetchReadState's
  // diagnostic) rather than swallowing it entirely.
  try {
    const resp = await fetch(
      `/api/read?wave=${encodeURIComponent(wave)}&blip=${encodeURIComponent(blip)}&version=${version}`,
      { method: "POST", credentials: "same-origin" },
    );
    if (!resp.ok) dlog("markBlipRead non-OK", resp.status, blip, version);
  } catch (e) {
    dlog("markBlipRead failed", blip, version, e);
  }
}
