// Browser component tests for <comment-sheet>.
//
// The critical behavior under test is auto-focus discipline: the sheet focuses its
// reply input ONCE on open (when created via "Comment"/"Reply inline"), and must NOT
// re-grab focus on subsequent re-renders. <wave-conversation> re-supplies a fresh
// Thread object every render (readManifest rebuilds the tree), so keying focus off
// "thread changed" would yank focus on every keystroke — the bug this guards against.
//
// Run via: npm run test:web  (from web/)

import { html } from "lit";
import type { T } from "../../testing/harness.ts";
import { eq, render } from "../../testing/harness.ts";

import { DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import type { Blip, Thread } from "../wave/conversation.ts";
import { initialBlipContent } from "../wave/conversation.ts";
import type { ConvController } from "./controller.ts";

import "./comment-sheet.ts";

// Minimal fake controller: only blipContent/user matter for rendering the thread.
function fakeController(): ConvController {
  return {
    user: "test@example.com",
    blipContent: (_id: string): DocOp => initialBlipContent(),
    editBlip: (_id: string, _ops: Component[]): void => {},
    continueThread: (_threadId: string): void => {},
    replyToBlip: (_p: string, _inline: boolean, _o?: number): string => "b+x",
    participants: (): string[] => [],
    addParticipant: (_addr: string): void => {},
    removeParticipant: (_addr: string): void => {},
    attachImage: (_b: string, _f: File, _o: number): void => {},
  };
}

// A comment thread with a single (comment) blip. Built fresh each call so two calls
// produce DISTINCT objects — mimicking readManifest returning a new tree per render.
function commentThread(): Thread {
  const b: Blip = { id: "b+c1", deleted: false, threads: [] };
  return { id: "b+c1", inline: true, blips: [b] };
}

function raf(): Promise<void> {
  return new Promise<void>((r) => requestAnimationFrame(() => r()));
}

async function settle(el: HTMLElement): Promise<void> {
  await new Promise<void>((r) => setTimeout(r, 0));
  const nested = el.querySelectorAll("comment-sheet, wave-thread, wave-blip, blip-view");
  for (const n of nested) {
    if ("updateComplete" in n) await (n as { updateComplete: Promise<unknown> }).updateComplete;
  }
  // The sheet focuses its input across a few animation frames (the nested editor
  // renders in later frames); wait out a handful so the focus has landed.
  for (let i = 0; i < 6; i++) await raf();
  await new Promise<void>((r) => setTimeout(r, 0));
}

// Auto-focus on open (a created comment), then NOT again on a re-render with a fresh
// thread object — the regression guard for the focus-fight bug.
export async function testAutoFocusOnceOnOpen(t: T): Promise<void> {
  const ctrl = fakeController();
  const el = await render(
    html`<comment-sheet .thread=${commentThread()} .controller=${ctrl} .autoFocus=${true}></comment-sheet>`,
  );
  await settle(el);

  const doc = el.querySelector<HTMLElement>(".blip-doc");
  eq(doc !== null, true, "comment input rendered");
  eq(document.activeElement === doc, true, "auto-focuses the comment input on open");

  // The user moves focus away (e.g. taps elsewhere).
  doc!.blur();
  eq(document.activeElement === doc, false, "focus released");

  // A re-render with a DISTINCT thread object (as the conversation re-renders) must
  // NOT pull focus back — otherwise typing in the comment would fight the caret.
  (el as HTMLElement & { thread: Thread }).thread = commentThread();
  await settle(el);
  eq(document.activeElement === doc, false, "does NOT re-grab focus on a later re-render");
}

// When opened to READ (tapping a 💬 anchor → autoFocus false), the sheet must not
// steal focus / raise the keyboard.
export async function testNoFocusWhenReading(t: T): Promise<void> {
  const ctrl = fakeController();
  const el = await render(
    html`<comment-sheet .thread=${commentThread()} .controller=${ctrl} .autoFocus=${false}></comment-sheet>`,
  );
  await settle(el);

  const doc = el.querySelector<HTMLElement>(".blip-doc");
  eq(doc !== null, true, "comment thread rendered");
  eq(document.activeElement === doc, false, "reading a comment does not steal focus");
}
