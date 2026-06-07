// App-shell browser e2e: drive the <wave-app> two-pane shell in real Chromium
// against a real waved (index on) to prove the whole wave-management stack —
// new-wave → server seeding → read index → /api/inbox → list render, and
// /api/search filtering. Complements the conversation-level convergence tests.
//
// Run with `npm run test:browser` (sandbox disabled — chromium needs --no-sandbox).

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

test("new wave is created, listed in the inbox, and searchable", async () => {
  const page = await openApp("alice@example.com");

  // Inbox starts empty for a fresh user.
  assert.deepEqual(await listTitles(page), []);

  // Create a new wave: the conversation mounts and the server seeds it.
  await page.locator(".wl-new").click();
  await page.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });

  // Give it a title by typing into the root blip (real key events → controlled editor).
  const blip = page.locator(".blip-doc").first();
  await blip.click();
  await blip.pressSequentially("Project kickoff", { delay: 5 });

  // It shows up in the inbox list (the conversation change triggers a refresh).
  await page.waitForFunction(
    () =>
      Array.from(document.querySelectorAll(".wl-title")).some((e) =>
        (e.textContent ?? "").includes("Project kickoff"),
      ),
    undefined,
    { timeout: 10_000 },
  );

  // Search matches it.
  await page.locator(".wl-search").fill("kickoff");
  await page.waitForFunction(
    () => {
      const titles = Array.from(document.querySelectorAll(".wl-title")).map((e) => e.textContent ?? "");
      return titles.length >= 1 && titles.some((t) => t.includes("Project kickoff"));
    },
    undefined,
    { timeout: 10_000 },
  );

  // A non-matching search excludes it.
  await page.locator(".wl-search").fill("zzz-no-such-text");
  await page.waitForFunction(
    () =>
      !Array.from(document.querySelectorAll(".wl-title")).some((e) =>
        (e.textContent ?? "").includes("Project kickoff"),
      ),
    undefined,
    { timeout: 10_000 },
  );

  // Clearing the search restores the inbox.
  await page.locator(".wl-search").fill("");
  await page.waitForFunction(
    () =>
      Array.from(document.querySelectorAll(".wl-title")).some((e) =>
        (e.textContent ?? "").includes("Project kickoff"),
      ),
    undefined,
    { timeout: 10_000 },
  );
});

test("navigating between two waves switches the conversation", async () => {
  const page = await openApp("carol@example.com");

  // Create wave A.
  await page.locator(".wl-new").click();
  await page.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });
  let blip = page.locator(".blip-doc").first();
  await blip.click();
  await blip.pressSequentially("Wave Alpha", { delay: 5 });
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll(".wl-title")).some((e) => (e.textContent ?? "").includes("Wave Alpha")),
    undefined,
    { timeout: 10_000 },
  );

  // Create wave B.
  await page.locator(".wl-new").click();
  await page.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });
  // The new wave's blip is empty; type a distinct title.
  blip = page.locator(".blip-doc").first();
  await blip.click();
  await blip.pressSequentially("Wave Beta", { delay: 5 });
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll(".wl-title")).some((e) => (e.textContent ?? "").includes("Wave Beta")),
    undefined,
    { timeout: 10_000 },
  );

  // Click wave Alpha in the list → the conversation switches to it.
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll(".wl-title")).filter((e) => (e.textContent ?? "").trim() !== "").length >= 2,
    undefined,
    { timeout: 10_000 },
  );
  await page
    .locator(".wl-item")
    .filter({ hasText: "Wave Alpha" })
    .first()
    .click();
  await page.waitForFunction(
    () => {
      const texts = Array.from(document.querySelectorAll(".blip-doc")).map((e) => e.textContent ?? "");
      return texts.some((t) => t.includes("Wave Alpha")) && !texts.some((t) => t.includes("Wave Beta"));
    },
    undefined,
    { timeout: 10_000 },
  );
});
