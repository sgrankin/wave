// End-to-end tests for the floating <selection-toolbar> (task #38): selecting text
// in a blip pops up a bar at the selection; its buttons format the selected text and
// create an inline comment anchored at the selection — all without losing the
// selection (the bar preventDefaults its pointerdown).
//
// Heavy (builds the bundle, spawns waved, launches Chromium); run with
// `npm run test:browser`. Plumbing in ./browser-harness.ts.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer, typeInto } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

// Select [start, end) runes of the first paragraph's first text node, firing the
// selectionchange the toolbar listens for.
function selectFirstPara(page: Page, start: number, end: number): Promise<void> {
  return page.evaluate(
    ({ start, end }) => {
      const para = document.querySelector(".blip-doc .para");
      if (para === null) throw new Error("no .para");
      const tn = Array.from(para.childNodes).find((n) => n.nodeType === Node.TEXT_NODE) ?? para.firstChild;
      if (tn === null) throw new Error("no text node");
      const r = document.createRange();
      r.setStart(tn, start);
      r.setEnd(tn, end);
      const s = window.getSelection()!;
      s.removeAllRanges();
      s.addRange(r);
    },
    { start, end },
  );
}

test("selecting text shows the toolbar; Bold formats the selection", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-bold");
  try {
    await typeInto(page, 0, "hello world");
    await selectFirstPara(page, 0, 5); // select "hello"

    // The floating bar appears for a non-empty selection inside the editor.
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });

    await page.locator('.sel-toolbar button[data-cmd="bold"]').click();

    // The model now carries a bold annotation → the paragraph renders a styled span.
    await page.waitForFunction(
      () => document.querySelector('.blip-doc .para span[style*="font-weight"]') !== null,
      undefined,
      { timeout: 5000 },
    );
    const bolded = await page.evaluate(
      () => document.querySelector('.blip-doc .para span[style*="font-weight"]')?.textContent ?? "",
    );
    assert.equal(bolded, "hello", "the selected text became bold");
  } finally {
    await page.close();
  }
});

test("the toolbar's Comment button anchors an inline reply at the selection", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-comment");
  try {
    await typeInto(page, 0, "comment on this");
    await selectFirstPara(page, 0, 7); // select "comment"

    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await page.locator('.sel-toolbar button[data-cmd="comment"]').click();

    // An inline reply thread is created: a 💬 anchor appears in the parent text and a
    // new (inline-styled) thread with its own editable blip is added.
    await page.locator(".blip-doc .reply-anchor").first().waitFor({ state: "attached", timeout: 5000 });
    await page.locator(".wave-thread.inline").first().waitFor({ state: "attached", timeout: 5000 });
    const blipCount = await page.locator(".blip-doc").count();
    assert.ok(blipCount >= 2, `inline reply added an editable blip (got ${blipCount})`);
  } finally {
    await page.close();
  }
});

test("collapsing the selection hides the toolbar", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-hide");
  try {
    await typeInto(page, 0, "select then collapse");
    await selectFirstPara(page, 0, 6);
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });

    // Collapse the selection (caret only) → the bar hides.
    await page.evaluate(() => {
      const s = window.getSelection()!;
      s.collapseToEnd();
    });
    await page.locator(".sel-toolbar.visible").waitFor({ state: "hidden", timeout: 5000 });
  } finally {
    await page.close();
  }
});
