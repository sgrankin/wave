// Per-blip read state e2e (task #52): a remote edit marks a blip unread for a
// peer who has already read it, and the marker clears once they view it again
// (mark-on-view, after a short dwell). Drives two real editor pages in headless
// Chromium against a real waved, so it exercises the whole path: the per-blip
// last-modified tracking in clientcc, the /api/read fetch+POST, the unread CSS
// class on <wave-blip>, and the IntersectionObserver dwell.
//
// Run with `npm run test:browser` (sandbox disabled — chromium needs --no-sandbox).

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import {
  clickContinue,
  client,
  startServer,
  stopServer,
  typeInto,
  waitForBlip,
  waitForBlipTexts,
} from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

// rootUnread waits until the root blip's unread class matches `want`.
function rootUnread(page: Page, want: boolean): Promise<void> {
  return page.waitForFunction(
    (w: boolean) => {
      const b = document.querySelector(".wave-blip");
      return b !== null && b.classList.contains("unread") === w;
    },
    want,
    { timeout: 8000 },
  );
}

test("a remote edit marks a blip unread; viewing it clears the marker", async () => {
  const wave = "w+btest-unread";
  const alice = await client("alice@example.com", wave);
  try {
    alice.on("dialog", (d) => void d.accept());
    // Alice writes the root and invites bob (so bob may submit to the wave).
    await typeInto(alice, 0, "root message");
    await waitForBlipTexts(alice, ["root message"]);
    await alice.locator(".add-participant-input").fill("bob@example.com");
    await alice.locator(".add-participant-btn").click();
    await alice.waitForFunction(
      () =>
        Array.from(document.querySelectorAll(".roster-chip .wave-participant-name")).some((e) =>
          (e.textContent ?? "").includes("bob@example.com"),
        ),
      undefined,
      { timeout: 10_000 },
    );

    // Alice is looking at the root; the mark-on-view dwell clears its initial
    // (first-open) unread state — establishing that she has read it.
    await rootUnread(alice, false);

    const bob = await client("bob@example.com", wave);
    try {
      await waitForBlipTexts(bob, ["root message"]);
      // Bob edits the root blip — a remote edit from Alice's point of view.
      const bblip = bob.locator(".blip-doc").first();
      await bblip.click();
      await bblip.pressSequentially(" + bob", { delay: 10 });

      // Alice receives the edit (text converges) AND the root flips to unread for
      // her (its last-modified version now exceeds her read version).
      await waitForBlipTexts(alice, ["root message + bob"]);
      await rootUnread(alice, true);

      // The root is on Alice's screen; after the read dwell, mark-on-view clears
      // the marker again (she has now seen bob's edit).
      await rootUnread(alice, false);
    } finally {
      await bob.close();
    }
  } finally {
    await alice.close();
  }
});

// The floating "jump to next unread" pill appears when an OFF-SCREEN blip is unread
// (so mark-on-view can't auto-clear it), and clicking it scrolls that blip into view
// — where it then clears. Uses a short viewport so a few messages push the last one
// below the fold without a long conversation.
test("the unread-nav pill jumps to an off-screen unread blip and clears it", async () => {
  const wave = "w+btest-unread-nav";
  const alice = await client("alice@example.com", wave);
  try {
    alice.on("dialog", (d) => void d.accept());
    await alice.setViewportSize({ width: 800, height: 300 });
    // Three of Alice's own messages (her own edits never mark a blip unread for her),
    // the last pushed below a short viewport.
    await typeInto(alice, 0, "first message");
    await clickContinue(alice);
    await waitForBlip(alice, 1);
    await typeInto(alice, 1, "second message");
    await clickContinue(alice);
    await waitForBlip(alice, 2);
    await typeInto(alice, 2, "third message down here");

    await alice.locator(".add-participant-input").fill("bob@example.com");
    await alice.locator(".add-participant-btn").click();
    await alice.waitForFunction(
      () =>
        Array.from(document.querySelectorAll(".roster-chip .wave-participant-name")).some((e) =>
          (e.textContent ?? "").includes("bob@example.com"),
        ),
      undefined,
      { timeout: 10_000 },
    );
    // Scroll to the top so the third blip is off-screen, and let any first-open
    // unread on the visible blips clear. No pill should be showing yet.
    await alice.evaluate(() => window.scrollTo(0, 0));
    await alice.locator(".unread-nav").waitFor({ state: "detached", timeout: 8000 });

    const bob = await client("bob@example.com", wave);
    try {
      await waitForBlipTexts(bob, ["third message down here"]);
      // Bob edits the LAST blip — off-screen for Alice, so her mark-on-view can't
      // clear it; the unread state (and the nav pill) persists until she navigates.
      const last = bob.locator(".blip-doc").nth(2);
      await last.click();
      await last.pressSequentially(" + bob", { delay: 10 });
      await waitForBlipTexts(alice, ["third message down here + bob"]);

      // The pill appears (one unread, off-screen).
      const pill = alice.locator(".unread-nav");
      await pill.waitFor({ state: "visible", timeout: 8000 });
      assert.match((await pill.textContent()) ?? "", /1 unread/, "pill shows the unread count");

      // Clicking it scrolls the unread blip into view; once viewed, mark-on-view
      // clears it and the pill goes away.
      await pill.click();
      await pill.waitFor({ state: "detached", timeout: 8000 });
    } finally {
      await bob.close();
    }
  } finally {
    await alice.close();
  }
});
