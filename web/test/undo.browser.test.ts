// Undo/redo browser e2e: drive Cmd/Ctrl-Z in real Chromium against a real waved,
// proving the per-blip undo manager is wired through blip-view -> controller ->
// client -> cc and that an undo converges to a second client. (Ctrl-Z is used so the
// test is platform-independent; blip-view's keydown accepts metaKey OR ctrlKey.)
//
// Run with `npm run test:browser` (sandbox disabled — chromium needs --no-sandbox).

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer, typeInto, waitForBlipTexts } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

// rootText waits until the first blip editor's text equals want (exact), so a
// substring like "hell" of "hello" can't pass spuriously.
async function rootText(page: Page, want: string): Promise<void> {
  await page.waitForFunction(
    (w: string) => ((document.querySelector(".blip-doc")?.textContent ?? "").trim()) === w,
    want,
    { timeout: 8000 },
  );
}

test("Ctrl-Z undoes edits and Ctrl-Shift-Z redoes them", async () => {
  const page = await client("alice@example.com", "w+undo");
  // Each typed character is one undo unit (one edit() call).
  await typeInto(page, 0, "abc");
  await waitForBlipTexts(page, ["abc"]);

  await page.keyboard.press("Control+z"); // undo "c"
  await rootText(page, "ab");
  await page.keyboard.press("Control+z"); // undo "b"
  await rootText(page, "a");

  await page.keyboard.press("Control+Shift+z"); // redo "b"
  await rootText(page, "ab");

  await page.close();
});

test("an undo converges to a second client", async () => {
  const a = await client("alice@example.com", "w+undo2");
  await typeInto(a, 0, "hello");
  await waitForBlipTexts(a, ["hello"]);

  const b = await client("bob@example.com", "w+undo2");
  await rootText(b, "hello"); // bob replays history and converges

  // Alice undoes her last character; bob must converge on the undone text.
  await a.keyboard.press("Control+z");
  await rootText(a, "hell");
  await rootText(b, "hell");

  await a.close();
  await b.close();
});
