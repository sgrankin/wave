// Cross-language integration test: the TypeScript OptimisticClient against a real
// Go `waved` server over a real WebSocket. This is the end-to-end interop proof —
// it exercises the whole stack (CBOR codec, OT, clientcc, framing, the WS
// transport) against the authoritative Go implementation, not a simulated server.
//
// It builds and spawns `waved -ws`, logs each client in via the dev /login
// endpoint (capturing the session cookie and carrying it on the WebSocket
// handshake), and asserts convergence. It runs with -seed-conversations=false so
// the wavelet starts empty and the raw version assertions below hold (server-side
// conversation seeding is exercised by the browser harness instead).
// Run from web/:  node --test test/integration.test.ts
// (Loopback networking + process spawn may require the sandbox to be disabled.)

import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import net from "node:net";
import os from "node:os";
import path from "node:path";

import { CONTRIBUTOR_ADD, DocOp, WaveletName, participant } from "../src/wave/types.ts";
import type { Component, Operation, Participant } from "../src/wave/types.ts";
import { OptimisticClient } from "../src/wave/transport.ts";

const REPO = path.resolve(import.meta.dirname, "..", "..");
const ALICE = "alice@example.com";
const BOB = "bob@example.com";

let proc: ChildProcess | undefined;
let port = 0;
const binPath = path.join(os.tmpdir(), `waved-itest-${process.pid}`);

// Each test uses its own wavelet so server state does not leak between tests
// (one waved process serves them all).
function waveletName(local: string): WaveletName {
  return new WaveletName("example.com", local, "example.com", "conv+root");
}

// --- op builders (mirror internal/transport/transport_test.go helpers) ---

function chars(s: string): DocOp {
  return new DocOp([{ kind: "characters", text: s }]);
}

function writeBlip(author: Participant, blipId: string, content: DocOp): Operation[] {
  return [
    {
      kind: "blip",
      blipId,
      op: {
        ctx: { creator: author, timestamp: 1000, versionIncrement: 1, hashedVersion: null },
        contentOp: content,
        method: CONTRIBUTOR_ADD,
      },
    },
  ];
}

function insertAt(author: Participant, blipId: string, length: number, pos: number, text: string): Operation[] {
  const comps: Component[] = [];
  if (pos > 0) comps.push({ kind: "retain", count: pos });
  comps.push({ kind: "characters", text });
  if (length - pos > 0) comps.push({ kind: "retain", count: length - pos });
  return writeBlip(author, blipId, new DocOp(comps));
}

/** Extract the flat text of a (flat-text) blip DocOp. */
function docText(d: DocOp | undefined): string {
  if (d === undefined) return "<missing>";
  let s = "";
  for (const c of d.components) if (c.kind === "characters") s += c.text;
  return s;
}

// --- server lifecycle ---

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const addr = s.address();
      if (addr === null || typeof addr === "string") {
        reject(new Error("no port"));
        return;
      }
      const p = addr.port;
      s.close(() => resolve(p));
    });
  });
}

function waitListening(p: number, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  return new Promise((resolve, reject) => {
    const tryOnce = (): void => {
      const c = net.connect(p, "127.0.0.1");
      c.once("connect", () => {
        c.destroy();
        resolve();
      });
      c.once("error", () => {
        c.destroy();
        if (Date.now() > deadline) reject(new Error("waved did not start listening"));
        else setTimeout(tryOnce, 50);
      });
    };
    tryOnce();
  });
}

before(async () => {
  execFileSync("go", ["build", "-o", binPath, "./cmd/waved"], { cwd: REPO, stdio: "inherit" });
  port = await freePort();
  const sock = path.join(os.tmpdir(), `waved-itest-${process.pid}.sock`);
  proc = spawn(
    binPath,
    [
      "-net", "unix", "-addr", sock,
      "-db", ":memory:",
      "-http", "",
      "-index=false",
      "-ws", `127.0.0.1:${port}`,
      "-seed-conversations=false",
      "-log-level", "warn",
    ],
    { cwd: REPO, stdio: "inherit" },
  );
  await waitListening(port, 10_000);
});

after(() => {
  proc?.kill("SIGTERM");
});

// loginCookie hits the dev /login endpoint for `user` and returns the session
// cookie (the "wave_session=…" pair) to carry on the WebSocket handshake. The
// endpoint redirects on success; we read Set-Cookie without following it.
async function loginCookie(user: string): Promise<string> {
  const resp = await fetch(`http://127.0.0.1:${port}/login?user=${encodeURIComponent(user)}`, {
    redirect: "manual",
  });
  const setCookie = resp.headers.get("set-cookie");
  if (setCookie === null) throw new Error(`no session cookie from /login for ${user}`);
  return setCookie.split(";")[0]; // "wave_session=<token>"
}

// connectAs logs `user` in (session cookie) and opens a client for `name`
// authoring as them; the cookie rides the WebSocket handshake and the server
// binds the authenticated participant to the session (deltas must be authored by
// it).
async function connectAs(name: WaveletName, user: string): Promise<OptimisticClient> {
  const cookie = await loginCookie(user);
  const url = `ws://127.0.0.1:${port}/socket`;
  const c = new OptimisticClient(url, name, participant(user), { headers: { Cookie: cookie } });
  await c.open();
  return c;
}

test("single client: create + edit round-trips against the Go server", async () => {
  const name = waveletName("w+single");
  const a = await connectAs(name, ALICE);
  try {
    await a.submit(writeBlip(participant(ALICE), "b", chars("hi")));
    // Optimistic replica reflects it immediately, before the ack settles.
    assert.equal(docText(a.blipContent("b")), "hi");
    await a.waitServerVersion(1);

    const len = a.blipContent("b")!.documentLength();
    await a.submit(insertAt(participant(ALICE), "b", len, len, "!"));
    await a.waitServerVersion(2);
    assert.equal(docText(a.blipContent("b")), "hi!");
  } finally {
    a.close();
  }
});

test("second client replays history and converges", async () => {
  const name = waveletName("w+single"); // same wavelet as the previous test
  const b = await connectAs(name, BOB);
  try {
    await b.waitServerVersion(2);
    assert.equal(docText(b.blipContent("b")), "hi!");
  } finally {
    b.close();
  }
});

test("two clients converge on concurrent edits", async () => {
  const name = waveletName("w+concurrent");
  const a = await connectAs(name, ALICE);
  const b = await connectAs(name, BOB);
  try {
    // Alice creates "hi"; both reach v1.
    await a.submit(writeBlip(participant(ALICE), "c", chars("hi")));
    await a.waitServerVersion(1);
    await b.waitServerVersion(1);

    // Both edit concurrently against v1 (each builds on its own v1 replica before
    // seeing the other): alice prepends "A", bob appends "B".
    await a.submitWith((blip) => {
      const cur = blip("c")!;
      return insertAt(participant(ALICE), "c", cur.documentLength(), 0, "A");
    });
    await b.submitWith((blip) => {
      const cur = blip("c")!;
      return insertAt(participant(BOB), "c", cur.documentLength(), cur.documentLength(), "B");
    });

    // The two concurrent deltas serialize to v3; both converge there.
    await a.waitServerVersion(3);
    await b.waitServerVersion(3);
    const ca = docText(a.blipContent("c"));
    const cb = docText(b.blipContent("c"));
    assert.equal(ca, cb, `divergence: alice ${ca} vs bob ${cb}`);
    assert.equal(ca, "AhiB");
  } finally {
    a.close();
    b.close();
  }
});
