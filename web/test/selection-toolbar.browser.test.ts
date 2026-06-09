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

// Indent/Outdent (the toolbar buttons and Tab / Shift-Tab) change the caret paragraph's
// indent level, which renders as a left margin on the .para div (model <line i="N"> →
// margin-left:N*1.5em). Both the toolbar and the keyboard path are exercised.
test("Indent/Outdent (buttons + Tab) change the paragraph indent", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-indent");
  try {
    await typeInto(page, 0, "indent me");

    const marginLeft = () =>
      page.evaluate(() => {
        const para = document.querySelector<HTMLElement>(".blip-doc .para");
        return para ? para.style.marginLeft : "";
      });
    const indented = (ml: string) => ml !== "" && ml !== "0px";

    // The toolbar shows for the caret; Increase indent gives the paragraph a left margin.
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await page.locator('.sel-toolbar button[data-cmd="indent"]').click();
    await page.waitForFunction(
      () => {
        const p = document.querySelector<HTMLElement>(".blip-doc .para");
        return p !== null && p.style.marginLeft !== "" && p.style.marginLeft !== "0px";
      },
      undefined,
      { timeout: 5000 },
    );
    assert.ok(indented(await marginLeft()), "Indent button added a left margin");

    // Outdent removes it.
    await page.locator('.sel-toolbar button[data-cmd="outdent"]').click();
    await page.waitForFunction(
      () => {
        const p = document.querySelector<HTMLElement>(".blip-doc .para");
        return p !== null && (p.style.marginLeft === "" || p.style.marginLeft === "0px");
      },
      undefined,
      { timeout: 5000 },
    );
    assert.ok(!indented(await marginLeft()), "Outdent button removed the left margin");

    // Tab indents via the keyboard; Shift-Tab outdents. Place the caret in the editor first.
    await page.locator(".blip-doc .para").first().click();
    await page.keyboard.press("Tab");
    await page.waitForFunction(
      () => {
        const p = document.querySelector<HTMLElement>(".blip-doc .para");
        return p !== null && p.style.marginLeft !== "" && p.style.marginLeft !== "0px";
      },
      undefined,
      { timeout: 5000 },
    );
    assert.ok(indented(await marginLeft()), "Tab indented the paragraph");

    await page.keyboard.press("Shift+Tab");
    await page.waitForFunction(
      () => {
        const p = document.querySelector<HTMLElement>(".blip-doc .para");
        return p !== null && (p.style.marginLeft === "" || p.style.marginLeft === "0px");
      },
      undefined,
      { timeout: 5000 },
    );
    assert.ok(!indented(await marginLeft()), "Shift-Tab outdented the paragraph");
  } finally {
    await page.close();
  }
});

// Clear formatting: bold a selection, then the Clear button strips it (one op removes
// every character annotation over the range).
test("Clear formatting removes character styling from the selection", async () => {
  const page = await client("alice@example.com", "w+seltoolbar-clear");
  try {
    await typeInto(page, 0, "format me");
    await selectFirstPara(page, 0, 6); // select "format"
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });

    // Bold it first.
    await page.locator('.sel-toolbar button[data-cmd="bold"]').click();
    await page.waitForFunction(
      () => document.querySelector('.blip-doc .para span[style*="font-weight"]') !== null,
      undefined,
      { timeout: 5000 },
    );

    // Re-select and Clear formatting → the styled span is gone.
    await selectFirstPara(page, 0, 6);
    await page.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await page.locator('.sel-toolbar button[data-cmd="clear"]').click();
    await page.waitForFunction(
      () => document.querySelector('.blip-doc .para span[style*="font-weight"]') === null,
      undefined,
      { timeout: 5000 },
    );
    // The text itself is intact.
    const text = await page.evaluate(() => document.querySelector(".blip-doc .para")?.textContent ?? "");
    assert.ok(text.startsWith("format me"), `text intact after clear (got ${JSON.stringify(text)})`);
  } finally {
    await page.close();
  }
});

