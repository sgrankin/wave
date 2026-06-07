// End-to-end browser convergence tests: drive the real conversation-view UI in
// headless Chromium against a real Go `waved`, and assert a fresh client
// converges via history replay. This is the regression guard for the whole
// browser stack — the controlled contenteditable, the recursive thread/blip
// components, the manifest authoring, and the OptimisticClient — against the
// authoritative server. It catches bugs unit/component tests cannot (fake
// controllers, synthetic events): notably a controlled editor that lets the
// browser edit natively, so text shows locally but is never submitted.
//
// The plumbing lives in ./browser-harness.ts; a scenario is just open clients →
// act → assert convergence. Heavy, so gated behind `npm run test:browser` (not the
// default suite). Requires `npx playwright install chromium`; loopback + spawn +
// browser need the host sandbox disabled.

import { after, before, test } from "node:test";
import assert from "node:assert/strict";

import {
  clickContinue,
  clickReply,
  client,
  startServer,
  stopServer,
  typeInto,
  waitForBlip,
  waitForBlipTexts,
} from "./browser-harness.ts";

before(startServer);
after(stopServer);

// The core regression: a blip a client creates and then immediately edits must
// converge. (The bug: the caret landed in a stray text node in the freshly
// created blip; the controlled editor failed to map it, fell through to native
// browser editing, and the text was never submitted — it showed locally but a
// fresh client never saw it.)
test("create-then-edit a blip converges to a fresh client", async () => {
  const wave = "w+btest-create";
  const alice = await client("alice@example.com", wave);
  try {
    await typeInto(alice, 0, "root by alice");
    await clickContinue(alice); // + New message (root thread)
    await waitForBlip(alice, 1);
    await typeInto(alice, 1, "second blip body");

    // A fresh client only sees server-committed state — it converges via replay.
    const dave = await client("dave@example.com", wave);
    try {
      const texts = await waitForBlipTexts(dave, ["root by alice", "second blip body"]);
      assert.ok(texts.some((t) => t.includes("root by alice")), `missing root; saw ${JSON.stringify(texts)}`);
      assert.ok(
        texts.some((t) => t.includes("second blip body")),
        `missing the created-then-edited blip; saw ${JSON.stringify(texts)}`,
      );
    } finally {
      await dave.close();
    }
  } finally {
    await alice.close();
  }
});

// A threaded reply (new reply thread + its first blip) converges, including the
// reply blip's edited content, nested in a reply thread (not a root sibling).
test("a threaded reply converges to a fresh client", async () => {
  const wave = "w+btest-reply";
  const alice = await client("alice@example.com", wave);
  try {
    await typeInto(alice, 0, "parent blip");
    await clickReply(alice, 0); // start a reply thread on the root blip
    await waitForBlip(alice, 1);
    await typeInto(alice, 1, "the reply");

    const dave = await client("dave@example.com", wave);
    try {
      await waitForBlipTexts(dave, ["parent blip", "the reply"]);
      const replyInThread = await dave.evaluate(() => {
        const t = document.querySelector(".wave-thread.reply");
        return t !== null && (t.textContent ?? "").includes("the reply");
      });
      assert.ok(replyInThread, "reply text should render inside a nested reply thread");
    } finally {
      await dave.close();
    }
  } finally {
    await alice.close();
  }
});

// Removing a participant via the roster 'x' takes them off the wave (the op already
// round-trips; this guards the new UI). The remove is confirm()-gated, so accept the
// dialog.
test("removing a participant updates the roster", async () => {
  const wave = "w+btest-removep";
  const alice = await client("alice@example.com", wave);
  try {
    alice.on("dialog", (d) => void d.accept());
    await alice.locator(".add-participant-input").fill("bob@example.com");
    await alice.locator(".add-participant-btn").click();
    // bob's chip appears.
    await alice.waitForFunction(
      () =>
        Array.from(document.querySelectorAll(".roster-chip .wave-participant-name")).some((e) =>
          (e.textContent ?? "").includes("bob@example.com"),
        ),
      undefined,
      { timeout: 10_000 },
    );
    // Click the 'x' on bob's chip.
    await alice.evaluate(() => {
      const chips = Array.from(document.querySelectorAll(".roster-chip"));
      const bob = chips.find((c) => (c.textContent ?? "").includes("bob@example.com"));
      bob?.querySelector<HTMLButtonElement>(".roster-remove")?.click();
    });
    // bob's chip disappears.
    await alice.waitForFunction(
      () =>
        !Array.from(document.querySelectorAll(".roster-chip .wave-participant-name")).some((e) =>
          (e.textContent ?? "").includes("bob@example.com"),
        ),
      undefined,
      { timeout: 10_000 },
    );
  } finally {
    await alice.close();
  }
});

// Presence: a second client on the same wave sees the first as present, and sees a
// "typing" indicator while they type — over the transient /presence channel,
// independent of the OT delta socket.
test("presence shows a peer present and typing", async () => {
  const wave = "w+btest-presence";
  const alice = await client("alice@example.com", wave);
  try {
    const bob = await client("bob@example.com", wave);
    try {
      // bob's presence bar shows alice as a present peer (avatar) once both joined.
      await bob.waitForFunction(() => document.querySelector(".conv-presence .presence-peer") !== null, undefined, {
        timeout: 10_000,
      });

      // alice types → bob sees a "typing" indicator naming her.
      const ablip = alice.locator(".blip-doc").first();
      await ablip.click();
      await ablip.pressSequentially("hi bob", { delay: 10 });
      await bob.waitForFunction(
        () => {
          const t = document.querySelector(".conv-presence .presence-typing")?.textContent ?? "";
          return t.includes("typing") && t.includes("alice@example.com");
        },
        undefined,
        { timeout: 10_000 },
      );
    } finally {
      await bob.close();
    }
  } finally {
    await alice.close();
  }
});
