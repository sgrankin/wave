// Tests for the DocOp-level transform against the Go-generated fixtures oracle.
// Each "transform" fixture carries a client / server / clientPrime / serverPrime
// DocOp (hex-encoded canonical CBOR). We decode all four, run transform on
// (client, server), and assert the result equals [clientPrime, serverPrime].

import { readFileSync } from "node:fs";
import { Buffer } from "node:buffer";
import test from "node:test";
import assert from "node:assert/strict";

import { decodeDocOp } from "./codec.ts";
import { docOpEqual } from "./docop.ts";
import { transform } from "./transform.ts";

interface TransformCase {
  note: string;
  client: string;
  server: string;
  clientPrime: string;
  serverPrime: string;
}

interface Fixtures {
  transform: TransformCase[];
}

function fromHex(hex: string): Uint8Array {
  return Uint8Array.from(Buffer.from(hex, "hex"));
}

const fx = JSON.parse(readFileSync("src/wave/testdata/fixtures.json", "utf8")) as Fixtures;

test("transform matches Go fixtures", () => {
  assert.ok(fx.transform.length > 0, "expected transform fixtures");
  for (const c of fx.transform) {
    const client = decodeDocOp(fromHex(c.client));
    const server = decodeDocOp(fromHex(c.server));
    const wantClientPrime = decodeDocOp(fromHex(c.clientPrime));
    const wantServerPrime = decodeDocOp(fromHex(c.serverPrime));

    const [gotClientPrime, gotServerPrime] = transform(client, server);

    assert.ok(
      docOpEqual(gotClientPrime, wantClientPrime),
      `clientPrime mismatch for case: ${c.note}\n  got:  ${JSON.stringify(gotClientPrime.components)}\n  want: ${JSON.stringify(wantClientPrime.components)}`,
    );
    assert.ok(
      docOpEqual(gotServerPrime, wantServerPrime),
      `serverPrime mismatch for case: ${c.note}\n  got:  ${JSON.stringify(gotServerPrime.components)}\n  want: ${JSON.stringify(wantServerPrime.components)}`,
    );
  }
});
