// Archive browser e2e: drive the <wave-app> shell in real Chromium against a real
// waved (index on) to prove the archive stack — the per-item Archive button →
// POST /api/archive → the inbox filter, the Archived view toggle (GET
// /api/inbox?archived=1), and restore. Run with `npm run test:browser`.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { openApp, startServer, stopServer } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

function listTitles(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".wl-title")).map((e) => (e.textContent ?? "").trim()),
  );
}

// makeWave creates a new wave and titles it by typing into the root blip, waiting for
// it to appear in the inbox list.
async function makeWave(page: Page, title: string): Promise<void> {
  await page.locator(".wl-new").click();
  await page.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });
  const blip = page.locator(".blip-doc").first();
  await blip.click();
  await blip.pressSequentially(title, { delay: 5 });
  await page.waitForFunction(
    (t) => Array.from(document.querySelectorAll(".wl-title")).some((e) => (e.textContent ?? "").includes(t)),
    title,
    { timeout: 10_000 },
  );
}

// archiveRow clicks the Archive (or, in the archived view, Restore) button on the list
// row whose title contains `title`.
function archiveRow(page: Page, title: string): Promise<void> {
  return page.evaluate((t) => {
    const item = Array.from(document.querySelectorAll(".wl-item")).find((el) =>
      (el.textContent ?? "").includes(t),
    );
    const btn = item?.querySelector<HTMLButtonElement>(".wl-archive");
    if (btn == null) throw new Error(`no archive button for ${t}`);
    btn.click();
  }, title);
}

test("a wave can be archived out of the inbox and restored", async () => {
  const page = await openApp("alice@example.com");

  await makeWave(page, "Keep this");
  await makeWave(page, "Archive this");
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll(".wl-title")).filter((e) => (e.textContent ?? "").trim() !== "").length >= 2,
    undefined,
    { timeout: 10_000 },
  );

  // Archive "Archive this": it leaves the inbox; "Keep this" stays.
  await archiveRow(page, "Archive this");
  await page.waitForFunction(
    () => {
      const titles = Array.from(document.querySelectorAll(".wl-title")).map((e) => e.textContent ?? "");
      return titles.some((t) => t.includes("Keep this")) && !titles.some((t) => t.includes("Archive this"));
    },
    undefined,
    { timeout: 10_000 },
  );

  // Switch to the Archived view: only the archived wave is there.
  await page.locator(".wl-view-toggle").click();
  await page.waitForFunction(
    () => {
      const titles = Array.from(document.querySelectorAll(".wl-title")).map((e) => e.textContent ?? "");
      return titles.some((t) => t.includes("Archive this")) && !titles.some((t) => t.includes("Keep this"));
    },
    undefined,
    { timeout: 10_000 },
  );

  // Restore it from the archived view; it disappears from the archived list.
  await archiveRow(page, "Archive this");
  await page.waitForFunction(
    () => !Array.from(document.querySelectorAll(".wl-title")).some((e) => (e.textContent ?? "").includes("Archive this")),
    undefined,
    { timeout: 10_000 },
  );

  // Back to the inbox: both waves are present again.
  await page.locator(".wl-view-toggle").click();
  await page.waitForFunction(
    () => {
      const titles = Array.from(document.querySelectorAll(".wl-title")).map((e) => e.textContent ?? "");
      return titles.some((t) => t.includes("Keep this")) && titles.some((t) => t.includes("Archive this"));
    },
    undefined,
    { timeout: 10_000 },
  );
  assert.ok((await listTitles(page)).some((t) => t.includes("Archive this")), "restored wave is back in the inbox");
});
