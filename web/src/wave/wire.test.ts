import { strict as assert } from "node:assert";
import test from "node:test";

import { frame, unframe } from "./wire.ts";

test("frame/unframe round-trip", () => {
  for (const payload of [
    new Uint8Array(0),
    Uint8Array.from([1, 2, 3]),
    Uint8Array.from({ length: 300 }, (_, i) => i & 0xff),
  ]) {
    const framed = frame(payload);
    assert.equal(framed.length, payload.length + 4);
    // big-endian length prefix
    const n = new DataView(framed.buffer, framed.byteOffset, framed.byteLength).getUint32(0, false);
    assert.equal(n, payload.length);
    assert.deepEqual(unframe(framed), payload);
  }
});

test("unframe rejects a length mismatch", () => {
  const framed = frame(Uint8Array.from([1, 2, 3]));
  assert.throws(() => unframe(framed.subarray(0, framed.length - 1)), /length mismatch/);
});

test("unframe rejects a truncated header", () => {
  assert.throws(() => unframe(Uint8Array.from([0, 0])), /truncated frame/);
});
