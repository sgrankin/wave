// Browser component tests for <wave-thread> and <wave-blip>.
//
// Covers:
//  1. Rendering: thread structure, blip nesting, continue/reply buttons.
//  2. Wiring: controller methods fire on user interactions.
//
// Run via: npm run test:web  (from web/)
// Node-only tests (blipdoc.test.ts) are not affected — this file does not
// import any node: builtin, so the web runner picks it up and node test
// runner skips it.

import { html } from "lit";
import type { T } from "../../testing/harness.ts";
import { eq, render } from "../../testing/harness.ts";

import { Attributes, DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import type { Blip, Thread } from "../wave/conversation.ts";
import { initialBlipContent } from "../wave/conversation.ts";
import type { ConvController } from "./controller.ts";

// Import the components so they register as custom elements.
import "./wave-thread.ts";
import "./wave-blip.ts";

// ---------------------------------------------------------------------------
// Fake ConvController
// ---------------------------------------------------------------------------

interface EditCall {
  blipId: string;
  ops: Component[];
}
interface ContinueCall {
  threadId: string;
}
interface ReplyCall {
  parentBlipId: string;
  inline: boolean;
  anchorOffset: number | undefined;
}

function fakeController(contents?: Map<string, DocOp>): ConvController & {
  editCalls: EditCall[];
  continueCalls: ContinueCall[];
  replyCalls: ReplyCall[];
} {
  const editCalls: EditCall[] = [];
  const continueCalls: ContinueCall[] = [];
  const replyCalls: ReplyCall[] = [];
  const map = contents ?? new Map<string, DocOp>();

  return {
    user: "test@example.com",
    blipContent(blipId: string): DocOp {
      return map.get(blipId) ?? DocOp.empty();
    },
    editBlip(blipId: string, ops: Component[]): void {
      editCalls.push({ blipId, ops });
    },
    continueThread(threadId: string): void {
      continueCalls.push({ threadId });
    },
    replyToBlip(parentBlipId: string, inline: boolean, anchorOffset?: number): void {
      replyCalls.push({ parentBlipId, inline, anchorOffset });
    },
    participants(): string[] {
      return [];
    },
    addParticipant(_addr: string): void {
      // no-op in thread/blip tests
    },
    removeParticipant(_addr: string): void {
      // no-op in thread/blip tests
    },
    attachImage(_blipId: string, _file: File, _offset: number): void {
      // no-op in thread/blip tests
    },
    editCalls,
    continueCalls,
    replyCalls,
  };
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Build a simple Blip with no reply threads. */
function blip(id: string): Blip {
  return { id, deleted: false, threads: [] };
}

/** Build a Thread. */
function thread(id: string, blips: Blip[], inline = false): Thread {
  return { id, inline, blips };
}

/** Wait for a wave-thread and all nested custom elements to finish rendering. */
async function waitForNestedUpdates(root: HTMLElement): Promise<void> {
  // One round is enough for the outer element; nested Lit elements share the
  // same microtask queue so a second Promise<void> drain covers them.
  await new Promise<void>((r) => setTimeout(r, 0));
  const allCustom = root.querySelectorAll("wave-thread, wave-blip, blip-view");
  for (const el of allCustom) {
    if ("updateComplete" in el) await (el as { updateComplete: Promise<unknown> }).updateComplete;
  }
  // One more tick for any child updates triggered by the above.
  await new Promise<void>((r) => setTimeout(r, 0));
}

// ---------------------------------------------------------------------------
// Rendering tests
// ---------------------------------------------------------------------------

export async function testRootThreadRendersTwoBlips(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("", [blip("b1"), blip("b2")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  // Should contain exactly two wave-blip elements.
  const blips = el.querySelectorAll("wave-blip");
  eq(blips.length, 2, "blip count");

  // Each wave-blip should contain a blip-view.
  for (const b of blips) {
    const bv = b.querySelector("blip-view");
    eq(bv !== null, true, "blip-view inside wave-blip");
  }
}

export async function testRootThreadHasContinueButton(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const btn = el.querySelector<HTMLButtonElement>(".continue-btn");
  eq(btn !== null, true, "continue button present");
  eq(btn!.textContent!.trim(), "+ New message", "root thread button label");
}

export async function testReplyThreadHasContinueButton(t: T): Promise<void> {
  const ctrl = fakeController();
  // A reply thread has a non-empty id.
  const th = thread("b1", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const btn = el.querySelector<HTMLButtonElement>(".continue-btn");
  eq(btn !== null, true, "continue button present");
  eq(btn!.textContent!.trim(), "+ Continue thread", "reply thread button label");
}

export async function testNestedReplyThreadRendered(t: T): Promise<void> {
  const ctrl = fakeController();

  // blip "b1" has a reply thread "r1" containing blip "r1".
  const replyThread = thread("r1", [blip("r1")]);
  const b1: Blip = { id: "b1", deleted: false, threads: [replyThread] };
  const th = thread("", [b1, blip("b2")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  // The outer thread has 2 top-level wave-blip elements.
  // wave-blip "b1" should contain one nested wave-thread.
  const outerBlips = el.querySelectorAll(":scope > div > wave-blip");
  eq(outerBlips.length, 2, "two top-level blips");

  const nestedThread = outerBlips[0]!.querySelector("wave-thread");
  eq(nestedThread !== null, true, "nested wave-thread found");

  // The nested thread should have the reply class.
  const nestedDiv = nestedThread!.querySelector("div");
  eq(nestedDiv!.classList.contains("reply"), true, "nested thread has reply class");

  // The nested thread should contain one wave-blip.
  const nestedBlips = nestedThread!.querySelectorAll("wave-blip");
  eq(nestedBlips.length, 1, "one blip in nested thread");
}

export async function testBlipWithKnownContentRendersText(t: T): Promise<void> {
  // Build blip content: <body><line/>Hello</body>
  const content = new DocOp([
    { kind: "elementStart", type: "body", attributes: Attributes.empty() },
    { kind: "elementStart", type: "line", attributes: Attributes.empty() },
    { kind: "elementEnd" }, // </line>
    { kind: "characters", text: "Hello" },
    { kind: "elementEnd" }, // </body>
  ]);

  const contents = new Map([["b1", content]]);
  const ctrl = fakeController(contents);
  const th = thread("", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  // The rendered DOM should contain "Hello" somewhere inside the blip-view.
  const bv = el.querySelector("blip-view");
  eq(bv !== null, true, "blip-view present");
  const text = bv!.textContent ?? "";
  eq(text.includes("Hello"), true, `blip-view text includes "Hello", got: ${JSON.stringify(text)}`);
}

export async function testInitialBlipContentRendersEmptyParagraph(t: T): Promise<void> {
  const content = initialBlipContent(); // <body><line/></body>
  const contents = new Map([["b1", content]]);
  const ctrl = fakeController(contents);
  const th = thread("", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  // blip-view renders one .para div for the <line/>.
  const bv = el.querySelector("blip-view");
  eq(bv !== null, true, "blip-view present");
  const para = bv!.querySelector(".para");
  eq(para !== null, true, ".para rendered for <line/>");
}

// ---------------------------------------------------------------------------
// Wiring tests
// ---------------------------------------------------------------------------

export async function testContinueButtonCallsContinueThread(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const btn = el.querySelector<HTMLButtonElement>(".continue-btn");
  eq(btn !== null, true, "continue button present");
  btn!.click();

  eq(ctrl.continueCalls.length, 1, "continueThread called once");
  eq(ctrl.continueCalls[0]!.threadId, "", "continueThread called with root thread id");
}

export async function testContinueButtonOnReplyThread(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("thread1", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const btn = el.querySelector<HTMLButtonElement>(".continue-btn");
  btn!.click();

  eq(ctrl.continueCalls.length, 1, "continueThread called once");
  eq(ctrl.continueCalls[0]!.threadId, "thread1", "continueThread called with reply thread id");
}

export async function testReplyButtonCallsReplyToBlip(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const replyBtn = el.querySelector<HTMLButtonElement>(".reply-btn");
  eq(replyBtn !== null, true, "reply button present");
  replyBtn!.click();

  eq(ctrl.replyCalls.length, 1, "replyToBlip called once");
  eq(ctrl.replyCalls[0]!.parentBlipId, "b1", "replyToBlip called with blip id");
  eq(ctrl.replyCalls[0]!.inline, false, "replyToBlip inline=false");
}

export async function testReplyButtonOnSecondBlip(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("", [blip("b1"), blip("b2")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const replyBtns = el.querySelectorAll<HTMLButtonElement>(".reply-btn");
  eq(replyBtns.length, 2, "two reply buttons");

  // Click the second blip's reply button.
  replyBtns[1]!.click();

  eq(ctrl.replyCalls.length, 1, "replyToBlip called once");
  eq(ctrl.replyCalls[0]!.parentBlipId, "b2", "replyToBlip called with second blip id");
}

export async function testReplyInlineButtonAnchors(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const inlineBtn = el.querySelector<HTMLButtonElement>(".reply-inline-btn");
  eq(inlineBtn !== null, true, "inline reply button present");
  inlineBtn!.click();

  eq(ctrl.replyCalls.length, 1, "replyToBlip called once");
  eq(ctrl.replyCalls[0]!.parentBlipId, "b1", "inline reply parent blip id");
  eq(ctrl.replyCalls[0]!.inline, true, "inline reply marks inline=true");
  // No caret is placed in the test, so it anchors at the end of the (empty) blip —
  // a defined, non-negative offset.
  eq(typeof ctrl.replyCalls[0]!.anchorOffset === "number", true, "an anchor offset is passed");
}

export async function testEditEventCallsEditBlip(t: T): Promise<void> {
  const ctrl = fakeController();
  const th = thread("", [blip("b1")]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  const bv = el.querySelector("blip-view");
  eq(bv !== null, true, "blip-view present");

  // Build a simple insert-text op for the detail payload.
  const ops: Component[] = [{ kind: "characters", text: "Hi" }];

  // Dispatch a synthetic "edit" CustomEvent from the blip-view. wave-blip
  // listens for this event (with bubbles:true, composed:true) and calls
  // controller.editBlip — stopPropagation keeps it from bubbling further.
  bv!.dispatchEvent(new CustomEvent<Component[]>("edit", { detail: ops, bubbles: true, composed: true }));

  eq(ctrl.editCalls.length, 1, "editBlip called once");
  eq(ctrl.editCalls[0]!.blipId, "b1", "editBlip called with correct blip id");
  eq(ctrl.editCalls[0]!.ops, ops, "editBlip called with the emitted ops");
}

export async function testEditEventStopsPropagation(t: T): Promise<void> {
  // A blip with a reply containing another blip: the inner blip's edit event
  // must NOT bubble up to the outer wave-blip (stopPropagation).
  const ctrl = fakeController();
  const innerBlip = blip("inner");
  const replyThread = thread("r1", [innerBlip]);
  const outerBlip: Blip = { id: "outer", deleted: false, threads: [replyThread] };
  const th = thread("", [outerBlip]);

  const el = await render(html`<wave-thread .thread=${th} .controller=${ctrl}></wave-thread>`);
  await waitForNestedUpdates(el);

  // Find the blip-view inside the nested (inner) blip.
  const outerWaveBlip = el.querySelector("wave-blip");
  eq(outerWaveBlip !== null, true, "outer wave-blip found");

  const innerWaveBlip = outerWaveBlip!.querySelector("wave-blip");
  eq(innerWaveBlip !== null, true, "inner wave-blip found");

  const innerBV = innerWaveBlip!.querySelector("blip-view");
  eq(innerBV !== null, true, "inner blip-view found");

  const ops: Component[] = [{ kind: "characters", text: "test" }];
  innerBV!.dispatchEvent(new CustomEvent<Component[]>("edit", { detail: ops, bubbles: true, composed: true }));

  // Only one editBlip call: for "inner", not "outer".
  eq(ctrl.editCalls.length, 1, "editBlip called exactly once");
  eq(ctrl.editCalls[0]!.blipId, "inner", "editBlip called with inner blip id, not outer");
}
