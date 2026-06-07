// Read-side wave query client: the inbox and search endpoints (served by the Go
// queryapi package). The session cookie authenticates the requests; results are
// already scoped to the signed-in participant's inbox server-side.

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
