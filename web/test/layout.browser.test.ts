// End-to-end responsive-layout tests: the editor must use the available width and
// stay usable at narrow widths. Regression guards for the demo-found defect B2 (the
// blip box stayed a fixed ~419px and did not grow with the window — caused by a
// body max-width cap plus inline-by-default custom elements) and the narrow-width
// two-pane squeeze.
//
// Heavy (builds the bundle, spawns waved, launches Chromium); run with
// `npm run test:browser`. Plumbing in ./browser-harness.ts.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

function blipWidth(page: Page): Promise<number> {
  return page.evaluate(() => Math.round(document.querySelector(".blip-doc")!.getBoundingClientRect().width));
}

function hasHorizontalScroll(page: Page): Promise<boolean> {
  return page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
}

// B2: the blip editor grows with the window width (it no longer shrink-wraps to a
// fixed narrow box), and a wide window is actually used (well past the old ~419px).
test("the blip editor grows with the window width", async () => {
  const page = await client("alice@example.com", "w+layout-grow");
  try {
    await page.setViewportSize({ width: 1400, height: 900 });
    const wide = await blipWidth(page);
    await page.setViewportSize({ width: 760, height: 900 });
    const narrow = await blipWidth(page);

    assert.ok(wide > narrow + 200, `editor should grow with width: wide=${wide} narrow=${narrow}`);
    assert.ok(wide > 800, `at a 1400px window the editor should be wide, got ${wide}px`);
    assert.equal(await hasHorizontalScroll(page), false, "no horizontal scroll at 1400px");
  } finally {
    await page.close();
  }
});

// Narrow widths must stay usable: the panes stack and the conversation keeps a
// workable width instead of being squeezed to a sliver by the fixed list pane.
test("a narrow viewport keeps the conversation usable (no horizontal scroll)", async () => {
  const page = await client("alice@example.com", "w+layout-narrow");
  try {
    await page.setViewportSize({ width: 380, height: 740 });
    assert.ok(await blipWidth(page) > 250, "the editor stays usably wide when the panes stack");
    assert.equal(await hasHorizontalScroll(page), false, "no horizontal scroll at 380px");
  } finally {
    await page.close();
  }
});
