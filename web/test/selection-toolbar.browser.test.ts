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
      // Walk to the first text node (it may be nested in a styled span — e.g. after a
      // color/bold is applied — where a direct-child lookup would miss it).
      const tn = document.createTreeWalker(para, NodeFilter.SHOW_TEXT).nextNode() ?? para.firstChild;
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

test("Highlight applies a background color to the selection", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-hl");
  try {
    await typeInto(page, 0, "highlight me");
    await selectFirstPara(page, 0, 9); // select "highlight"
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });

    await page.locator('.sel-toolbar button[data-cmd="highlight"]').click();

    await page.waitForFunction(
      () => document.querySelector('.blip-doc .para span[style*="background-color"]') !== null,
      undefined,
      { timeout: 5000 },
    );
    const highlighted = await page.evaluate(
      () => document.querySelector('.blip-doc .para span[style*="background-color"]')?.textContent ?? "",
    );
    assert.equal(highlighted, "highlight", "the selected text was highlighted");
  } finally {
    await page.close();
  }
});

test("a color swatch applies text color to the selection; Default clears it", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-color");
  try {
    await typeInto(page, 0, "color me");
    await selectFirstPara(page, 0, 5); // select "color"
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });

    await page.locator('.sel-toolbar button[data-cmd="color:#e11d48"]').click();

    // The model now carries a style/color annotation → the run renders with a color.
    await page.waitForFunction(
      () => Array.from(document.querySelectorAll(".blip-doc .para span")).some((s) => (s as HTMLElement).style.color !== ""),
      undefined,
      { timeout: 5000 },
    );
    const colored = await page.evaluate(() => {
      const el = Array.from(document.querySelectorAll<HTMLElement>(".blip-doc .para span")).find((s) => s.style.color !== "");
      return el ? { text: el.textContent ?? "", color: el.style.color } : null;
    });
    assert.ok(colored !== null, "a colored span exists");
    assert.equal(colored.text, "color", "the selected text got the color");
    assert.equal(colored.color, "rgb(225, 29, 72)", "the color is the chosen #e11d48");

    // The Default (clear) button removes the color.
    await selectFirstPara(page, 0, 5);
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await page.locator('.sel-toolbar button[data-cmd="color:"]').click();
    await page.waitForFunction(
      () => !Array.from(document.querySelectorAll(".blip-doc .para span")).some((s) => (s as HTMLElement).style.color !== ""),
      undefined,
      { timeout: 5000 },
    );
  } finally {
    await page.close();
  }
});

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

// Positioning guard (the thing the ad-hoc screenshot checked by eye): the floating
// bar must render fully on-screen and adjacent to the selection — not off-viewport,
// not parked at 0,0, not far from the text it acts on. (Fine-pointer/desktop layout;
// the coarse layout docks to the bottom edge instead and is exercised by eye on a
// real device.)
test("the toolbar floats on-screen, next to the selection", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-pos");
  try {
    await typeInto(page, 0, "position the bar near this selected text");
    await selectFirstPara(page, 13, 21); // select "the bar "
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    // The bar is already visible from the typing caret; wait until the RANGE selection
    // is processed (Bold enabled) so it has repositioned over the selection, not the
    // caret — otherwise the geometry read races the reposition.
    await page.waitForFunction(
      () => document.querySelector('.sel-toolbar button[data-cmd="bold"]')?.hasAttribute("disabled") === false,
      undefined,
      { timeout: 5000 },
    );

    const geom = await page.evaluate(() => {
      const bar = document.querySelector(".sel-toolbar")!.getBoundingClientRect();
      const sel = window.getSelection()!.getRangeAt(0).getBoundingClientRect();
      return {
        bar: { left: bar.left, top: bar.top, right: bar.right, bottom: bar.bottom, w: bar.width, h: bar.height },
        sel: { left: sel.left, top: sel.top, right: sel.right, bottom: sel.bottom },
        vw: window.innerWidth,
        vh: window.innerHeight,
      };
    });

    // Fully within the viewport.
    assert.ok(geom.bar.w > 0 && geom.bar.h > 0, "bar has a real size");
    assert.ok(geom.bar.left >= 0 && geom.bar.top >= 0, `bar not off the top/left: ${JSON.stringify(geom.bar)}`);
    assert.ok(geom.bar.right <= geom.vw + 1 && geom.bar.bottom <= geom.vh + 1, "bar within the viewport");
    // Not parked at the origin (the failure mode when positioning never runs).
    assert.ok(geom.bar.top > 8 || geom.bar.left > 8, "bar is positioned, not at 0,0");
    // Vertically adjacent to the selection (just above, or flipped just below).
    const selMidY = (geom.sel.top + geom.sel.bottom) / 2;
    const barMidY = (geom.bar.top + geom.bar.bottom) / 2;
    assert.ok(
      Math.abs(barMidY - selMidY) < 120,
      `bar should hug the selection vertically: barMidY=${barMidY} selMidY=${selMidY}`,
    );
    // Horizontally overlapping the selection's x-span (centered over it, clamped to edges).
    assert.ok(
      geom.bar.right >= geom.sel.left && geom.bar.left <= geom.sel.right,
      `bar should overlap the selection horizontally: ${JSON.stringify(geom)}`,
    );
  } finally {
    await page.close();
  }
});

