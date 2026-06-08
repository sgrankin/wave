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
// fixed ~419px box), but is capped at a readable measure on a wide window — the
// conversation column maxes at 820px, so .blip-doc lands ~800px (card minus padding)
// rather than spanning the whole 1400px pane.
test("the blip editor grows with the window width, up to the readable cap", async () => {
  const page = await client("alice@example.com", "w+layout-grow");
  try {
    await page.setViewportSize({ width: 1400, height: 900 });
    const wide = await blipWidth(page);
    // Below the 820px breakpoint the panes become master-detail (one at a time); a wave
    // is open so the conversation shows full-screen. Crossing the breakpoint re-derives
    // that on the resize event, so wait for the conversation pane to settle before measuring.
    await page.setViewportSize({ width: 420, height: 900 });
    await page.locator(".blip-doc").first().waitFor({ state: "visible", timeout: 5000 });
    const narrow = await blipWidth(page);

    assert.ok(wide > narrow + 200, `editor should grow with width: wide=${wide} narrow=${narrow}`);
    assert.ok(wide > 700 && wide <= 840, `wide editor should sit at the readable cap (~800px), got ${wide}px`);
    assert.equal(await hasHorizontalScroll(page), false, "no horizontal scroll at 1400px");
  } finally {
    await page.close();
  }
});

// PWA: the app links a manifest and registers a service worker (installable). The
// harness serves over http://127.0.0.1, a secure context, so the SW registers.
test("the app is an installable PWA (service worker + manifest)", async () => {
  const page = await client("alice@example.com", "w+pwa");
  try {
    // clients.claim() in the SW makes it control this page; wait for that.
    await page.waitForFunction(() => navigator.serviceWorker.controller !== null, undefined, {
      timeout: 10_000,
    });
    const manifest = await page.evaluate(
      () => document.querySelector("link[rel=manifest]")?.getAttribute("href") ?? null,
    );
    assert.equal(manifest, "/manifest.webmanifest", "a web manifest is linked");
  } finally {
    await page.close();
  }
});

// On a phone the inbox and conversation are a master-detail pair (one at a time):
// opening a wave shows the conversation full-screen with a "‹ Inbox" back button that
// returns to the inbox.
test("on a phone the inbox and conversation are a master-detail pair", async () => {
  const page = await client("alice@example.com", "w+layout-md");
  try {
    await page.setViewportSize({ width: 380, height: 740 });
    // A wave is open → conversation full-screen, inbox (app-left) hidden, back button shown.
    await page.locator(".blip-doc").first().waitFor({ state: "visible", timeout: 5000 });
    const leftHidden = await page.evaluate(() => {
      const left = document.querySelector("wave-app .app-left");
      return left !== null && getComputedStyle(left).display === "none";
    });
    assert.equal(leftHidden, true, "inbox is hidden while viewing a wave on a phone");
    await page.locator(".nav-toggle.collapsed").waitFor({ state: "visible", timeout: 5000 });

    // Tapping "‹ Inbox" reveals the inbox again (and hides the conversation pane).
    await page.locator(".nav-toggle.collapsed").click();
    await page.locator("wave-list .wl-new").waitFor({ state: "visible", timeout: 5000 });
    const leftShown = await page.evaluate(
      () => getComputedStyle(document.querySelector("wave-app .app-left")!).display !== "none",
    );
    assert.equal(leftShown, true, "the back button reveals the inbox");
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
    // Master-detail: the open wave's conversation shows full-screen once the resize
    // re-derives the collapse; wait for it before measuring.
    await page.locator(".blip-doc").first().waitFor({ state: "visible", timeout: 5000 });
    assert.ok(await blipWidth(page) > 250, "the editor stays usably wide when the panes stack");
    assert.equal(await hasHorizontalScroll(page), false, "no horizontal scroll at 380px");
  } finally {
    await page.close();
  }
});
