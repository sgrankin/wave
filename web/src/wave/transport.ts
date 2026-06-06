// The reconnecting optimistic wavelet client over the browser WebSocket API.
//
// OptimisticClient is the collaborative wavelet client: it applies local edits
// immediately to an optimistic replica (via the clientcc state machine) and
// transforms concurrent server deltas, instead of waiting for the server to echo
// its own edits. It opens with self-suppression, so its update stream carries
// only other participants' deltas and it learns its own outcomes from submit
// acks.
//
// A supervisor (an async loop, the analogue of the Go supervisor goroutine) owns
// the connection lifecycle: it dials (opens a WebSocket), runs a session (the
// first sends Open, reconnections send Resync), and on a recoverable failure
// (dropped connection, dropped live stream, or a TooOld/VersionError nack)
// redials and resyncs — preserving the optimistic edits the clientcc core holds
// across the gap (recognizing its own committed delta in the resync tail by
// nonce, or re-submitting an uncommitted one). A fatal failure (illegal op,
// protocol error) stops the supervisor. The clientcc core owns OT bookkeeping;
// this adapter owns connections, the supervisor loop, and serialization.
//
// Ported from internal/transport/optimistic.go (the supervisor + session) and
// internal/transport/server.go (the protocol it speaks against).
//
// Concurrency model note: Go uses a mutex + cond + goroutines; JS is
// single-threaded with an event loop, so there is no data-race surface and no
// lock. We keep the same control flow and the same edge cases. The Go
// "out channel / outDone" write pump becomes a direct ws.send (the browser
// WebSocket buffers writes itself); the "notify channel" becomes onChange
// callbacks; the cond.Wait waiters become Promises woken by a broadcast.

import { HashedVersion } from "./types.ts";
import type { DocOp, Operation, Participant, WaveletName } from "./types.ts";

import { CC } from "./clientcc.ts";
import type { Outgoing } from "./clientcc.ts";

import {
  decodeError,
  decodeOpenResponse,
  decodeResyncResponse,
  decodeSubmitResponse,
  decodeUpdate,
  encodeOpen,
  encodeResync,
  encodeSubmit,
  frame,
  Kind,
  messageKind,
  resyncReset,
  resyncTail,
  unframe,
} from "./wire.ts";

import { decodeHashedVersion, decodeStoredDelta, encodeClientDelta } from "./codec.ts";

// --- response codes (cc.ResponseCode; internal/cc/delta.go) ---
// Mirror of the server's submit-response codes. Only the recoverable ones matter
// to the client; the rest are folded into "fatal".
const codeOK = 0;
const codeVersionError = 4;
const codeTooOld = 10;

// isRecoverableNack reports whether a nack response code should trigger a resync
// rather than fail the client. VersionError/TooOld mean the client's target was
// stale or pruned — recover by reconnecting and resyncing.
function isRecoverableNack(code: number): boolean {
  return code === codeVersionError || code === codeTooOld;
}

// reconnectDelay is the pause before redialing after a recoverable failure.
const reconnectDelay = 100; // milliseconds

// FatalError marks a non-recoverable session error: the supervisor stops rather
// than reconnecting. (The Go fatalError sentinel.)
class FatalError extends Error {
  override readonly name = "FatalError";
  constructor(message: string) {
    super(message);
  }
}

// SessionResult classifies how a session ended (the Go runSession return):
//  - "clean": the client closed; stop.
//  - "recoverable": reconnect after a delay.
//  - "fatal": a non-recoverable condition; stop and fail waiters.
type SessionResult =
  | { kind: "clean" }
  | { kind: "recoverable"; err: Error }
  | { kind: "fatal"; err: Error };

// newSessionID returns a random per-session token used to scope submission
// nonces (so a client recognizes only its own deltas).
function newSessionID(): string {
  const b = new Uint8Array(8);
  crypto.getRandomValues(b);
  let s = "";
  for (const x of b) s += x.toString(16).padStart(2, "0");
  return s;
}

/** Options for the OptimisticClient constructor. */
export interface OptimisticClientOptions {
  // Extra headers to attach to the WebSocket upgrade request. Browsers cannot set
  // arbitrary headers on a WebSocket; this is honored only where the runtime's
  // WebSocket constructor accepts an options object (Node's undici/ws). In the
  // browser, carry identity in the URL (query param) or a subprotocol instead —
  // see the constructor docs.
  headers?: Record<string, string>;
}

