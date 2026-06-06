// Transport message envelope + framing. The client-facing wire on top of the
// wavelet-serving core. Each message is a CBOR array [kind, fields...]; this
// envelope is the evolvable wire layer — not the frozen hash-feeding encoding
// (codec). Delta payloads inside the envelope use the frozen canonical encoding.
//
// Framing is a 4-byte big-endian length prefix followed by the CBOR envelope
// bytes. The Go server rides one envelope per WebSocket binary message (one
// write = one message = [len][payload]).
//
// Ported from internal/transport/message.go and internal/transport/frame.go.

import { decode, encode } from "./cbor.ts";
import type { CborValue } from "./cbor.ts";

// Message kinds. Each message is a CBOR array [kind, fields...].
export const Kind = {
  open: 0, // client→server: bind the connection to a wavelet
  openResponse: 1, // server→client: starting view (state snapshot or delta history)
  submit: 2, // client→server: a client delta to apply
  submitResponse: 3, // server→client: ack/nack for a submit
  update: 4, // server→client: a newly applied delta (live stream)
  error: 5, // server→client: a protocol/processing error
  resync: 6, // client→server: reconnect/resync at a known version
  resyncResponse: 7, // server→client: incremental tail, or a full-view reset
  resyncRequired: 8, // server→client: the live stream gapped; client must Resync
} as const;

// Resync response modes.
export const resyncTail = 0; // tail holds the stored deltas after the client's known version
export const resyncReset = 1; // known point unusable (fork/pruned): full view follows; discard local state

// --- framing ---

// maxFrameSize bounds a single frame's payload, guarding against a corrupt or
// hostile length prefix triggering an unbounded allocation.
const maxFrameSize = 64 << 20; // 64 MiB

// frameHeaderSize is the big-endian length prefix preceding each frame payload.
const frameHeaderSize = 4;

/** frame prepends a 4-byte big-endian length prefix to the envelope. */
export function frame(envelope: Uint8Array): Uint8Array {
  if (envelope.length > maxFrameSize) {
    throw new Error(`transport: frame too large to send: ${envelope.length} bytes`);
  }
  const buf = new Uint8Array(frameHeaderSize + envelope.length);
  new DataView(buf.buffer).setUint32(0, envelope.length, false); // big-endian
  buf.set(envelope, frameHeaderSize);
  return buf;
}

/** unframe strips and verifies the 4-byte big-endian length prefix, returning the payload. */
export function unframe(message: Uint8Array): Uint8Array {
  if (message.length < frameHeaderSize) {
    throw new Error(`transport: truncated frame: ${message.length} bytes`);
  }
  const n = new DataView(message.buffer, message.byteOffset, message.byteLength).getUint32(0, false);
  if (n > maxFrameSize) {
    throw new Error(`transport: frame too large to read: ${n} bytes`);
  }
  if (message.length - frameHeaderSize !== n) {
    throw new Error(
      `transport: frame length mismatch: header ${n}, payload ${message.length - frameHeaderSize}`,
    );
  }
  return message.subarray(frameHeaderSize).slice();
}

// --- envelope encode/decode helpers ---

function marshal(v: CborValue[]): Uint8Array {
  return encode(v);
}

// decodeEnvelope decodes the envelope to a CBOR array. An empty array is an error.
function decodeEnvelope(envelope: Uint8Array): CborValue[] {
  const raw = decode(envelope);
  if (!Array.isArray(raw)) {
    throw new Error("transport: message is not an array");
  }
  if (raw.length === 0) {
    throw new Error("transport: empty message");
  }
  return raw;
}

// need bounds-checks a decoded envelope before indexing its fields.
function need(raw: CborValue[], n: number): void {
  if (raw.length < n) {
    throw new Error(`transport: truncated message: have ${raw.length} fields, need ${n}`);
  }
}

function asNumber(v: CborValue, field: string): number {
  if (typeof v === "number") return v;
  if (typeof v === "bigint") return Number(v);
  throw new Error(`transport: ${field}: expected integer, got ${typeof v}`);
}

function asString(v: CborValue, field: string): string {
  if (typeof v !== "string") throw new Error(`transport: ${field}: expected string, got ${typeof v}`);
  return v;
}

function asBool(v: CborValue, field: string): boolean {
  if (typeof v !== "boolean") throw new Error(`transport: ${field}: expected bool, got ${typeof v}`);
  return v;
}

