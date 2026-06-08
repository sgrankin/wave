// Blip-delete browser e2e: deleting a message replaces it with a "message deleted"
// tombstone, removes its text, and converges to a second client (which never sees
// the deleted text). Run with `npm run test:browser` (sandbox disabled).

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import { client, startServer, stopServer, typeInto, waitForBlipTexts } from "./browser-harness.ts";
import type { Page } from "playwright";

before(startServer);
after(stopServer);

// hasSecret reports whether any blip editor still shows the deleted text.
function hasSecret(page: Page): Promise<boolean> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".blip-doc")).some((e) => (e.textContent ?? "").includes("secret")),
  );
}

test("deleting a blip leaves a tombstone, removes the text, and converges", async () => {
  const a = await client("alice@example.com", "w+del");
  await typeInto(a, 0, "secret message");
  await waitForBlipTexts(a, ["secret message"]);

  // Bob joins while the blip still exists (the harness waits for a .blip-doc).
  const b = await client("bob@example.com", "w+del");
  await waitForBlipTexts(b, ["secret message"]);

  // Alice deletes the blip (accept the confirm dialog).
  a.on("dialog", (d) => void d.accept());
  await a.locator(".delete-btn").first().click();

  // Both clients converge on a tombstone with the text gone.
  for (const p of [a, b]) {
    await p.waitForFunction(
      () => {
        const tomb = document.querySelector(".blip-deleted");
        const stillSecret = Array.from(document.querySelectorAll(".blip-doc")).some((e) =>
          (e.textContent ?? "").includes("secret"),
        );
        return tomb !== null && !stillSecret;
      },
      undefined,
      { timeout: 8000 },
    );
  }
  assert.equal(await hasSecret(a), false, "alice: deleted text removed");
  assert.equal(await hasSecret(b), false, "bob: deleted text never persisted to a peer");

  await a.close();
  await b.close();
});
