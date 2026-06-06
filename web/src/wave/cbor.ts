// A minimal CBOR (RFC 8949) encoder/decoder for exactly the wire subset the Wave
// protocol uses: unsigned/negative integers, byte strings, text strings, arrays,
// booleans, and null. No maps, tags, or floats — the Go codec emits only
// positional arrays and toarray structs (internal/codec), so this is sufficient
// and there is no map-ordering concern. The Go side encodes with RFC 8949 Core
// Deterministic; that only constrains encoding, and CBOR decoders accept any
// valid length form, so this encoder uses shortest-form ints (compatible) and the
// decoder accepts definite-length items (which is all the server emits).
//
// Integers within ±2^53 decode to `number`; larger ones decode to `bigint`
// (the protocol's versions/counts/timestamps stay well within the safe range).

export type CborValue = number | bigint | string | Uint8Array | boolean | null | CborValue[];

const MAJOR_UINT = 0;
const MAJOR_NEGINT = 1;
const MAJOR_BYTES = 2;
const MAJOR_TEXT = 3;
const MAJOR_ARRAY = 4;
const MAJOR_SIMPLE = 7;

// --- encode ---

export function encode(v: CborValue): Uint8Array {
  const out: number[] = [];
  encodeInto(out, v);
  return Uint8Array.from(out);
}

function encodeInto(out: number[], v: CborValue): void {
  if (v === null) {
    out.push(0xf6);
  } else if (typeof v === "boolean") {
    out.push(v ? 0xf5 : 0xf4);
  } else if (typeof v === "number") {
    if (!Number.isInteger(v)) throw new Error(`cbor: non-integer number ${v}`);
    encodeInt(out, v);
  } else if (typeof v === "bigint") {
    encodeBigInt(out, v);
  } else if (typeof v === "string") {
    const bytes = new TextEncoder().encode(v);
    encodeHead(out, MAJOR_TEXT, bytes.length);
    for (const b of bytes) out.push(b);
  } else if (v instanceof Uint8Array) {
    encodeHead(out, MAJOR_BYTES, v.length);
    for (const b of v) out.push(b);
  } else if (Array.isArray(v)) {
    encodeHead(out, MAJOR_ARRAY, v.length);
    for (const item of v) encodeInto(out, item);
  } else {
    throw new Error(`cbor: cannot encode value of type ${typeof v}`);
  }
}

function encodeInt(out: number[], n: number): void {
  if (n >= 0) encodeHead(out, MAJOR_UINT, n);
  else encodeHead(out, MAJOR_NEGINT, -1 - n);
}

function encodeBigInt(out: number[], n: bigint): void {
  if (n >= 0n) encodeHeadBig(out, MAJOR_UINT, n);
  else encodeHeadBig(out, MAJOR_NEGINT, -1n - n);
}

// encodeHead writes a major type + argument (the length, or the integer value)
// in shortest form. arg is a non-negative integer that fits in a JS number.
function encodeHead(out: number[], major: number, arg: number): void {
  const m = major << 5;
  if (arg < 24) {
    out.push(m | arg);
  } else if (arg < 0x100) {
    out.push(m | 24, arg);
  } else if (arg < 0x10000) {
    out.push(m | 25, (arg >> 8) & 0xff, arg & 0xff);
  } else if (arg < 0x100000000) {
    out.push(m | 26, (arg >>> 24) & 0xff, (arg >>> 16) & 0xff, (arg >>> 8) & 0xff, arg & 0xff);
  } else {
    encodeHeadBig(out, major, BigInt(arg));
  }
}

