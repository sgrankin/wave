// Wire codec: the canonical serialization of operations and deltas, ported from
// internal/codec (codec.go + wire.go). It is the single encoding used for the
// wavelet history hash chain on the server and for the browser wire here.
//
// Everything is emitted as POSITIONAL CBOR ARRAYS (no maps), matching the Go
// toarray structs exactly. The Go side encodes with RFC 8949 Core Deterministic;
// this encoder need NOT be byte-canonical (the server re-hashes), but the wire
// SHAPES must be identical so the Go side decodes our output correctly, and we
// must decode the Go side's canonical output.
//
// Wire shapes (all positional arrays):
//   wireHV       = [version, hash]
//   wireContext  = [creator, timestamp, versionIncrement, hv|null]
//   wireAttr     = [name, value]
//   wireChange   = [key, old|null, new|null]
//   component    = [kind, ...] (cRetain=0 .. cAnnotationBoundary=9)
//   docOp        = [component, ...]
//   operation    = [kind, ...] (oWaveletBlip=0 .. oNoOp=3)
//   ops          = [operation, ...]
//   ClientDelta  = [author, hv, ops, nonce]
//   StoredDelta  = [author, hv, timestamp, ops, nonce]
//
// Go reference: internal/codec/codec.go, internal/codec/wire.go.

import { decode, encode } from "./cbor.ts";
import type { CborValue } from "./cbor.ts";
import {
  AnnotationBoundaryMap,
  Attributes,
  AttributesUpdate,
  DocOp,
  HashedVersion,
  participant,
} from "./types.ts";
import type {
  AnnotationChange,
  Attribute,
  AttributeChange,
  Component,
  Context,
  Operation,
  Participant,
} from "./types.ts";

// --- component (DocOp) kinds ---

const cRetain = 0;
const cCharacters = 1;
const cElementStart = 2;
const cElementEnd = 3;
const cDeleteCharacters = 4;
const cDeleteElementStart = 5;
const cDeleteElementEnd = 6;
const cReplaceAttributes = 7;
const cUpdateAttributes = 8;
const cAnnotationBoundary = 9;

// --- wavelet operation kinds ---

const oWaveletBlip = 0;
const oAddParticipant = 1;
const oRemoveParticipant = 2;
const oNoOp = 3;

// --- client / stored delta shapes ---

export interface ClientDelta {
  author: Participant;
  targetVersion: HashedVersion;
  ops: Operation[];
  nonce: string;
}

export interface StoredDelta {
  author: Participant;
  resultingVersion: HashedVersion;
  timestamp: number;
  ops: Operation[];
  nonce: string;
}

// --- decode helpers (CBOR shape checks) ---

// need bounds-checks a decoded element array before indexing. Decode input may
// be corrupt, so a truncated array must surface as an error.
function need(raw: CborValue[], n: number): void {
  if (raw.length < n) {
    throw new Error(`codec: truncated: have ${raw.length} elements, need ${n}`);
  }
}

function asArray(v: CborValue): CborValue[] {
  if (!Array.isArray(v)) throw new Error("codec: expected array");
  return v;
}

function asString(v: CborValue): string {
  if (typeof v !== "string") throw new Error("codec: expected string");
  return v;
}

// asNullableString reads a CBOR text-or-null into string|null (wireChange Old/New).
function asNullableString(v: CborValue): string | null {
  if (v === null) return null;
  if (typeof v !== "string") throw new Error("codec: expected string or null");
  return v;
}

function asUint(v: CborValue): number {
  if (typeof v === "bigint") {
    // The protocol's versions/counts/methods stay well within the safe range; a
    // bigint here means the value overflowed, which is malformed for these fields.
    throw new Error(`codec: integer ${v} out of safe range`);
  }
  if (typeof v !== "number" || !Number.isInteger(v) || v < 0) {
    throw new Error(`codec: expected unsigned integer, got ${String(v)}`);
  }
  return v;
}

// asInt reads a signed integer (timestamp / versionIncrement may be negative,
// e.g. NO_TIMESTAMP = -1).
function asInt(v: CborValue): number {
  if (typeof v === "bigint") {
    throw new Error(`codec: integer ${v} out of safe range`);
  }
  if (typeof v !== "number" || !Number.isInteger(v)) {
    throw new Error(`codec: expected integer, got ${String(v)}`);
  }
  return v;
}

function asBytes(v: CborValue): Uint8Array {
  if (!(v instanceof Uint8Array)) throw new Error("codec: expected byte string");
  return v;
}

// --- leaf wire types: attributes / updates / annotations ---

function wireAttrs(a: Attributes): CborValue {
  return a.all().map((at) => [at.name, at.value] as CborValue);
}

function attrsFrom(v: CborValue): Attributes {
  const raw = asArray(v);
  if (raw.length === 0) return Attributes.empty();
  const pairs: Attribute[] = raw.map((r) => {
    const w = asArray(r);
    need(w, 2);
    return { name: asString(w[0]!), value: asString(w[1]!) };
  });
  return Attributes.fromPairs(pairs);
}

function wireUpdate(u: AttributesUpdate): CborValue {
  return u.all().map((c) => [c.name, c.oldValue, c.newValue] as CborValue);
}

