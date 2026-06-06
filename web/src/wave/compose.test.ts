// Compose conformance: drive the ported DocOp composer against the Go-generated
// fixtures. For each "compose" case the fixture carries three canonical-CBOR
// DocOps — a, b, and the expected out = compose(a, b). We decode all three and
// assert docOpEqual(compose(a, b), out).
//
// The fixtures.json oracle is produced by cmd/genfixtures from the Go reference
// (internal/op.Compose), so a green run means the TS port agrees with Go on
// every covered case.

import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";

import { decodeDocOp } from "./codec.ts";
import { docOpEqual } from "./docop.ts";
import { compose } from "./compose.ts";

interface ComposeCase {
  note: string;
  a: string;
  b: string;
  out: string;
}

interface Fixtures {
  compose: ComposeCase[];
}

const fx = JSON.parse(readFileSync(new URL("./testdata/fixtures.json", import.meta.url), "utf8")) as Fixtures;

function bytes(hex: string): Uint8Array {
  return Uint8Array.from(Buffer.from(hex, "hex"));
}

for (const c of fx.compose) {
  test(`compose: ${c.note}`, () => {
    const a = decodeDocOp(bytes(c.a));
    const b = decodeDocOp(bytes(c.b));
    const out = decodeDocOp(bytes(c.out));
    assert.ok(docOpEqual(compose(a, b), out), `compose(a, b) != out for ${c.note}`);
  });
}