export class OptimisticClient {
  private readonly url: string;
  private readonly name: WaveletName;
  private readonly author: Participant;
  private readonly sessionID: string;
  private readonly headers: Record<string, string> | undefined;

  // The pure CC core. Replaced on a resync reset.
  private cc: CC;

  // The current session's live WebSocket, or null while disconnected. Sends drop
  // when null (the core retains the delta; the supervisor re-derives it on
  // reconnect via afterResync/trySend), exactly like the Go nil out channel.
  private ws: WebSocket | null = null;

  private opened = false; // first open completed
  private openErr: Error | null = null;
  private fatal: Error | null = null; // non-recoverable failure; stops the supervisor and fails waiters
  private closed = false;

  // Waiters woken on any state change (open / version advance / fatal / close):
  // the JS analogue of sync.Cond.Broadcast. Each is a resolve callback.
  private waiters: Array<() => void> = [];

  // Replica-changed listeners (the Go coalesced notify channel).
  private changeListeners: Array<() => void> = [];

  /**
   * Create an optimistic client for the given wavelet, authoring as author.
   *
   * url is the WebSocket endpoint (ws:// or wss://). The supervisor opens a fresh
   * WebSocket on the initial connect and on every reconnect.
   *
   * Identity: the Go WebSocketHandler resolves the authenticated participant from
   * the HTTP request (a session cookie verified by middleware) and binds it to
   * the session — submitted deltas must be authored by it. Browsers cannot set
   * arbitrary headers on a WebSocket handshake, so for browser use authenticate
   * out-of-band (the cookie rides the handshake automatically) or carry identity
   * in the URL. For the Node integration test (no cookie middleware) prefer a
   * query param on `url` (e.g. `?as=<author>`); opts.headers is passed through to
   * the WebSocket constructor only where the runtime supports it. We choose the
   * query-param/cookie route over a subprotocol because it is the least magical
   * and works uniformly in the browser and Node.
   */
  constructor(url: string, name: WaveletName, author: Participant, opts?: OptimisticClientOptions) {
    this.url = url;
    this.name = name;
    this.author = author;
    this.sessionID = newSessionID();
    this.headers = opts?.headers;
    this.cc = new CC(name, author, name.zeroVersion(), this.sessionID);
  }

  // --- public API ---

  /**
   * Start the supervisor and resolve when the initial open completes (or reject
   * on a fatal error / close before open). Idempotent-ish: call once.
   */
  open(): Promise<void> {
    // Kick off the supervisor loop; it runs until close()/fatal.
    void this.supervise();
    return new Promise<void>((resolve, reject) => {
      const check = (): boolean => {
        if (this.fatal !== null) {
          reject(this.fatal);
          return true;
        }
        if (this.opened) {
          if (this.openErr !== null) reject(this.openErr);
          else resolve();
          return true;
        }
        if (this.closed) {
          reject(new Error("transport: client closed"));
          return true;
        }
        return false;
      };
      if (check()) return;
      this.waitFor(check);
    });
  }

  /**
   * Build and submit a local edit from a consistent snapshot of the optimistic
   * replica: build is called once with a reader for the replica's blips. The ops
   * apply optimistically immediately; a delta is sent if the in-flight slot is
   * free and a connection is up (otherwise the core queues it and the supervisor
   * sends/resubmits it after the next (re)connect).
   */
  async submitWith(
    build: (blip: (blipId: string) => DocOp | undefined) => Operation[],
  ): Promise<void> {
    if (this.fatal !== null) throw this.fatal;
    const ops = build((id) => this.cc.blip(id));
    let o: Outgoing | null;
    try {
      o = this.cc.edit(ops);
    } catch (e) {
      this.signal();
      throw new Error(`transport: optimistic submit: ${errMsg(e)}`);
    }
    this.signal();
    this.sendDelta(o);
  }

  /** Apply and submit ops as a single edit. */
  submit(ops: Operation[]): Promise<void> {
    return this.submitWith(() => ops);
  }