function encodeHeadBig(out: number[], major: number, arg: bigint): void {
  const m = major << 5;
  if (arg < 24n) {
    out.push(m | Number(arg));
  } else if (arg < 0x100n) {
    out.push(m | 24, Number(arg));
  } else if (arg < 0x10000n) {
    out.push(m | 25, Number((arg >> 8n) & 0xffn), Number(arg & 0xffn));
  } else if (arg < 0x100000000n) {
    out.push(m | 26, Number((arg >> 24n) & 0xffn), Number((arg >> 16n) & 0xffn), Number((arg >> 8n) & 0xffn), Number(arg & 0xffn));
  } else {
    const b: number[] = [];
    let x = arg;
    for (let i = 0; i < 8; i++) {
      b.unshift(Number(x & 0xffn));
      x >>= 8n;
    }
    out.push(m | 27, ...b);
  }
}

// --- decode ---

class Reader {
  readonly data: Uint8Array;
  pos = 0;
  constructor(data: Uint8Array) {
    this.data = data;
  }
  byte(): number {
    if (this.pos >= this.data.length) throw new Error("cbor: unexpected end of input");
    return this.data[this.pos++]!;
  }
  bytes(n: number): Uint8Array {
    if (this.pos + n > this.data.length) throw new Error("cbor: unexpected end of input");
    const out = this.data.subarray(this.pos, this.pos + n);
    this.pos += n;
    return out;
  }
}

/** Decode a single CBOR item from bytes. Trailing bytes are an error. */
export function decode(bytes: Uint8Array): CborValue {
  const r = new Reader(bytes);
  const v = decodeItem(r);
  if (r.pos !== bytes.length) throw new Error(`cbor: ${bytes.length - r.pos} trailing bytes`);
  return v;
}

function decodeItem(r: Reader): CborValue {
  const ib = r.byte();
  const major = ib >> 5;
  const ai = ib & 0x1f;
  switch (major) {
    case MAJOR_UINT:
      return argToNumberOrBig(readArg(r, ai));
    case MAJOR_NEGINT: {
      const arg = readArg(r, ai);
      return typeof arg === "bigint" ? -1n - arg : -1 - arg;
    }
    case MAJOR_BYTES: {
      const n = lenArg(readArg(r, ai));
      return r.bytes(n).slice();
    }
    case MAJOR_TEXT: {
      const n = lenArg(readArg(r, ai));
      return new TextDecoder("utf-8", { fatal: true }).decode(r.bytes(n));
    }
    case MAJOR_ARRAY: {
      const n = lenArg(readArg(r, ai));
      const out: CborValue[] = [];
      for (let i = 0; i < n; i++) out.push(decodeItem(r));
      return out;
    }
    case MAJOR_SIMPLE:
      if (ai === 20) return false;
      if (ai === 21) return true;
      if (ai === 22) return null; // null
      if (ai === 23) return null; // undefined → null
      throw new Error(`cbor: unsupported simple/float value (ai ${ai})`);
    default:
      throw new Error(`cbor: unsupported major type ${major}`);
  }
}

// readArg reads the integer argument for additional-info ai. Returns number when
// it fits safely, else bigint. Indefinite length (ai 31) is unsupported.
function readArg(r: Reader, ai: number): number | bigint {
  if (ai < 24) return ai;
  if (ai === 24) return r.byte();
  if (ai === 25) {
    const b = r.bytes(2);
    return (b[0]! << 8) | b[1]!;
  }
  if (ai === 26) {
    const b = r.bytes(4);
    return (b[0]! * 0x1000000 + (b[1]! << 16) + (b[2]! << 8) + b[3]!) >>> 0;
  }
  if (ai === 27) {
    const b = r.bytes(8);
    let x = 0n;
    for (const byte of b) x = (x << 8n) | BigInt(byte);
    return x;
  }
  throw new Error(`cbor: unsupported additional info ${ai}`);
}

function argToNumberOrBig(arg: number | bigint): number | bigint {
  if (typeof arg === "number") return arg;
  return arg <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(arg) : arg;
}

function lenArg(arg: number | bigint): number {
  const n = typeof arg === "bigint" ? Number(arg) : arg;
  if (!Number.isSafeInteger(n) || n < 0) throw new Error(`cbor: invalid length ${arg}`);
  return n;
}
