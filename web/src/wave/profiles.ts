// Participant profiles: resolving the human-readable display name (and a derived
// initials avatar) for an address. The address is always the identity; a profile
// is presentation metadata fetched from the server's profile API and cached, with
// a fallback to the address itself so rendering never blocks on a fetch.
//
// ProfileCache batches lookups: components call ensure(addresses) freely during
// render, the cache coalesces the unknowns into one /api/profiles request per
// microtask, and fires "change" when results land so views re-render. Pure
// helpers (displayNameFor / initialsFor / colorFor) are deterministic and need no
// cache, so they are safe to call anywhere.

// Profile is one participant's public presentation metadata (mirrors
// profileapi.Profile). displayName is "" when unset.
export interface Profile {
  address: string;
  displayName: string;
}

// firstChars returns the first n code points of s (surrogate-pair safe).
function firstChars(s: string, n: number): string {
  return [...s].slice(0, n).join("");
}

/** The best human label for an address: the profile's display name, else the
 *  address (a fallback that always works). */
export function displayNameFor(address: string, profile?: Profile): string {
  const name = profile?.displayName.trim() ?? "";
  return name !== "" ? name : address;
}

/** 1–2 uppercase initials for an avatar glyph. From a display name: the first
 *  letter of each of the first two words. From a bare address: the first one or
 *  two letters of the local part. */
export function initialsFor(address: string, profile?: Profile): string {
  const name = profile?.displayName.trim() ?? "";
  if (name !== "") {
    const words = name.split(/\s+/).filter((w) => w !== "");
    if (words.length >= 2) return (firstChars(words[0]!, 1) + firstChars(words[1]!, 1)).toUpperCase();
    if (words.length === 1) return firstChars(words[0]!, 2).toUpperCase();
  }
  const local = address.split("@")[0] ?? address;
  return (firstChars(local, 2) || "?").toUpperCase();
}

/** A stable avatar background color for an address (deterministic hue, so the
 *  same person is always the same color). */
export function colorFor(address: string): string {
  let h = 0;
  for (let i = 0; i < address.length; i++) h = (Math.imul(h, 31) + address.charCodeAt(i)) >>> 0;
  // Lightness 30%: the lightest value where white avatar text clears WCAG AA (4.5:1)
  // for every hue (42% failed for ~half the hue wheel — yellow/green/cyan especially).
  return `hsl(${h % 360}, 52%, 30%)`;
}

const PROFILES_CHANGED = "change";

// fetchProfiles batch-resolves display names (one request). Throws on a non-OK
// response so the caller can leave the addresses uncached for a later retry.
async function fetchProfiles(addresses: string[]): Promise<Profile[]> {
  const q = addresses.map((a) => `addr=${encodeURIComponent(a)}`).join("&");
  const resp = await fetch(`/api/profiles?${q}`, { credentials: "same-origin" });
  if (!resp.ok) throw new Error(`/api/profiles: ${resp.status}`);
  const body = (await resp.json()) as { profiles?: Profile[] };
  return body.profiles ?? [];
}

// postOwnProfile sets the signed-in participant's display name. Best-effort:
// returns false (never throws) on any failure.
async function postOwnProfile(displayName: string): Promise<boolean> {
  try {
    const resp = await fetch("/api/profile", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ displayName }),
    });
    return resp.ok;
  } catch {
    return false;
  }
}

// ProfileCache resolves and caches display names by address. It is an
// EventTarget; subscribe with onChange to re-render when fetched profiles arrive.
export class ProfileCache extends EventTarget {
  // Intentionally unbounded for the page session: one entry per distinct address
  // ever seen (including unknowns, cached as empty entries). Realistic cardinality
  // is small (the participants/contacts a user touches in a session), so there is
  // no eviction; a new page load starts fresh.
  private cache = new Map<string, Profile>();
  private pending = new Set<string>();
  private flushScheduled = false;

  /** The cached profile for an address (case-insensitive), or undefined if not
   *  yet fetched. */
  get(address: string): Profile | undefined {
    return this.cache.get(address.toLowerCase());
  }

  /** Every cached profile (for contact suggestions in the participant picker). */
  known(): Profile[] {
    return [...this.cache.values()];
  }

  /** Request profiles for any of `addresses` not already cached or in flight,
   *  coalescing the network fetch across a microtask. Cheap to call per render;
   *  converges (a render triggered by the resulting "change" finds everything
   *  cached and queues nothing further). */
  ensure(addresses: Iterable<string>): void {
    let added = false;
    for (const a of addresses) {
      const addr = a.toLowerCase();
      if (addr === "" || this.cache.has(addr) || this.pending.has(addr)) continue;
      this.pending.add(addr);
      added = true;
    }
    if (added) this.scheduleFlush();
  }

  private scheduleFlush(): void {
    if (this.flushScheduled) return;
    this.flushScheduled = true;
    queueMicrotask(() => {
      this.flushScheduled = false;
      void this.flush();
    });
  }

  private async flush(): Promise<void> {
    const batch = [...this.pending];
    this.pending.clear();
    if (batch.length === 0) return;
    let fetched: Profile[];
    try {
      fetched = await fetchProfiles(batch);
    } catch {
      // Leave the batch uncached (and unmarked) so a later ensure() retries.
      return;
    }
    const byAddr = new Map(fetched.map((p) => [p.address.toLowerCase(), p]));
    for (const addr of batch) {
      // Cache every requested address — including ones the server returned no
      // profile for (an empty-name entry) — so we never refetch in a loop.
      this.cache.set(addr, byAddr.get(addr) ?? { address: addr, displayName: "" });
    }
    this.dispatchEvent(new Event(PROFILES_CHANGED));
  }

  /** Set the signed-in participant's own display name, update the cache for
   *  selfAddress, and fire "change". Returns false on failure (best-effort). */
  async setOwn(selfAddress: string, displayName: string): Promise<boolean> {
    if (!(await postOwnProfile(displayName))) return false;
    const addr = selfAddress.toLowerCase();
    this.cache.set(addr, { address: addr, displayName: displayName.trim() });
    this.dispatchEvent(new Event(PROFILES_CHANGED));
    return true;
  }

  /** Subscribe to cache updates; returns an unsubscribe function. */
  onChange(fn: () => void): () => void {
    const handler = (): void => fn();
    this.addEventListener(PROFILES_CHANGED, handler);
    return () => this.removeEventListener(PROFILES_CHANGED, handler);
  }
}

// The app-wide profile cache (one signed-in user per page).
export const profiles = new ProfileCache();

/** Contact suggestions for a participant picker: every cached profile minus the
 *  `exclude` addresses (case-insensitive), sorted by display name. Shared by the
 *  roster render and its test so the two cannot drift. */
export function contactSuggestions(cache: ProfileCache, exclude: Iterable<string>): Profile[] {
  const present = new Set([...exclude].map((a) => a.toLowerCase()));
  return cache
    .known()
    .filter((p) => !present.has(p.address.toLowerCase()))
    .sort((a, b) => displayNameFor(a.address, a).localeCompare(displayNameFor(b.address, b)));
}