function updateFrom(v: CborValue): AttributesUpdate {
  const raw = asArray(v);
  const changes: AttributeChange[] = raw.map((r) => {
    const w = asArray(r);
    need(w, 3);
    return { name: asString(w[0]!), oldValue: asNullableString(w[1]!), newValue: asNullableString(w[2]!) };
  });
  return AttributesUpdate.of(changes);
}

function wireChanges(m: AnnotationBoundaryMap): CborValue {
  return m.changes.map((c) => [c.key, c.oldValue, c.newValue] as CborValue);
}

function boundaryFrom(endsV: CborValue, changesV: CborValue): AnnotationBoundaryMap {
  const ends = asArray(endsV).map((e) => asString(e));
  const changes: AnnotationChange[] = asArray(changesV).map((r) => {
    const w = asArray(r);
    need(w, 3);
    return { key: asString(w[0]!), oldValue: asNullableString(w[1]!), newValue: asNullableString(w[2]!) };
  });
  return AnnotationBoundaryMap.of(ends, changes);
}

// --- hashed version (wireHV = [version, hash]) ---

function wireHV(hv: HashedVersion): CborValue {
  return [hv.version, hv.historyHash];
}

function hvFrom(v: CborValue): HashedVersion {
  const raw = asArray(v);
  need(raw, 2);
  return new HashedVersion(asUint(raw[0]!), asBytes(raw[1]!));
}

// --- context (wireContext = [creator, timestamp, versionIncrement, hv|null]) ---

function wireCtx(c: Context): CborValue {
  return [c.creator, c.timestamp, c.versionIncrement, c.hashedVersion === null ? null : wireHV(c.hashedVersion)];
}

function ctxFrom(v: CborValue): Context {
  const raw = asArray(v);
  need(raw, 4);
  const creator = participant(asString(raw[0]!));
  const timestamp = asInt(raw[1]!);
  const versionIncrement = asInt(raw[2]!);
  const hashedVersion = raw[3] === null ? null : hvFrom(raw[3]!);
  return { creator, timestamp, versionIncrement, hashedVersion };
}

// --- component encode/decode ---

function componentValue(c: Component): CborValue {
  switch (c.kind) {
    case "retain":
      return [cRetain, c.count];
    case "characters":
      return [cCharacters, c.text];
    case "elementStart":
      return [cElementStart, c.type, wireAttrs(c.attributes)];
    case "elementEnd":
      return [cElementEnd];
    case "deleteCharacters":
      return [cDeleteCharacters, c.text];
    case "deleteElementStart":
      return [cDeleteElementStart, c.type, wireAttrs(c.attributes)];
    case "deleteElementEnd":
      return [cDeleteElementEnd];
    case "replaceAttributes":
      return [cReplaceAttributes, wireAttrs(c.oldAttributes), wireAttrs(c.newAttributes)];
    case "updateAttributes":
      return [cUpdateAttributes, wireUpdate(c.update)];
    case "annotationBoundary":
      return [cAnnotationBoundary, c.boundary.endKeys.slice(), wireChanges(c.boundary)];
  }
}

function componentFrom(raw: CborValue[]): Component {
  if (raw.length === 0) throw new Error("codec: empty component");
  const kind = asUint(raw[0]!);
  switch (kind) {
    case cRetain: {
      need(raw, 2);
      // Decode via signed int then range-check: a negative count is malformed,
      // not a valid retain.
      const n = asInt(raw[1]!);
      if (n < 0) throw new Error(`codec: retain count ${n} out of range`);
      return { kind: "retain", count: n };
    }
    case cCharacters:
      need(raw, 2);
      return { kind: "characters", text: asString(raw[1]!) };
    case cElementStart: {
      const { type, attributes } = decodeTypedElement(raw);
      return { kind: "elementStart", type, attributes };
    }
    case cElementEnd:
      return { kind: "elementEnd" };
    case cDeleteCharacters:
      need(raw, 2);
      return { kind: "deleteCharacters", text: asString(raw[1]!) };
    case cDeleteElementStart: {
      const { type, attributes } = decodeTypedElement(raw);
      return { kind: "deleteElementStart", type, attributes };
    }
    case cDeleteElementEnd:
      return { kind: "deleteElementEnd" };
    case cReplaceAttributes: {
      need(raw, 3);
      const oldAttributes = attrsFrom(raw[1]!);
      const newAttributes = attrsFrom(raw[2]!);
      return { kind: "replaceAttributes", oldAttributes, newAttributes };
    }
    case cUpdateAttributes: {
      need(raw, 2);
      return { kind: "updateAttributes", update: updateFrom(raw[1]!) };
    }
    case cAnnotationBoundary: {
      need(raw, 3);
      return { kind: "annotationBoundary", boundary: boundaryFrom(raw[1]!, raw[2]!) };
    }
    default:
      throw new Error(`codec: unknown component kind ${kind}`);
  }
}

function decodeTypedElement(raw: CborValue[]): { type: string; attributes: Attributes } {
  need(raw, 3);
  return { type: asString(raw[1]!), attributes: attrsFrom(raw[2]!) };
}

