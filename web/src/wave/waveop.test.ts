// Conformance tests for the wavelet-level op-list transform (transformOps),
// driven by the Go-generated fixtures. Each "deltaTransform" case carries four
// StoredDelta hex blobs (client / server / clientPrime / serverPrime); we decode
// each, take its .ops, run transformOps(clientOps, serverOps), and assert the
// result equals [clientPrimeOps, serverPrimeOps].
//
// Equality for blip ops compares blipId, contributor method, context creator,
// and the inner DocOp (component-wise); participant ops compare kind +
// participant; noOp compares kind + creator. This is stricter than the package's
// EqualOps (which ignores context) because the transform's correctness includes
// the method reset to ADD and the preserved per-side context creator.

import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";

import * as codec from "./codec.ts";
import { transformOps } from "./waveop.ts";
import { docOpEqual } from "./docop.ts";
import { opContext } from "./types.ts";
import type { DocOp, Operation } from "./types.ts";

const fx = JSON.parse(readFileSync("src/wave/testdata/fixtures.json", "utf8")) as {
  deltaTransform: { note: string; client: string; server: string; clientPrime: string; serverPrime: string }[];
};

function hexToBytes(hex: string): Uint8Array {
  return Uint8Array.from(Buffer.from(hex, "hex"));
}

// decodeOps decodes a StoredDelta hex blob and returns its operations.
function decodeOps(hex: string): Operation[] {
  return codec.decodeStoredDelta(hexToBytes(hex)).ops.slice();
}

// --- structural equality (DocOp via docop.docOpEqual, op-wise list) ---

// opEqual reports equality of two wavelet operations: blip ops by
// blipId/method/creator/inner DocOp, participant ops by participant, noOp by
// creator. The kinds must match.
function opEqual(a: Operation, b: Operation): boolean {
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "blip": {
      const o = b as typeof a;
      return (
        a.blipId === o.blipId &&
        a.op.method === o.op.method &&
        a.op.ctx.creator === o.op.ctx.creator &&
        docOpEqual(a.op.contentOp, o.op.contentOp)
      );
    }
    case "addParticipant":
      return a.participant === (b as typeof a).participant;
    case "removeParticipant":
      return a.participant === (b as typeof a).participant;
    case "noOp":
      return opContext(a).creator === opContext(b).creator;
  }
}

function opsEqual(a: Operation[], b: Operation[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (!opEqual(a[i]!, b[i]!)) return false;
  }
  return true;
}

function opStr(o: Operation): string {
  switch (o.kind) {
    case "blip":
      return `blip(${o.blipId}, method=${o.op.method}, creator=${o.op.ctx.creator}, doc=${docOpStr(o.op.contentOp)})`;
    case "addParticipant":
      return `add(${o.participant})`;
    case "removeParticipant":
      return `remove(${o.participant})`;
    case "noOp":
      return `noOp(creator=${opContext(o).creator})`;
  }
}

function docOpStr(d: DocOp): string {
  return "[" + d.components.map((c) => c.kind + (c.kind === "characters" || c.kind === "deleteCharacters" ? `:${c.text}` : c.kind === "retain" ? `:${c.count}` : "")).join(",") + "]";
}

function opsStr(ops: Operation[]): string {
  return "[" + ops.map(opStr).join(", ") + "]";
}

for (const c of fx.deltaTransform) {
  test(`deltaTransform: ${c.note}`, () => {
    const clientOps = decodeOps(c.client);
    const serverOps = decodeOps(c.server);
    const wantClientPrime = decodeOps(c.clientPrime);
    const wantServerPrime = decodeOps(c.serverPrime);

    const [gotClientPrime, gotServerPrime] = transformOps(clientOps, serverOps);

    assert.ok(
      opsEqual(gotClientPrime, wantClientPrime),
      `clientPrime mismatch:\n  got:  ${opsStr(gotClientPrime)}\n  want: ${opsStr(wantClientPrime)}`,
    );
    assert.ok(
      opsEqual(gotServerPrime, wantServerPrime),
      `serverPrime mismatch:\n  got:  ${opsStr(gotServerPrime)}\n  want: ${opsStr(wantServerPrime)}`,
    );
  });
}
