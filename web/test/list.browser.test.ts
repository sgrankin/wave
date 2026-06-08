// List-continuation browser e2e: pressing Enter inside a list item starts a NEW
// list item (instead of dropping to a plain line), and Enter on an empty item exits
// the list. Run with `npm run test:browser` (sandbox disabled).

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer, typeInto, waitForBlipTexts } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

// makeList turns the focused blip's current line into a list item via the editor's
// public applyCommand (the same entry point the toolbar uses). cmd "li" = bullet,
// "ol" = numbered.
function makeList(page: Page, cmd: "li" | "ol" = "li"): Promise<void> {
  return page.evaluate(
    (c) => (document.querySelector("blip-view") as HTMLElement & { applyCommand(c: string): void }).applyCommand(c),
    cmd,
  );
}

// listItems returns the text of every list-item paragraph (display:list-item).
function listItems(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".blip-doc .para"))
      .filter((p) => getComputedStyle(p).display === "list-item")
      .map((p) => (p.textContent ?? "").trim()),
  );
}

test("Enter inside a list item continues the list; empty item exits", async () => {
  const page = await client("alice@example.com", "w+list");
  await typeInto(page, 0, "first");
  await makeList(page);
  await page.waitForFunction(() => {
    return Array.from(document.querySelectorAll(".blip-doc .para")).some(
      (p) => getComputedStyle(p).display === "list-item",
    );
  });

  // Enter continues the list: a second list item.
  await page.keyboard.press("Enter");
  await page.keyboard.type("second");
  await page.waitForFunction(() => {
    const lis = Array.from(document.querySelectorAll(".blip-doc .para")).filter(
      (p) => getComputedStyle(p).display === "list-item",
    );
    return lis.length === 2;
  });
  assert.deepEqual(await listItems(page), ["first", "second"]);

  // Enter on the now-empty third item exits the list (back to one... two items).
  await page.keyboard.press("Enter"); // start a third (empty) item
  await page.waitForFunction(() => {
    return (
      Array.from(document.querySelectorAll(".blip-doc .para")).filter(
        (p) => getComputedStyle(p).display === "list-item",
      ).length === 3
    );
  });
  await page.keyboard.press("Enter"); // Enter on the empty item exits the list
  await page.waitForFunction(() => {
    return (
      Array.from(document.querySelectorAll(".blip-doc .para")).filter(
        (p) => getComputedStyle(p).display === "list-item",
      ).length === 2
    );
  });
  assert.deepEqual(await listItems(page), ["first", "second"]);

  await page.close();
});

// numberedItems returns the text of every NUMBERED list-item paragraph (decimal marker).
function numberedItems(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".blip-doc .para"))
      .filter((p) => getComputedStyle(p).display === "list-item" && getComputedStyle(p).listStyleType === "decimal")
      .map((p) => (p.textContent ?? "").trim()),
  );
}

test("Numbered list: items render decimal, Enter continues numbered, and it converges", async () => {
  const wave = "w+numlist";
  const alice = await client("alice@example.com", wave);
  try {
    await typeInto(alice, 0, "one");
    await makeList(alice, "ol");
    await alice.waitForFunction(
      () =>
        Array.from(document.querySelectorAll(".blip-doc .para")).some(
          (p) => getComputedStyle(p).display === "list-item" && getComputedStyle(p).listStyleType === "decimal",
        ),
      undefined,
      { timeout: 5000 },
    );

    // Enter continues the NUMBERED list (carries listyle=decimal), not a bullet/plain line.
    await alice.keyboard.press("Enter");
    await alice.keyboard.type("two");
    await alice.waitForFunction(
      () =>
        Array.from(document.querySelectorAll(".blip-doc .para")).filter(
          (p) => getComputedStyle(p).display === "list-item" && getComputedStyle(p).listStyleType === "decimal",
        ).length === 2,
      undefined,
      { timeout: 5000 },
    );
    assert.deepEqual(await numberedItems(alice), ["one", "two"]);

    // A fresh client converges via history replay AND sees them as numbered items.
    const bob = await client("bob@example.com", wave);
    try {
      await waitForBlipTexts(bob, ["one", "two"]);
      await bob.waitForFunction(
        () =>
          Array.from(document.querySelectorAll(".blip-doc .para")).filter(
            (p) => getComputedStyle(p).display === "list-item" && getComputedStyle(p).listStyleType === "decimal",
          ).length === 2,
        undefined,
        { timeout: 8000 },
      );
    } finally {
      await bob.close();
    }
  } finally {
    await alice.close();
  }
});