  /** Return the optimistic content of a blip, or undefined if it does not exist. */
  blipContent(blipId: string): DocOp | undefined {
    return this.cc.blip(blipId);
  }

  /** Return the ids of all blips in the optimistic replica. */
  blipIds(): string[] {
    return this.cc.blipIds();
  }

  /** Return the latest confirmed server version. */
  version(): HashedVersion {
    return this.cc.serverVersion();
  }

  /**
   * Resolve when the confirmed server version reaches v (or reject if the client
   * fails/closes first).
   */
  waitServerVersion(v: number): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      const check = (): boolean => {
        if (this.fatal !== null) {
          reject(this.fatal);
          return true;
        }
        if (this.closed) {
          reject(new Error("transport: client closed"));
          return true;
        }
        if (this.cc.serverVersion().version >= v) {
          resolve();
          return true;
        }
        return false;
      };
      if (check()) return;
      this.waitFor(check);
    });
  }

  /** Register a replica-changed listener (the Go coalesced notify channel). */
  onChange(cb: () => void): void {
    this.changeListeners.push(cb);
  }

  /** End the session and unblock any waiters. */
  close(): void {
    if (this.closed) return;
    this.closed = true;
    if (this.ws !== null) {
      const ws = this.ws;
      this.ws = null;
      try {
        ws.close();
      } catch {
        // ignore
      }
    }
    this.broadcast();
  }

  // --- supervisor ---

  // supervise dials, runs a session, and reconnects on recoverable failures
  // until the client closes or hits a fatal error. (Port of optimistic.supervise.)
  private async supervise(): Promise<void> {
    let first = true;
    for (;;) {
      if (this.closed) return;
      let ws: WebSocket;
      try {
        ws = await this.dial();
      } catch (e) {
        // Dial failed; retry after a delay (recoverable). The Go path logs and
        // sleeps, then continues the loop.
        if (!(await this.sleep(reconnectDelay))) return;
        continue;
      }
      if (this.closed) {
        try {
          ws.close();
        } catch {
          // ignore
        }
        return;
      }
      const res = await this.runSession(ws, first);
      first = false;
      if (this.closed) return;
      switch (res.kind) {
        case "clean":
          return;
        case "fatal":
          this.setFatal(res.err);
          return;
        case "recoverable":
          if (!(await this.sleep(reconnectDelay))) return;
          continue;
      }
    }
  }

  // dial opens a fresh WebSocket and resolves once it is open, or rejects on a
  // connection error. binaryType is arraybuffer so message data arrives as
  // ArrayBuffer (not Blob), matching the framed-binary protocol.
  private dial(): Promise<WebSocket> {
    return new Promise<WebSocket>((resolve, reject) => {
      let ws: WebSocket;
      try {
        // Node's WebSocket (undici) accepts an options object as the second arg
        // carrying headers; the browser's two-arg form is (url, protocols), so we
        // only pass the second arg when headers are set (Node integration path).
        if (this.headers !== undefined) {
          // The two runtimes disagree on the 2nd arg's type; cast through unknown.
          const Ctor = WebSocket as unknown as new (u: string, o: unknown) => WebSocket;
          ws = new Ctor(this.url, { headers: this.headers });
        } else {
          ws = new WebSocket(this.url);
        }
      } catch (e) {
        reject(new Error(`transport: websocket dial: ${errMsg(e)}`));
        return;
      }
      ws.binaryType = "arraybuffer";
      const onOpen = (): void => {
        ws.removeEventListener("open", onOpen);
        ws.removeEventListener("error", onError);
        resolve(ws);
      };
      const onError = (): void => {
        ws.removeEventListener("open", onOpen);
        ws.removeEventListener("error", onError);
        reject(new Error("transport: websocket dial failed"));
      };
      ws.addEventListener("open", onOpen);
      ws.addEventListener("error", onError);
    });
  }

  // runSession drives one connection to completion. It sends Open (first) or
  // Resync (reconnect), reads+handles frames until the connection fails or the
  // client closes, and returns how the session ended. (Port of runSession +
  // readSession.)
  private runSession(ws: WebSocket, first: boolean): Promise<SessionResult> {
    this.ws = ws;
    return new Promise<SessionResult>((resolve) => {
      let settled = false;
      const finish = (res: SessionResult): void => {
        if (settled) return;
        settled = true;
        ws.removeEventListener("message", onMessage);
        ws.removeEventListener("close", onClose);
        ws.removeEventListener("error", onErrorEvt);
        if (this.ws === ws) this.ws = null;
        try {
          ws.close();
        } catch {
          // ignore
        }
        resolve(res);
      };

      const onMessage = (ev: MessageEvent): void => {
        let data: Uint8Array;
        try {
          data = toBytes(ev.data);
        } catch (e) {
          finish({ kind: "fatal", err: new FatalError(`transport: bad message data: ${errMsg(e)}`) });
          return;
        }
        const r = this.handle(data);
        if (r !== null) finish(r);
      };
      const onClose = (): void => {
        // A dropped socket is recoverable: reconnect and resync. If the client is
        // closing, treat it as a clean end.
        if (this.closed) finish({ kind: "clean" });
        else finish({ kind: "recoverable", err: new Error("transport: connection closed") });
      };
      const onErrorEvt = (): void => {
        if (this.closed) finish({ kind: "clean" });
        else finish({ kind: "recoverable", err: new Error("transport: connection error") });
      };

      ws.addEventListener("message", onMessage);
      ws.addEventListener("close", onClose);
      ws.addEventListener("error", onErrorEvt);

      // Initiate the session: open or resync.
      try {
        if (first) {
          this.sendFrame(ws, encodeOpen(this.name.serialize(), true));
        } else {
          const v = this.cc.serverVersion();
          this.sendFrame(ws, encodeResync(this.name.serialize(), v.version, v.historyHash));
        }
      } catch (e) {
        // A send failure here means the socket is already dead; reconnect.
        finish({ kind: "recoverable", err: new Error(`transport: send failed: ${errMsg(e)}`) });
      }
    });
  }

  // handle dispatches one inbound envelope. Returns a SessionResult to end the
  // session, or null to keep reading. (Port of optimistic.handle + the *apply*
  // methods; here they are inlined since there is no lock to manage.)
  private handle(data: Uint8Array): SessionResult | null {
    let envelope: Uint8Array;
    let kind: number;
    try {
      envelope = unframe(data);
      kind = messageKind(envelope);
    } catch (e) {
      return { kind: "fatal", err: new FatalError(`transport: ${errMsg(e)}`) };
    }
    try {
      switch (kind) {
        case Kind.openResponse: {
          const { snapshotBlob, history } = decodeOpenResponse(envelope);
          return this.applyOpen(snapshotBlob, history);
        }
        case Kind.resyncResponse: {
          const { mode, tail, snapshotBlob, history } = decodeResyncResponse(envelope);
          return this.applyResync(mode, tail, snapshotBlob, history);
        }
        case Kind.update: {
          const db = decodeUpdate(envelope);
          return this.applyServerDelta(db);
        }
        case Kind.submitResponse: {
          const r = decodeSubmitResponse(envelope);
          return this.applyAck(r);
        }
        case Kind.resyncRequired:
          // The live stream was dropped; reconnect and resync (recoverable).
          return { kind: "recoverable", err: new Error("transport: live stream dropped; resyncing") };
        case Kind.error: {
          let msg = "";
          try {
            msg = decodeError(envelope);
          } catch {
            // ignore decode failure on the error message itself
          }
          return { kind: "fatal", err: new FatalError(`transport: server error: ${msg}`) };
        }
        default:
          return { kind: "fatal", err: new FatalError(`transport: unexpected message kind ${kind}`) };
      }
    } catch (e) {
      // A decode/protocol error inside a handler is fatal (matches the Go
      // fatalError{} wrapping of decode errors and core errors).
      if (e instanceof FatalError) return { kind: "fatal", err: e };
      return { kind: "fatal", err: new FatalError(`transport: ${errMsg(e)}`) };
    }
  }

  private applyOpen(snapshotBlob: Uint8Array, history: Uint8Array[]): SessionResult | null {
    if (this.opened) {
      return { kind: "fatal", err: new FatalError("transport: duplicate open response") };
    }
    try {
      this.initFromView(snapshotBlob, history);
    } catch (e) {
      // A bad initial view is recorded as openErr (Open() rejects with it) but is
      // not a session-fatal: the Go path sets openErr and still marks opened.
      this.openErr = e instanceof Error ? e : new Error(errMsg(e));
    }
    this.opened = true;
    this.broadcast();
    this.signal();
    return null;
  }

  // applyResync reconciles a reconnection: a tail mode feeds the missed deltas
  // (the core recognizes its own committed delta in them by nonce) then
  // re-submits any still-unacked in-flight delta; a reset mode rebuilds the core
  // from the full view, discarding unacknowledged local edits.
  private applyResync(
    mode: number,
    tail: Uint8Array[],
    snapshotBlob: Uint8Array,
    history: Uint8Array[],
  ): SessionResult | null {
    if (mode === resyncReset) {
      this.cc = new CC(this.name, this.author, this.name.zeroVersion(), this.sessionID);
      try {
        this.initFromView(snapshotBlob, history);
      } catch (e) {
        return { kind: "fatal", err: toFatal(e) };
      }
    } else if (mode === resyncTail) {
      for (const db of tail) {
        let sd: ReturnType<typeof decodeStoredDelta>;
        try {
          sd = decodeStoredDelta(db);
        } catch (e) {
          return { kind: "fatal", err: toFatal(e) };
        }
        try {
          this.cc.onServerDelta(sd.ops, sd.resultingVersion, sd.nonce);
        } catch (e) {
          return { kind: "fatal", err: toFatal(e) };
        }
      }
      try {
        this.sendDelta(this.cc.afterResync());
      } catch (e) {
        return { kind: "fatal", err: toFatal(e) };
      }
    } else {
      return { kind: "fatal", err: new FatalError(`transport: unknown resync mode ${mode}`) };
    }
    // A resync response means we are synced/open; mark opened so open() unblocks
    // even in the (server-buggy) case it arrives before any open response.
    this.opened = true;
    this.broadcast();
    this.signal();
    return null;
  }

  // initFromView seeds the core from a starting view: a current-state snapshot,
  // or a replayed delta history from version zero. (Port of initLocked.)
  private initFromView(snapshotBlob: Uint8Array, history: Uint8Array[]): void {
    if (snapshotBlob.length > 0) {
      // TODO: decode the snapshot blob (internal/snapshot.Decode +
      // wavelet.FromState) and seed the core via cc.loadSnapshot. Until the
      // snapshot codec is ported, run the server history-only (no snapshot
      // compaction) so the open response carries the full delta log instead.
      throw new Error("transport: snapshot open not yet supported in TS client");
    }
    for (const db of history) {
      const sd = decodeStoredDelta(db);
      this.cc.onServerDelta(sd.ops, sd.resultingVersion, sd.nonce);
    }
  }

  private applyServerDelta(db: Uint8Array): SessionResult | null {
    let sd: ReturnType<typeof decodeStoredDelta>;
    try {
      sd = decodeStoredDelta(db);
    } catch (e) {
      return { kind: "fatal", err: toFatal(e) };
    }
    let o: Outgoing | null;
    try {
      o = this.cc.onServerDelta(sd.ops, sd.resultingVersion, sd.nonce);
    } catch (e) {
      this.signal();
      return { kind: "fatal", err: toFatal(e) };
    }
    this.sendDelta(o);
    this.signal();
    return null;
  }

  private applyAck(r: {
    ok: boolean;
    code: number;
    msg: string;
    resultingVersion: Uint8Array;
    opsApplied: number;
  }): SessionResult | null {
    if (!r.ok) {
      if (isRecoverableNack(r.code)) {
        // Reconnect and resync; the in-flight delta is re-derived there.
        return {
          kind: "recoverable",
          err: new Error(`transport: submit nacked (code ${r.code}): ${r.msg}; resyncing`),
        };
      }
      return {
        kind: "fatal",
        err: new FatalError(`transport: submit nacked (code ${r.code}): ${r.msg}`),
      };
    }
    let rv: HashedVersion;
    try {
      rv = decodeHashedVersion(r.resultingVersion);
    } catch (e) {
      return {
        kind: "fatal",
        err: new FatalError(`transport: bad resulting version in ack: ${errMsg(e)}`),
      };
    }
    const o = this.cc.onAck(rv, r.opsApplied);
    this.sendDelta(o);
    this.signal();
    return null;
  }

  // --- sending ---

  // sendDelta encodes and sends a client delta produced by the core, if any,
  // tagging it with the core's submission nonce. A disconnected socket drops it:
  // the core retains the delta and the supervisor re-submits it on reconnect via
  // afterResync/trySend. (Port of sendDelta + enqueue.)
  private sendDelta(o: Outgoing | null): void {
    if (o === null) return;
    const db = encodeClientDelta({
      author: o.delta.author,
      targetVersion: o.delta.targetVersion,
      ops: o.delta.ops.slice(),
      nonce: o.nonce,
    });
    const ws = this.ws;
    if (ws === null) return; // disconnected: drop; re-derived on reconnect
    try {
      this.sendFrame(ws, encodeSubmit(db));
    } catch {
      // A failed send means the socket is dying; the close/error event will end
      // the session and trigger a reconnect+resync, which re-derives this delta.
    }
  }

  // sendFrame writes one framed envelope as a single WebSocket binary message
  // (one send = one frame = one server-side Write), matching the Go writeFrame
  // single-Write contract.
  private sendFrame(ws: WebSocket, envelope: Uint8Array): void {
    const f = frame(envelope);
    // ArrayBufferView is an accepted WebSocket.send argument; send the exact slice.
    ws.send(f);
  }

  // --- waiter / listener plumbing ---

  // waitFor registers a predicate that is re-checked on every broadcast; when it
  // returns true the waiter is removed. The JS analogue of cond.Wait in a loop.
  private waitFor(check: () => boolean): void {
    const w = (): void => {
      if (check()) {
        const i = this.waiters.indexOf(w);
        if (i >= 0) this.waiters.splice(i, 1);
      }
    };
    this.waiters.push(w);
  }

  // broadcast wakes every state waiter (open / version / fatal / close), the JS
  // analogue of sync.Cond.Broadcast. Waiters self-remove when satisfied.
  private broadcast(): void {
    // Copy: a waiter may remove itself (and others) during iteration.
    for (const w of this.waiters.slice()) w();
  }

  // signal fires the replica-changed listeners and wakes version/open waiters.
  // (Port of signalLocked: cond.Broadcast + the coalesced notify.)
  private signal(): void {
    this.broadcast();
    for (const cb of this.changeListeners.slice()) {
      try {
        cb();
      } catch {
        // a listener throwing must not break the protocol loop
      }
    }
  }

  private setFatal(err: Error): void {
    if (this.fatal === null) this.fatal = err;
    this.broadcast();
  }

  // sleep waits ms or until the client closes; resolves false if the client
  // closed during the wait. (Port of optimistic.sleep.)
  private sleep(ms: number): Promise<boolean> {
    return new Promise<boolean>((resolve) => {
      if (this.closed) {
        resolve(false);
        return;
      }
      let done = false;
      const timer = setTimeout(() => {
        if (done) return;
        done = true;
        const i = this.waiters.indexOf(waiter);
        if (i >= 0) this.waiters.splice(i, 1);
        resolve(true);
      }, ms);
      const waiter = (): void => {
        if (done || !this.closed) return;
        done = true;
        clearTimeout(timer);
        const i = this.waiters.indexOf(waiter);
        if (i >= 0) this.waiters.splice(i, 1);
        resolve(false);
      };
      this.waiters.push(waiter);
    });
  }
}

// --- helpers ---

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// toFatal wraps an arbitrary thrown value as a FatalError, preserving an already
// fatal one. Mirrors the Go fatalError{err} wrapping of core/decode errors.
function toFatal(e: unknown): Error {
  if (e instanceof FatalError) return e;
  return new FatalError(`transport: ${errMsg(e)}`);
}

// toBytes coerces a WebSocket message payload to a Uint8Array. With
// binaryType='arraybuffer' the data is an ArrayBuffer; we also accept a
// Uint8Array (some runtimes) and reject text.
function toBytes(data: unknown): Uint8Array {
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (data instanceof Uint8Array) return data;
  if (ArrayBuffer.isView(data)) {
    const v = data as ArrayBufferView;
    return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
  }
  throw new Error("expected binary message");
}
