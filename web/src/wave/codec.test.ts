// Round-trip tests for the wire codec against the Go-generated fixtures oracle.
// Each "codec" fixture is the canonical CBOR (real Go codec output); we decode it
// by kind, re-encode, and re-decode, asserting the two decoded forms are
// structurally equal. This proves (a) we decode the Go bytes correctly and (b)
// our encoding round-trips through the same positional shape.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { decodeClientDelta, decodeDocOp, decodeStoredDelta, encodeClientDelta, encodeDocOp, encodeStoredDelta } from "./codec.ts";
import type { ClientDelta, StoredDelta } from "./codec.ts";
import { bytesEqual, DocOp, HashedVersion } from "./types.ts";
import type { Component, Context, Operation } from "./types.ts";

// --- structural equality helpers ---

function componentEqual(a: Component, b: Component): boolean {
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "retain":
      return b.kind === "retain" && a.count === b.count;
    case "characters":
      return b.kind === "characters" && a.text === b.text;
    case "deleteCharacters":
      return b.kind === "deleteCharacters" && a.text === b.text;
    case "elementStart":
      return b.kind === "elementStart" && a.type === b.type && a.attributes.equal(b.attributes);
    case "deleteElementStart":
      return b.kind === "deleteElementStart" && a.type === b.type && a.attributes.equal(b.attributes);
    case "elementEnd":
      return b.kind === "elementEnd";
    case "deleteElementEnd":
      return b.kind === "deleteElementEnd";
    case "replaceAttributes":
      return b.kind === "replaceAttributes" && a.oldAttributes.equal(b.oldAttributes) && a.newAttributes.equal(b.newAttributes);
    case "updateAttributes":
      return b.kind === "updateAttributes" && a.update.equal(b.update);
    case "annotationBoundary":
      return b.kind === "annotationBoundary" && a.boundary.equal(b.boundary);
  }
}

function docOpEqual(a: DocOp, b: DocOp): boolean {
  if (a.components.length !== b.components.length) return false;
  for (let i = 0; i < a.components.length; i++) {
    if (!componentEqual(a.components[i]!, b.components[i]!)) return false;
  }
  return true;
}

function contextEqual(a: Context, b: Context): boolean {
  if (a.creator !== b.creator || a.timestamp !== b.timestamp || a.versionIncrement !== b.versionIncrement) return false;
  if (a.hashedVersion === null || b.hashedVersion === null) return a.hashedVersion === b.hashedVersion;
  return a.hashedVersion.equal(b.hashedVersion);
}

function operationEqual(a: Operation, b: Operation): boolean {
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "blip":
      return (
        b.kind === "blip" &&
        a.blipId === b.blipId &&
        contextEqual(a.op.ctx, b.op.ctx) &&
        a.op.method === b.op.method &&
        docOpEqual(a.op.contentOp, b.op.contentOp)
      );
    case "addParticipant":
      return b.kind === "addParticipant" && a.participant === b.participant && contextEqual(a.ctx, b.ctx);
    case "removeParticipant":
      return b.kind === "removeParticipant" && a.participant === b.participant && contextEqual(a.ctx, b.ctx);
    case "noOp":
      return b.kind === "noOp" && contextEqual(a.ctx, b.ctx);
  }
}

function opsEqual(a: readonly Operation[], b: readonly Operation[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (!operationEqual(a[i]!, b[i]!)) return false;
  return true;
}

function hvEqual(a: HashedVersion, b: HashedVersion): boolean {
  return a.version === b.version && bytesEqual(a.historyHash, b.historyHash);
}

function clientDeltaEqual(a: ClientDelta, b: ClientDelta): void {
  assert.equal(a.author, b.author);
  assert.ok(hvEqual(a.targetVersion, b.targetVersion), "targetVersion");
  assert.equal(a.nonce, b.nonce);
  assert.ok(opsEqual(a.ops, b.ops), "ops");
}

function storedDeltaEqual(a: StoredDelta, b: StoredDelta): void {
  assert.equal(a.author, b.author);
  assert.ok(hvEqual(a.resultingVersion, b.resultingVersion), "resultingVersion");
  assert.equal(a.timestamp, b.timestamp);
  assert.equal(a.nonce, b.nonce);
  assert.ok(opsEqual(a.ops, b.ops), "ops");
}

// --- fixtures ---

interface CodecCase {
  note: string;
  kind: "docOp" | "clientDelta" | "storedDelta";
  hex: string;
}

const fx = JSON.parse(readFileSync("src/wave/testdata/fixtures.json", "utf8")) as { codec: CodecCase[] };

function fromHex(hex: string): Uint8Array {
  return Uint8Array.from(Buffer.from(hex, "hex"));
}

test("codec fixtures decode and round-trip", () => {
  assert.ok(fx.codec.length > 0, "expected codec fixtures");
  for (const c of fx.codec) {
    const bytes = fromHex(c.hex);
    switch (c.kind) {
      case "docOp": {
        const got = decodeDocOp(bytes);
        const round = decodeDocOp(encodeDocOp(got));
        assert.ok(docOpEqual(got, round), `docOp round-trip mismatch: ${c.note}`);
        break;
      }
      case "clientDelta": {
        const got = decodeClientDelta(bytes);
        const round = decodeClientDelta(encodeClientDelta(got));
        clientDeltaEqual(got, round);
        break;
      }
      case "storedDelta": {
        const got = decodeStoredDelta(bytes);
        const round = decodeStoredDelta(encodeStoredDelta(got));
        storedDeltaEqual(got, round);
        break;
      }
    }
  }
});
