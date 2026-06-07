// End-to-end caret-correctness tests: drive the real controlled contenteditable in
// headless Chromium with REAL keyboard + mouse events, against a real Go `waved`.
// These are the faithful adversarial guards for the editor's DOM↔offset mapping —
// the invariant the whole editor rests on — complementing the deterministic,
// synthetic-selection component tests in src/editor/blip-caret.test.ts.
//
// They lock in the demo-found defects:
//   B1 — the first line of text sat partway down the box (leading whitespace gap).
//   B3 — typing on a later line landed at the start of line 1.
// plus the multi-line gestures the editor must get right: click-to-caret on each
// line, Enter mid-word, and Backspace line-merge.
//
// Heavy (builds the bundle, spawns waved, launches Chromium); run with
// `npm run test:browser`. Plumbing in ./browser-harness.ts.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer, waitForBlipTexts } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

/** Text of each paragraph in the first blip, in document order. */
function paraTexts(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".blip-doc")[0]?.querySelectorAll(".para") ?? []).map(
      (p) => p.textContent ?? "",
    ),
  );
}

// B3 (the reported gesture): type a line, Enter, click into the empty second line,
// type — the text must land on line 2, not at the start of line 1.
test("typing on a clicked empty later line lands there", async () => {
  const page = await client("alice@example.com", "w+caret-b3");
  try {
    const blip = page.locator(".blip-doc").first();
    await blip.click();
    await page.keyboard.type("line one");
    await page.keyboard.press("Enter");
    await page.locator(".blip-doc .para").nth(1).click(); // click the empty line 2
    await page.keyboard.type("line two");

    assert.deepEqual(await paraTexts(page), ["line one", "line two"]);
  } finally {
    await page.close();
  }
});

// B1: the first line must sit at the top of the editor (no leading whitespace gap).
test("the first line sits at the top of the editor (no leading gap)", async () => {
  const page = await client("alice@example.com", "w+caret-gap");
  try {
    const blip = page.locator(".blip-doc").first();
    await blip.click();
    await page.keyboard.type("hi");
    const gap = await page.evaluate(() => {
      const d = document.querySelector(".blip-doc")!;
      const p = d.querySelector(".para")!;
      return p.getBoundingClientRect().top - d.getBoundingClientRect().top;
    });
    assert.ok(gap < 6, `first line should be at the top, but the leading gap is ${Math.round(gap)}px`);
  } finally {
    await page.close();
  }
});

// Clicking into any of several lines and typing must edit THAT line and no other —
// the multi-line generalisation of B3 (the old root-anchored mapping mis-routed
// clicks once there were 3+ lines).
test("clicking each of three lines types on that line only", async () => {
  const page = await client("alice@example.com", "w+caret-lines");
  try {
    const blip = page.locator(".blip-doc").first();
    await blip.click();
    await page.keyboard.type("alpha");
    await page.keyboard.press("Enter");
    await page.keyboard.type("bravo");
    await page.keyboard.press("Enter");
    await page.keyboard.type("charlie");
    assert.deepEqual(await paraTexts(page), ["alpha", "bravo", "charlie"], "three lines typed");

    // Click each line and insert a digit at its start; only that line changes.
    const want = ["alpha", "bravo", "charlie"];
    const sentinels = ["1", "2", "3"];
    for (let i = 0; i < 3; i++) {
      await page.locator(".blip-doc .para").nth(i).click();
      await page.keyboard.press("Home"); // normalise to the visual start of the clicked line
      await page.keyboard.type(sentinels[i]!);
      want[i] = sentinels[i]! + want[i];
      assert.deepEqual(await paraTexts(page), want, `after editing line ${i}`);
    }
  } finally {
    await page.close();
  }
});

// Enter mid-word splits the line at the caret, not at the line start/end.
test("Enter mid-word splits the line at the caret", async () => {
  const page = await client("alice@example.com", "w+caret-split");
  try {
    const blip = page.locator(".blip-doc").first();
    await blip.click();
    await page.keyboard.type("helloworld");
    for (let i = 0; i < 5; i++) await page.keyboard.press("ArrowLeft"); // caret between hello|world
    await page.keyboard.press("Enter");
    assert.deepEqual(await paraTexts(page), ["hello", "world"]);
  } finally {
    await page.close();
  }
});

// Live remote carets: a peer's caret renders as a colored, labeled bar in the other
// client's editor (the flagship presence feature — roadmap 08 §1a).
test("a peer's caret renders as a labeled colored bar in the other client", async () => {
  const wave = "w+caret-remote";
  const alice = await client("alice@example.com", wave);
  try {
    const bob = await client("bob@example.com", wave);
    try {
      // alice types in the root blip and bob converges to the text.
      const ablip = alice.locator(".blip-doc").first();
      await ablip.click();
      await alice.keyboard.type("hello world");
      await waitForBlipTexts(bob, ["hello world"]);

      // alice's caret (published on the presence channel) renders in bob's editor as a
      // .remote-caret bar whose flag names alice.
      await bob.waitForFunction(
        () => {
          const flag = document.querySelector(".remote-caret .remote-caret-flag");
          return flag !== null && (flag.textContent ?? "").includes("alice");
        },
        undefined,
        { timeout: 10_000 },
      );
      const info = await bob.evaluate(() => {
        const bar = document.querySelector(".remote-caret");
        const r = bar?.getBoundingClientRect();
        const blip = document.querySelector(".blip-doc")?.getBoundingClientRect();
        return {
          count: document.querySelectorAll(".remote-caret").length,
          left: r ? Math.round(r.left) : -1,
          insideBlip: !!(r && blip && r.left >= blip.left - 2 && r.left <= blip.right + 2),
        };
      });
      assert.equal(info.count, 1, "exactly one remote caret (alice's) is shown");
      assert.ok(info.insideBlip, `the caret bar sits within the blip (left=${info.left})`);
    } finally {
      await bob.close();
    }
  } finally {
    await alice.close();
  }
});

// Backspace at the start of a line merges it into the previous line.
test("Backspace at the start of line 2 merges into line 1", async () => {
  const page = await client("alice@example.com", "w+caret-merge");
  try {
    const blip = page.locator(".blip-doc").first();
    await blip.click();
    await page.keyboard.type("foo");
    await page.keyboard.press("Enter");
    await page.keyboard.type("bar");
    await page.keyboard.press("Home"); // caret at the start of "bar"
    await page.keyboard.press("Backspace"); // delete the line marker → merge
    assert.deepEqual(await paraTexts(page), ["foobar"]);
  } finally {
    await page.close();
  }
});
