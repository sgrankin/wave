// Browser convergence test harness: the plumbing for driving the real editor UI
// in headless Chromium against a real Go `waved`, so a scenario reads as a few
// lines (open clients, act, assert convergence). Used by the *.browser.test.ts
// files; run them with `npm run test:browser`.
//
// Lifecycle: call startServer() in `before` and stopServer() in `after` (one
// waved + one browser shared across a file's tests; each test uses its own
// wavelet local id so server state doesn't leak). Then `client(user, wave)` opens
// a connected page, and the type/read/wait/click helpers drive it.
//
// Heavy (builds the bundle, spawns waved, launches Chromium); needs
// `npx playwright install chromium`, and the host sandbox disabled for
// spawn/loopback/browser.

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

/** Build the bundle + waved, spawn the server, and launch Chromium. */
export async function startServer(): Promise<void> {
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
      "-ws", `127.0.0.1:${port}`,
      "-webroot", "web/dist",
      "-log-level", "warn",
    ],
    { cwd: REPO, stdio: "inherit" },
  );
  await waitListening(port, 10_000);
  // A normal headless launch (NOT the web-runner's --single-process/--no-zygote
  // Mach-port workaround, which is unstable for full page navigation): this test
  // runs with the host sandbox disabled, so Chromium's own sandbox works.
  browser = await chromium.launch({ args: ["--no-sandbox", "--disable-gpu"] });
}

/** Close the browser and stop waved. */
export async function stopServer(): Promise<void> {
  await browser?.close();
  proc?.kill("SIGTERM");
}

function pageURL(user: string, waveLocal: string): string {
  const wave = `example.com/${waveLocal}/~/conv+root`;
  // Go through the dev login endpoint: it trusts the address, sets the session
  // cookie, and redirects (303) to the app at ?wave=… . Identity then rides the
  // cookie on the WebSocket handshake — there is no ?user= on the app URL.
  const redirect = `/?wave=${encodeURIComponent(wave)}`;
  return `http://127.0.0.1:${port}/login?user=${encodeURIComponent(user)}&redirect=${encodeURIComponent(redirect)}`;
}

/** Open a connected editor page for `user` on `waveLocal` (e.g. "w+demo"); waits
 *  until at least one editable blip has rendered (logged in + connected + seeded). */
export async function client(user: string, waveLocal: string): Promise<Page> {
  if (browser === undefined) throw new Error("browser-harness: startServer() not called");
  const page = await browser.newPage();
  await page.goto(pageURL(user, waveLocal));
  await page.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });
  return page;
}

/** Open the app shell for `user` at the root (inbox, no wave selected); waits
 *  until the shell (the New-wave button) has rendered. */
export async function openApp(user: string): Promise<Page> {
  if (browser === undefined) throw new Error("browser-harness: startServer() not called");
  const page = await browser.newPage();
  const redirect = "/";
  await page.goto(
    `http://127.0.0.1:${port}/login?user=${encodeURIComponent(user)}&redirect=${encodeURIComponent(redirect)}`,
  );
  await page.locator(".wl-new").waitFor({ state: "attached", timeout: 10_000 });
  return page;
}

/** Focus the nth blip editor and type `text` via real key events (so the
 *  controlled editor's beforeinput path runs). */
export async function typeInto(page: Page, nth: number, text: string): Promise<void> {
  const blip = page.locator(".blip-doc").nth(nth);
  await blip.click();
  await blip.pressSequentially(text, { delay: 5 });
}

/** Click the nth blip's "Reply" button (starts a new reply thread on it). */
export async function clickReply(page: Page, nth = 0): Promise<void> {
  await page.locator(".reply-btn").nth(nth).click();
}

/** Click the nth thread's continue button ("+ New message" on the root thread,
 *  "+ Continue thread" on a reply thread). nth indexes all continue buttons in
 *  document order (root thread's is first). */
export async function clickContinue(page: Page, nth = 0): Promise<void> {
  await page.locator(".continue-btn").nth(nth).click();
}

/** The visible text of every blip editor on the page, in document order. */
export function blipTexts(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".blip-doc")).map((el) => (el.textContent ?? "").trim()),
  );
}

/** Poll until every `want` substring appears across the page's blips (a fresh
 *  client converging via history replay), or time out. Returns the final texts. */
export async function waitForBlipTexts(page: Page, want: string[], timeoutMs = 10_000): Promise<string[]> {
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

/** Wait until the nth blip editor exists (e.g. after a reply/continue created it). */
export function waitForBlip(page: Page, nth: number): Promise<void> {
  return page.locator(".blip-doc").nth(nth).waitFor({ state: "attached", timeout: 10_000 });
}
