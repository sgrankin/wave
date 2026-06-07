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

test("a wave is unread for a participant until they open it", async () => {
  const alice = await openApp("alice@example.com");

  // Alice creates a wave with content and invites bob.
  await alice.locator(".wl-new").click();
  await alice.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });
  const blip = alice.locator(".blip-doc").first();
  await blip.click();
  await blip.pressSequentially("Shared agenda", { delay: 5 });
  await alice.locator(".add-participant-input").fill("bob@example.com");
  await alice.locator(".add-participant-btn").click();
  // Confirm the invite applied locally (so it has been submitted to the server).
  await alice.waitForFunction(
    () => Array.from(document.querySelectorAll(".roster-chip")).some((e) => (e.textContent ?? "").includes("bob@example.com")),
    undefined,
    { timeout: 10_000 },
  );

  // Bob opens the app: the shared wave appears in his inbox, marked unread (the
  // list polls, so it shows up without a manual reload).
  const bob = await openApp("bob@example.com");
  await bob.waitForFunction(
    () =>
      Array.from(document.querySelectorAll(".wl-item")).some(
        (el) => (el.textContent ?? "").includes("Shared agenda") && el.classList.contains("unread"),
      ),
    undefined,
    { timeout: 15_000 },
  );

  // Bob opens it → it becomes read (the unread marker clears).
  await bob.locator(".wl-item").filter({ hasText: "Shared agenda" }).first().click();
  await bob.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });
  await bob.waitForFunction(
    () => {
      const it = Array.from(document.querySelectorAll(".wl-item")).find((el) =>
        (el.textContent ?? "").includes("Shared agenda"),
      );
      return it !== undefined && !it.classList.contains("unread");
    },
    undefined,
    { timeout: 15_000 },
  );
});

test("an inline reply anchors a marker in the parent text and keeps it editable", async () => {
  const page = await openApp("dave@example.com");
  await page.locator(".wl-new").click();
  await page.locator(".blip-doc").first().waitFor({ state: "attached", timeout: 10_000 });

  const blip = page.locator(".blip-doc").first();
  await blip.click();
  await blip.pressSequentially("Topic one", { delay: 5 });

  // Reply inline: an anchor marker appears in the parent text, and a distinctly
  // styled inline reply thread is added.
  await page.locator(".reply-inline-btn").first().click();
  await page.waitForFunction(
    () => {
      const parent = document.querySelector(".blip-doc");
      return (
        parent !== null &&
        parent.querySelectorAll(".reply-anchor").length === 1 &&
        document.querySelectorAll(".wave-thread.inline").length === 1 &&
        document.querySelectorAll(".blip-doc").length === 2
      );
    },
    undefined,
    { timeout: 10_000 },
  );

  // The parent blip stays editable and caret-correct despite the embedded anchor:
  // typing more text appends without corruption.
  await blip.click();
  await blip.pressSequentially(" more", { delay: 5 });
  await page.waitForFunction(
    () => {
      const para = document.querySelector(".blip-doc .para");
      return para !== null && (para.textContent ?? "").includes("Topic one more");
    },
    undefined,
    { timeout: 10_000 },
  );
  // The anchor marker survived the edit (still exactly one).
  const markers = await page.evaluate(
    () => document.querySelector(".blip-doc")?.querySelectorAll(".reply-anchor").length ?? -1,
  );
  assert.equal(markers, 1, "anchor marker intact after editing the parent");
});
