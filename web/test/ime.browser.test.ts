// IME / composition input e2e: drive a real composition (CJK) through the
// controlled contenteditable via CDP (Input.imeSetComposition + Input.insertText),
// and prove the composed text is COMMITTED to the model and SUBMITTED (a fresh
// client converges on it) — not left as out-of-band DOM the next render drops.
//
// A controlled editor preventDefaults every beforeinput, but browsers ignore
// preventDefault on composition, so without explicit handling the IME mutates the
// DOM natively and the composed text is never turned into an op. This guards the
// fix. Run with `npm run test:browser` (sandbox disabled).
//
// Coverage gaps (guarded in code, but not e2e-covered here — both are awkward to
// drive deterministically via CDP, so they are called out rather than left silent):
//   1. Compose over a selection spanning an inline widget (reply/image): replaceText
//      throws, so onCompositionEnd forces a .blip-doc rebuild (renderKey++) to scrub
//      the browser's native nodes instead of silently swallowing — same reconcile as
//      the cancel path below, which IS covered. Needs a widget + a cross-widget DOM
//      selection, which the harness can't set up cleanly.
//   2. A remote delta landing mid-composition: onCompositionStart snapshots the
//      content instance and onCompositionEnd aborts (drops + reconciles) if it changed,
//      rather than committing stale offsets. Needs a second client's delta to land in
//      the exact compositionstart→end window — not deterministically schedulable.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

// composeCJK drives a marked composition then commits it, the way an IME does.
async function composeCJK(page: Page, text: string): Promise<void> {
  const cdp = await page.context().newCDPSession(page);
  // Show the marked (composing) text with the caret at its end…
  await cdp.send("Input.imeSetComposition", {
    text,
    selectionStart: text.length,
    selectionEnd: text.length,
  });
  // …then commit it (fires compositionend + the final insertCompositionText).
  await cdp.send("Input.insertText", { text });
  await cdp.detach();
}

function blipHasText(page: Page, want: string): Promise<unknown> {
  return page.waitForFunction(
    (w: string) => (document.querySelector(".blip-doc")?.textContent ?? "").includes(w),
    want,
    { timeout: 8000 },
  );
}

test("IME composition commits CJK text into the blip and converges", async () => {
  const alice = await client("alice@example.com", "w+ime");
  try {
    const blip = alice.locator(".blip-doc").first();
    await blip.click();
    await composeCJK(alice, "にほんご");

    // The composed text is in the model (the controlled editor emitted the edit).
    await blipHasText(alice, "にほんご");

    // And it was SUBMITTED — a fresh client converges via history replay (this is
    // the assertion that fails when composition is only native DOM, never an op).
    const bob = await client("bob@example.com", "w+ime");
    try {
      await blipHasText(bob, "にほんご");
    } finally {
      await bob.close();
    }
  } finally {
    await alice.close();
  }
});

// A CANCELLED composition must leave the model unchanged AND scrub the marked text
// the browser inserted natively — otherwise the DOM keeps nodes the model doesn't
// have (a divergence that silently mis-maps the next edit). Guards the compositionend
// reconcile (the .blip-doc rebuild) on the empty/cancel path.
test("a cancelled composition leaves no stray text in the DOM", async () => {
  const alice = await client("alice@example.com", "w+ime-cancel");
  try {
    const blip = alice.locator(".blip-doc").first();
    await blip.click();
    await blip.pressSequentially("hello", { delay: 5 });
    await blipHasText(alice, "hello");
    // Caret at the end of "hello".
    await alice.evaluate(() => {
      const para = document.querySelector(".blip-doc .para");
      if (para === null) throw new Error("no .para");
      const tn = document.createTreeWalker(para, NodeFilter.SHOW_TEXT).nextNode() as Text | null;
      if (tn === null) throw new Error("no text node");
      const r = document.createRange();
      r.setStart(tn, tn.length);
      r.collapse(true);
      const s = window.getSelection()!;
      s.removeAllRanges();
      s.addRange(r);
    });
    const cdp = await alice.context().newCDPSession(alice);
    // Compose "abc" (marked) — inserted natively by the browser — then cancel it by
    // setting the composition to empty (fires compositionend with empty data).
    await cdp.send("Input.imeSetComposition", { text: "abc", selectionStart: 3, selectionEnd: 3 });
    await cdp.send("Input.imeSetComposition", { text: "", selectionStart: 0, selectionEnd: 0 });
    await cdp.detach();
    // The model never changed, and the native "abc" is gone — exactly "hello".
    await alice.waitForFunction(
      () => ((document.querySelector(".blip-doc")?.textContent ?? "").trim()) === "hello",
      undefined,
      { timeout: 8000 },
    );
  } finally {
    await alice.close();
  }
});

test("composing into a selection replaces it", async () => {
  const alice = await client("alice@example.com", "w+ime-replace");
  try {
    const blip = alice.locator(".blip-doc").first();
    await blip.click();
    await blip.pressSequentially("abc", { delay: 5 });
    await blipHasText(alice, "abc");
    // Select all three characters, then compose over them. The text lives in a text
    // node nested in a styled span, so walk to the first text node (selecting the
    // .para element by child-index would collapse the range).
    await alice.evaluate(() => {
      const para = document.querySelector(".blip-doc .para");
      if (para === null) throw new Error("no .para");
      const tn = document.createTreeWalker(para, NodeFilter.SHOW_TEXT).nextNode();
      if (tn === null) throw new Error("no text node");
      const r = document.createRange();
      r.setStart(tn, 0);
      r.setEnd(tn, 3);
      const s = window.getSelection()!;
      s.removeAllRanges();
      s.addRange(r);
    });
    // Sanity: the selection really spans "abc" before we compose over it.
    assert.equal(
      await alice.evaluate(() => window.getSelection()?.toString() ?? ""),
      "abc",
      "the three characters are selected before composing",
    );
    await composeCJK(alice, "かな");
    await alice.waitForFunction(
      () => ((document.querySelector(".blip-doc")?.textContent ?? "").trim()) === "かな",
      undefined,
      { timeout: 8000 },
    );
  } finally {
    await alice.close();
  }
});
