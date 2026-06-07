// Read-side playback client: the history timeline and the rendered conversation at
// a past version (served by the Go playbackapi package). The session cookie
// authenticates; the server gates by wavelet membership. State is rendered to plain
// text server-side, so there is no document-op decoding here.

// DeltaDigest is one entry in the playback timeline (mirrors playbackapi.DeltaDigest).
export interface DeltaDigest {
  author: string;
  version: number; // resulting wavelet version after this delta
  timestamp: number;
  opCount: number;
}

// BlipView / ConversationView mirror playbackapi: a blip's rendered plain text, and
// the conversation as it stood at a version.
export interface BlipView {
  id: string;
  author: string;
  text: string;
}

export interface ConversationView {
  version: number;
  participants: string[];
  blips: BlipView[];
}

/** The timeline of applied deltas for a wave (oldest first). */
export async function fetchPlaybackDeltas(wave: string): Promise<DeltaDigest[]> {
  const resp = await fetch(`/api/playback/deltas?wave=${encodeURIComponent(wave)}`, {
    credentials: "same-origin",
  });
  if (!resp.ok) throw new Error(`/api/playback/deltas: ${resp.status}`);
  const body = (await resp.json()) as { deltas?: DeltaDigest[] };
  return body.deltas ?? [];
}

/** The conversation rendered as it stood at the given version. */
export async function fetchPlaybackState(wave: string, version: number): Promise<ConversationView> {
  const resp = await fetch(
    `/api/playback/state?wave=${encodeURIComponent(wave)}&version=${version}`,
    { credentials: "same-origin" },
  );
  if (!resp.ok) throw new Error(`/api/playback/state: ${resp.status}`);
  return (await resp.json()) as ConversationView;
}