function docOpValue(d: DocOp): CborValue {
  return d.components.map((c) => componentValue(c));
}

function docOpFrom(raw: CborValue[]): DocOp {
  const comps: Component[] = raw.map((r) => componentFrom(asArray(r)));
  return new DocOp(comps);
}

/** EncodeDocOp returns the CBOR encoding of a DocOp. */
export function encodeDocOp(d: DocOp): Uint8Array {
  return encode(docOpValue(d));
}

/** DecodeDocOp parses a CBOR DocOp encoding. */
export function decodeDocOp(b: Uint8Array): DocOp {
  return docOpFrom(asArray(decode(b)));
}

// --- wavelet operation encode/decode ---

function operationValue(o: Operation): CborValue {
  switch (o.kind) {
    case "blip":
      return [oWaveletBlip, o.blipId, wireCtx(o.op.ctx), docOpValue(o.op.contentOp), o.op.method];
    case "addParticipant":
      return [oAddParticipant, wireCtx(o.ctx), o.participant];
    case "removeParticipant":
      return [oRemoveParticipant, wireCtx(o.ctx), o.participant];
    case "noOp":
      return [oNoOp, wireCtx(o.ctx)];
  }
}

function operationFrom(raw: CborValue[]): Operation {
  if (raw.length === 0) throw new Error("codec: empty operation");
  const kind = asUint(raw[0]!);
  switch (kind) {
    case oWaveletBlip: {
      need(raw, 5);
      const blipId = asString(raw[1]!);
      const ctx = ctxFrom(raw[2]!);
      const contentOp = docOpFrom(asArray(raw[3]!));
      const method = asUint(raw[4]!);
      if (method !== 0 && method !== 1 && method !== 2) {
        throw new Error(`codec: unknown contributor method ${method}`);
      }
      return { kind: "blip", blipId, op: { ctx, contentOp, method } };
    }
    case oAddParticipant: {
      const { ctx, addr } = decodeCtxParticipant(raw);
      return { kind: "addParticipant", ctx, participant: addr };
    }
    case oRemoveParticipant: {
      const { ctx, addr } = decodeCtxParticipant(raw);
      return { kind: "removeParticipant", ctx, participant: addr };
    }
    case oNoOp:
      need(raw, 2);
      return { kind: "noOp", ctx: ctxFrom(raw[1]!) };
    default:
      throw new Error(`codec: unknown operation kind ${kind}`);
  }
}

function decodeCtxParticipant(raw: CborValue[]): { ctx: Context; addr: Participant } {
  need(raw, 3);
  const ctx = ctxFrom(raw[1]!);
  const addr = participant(asString(raw[2]!));
  return { ctx, addr };
}

function opsValue(ops: Operation[]): CborValue {
  return ops.map((o) => operationValue(o));
}

function opsFrom(v: CborValue): Operation[] {
  return asArray(v).map((r) => operationFrom(asArray(r)));
}

// --- client delta (wire.go) ---

/** EncodeClientDelta returns the CBOR encoding [author, targetVersion, ops, nonce]. */
export function encodeClientDelta(d: ClientDelta): Uint8Array {
  return encode([d.author, wireHV(d.targetVersion), opsValue(d.ops), d.nonce]);
}

/** DecodeClientDelta parses a client delta encoding. */
export function decodeClientDelta(b: Uint8Array): ClientDelta {
  const raw = asArray(decode(b));
  if (raw.length !== 4) {
    throw new Error(`codec: client delta has ${raw.length} fields, want 4`);
  }
  const author = participant(asString(raw[0]!));
  const targetVersion = hvFrom(raw[1]!);
  const ops = opsFrom(raw[2]!);
  const nonce = asString(raw[3]!);
  return { author, targetVersion, ops, nonce };
}

// --- stored delta (codec.go) ---

/** EncodeStoredDelta returns the CBOR encoding [author, resultingVersion, timestamp, ops, nonce]. */
export function encodeStoredDelta(d: StoredDelta): Uint8Array {
  return encode([d.author, wireHV(d.resultingVersion), d.timestamp, opsValue(d.ops), d.nonce]);
}

/** DecodeStoredDelta parses a stored delta encoding. */
export function decodeStoredDelta(b: Uint8Array): StoredDelta {
  const raw = asArray(decode(b));
  if (raw.length !== 5) {
    throw new Error(`codec: stored delta has ${raw.length} fields, want 5`);
  }
  const author = participant(asString(raw[0]!));
  const resultingVersion = hvFrom(raw[1]!);
  const timestamp = asInt(raw[2]!);
  const ops = opsFrom(raw[3]!);
  const nonce = asString(raw[4]!);
  return { author, resultingVersion, timestamp, ops, nonce };
}

// --- hashed version (wire.go) ---

/** EncodeHashedVersion returns the CBOR encoding [version, hash]. */
export function encodeHashedVersion(hv: HashedVersion): Uint8Array {
  return encode(wireHV(hv));
}

/** DecodeHashedVersion parses a hashed version encoding. */
export function decodeHashedVersion(b: Uint8Array): HashedVersion {
  return hvFrom(decode(b));
}