// Clear formatting must produce a SERVER-VALID op. A single-client DOM check is
// insufficient: the editor applies edits optimistically, so a clear op the server later
// REJECTS still updates the local DOM (the assertion passes) while the session silently
// dies. This test proves server acceptance the only honest way — a second client on the
// same wave must SEE the formatting removed, which only happens if the server accepted the
// clear op and fanned it out. (Regression guard for the dangling-annotation bug:
// setAnnotationRange left a clear's change unclosed, which the structural validator NACKs.)
test("Clear formatting persists on the server (a second client sees it removed)", async () => {
  const a = await client("alice@example.com", "w+seltoolbar-clearsync");
  const b = await client("alice@example.com", "w+seltoolbar-clearsync");
  try {
    const boldGone = () => document.querySelector('.blip-doc .para span[style*="font-weight"]') === null;
    const boldThere = () => document.querySelector('.blip-doc .para span[style*="font-weight"]') !== null;

    // A types and bolds "format"; both clients see the bold (server fanned it out).
    await typeInto(a, 0, "format me");
    await selectFirstPara(a, 0, 6);
    await a.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await a.locator('.sel-toolbar button[data-cmd="bold"]').click();
    await a.waitForFunction(boldThere, undefined, { timeout: 5000 });
    await b.waitForFunction(boldThere, undefined, { timeout: 5000 });

    // A clears the formatting.
    await selectFirstPara(a, 0, 6);
    await a.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await a.locator('.sel-toolbar button[data-cmd="clear"]').click();

    // The decisive check: B (a separate session) sees the bold GONE — only possible if
    // the server ACCEPTED the clear op. A rejected clear would be NACK'd before reaching B
    // (A's optimistic apply would mask it locally, but B would stay bold and time out here).
    await b.waitForFunction(boldGone, undefined, { timeout: 5000 });
    const text = await b.evaluate(() => document.querySelector(".blip-doc .para")?.textContent ?? "");
    assert.ok(text.startsWith("format me"), `B's text intact after clear (got ${JSON.stringify(text)})`);
  } finally {
    await a.close();
    await b.close();
  }
});

// Clear formatting across a PARAGRAPH boundary is the case that crosses a null annotation
// gap (the <line> marker between paragraphs, where character annotations reset). The
// op-builder must end + re-open the override across that gap; getting it wrong emits an op
// the server rejects (the interior-skip bug). Bold each paragraph SEPARATELY (so the line
// markers stay null), then clear across both, and confirm a second client sees both
// cleared — proving the cross-paragraph clear op is server-valid end to end.
test("Clear formatting across a paragraph boundary persists on the server", async () => {
  const a = await client("alice@example.com", "w+seltoolbar-clearmulti");
  const b = await client("alice@example.com", "w+seltoolbar-clearmulti");
  try {
    // Two paragraphs in blip 0: "alpha" ⏎ "beta".
    await typeInto(a, 0, "alpha");
    await a.keyboard.press("Enter");
    await a.keyboard.type("beta");
    await a.waitForFunction(() => document.querySelectorAll(".blip-doc .para").length >= 2, undefined, { timeout: 5000 });

    // selectParaFull selects all of paragraph `i`'s content (re-resolved each call, since
    // bolding wraps text in spans).
    const selectParaFull = (page: Page, i: number) =>
      page.evaluate((i) => {
        const p = document.querySelectorAll(".blip-doc .para")[i];
        if (!p) throw new Error("no para " + i);
        const r = document.createRange();
        r.selectNodeContents(p);
        const s = window.getSelection()!;
        s.removeAllRanges();
        s.addRange(r);
      }, i);
    // selectAcross selects from the first paragraph's first text node to the last
    // paragraph's last text node — a range that spans the inter-paragraph null gap.
    const selectAcross = (page: Page) =>
      page.evaluate(() => {
        const paras = document.querySelectorAll(".blip-doc .para");
        const p0 = paras[0]!;
        const p1 = paras[paras.length - 1]!;
        const first = document.createTreeWalker(p0, NodeFilter.SHOW_TEXT).nextNode();
        const w = document.createTreeWalker(p1, NodeFilter.SHOW_TEXT);
        let last: Node | null = null;
        for (let n = w.nextNode(); n !== null; n = w.nextNode()) last = n;
        if (first === null || last === null) throw new Error("no text nodes");
        const r = document.createRange();
        r.setStart(first, 0);
        r.setEnd(last, (last as Text).length);
        const s = window.getSelection()!;
        s.removeAllRanges();
        s.addRange(r);
      });

    // Bold each paragraph separately → the line markers between them stay null.
    await selectParaFull(a, 0);
    await a.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await a.locator('.sel-toolbar button[data-cmd="bold"]').click();
    await selectParaFull(a, 1);
    await a.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await a.locator('.sel-toolbar button[data-cmd="bold"]').click();
    await a.waitForFunction(() => document.querySelectorAll('.blip-doc .para span[style*="font-weight"]').length >= 2, undefined, { timeout: 5000 });
    await b.waitForFunction(() => document.querySelectorAll('.blip-doc .para span[style*="font-weight"]').length >= 2, undefined, { timeout: 5000 });

    // Select across BOTH paragraphs and Clear formatting.
    await selectAcross(a);
    await a.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await a.locator('.sel-toolbar button[data-cmd="clear"]').click();

    // B (separate session) sees BOTH paragraphs cleared — only if the server accepted the
    // cross-paragraph clear op (the interior-skip bug would NACK it; A's optimistic apply
    // would hide that, but B would stay bold and time out here).
    await b.waitForFunction(() => document.querySelectorAll('.blip-doc .para span[style*="font-weight"]').length === 0, undefined, { timeout: 5000 });
  } finally {
    await a.close();
    await b.close();
  }
});
