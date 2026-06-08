// Manual-link e2e: select text, hit the floating toolbar's Link button, and prove the
// selection becomes an <a> in the model — submitted (a fresh client converges on it),
// not just a local DOM decoration. Drives the real selection-toolbar → blip-view →
// setLink path in headless Chromium against a real waved.
//
// The URL prompt (window.prompt) is stubbed so the command has a URL without a native
// dialog. Run with `npm run test:browser` (sandbox disabled).

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer, typeInto } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

// selectFirstPara selects [start, end) chars of the first paragraph's first text node.
// Uses a TreeWalker so it descends into wrapping elements (a span, or — after a link is
// applied — the <a>), where the text actually lives.
function selectFirstPara(page: Page, start: number, end: number): Promise<void> {
  return page.evaluate(
    ({ start, end }) => {
      const para = document.querySelector(".blip-doc .para");
      if (para === null) throw new Error("no .para");
      const tn = document.createTreeWalker(para, NodeFilter.SHOW_TEXT).nextNode();
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

function firstLink(page: Page): Promise<{ text: string; href: string } | null> {
  return page.evaluate(() => {
    const a = document.querySelector<HTMLAnchorElement>(".blip-doc .para a.wave-link");
    return a === null ? null : { text: a.textContent ?? "", href: a.getAttribute("href") ?? "" };
  });
}

test("the Link button wraps the selection in an <a> (scheme normalized) and converges", async () => {
  const alice = await client("alice@example.com", "w+link");
  try {
    await typeInto(alice, 0, "click here please");
    await selectFirstPara(alice, 0, 10); // "click here"
    await alice.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });

    // Stub the URL prompt (scheme-less, to exercise normalizeUrl → https://).
    await alice.evaluate(() => {
      (window as unknown as { prompt: () => string }).prompt = () => "example.com/x";
    });
    await alice.locator('.sel-toolbar button[data-cmd="link"]').click();

    await alice.waitForFunction(
      () => document.querySelector(".blip-doc .para a.wave-link") !== null,
      undefined,
      { timeout: 5000 },
    );
    const link = await firstLink(alice);
    assert.ok(link !== null, "a link rendered");
    assert.equal(link.text, "click here", "the selected text became the link");
    assert.equal(link.href, "https://example.com/x", "a scheme-less URL is normalized to https");

    // Converges to a fresh client — proving the link is a submitted annotation op, not
    // just local DOM (the assertion that fails if setLink never reached the server).
    const bob = await client("bob@example.com", "w+link");
    try {
      await bob.waitForFunction(
        () => {
          const a = document.querySelector<HTMLAnchorElement>(".blip-doc .para a.wave-link");
          return a !== null && a.getAttribute("href") === "https://example.com/x" && a.textContent === "click here";
        },
        undefined,
        { timeout: 8000 },
      );
    } finally {
      await bob.close();
    }
  } finally {
    await alice.close();
  }
});

// A javascript: (or other script-capable) URL must never become a live href — the
// XSS gate. Whether typed locally or arriving in a remote peer's annotation, the run
// renders as inert text, not an executable link.
test("a javascript: URL does not become a live link (XSS gate)", async () => {
  const alice = await client("alice@example.com", "w+link-xss");
  try {
    await typeInto(alice, 0, "danger zone");
    await selectFirstPara(alice, 0, 6); // "danger"
    await alice.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await alice.evaluate(() => {
      (window as unknown as { prompt: () => string }).prompt = () => "javascript:alert(1)";
    });
    await alice.locator('.sel-toolbar button[data-cmd="link"]').click();

    // Give the editor a moment to (not) apply it, then assert no live link exists and
    // the text is intact.
    await alice.waitForTimeout(300);
    const links = await alice.evaluate(() => document.querySelectorAll(".blip-doc .para a.wave-link").length);
    assert.equal(links, 0, "a javascript: URL must not render as a live <a>");
    const anyJsHref = await alice.evaluate(() =>
      Array.from(document.querySelectorAll(".blip-doc a")).some((a) =>
        (a.getAttribute("href") ?? "").toLowerCase().startsWith("javascript:"),
      ),
    );
    assert.equal(anyJsHref, false, "no anchor carries a javascript: href");
    const text = await alice.evaluate(() => (document.querySelector(".blip-doc")?.textContent ?? "").trim());
    assert.equal(text, "danger zone", "the text is unchanged");
  } finally {
    await alice.close();
  }
});

test("emptying the URL removes the link", async () => {
  const alice = await client("alice@example.com", "w+link-clear");
  try {
    await typeInto(alice, 0, "linked text");
    await selectFirstPara(alice, 0, 6); // "linked"
    await alice.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await alice.evaluate(() => {
      (window as unknown as { prompt: () => string }).prompt = () => "https://example.com";
    });
    await alice.locator('.sel-toolbar button[data-cmd="link"]').click();
    await alice.waitForFunction(() => document.querySelector(".blip-doc .para a.wave-link") !== null, undefined, {
      timeout: 5000,
    });

    // Re-select the linked text and clear the link (empty URL).
    await selectFirstPara(alice, 0, 6);
    await alice.locator(".sel-toolbar.visible").waitFor({ state: "visible", timeout: 5000 });
    await alice.evaluate(() => {
      (window as unknown as { prompt: () => string }).prompt = () => "";
    });
    await alice.locator('.sel-toolbar button[data-cmd="link"]').click();

    await alice.waitForFunction(() => document.querySelector(".blip-doc .para a.wave-link") === null, undefined, {
      timeout: 5000,
    });
    // The text survives the unlink.
    const text = await alice.evaluate(() => (document.querySelector(".blip-doc")?.textContent ?? "").trim());
    assert.equal(text, "linked text", "unlinking keeps the text");
  } finally {
    await alice.close();
  }
});
