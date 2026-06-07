// End-to-end browser convergence test: drive the real conversation-view UI in
// headless Chromium against a real Go `waved`, and assert a fresh client
// converges. This is the regression guard for the whole browser stack — the
// controlled contenteditable, the recursive thread/blip components, the manifest
// authoring, and the OptimisticClient — against the authoritative server.
//
// It catches bugs that unit/component tests (fake controllers, synthetic events)
// cannot: notably a controlled-editor that lets the browser edit natively, so the
// text shows locally but is never submitted (the create-then-edit-a-blip case).
//
// Heavy (builds the bundle, spawns waved, launches Chromium) so it is NOT in the
// default `npm test`; run it with `npm run test:browser`.
// Requires: `npx playwright install chromium`. Loopback + process spawn may need
// the sandbox disabled.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import net from "node:net";
import os from "node:os";
import path from "node:path";

import { chromium, type Browser, type Page } from "playwright";

const WEB = path.resolve(import.meta.dirname, "..");
const REPO = path.resolve(WEB, "..");
const binPath = path.join(os.tmpdir(), `waved-btest-${process.pid}`);

let proc: ChildProcess | undefined;
let browser: Browser | undefined;
let port = 0;

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const addr = s.address();
      if (addr === null || typeof addr === "string") return reject(new Error("no port"));
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

// pageURL builds the editor URL for a user + wavelet, URL-encoding the wave param.
function pageURL(user: string, waveLocal: string): string {
  const wave = `example.com/${waveLocal}/~/conv+root`;
  return `http://127.0.0.1:${port}/?user=${encodeURIComponent(user)}&wave=${encodeURIComponent(wave)}`;
}

// open a connected editor page; waits until the conversation has rendered at
// least one editable blip (i.e. connected, and bootstrapped if it was empty).
async function open(user: string, waveLocal: string): Promise<Page> {
  const page = await browser!.newPage();
  await page.goto(pageURL(user, waveLocal));
  await page.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });
  return page;
}

// typeInto focuses the nth blip editor and types text through real key events
// (so beforeinput fires and the controlled editor translates it to ops).
async function typeInto(page: Page, nth: number, text: string): Promise<void> {
  const blip = page.locator(".blip-doc").nth(nth);
  await blip.click();
  await blip.pressSequentially(text, { delay: 5 });
}

// blipTexts returns the visible text of every blip editor on the page, in order.
function blipTexts(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".blip-doc")).map((el) => (el.textContent ?? "").trim()),
  );
}

// waitForBlipTexts polls until every `want` substring is present across the
// page's blips (a fresh client converging via history replay), or times out.
async function waitForBlipTexts(page: Page, want: string[], timeoutMs = 10_000): Promise<string[]> {
  await page.waitForFunction(
    (wanted: string[]) => {
      const texts = Array.from(document.querySelectorAll(".blip-doc")).map((el) => el.textContent ?? "");
      return wanted.every((w) => texts.some((t) => t.includes(w)));
    },
    want,
    { timeout: timeoutMs },
  );
  return blipTexts(page);
}

before(async () => {
  execFileSync("go", ["build", "-o", binPath, "./cmd/waved"], { cwd: REPO, stdio: "inherit" });
  execFileSync("node", ["esbuild.mjs"], { cwd: WEB, stdio: "inherit" });
  port = await freePort();
  const sock = path.join(os.tmpdir(), `waved-btest-${process.pid}.sock`);
  proc = spawn(
    binPath,
    [
      "-net", "unix", "-addr", sock,
      "-db", ":memory:",
      "-http", "",
      "-index=false",
      "-ws", `127.0.0.1:${port}`,
      "-webroot", "web/dist",
      "-log-level", "warn",
    ],
    { cwd: REPO, stdio: "inherit" },
  );
  await waitListening(port, 10_000);
  // A normal headless launch (NOT the web-runner's --single-process/--no-zygote
  // Mach-port workaround, which is unstable for full page navigation): this heavy
  // test is run with the host sandbox disabled, so Chromium's own sandbox works;
  // --no-sandbox keeps it robust across environments.
  browser = await chromium.launch({ args: ["--no-sandbox", "--disable-gpu"] });
});

after(async () => {
  await browser?.close();
  proc?.kill("SIGTERM");
});

// The core regression: a blip a client creates and then immediately edits must
// converge. (The bug: the caret landed in a stray text node in the freshly
// created blip; the controlled editor failed to map it, fell through to native
// browser editing, and the text was never submitted — it showed locally but a
// fresh client never saw it.)
test("create-then-edit a blip converges to a fresh client", async () => {
  const wave = "w+btest-create";
  const alice = await open("alice@example.com", wave);
  try {
    await typeInto(alice, 0, "root by alice");
    await alice.locator(".continue-btn").first().click(); // + New message
    await alice.locator(".blip-doc").nth(1).waitFor({ state: "attached" });
    await typeInto(alice, 1, "second blip body");

    // A fresh client only sees server-committed state — it converges via replay.
    const dave = await open("dave@example.com", wave);
    try {
      const texts = await waitForBlipTexts(dave, ["root by alice", "second blip body"]);
      assert.ok(
        texts.some((t) => t.includes("root by alice")),
        `fresh client missing root text; saw ${JSON.stringify(texts)}`,
      );
      assert.ok(
        texts.some((t) => t.includes("second blip body")),
        `fresh client missing the created-then-edited blip; saw ${JSON.stringify(texts)}`,
      );
    } finally {
      await dave.close();
    }
  } finally {
    await alice.close();
  }
});

// A threaded reply (new reply thread + its first blip) converges, including the
// reply blip's edited content.
test("a threaded reply converges to a fresh client", async () => {
  const wave = "w+btest-reply";
  const alice = await open("alice@example.com", wave);
  try {
    await typeInto(alice, 0, "parent blip");
    await alice.locator(".reply-btn").first().click(); // start a reply thread on the root blip
    await alice.locator(".wave-thread.reply .blip-doc").first().waitFor({ state: "attached" });
    // The reply blip is the 2nd editor on the page.
    await typeInto(alice, 1, "the reply");

    const dave = await open("dave@example.com", wave);
    try {
      await waitForBlipTexts(dave, ["parent blip", "the reply"]);
      // The reply must be nested in a reply thread, not a root sibling.
      const replyInThread = await dave.evaluate(() => {
        const t = document.querySelector(".wave-thread.reply");
        return t !== null && (t.textContent ?? "").includes("the reply");
      });
      assert.ok(replyInThread, "reply text should render inside a nested reply thread");
    } finally {
      await dave.close();
    }
  } finally {
    await alice.close();
  }
});
