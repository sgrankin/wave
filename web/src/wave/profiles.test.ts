// ProfileCache + pure-helper conformance. Pure helpers are deterministic; the
// cache's network path is exercised against a stubbed global fetch.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  ProfileCache,
  colorFor,
  contactSuggestions,
  displayNameFor,
  initialsFor,
  type Profile,
} from "./profiles.ts";

// --- pure helpers ---

test("displayNameFor prefers the profile name, falls back to the address", () => {
  assert.equal(displayNameFor("alice@example.com", { address: "alice@example.com", displayName: "Alice" }), "Alice");
  assert.equal(displayNameFor("alice@example.com", { address: "alice@example.com", displayName: "  " }), "alice@example.com");
  assert.equal(displayNameFor("alice@example.com", undefined), "alice@example.com");
});

test("initialsFor derives 1-2 letters from name or address", () => {
  assert.equal(initialsFor("a@x.com", { address: "a@x.com", displayName: "Alice Smith" }), "AS");
  assert.equal(initialsFor("a@x.com", { address: "a@x.com", displayName: "Madonna" }), "MA");
  // No name → first two letters of the local part.
  assert.equal(initialsFor("bob@example.com", undefined), "BO");
  assert.equal(initialsFor("x@example.com", { address: "x@example.com", displayName: "" }), "X");
});

test("colorFor is deterministic and well-formed", () => {
  const c1 = colorFor("alice@example.com");
  const c2 = colorFor("alice@example.com");
  assert.equal(c1, c2, "same address → same color");
  assert.notEqual(colorFor("alice@example.com"), colorFor("bob@example.com"));
  assert.match(c1, /^hsl\(\d+, \d+%, \d+%\)$/);
});

// --- cache ---

// stubFetch installs a global fetch returning the given address→name map for
// /api/profiles, recording each request's address list. Returns the call log.
function stubFetch(names: Record<string, string>): { calls: string[][]; restore: () => void } {
  const calls: string[][] = [];
  const orig = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = typeof input === "string" ? input : input.toString();
    const addrs = [...new URLSearchParams(url.split("?")[1] ?? "").entries()]
      .filter(([k]) => k === "addr")
      .map(([, v]) => v);
    calls.push(addrs);
    const profiles: Profile[] = addrs.map((a) => ({ address: a, displayName: names[a] ?? "" }));
    return new Response(JSON.stringify({ profiles }), { status: 200 });
  }) as typeof fetch;
  return { calls, restore: () => void (globalThis.fetch = orig) };
}

// nextChange resolves after the cache's next "change" event (or rejects on timeout).
function nextChange(cache: ProfileCache): Promise<void> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("no change event")), 1000);
    const off = cache.onChange(() => {
      clearTimeout(t);
      off();
      resolve();
    });
  });
}

test("ensure coalesces unknowns into one fetch and caches results", async () => {
  const { calls, restore } = stubFetch({ "alice@example.com": "Alice", "bob@example.com": "Bob" });
  try {
    const cache = new ProfileCache();
    assert.equal(cache.get("alice@example.com"), undefined, "uncached before");

    const changed = nextChange(cache);
    cache.ensure(["alice@example.com", "bob@example.com", "alice@example.com"]); // dup ignored
    await changed;

    assert.equal(calls.length, 1, "one batched request");
    assert.deepEqual(calls[0]!.sort(), ["alice@example.com", "bob@example.com"]);
    assert.equal(cache.get("alice@example.com")?.displayName, "Alice");
    assert.equal(cache.get("BOB@EXAMPLE.COM")?.displayName, "Bob", "lookup is case-insensitive");
  } finally {
    restore();
  }
});

test("ensure caches unknowns and never refetches them", async () => {
  const { calls, restore } = stubFetch({}); // server knows nobody
  try {
    const cache = new ProfileCache();
    const changed = nextChange(cache);
    cache.ensure(["ghost@example.com"]);
    await changed;
    assert.equal(cache.get("ghost@example.com")?.displayName, "", "cached as empty entry");

    // A second ensure for the same address must not trigger another fetch.
    cache.ensure(["ghost@example.com"]);
    await new Promise((r) => setTimeout(r, 10));
    assert.equal(calls.length, 1, "no refetch for a known-empty address");
  } finally {
    restore();
  }
});

test("setOwn posts and updates the cache for self", async () => {
  const orig = globalThis.fetch;
  let posted: { url: string; body: string; method: string; credentials: string; contentType: string } | null = null;
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    posted = {
      url: typeof input === "string" ? input : input.toString(),
      body: String(init?.body ?? ""),
      method: init?.method ?? "GET",
      credentials: init?.credentials ?? "",
      contentType: headers.get("Content-Type") ?? "",
    };
    return new Response(null, { status: 204 });
  }) as typeof fetch;
  try {
    const cache = new ProfileCache();
    const ok = await cache.setOwn("Me@Example.com", "  My Name  ");
    assert.equal(ok, true);
    assert.equal(posted!.url, "/api/profile");
    // The session cookie must ride the request, and the server routes "POST /api/profile".
    assert.equal(posted!.method, "POST", "uses POST (server routes by method)");
    assert.equal(posted!.credentials, "same-origin", "sends the session cookie");
    assert.equal(posted!.contentType, "application/json");
    assert.deepEqual(JSON.parse(posted!.body), { displayName: "  My Name  " });
    // Cached under the lowercased address, trimmed.
    assert.equal(cache.get("me@example.com")?.displayName, "My Name");
  } finally {
    globalThis.fetch = orig;
  }
});

test("contactSuggestions excludes current participants and sorts by display name", async () => {
  const { restore } = stubFetch({
    "zoe@example.com": "Zoe",
    "alice@example.com": "Alice",
    "bob@example.com": "Bob",
  });
  try {
    const cache = new ProfileCache();
    const changed = nextChange(cache);
    cache.ensure(["zoe@example.com", "alice@example.com", "bob@example.com"]);
    await changed;

    // Exclude bob (already a participant, case-insensitively); the rest come back
    // sorted by display name.
    const sugg = contactSuggestions(cache, ["BOB@EXAMPLE.COM"]);
    assert.deepEqual(
      sugg.map((p) => p.address),
      ["alice@example.com", "zoe@example.com"],
      "bob excluded, others sorted by name",
    );
    assert.equal(sugg[0]!.displayName, "Alice", "profile carried through");
  } finally {
    restore();
  }
});

test("setOwn returns false on a failed POST and leaves the cache untouched", async () => {
  const orig = globalThis.fetch;
  globalThis.fetch = (async () => new Response("nope", { status: 400 })) as typeof fetch;
  try {
    const cache = new ProfileCache();
    const ok = await cache.setOwn("me@example.com", "Name");
    assert.equal(ok, false);
    assert.equal(cache.get("me@example.com"), undefined);
  } finally {
    globalThis.fetch = orig;
  }
});