test("the toolbar's Comment button anchors an inline reply and opens the sheet", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-comment");
  try {
    await typeInto(page, 0, "comment on this");
    await selectFirstPara(page, 0, 7); // select "comment"

    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await page.locator('.sel-toolbar button[data-cmd="comment"]').click();

    // A 💬 anchor appears in the parent text, and the new comment opens in the bottom
    // sheet (inline threads live in the sheet, not in flow) with its own editable blip.
    await page.locator(".blip-doc .reply-anchor").first().waitFor({ state: "attached", timeout: 5000 });
    await page.locator("comment-sheet .cs-panel").waitFor({ state: "visible", timeout: 5000 });
    await page.locator("comment-sheet .wave-thread.inline").first().waitFor({ state: "attached", timeout: 5000 });
    const blipCount = await page.locator(".blip-doc").count();
    assert.ok(blipCount >= 2, `inline reply added an editable blip in the sheet (got ${blipCount})`);

    // The 💬 anchor is NOT rendered as an in-flow thread (only the parent text shows it).
    const inflowInline = await page.locator(".wave-blip > .wave-thread.inline").count();
    assert.equal(inflowInline, 0, "inline threads are not rendered in the document flow");

    // Dismiss with Escape; the sheet closes.
    await page.keyboard.press("Escape");
    await page.locator("comment-sheet .cs-panel").waitFor({ state: "detached", timeout: 5000 });

    // Read path: tapping the 💬 anchor reopens that thread's sheet.
    await page.locator(".blip-doc .reply-anchor").first().click();
    await page.locator("comment-sheet .cs-panel").waitFor({ state: "visible", timeout: 5000 });
  } finally {
    await page.close();
  }
});

test("the bar shows for a caret (Bold disabled, H1/Comment usable) and hides on blur", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-caret");
  try {
    // Typing leaves a collapsed caret in the blip — the bar shows without a selection
    // (H1/Comment don't need one), and remains for caret-only line/comment commands.
    await typeInto(page, 0, "caret only here");
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });

    // Bold/Italic need a range, so they're disabled with just a caret; H1 and Comment
    // act on the caret's line and stay enabled.
    assert.equal(
      await page.locator('.sel-toolbar button[data-cmd="bold"]').isDisabled(),
      true,
      "Bold is disabled with no selection",
    );
    assert.equal(
      await page.locator('.sel-toolbar button[data-cmd="h1"]').isDisabled(),
      false,
      "H1 is usable with just a caret",
    );
    assert.equal(
      await page.locator('.sel-toolbar button[data-cmd="comment"]').isDisabled(),
      false,
      "Comment is usable with just a caret",
    );

    // Selecting a range enables Bold.
    await selectFirstPara(page, 0, 5);
    await page.waitForFunction(
      () => document.querySelector('.sel-toolbar button[data-cmd="bold"]')?.hasAttribute("disabled") === false,
      undefined,
      { timeout: 5000 },
    );

    // Moving focus out of the editor hides the bar.
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await page.locator(".sel-toolbar.visible").waitFor({ state: "hidden", timeout: 5000 });
  } finally {
    await page.close();
  }
});