function asBytes(v: CborValue, field: string): Uint8Array {
  if (!(v instanceof Uint8Array)) {
    throw new Error(`transport: ${field}: expected byte string, got ${typeof v}`);
  }
  return v;
}

function asBytesArray(v: CborValue, field: string): Uint8Array[] {
  if (!Array.isArray(v)) throw new Error(`transport: ${field}: expected array, got ${typeof v}`);
  return v.map((item, i) => asBytes(item, `${field}[${i}]`));
}

// messageKind decodes the envelope and returns the message kind (raw[0]).
export function messageKind(envelope: Uint8Array): number {
  const raw = decodeEnvelope(envelope);
  return asNumber(raw[0]!, "kind");
}

// --- open: [mOpen, waveletName, suppressEcho] ---
// suppressEcho: when true the server omits this connection's own applied deltas
// from its update stream. Optimistic clients set it; the pessimistic replica
// client leaves it false and advances by applying its own echoed delta.
export function encodeOpen(waveletName: string, suppressEcho: boolean): Uint8Array {
  return marshal([Kind.open, waveletName, suppressEcho]);
}

// --- open response: [mOpenResponse, snapshotBlob, [storedDeltaBytes...]] ---
// snapshotBlob is empty for a history-based join; non-empty for a snapshot-based
// join (history is then empty).
export function decodeOpenResponse(envelope: Uint8Array): {
  snapshotBlob: Uint8Array;
  history: Uint8Array[];
} {
  const raw = decodeEnvelope(envelope);
  need(raw, 3);
  return {
    snapshotBlob: asBytes(raw[1]!, "snapshotBlob"),
    history: asBytesArray(raw[2]!, "history"),
  };
}

// --- submit: [mSubmit, clientDeltaBytes] ---
export function encodeSubmit(clientDeltaBytes: Uint8Array): Uint8Array {
  return marshal([Kind.submit, clientDeltaBytes]);
}

// --- submit response: [mSubmitResponse, ok, code, msg, resultingVersionBytes, opsApplied] ---
// opsApplied is the number of operations the server actually applied (the
// authoritative version span of the delta): equal to the submitted op count
// normally, but zero for a deduped resend or a fully transformed-away delta.
export function decodeSubmitResponse(envelope: Uint8Array): {
  ok: boolean;
  code: number;
  msg: string;
  resultingVersion: Uint8Array;
  opsApplied: number;
} {
  const raw = decodeEnvelope(envelope);
  need(raw, 6);
  return {
    ok: asBool(raw[1]!, "ok"),
    code: asNumber(raw[2]!, "code"),
    msg: asString(raw[3]!, "msg"),
    resultingVersion: asBytes(raw[4]!, "resultingVersion"),
    opsApplied: asNumber(raw[5]!, "opsApplied"),
  };
}

// --- update: [mUpdate, storedDeltaBytes] ---
export function decodeUpdate(envelope: Uint8Array): Uint8Array {
  const raw = decodeEnvelope(envelope);
  need(raw, 2);
  return asBytes(raw[1]!, "storedDeltaBytes");
}

// --- error: [mError, msg] ---
export function decodeError(envelope: Uint8Array): string {
  const raw = decodeEnvelope(envelope);
  need(raw, 2);
  return asString(raw[1]!, "msg");
}

// --- resync: [mResync, waveletName, knownVersion, knownHash, suppressEcho] ---
// A reconnecting client states the (version, history-hash) it already holds. The
// continuation stream is always self-suppressed (only the optimistic client
// resyncs), so suppressEcho is sent true.
export function encodeResync(
  waveletName: string,
  knownVersion: number,
  knownHash: Uint8Array,
): Uint8Array {
  return marshal([Kind.resync, waveletName, knownVersion, knownHash, true]);
}

// --- resync response: [mResyncResponse, mode, tail, snapshotBlob, history] ---
// mode resyncTail: tail holds the stored deltas after knownVersion; snapshotBlob
// and history are empty. mode resyncReset: the full view is in snapshotBlob (or
// history), exactly as an open response, and tail is empty.
export function decodeResyncResponse(envelope: Uint8Array): {
  mode: number;
  tail: Uint8Array[];
  snapshotBlob: Uint8Array;
  history: Uint8Array[];
} {
  const raw = decodeEnvelope(envelope);
  need(raw, 5);
  return {
    mode: asNumber(raw[1]!, "mode"),
    tail: asBytesArray(raw[2]!, "tail"),
    snapshotBlob: asBytes(raw[3]!, "snapshotBlob"),
    history: asBytesArray(raw[4]!, "history"),
  };
}
