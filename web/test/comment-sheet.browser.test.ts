// End-to-end test for the comment sheet's TOUCH layout (the full-height sheet that is
// its whole reason to exist and only engages on a coarse pointer — so it runs in a
// touch context here, not the default mouse context).
//
// Heavy (builds the bundle, spawns waved, launches Chromium); run with
// `npm run test:browser`. Plumbing in ./browser-harness.ts.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { startServer, stopServer, touchClient, typeInto } from "./browser-harness.ts";

before(startServer);
after(stopServer);

test("on touch the comment sheet is a full-height sheet with a sticky Done footer", async () => {
  const page = await touchClient("alice@example.com", "w+sheet-coarse");
  try {
    await typeInto(page, 0, "comment target text on a phone");
    // Create an inline comment → the sheet opens (touch → full-height).
    await page.locator(".reply-inline-btn").first().click();
    await page.locator("comment-sheet .cs-panel").waitFor({ state: "visible", timeout: 10_000 });

    const geom = await page.evaluate(() => {
      const panel = document.querySelector("comment-sheet .cs-panel")!.getBoundingClientRect();
      const vh = window.visualViewport?.height ?? window.innerHeight;
      const backdrop = document.querySelector("comment-sheet .cs-backdrop");
      const body = document.querySelector("comment-sheet .cs-body");
      return {
        panelH: panel.height,
        vh,
        coarse: backdrop?.classList.contains("coarse") ?? false,
        overscroll: body ? getComputedStyle(body).overscrollBehaviorY : "",
      };
    });

    assert.equal(geom.coarse, true, "touch → coarse full-height sheet (not the desktop card)");
    assert.ok(geom.panelH > geom.vh * 0.85, `panel ~fills the viewport: ${geom.panelH} vs vh ${geom.vh}`);
    assert.equal(geom.overscroll, "contain", "the thread body contains its scroll (no leak to the page)");

    // The Done footer is present and dismisses the sheet.
    await page.locator("comment-sheet .cs-foot .cs-done").click();
    await page.locator("comment-sheet .cs-panel").waitFor({ state: "detached", timeout: 5000 });
  } finally {
    await page.context().close();
  }
});
