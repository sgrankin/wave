# 03 — Live Delta-Channel Protocol (client↔server)

Status: **draft** (2026-06-06). Pins the live client↔server contract that
client-side concurrency control (work item #11) builds on. Scope: the logical
delta channel — open/resync, submit/ack, the live delta stream, errors — as it
exists today plus the additions #11 requires. Out of scope: the *browser*
transport choice and over-the-wire encoding (that is Phase 8a — this contract is
transport-agnostic and currently rides the framed-CBOR session on stdio/tcp/unix).

References the implementation: `internal/transport/message.go` (envelope),
`internal/transport/server.go` (session), `internal/server/container.go` (submit
pipeline), `internal/server/fanout.go` (join + live stream). Compare against the
retroactive Java specs [`../specs/04-wire-protocol.md`](../specs/04-wire-protocol.md)
and [`../specs/03-concurrency-control.md`](../specs/03-concurrency-control.md);
this document is the Go redesign, not a port of those.

## Model differences from Java that simplify the contract

Three properties of our server collapse machinery the Java protocol carried.
State them up front because the rest of the contract depends on them.

1. **Synchronous persist ⇒ applied == committed.** `WaveletContainer.Submit`
   appends the delta to durable storage (`deltas.Append`) *before* it fans the
   delta out or acks it. So no client ever observes a delta that is not already
   durable. Java distinguished *applied* (in-memory) from *committed* (on disk)
   and shipped a separate `commit_notice` / `lastCommitVersion` / "unsaved data"
   surface to bridge the async-persist gap. **We drop all of it.** The ack itself
   means committed. (Future seam: if we ever move to async persistence for write
   throughput, reintroduce a commit watermark message; until then it is dead
   weight.)

2. **One wavelet per connection ⇒ no channel id, no multiplexing (for now).**
   `handleOpen` binds a connection to exactly one wavelet and rejects a second
   open. Java's `channel_id` served two jobs — multiplexing many wavelets over one
   socket, and letting the server suppress a client's own deltas from its stream.
   We don't multiplex, and we get suppression more directly (see §Self-suppression).
   Multiplexing (fewer browser sockets) is a Phase-8 optimization: adding it means
   introducing a channel id and routing; the envelope has room for the field.

3. **Server-side double-submit dedup already exists.** `Submit` checks the delta
   already applied at the resend's target version; if it matches by author +
   `EqualOps` (op-list equality ignoring re-stamped context), it returns the
   original result idempotently. This is the *server half* of reconnection — the
   ghost/echo-back defense — and it is done.

## Current contract (implemented)

Envelope: a CBOR array `[kind, fields...]`. Six kinds (`message.go`). This is the
evolvable wire layer, distinct from the frozen hash-feeding `codec` encoding.

| Message | Dir | Fields | Meaning |
|---|---|---|---|
| `Open` | C→S | `waveletName` | Bind the connection to a wavelet; join. |
| `OpenResponse` | S→C | `snapshotBlob`, `history[]` | Starting view. Exactly one is populated: a current-state `snapshotBlob` (snapshots enabled), else the full applied-delta `history` from version 0. |
| `Submit` | C→S | `clientDeltaBytes` | A client delta (codec-encoded: author, targetVersion, ops). |
| `SubmitResponse` | S→C | `ok`, `code`, `msg`, `resultingVersionBytes` | Ack/nack. `code` is a `cc.ResponseCode`; `resultingVersion` is the codec-encoded post-apply hashed version (nil on nack). |
| `Update` | S→C | `storedDeltaBytes` | A newly applied delta (author, resultingVersion, timestamp, ops), live. |
| `Error` | S→C | `msg` | Protocol/processing error string. |

Lifecycle: `Open` → `OpenResponse` + a live `Update` stream that begins exactly
where the starting view ends (both `Open` and `Submit` hold the container lock, so
a delta is either in the starting view or on the stream, never both, never
dropped). `Submit` may be sent any time after `Open`; each is answered by one
`SubmitResponse`.

**Slow-consumer drop.** A subscriber that falls more than `DefaultSubBuffer`
(256) behind is dropped: its channel closes and `forward` emits
`Error("update stream dropped; reopen to resync")`. Today "resync" means a full
fresh `Open`. §Resync replaces that with an incremental path.

### Response codes (nack → client action)

The action column is the client-CC contract #11 must implement.

| Code | Condition | Client action |
|---|---|---|
| `OK` | applied (or deduped/transformed-away: `ok=true`, `opsApplied` may be 0) | advance to `resultingVersion`. |
| `BadRequest` | undecodable delta | fatal: drop connection, surface a bug. |
| `VersionError` | target version/hash not on history (and not too old) | recoverable: re-transform queued+in-flight to head against the deltas received since, resubmit. |
| `TooOld` | target version older than the pruned history floor (pre-snapshot) | recoverable: **resync** (cannot transform forward without the pruned deltas). |
| `InvalidOperation` | transform/apply failed on a well-formed delta | fatal for that delta: the op was illegal against actual state. |
| `InternalError` | persistence failed; wavelet marked corrupted | retry after backoff / reopen; server requires reload. |

## Additions required for #11

Three changes. (1) and (2) are the substance; (3) is bookkeeping the client owns.

### 1. Self-suppression on the live connection

**Change the current self-echo to suppression.** Today `publish` fans a delta out
to *all* subscribers including the one whose session submitted it, so a client
sees its own delta echoed on `Update`. For a stable connection we instead want the
submitting connection's `Update` stream to carry **only other participants'
deltas** — which is exactly the input client CC transforms its in-flight/queued ops
against. A client learns the fate of its *own* delta solely from the `SubmitResponse`
ack.

Mechanism: thread the originating subscription through submit so fan-out can skip
it — `Submit(delta, origin *Subscription)` / `publish(u, origin)` skips `origin`.
Small, local change in `container.go`/`fanout.go`; the session already holds both
its container ref and its `*Subscription`.

Rationale over keeping the echo + version-dedup on the client: suppression removes
an ordering hazard. Today the ack (pushed by the read loop after `Submit` returns)
and the echo (pushed by the `forward` goroutine) race on the session's single
`out` channel, so a client could receive its own delta *before* its ack. With
suppression there is no echo to order against the ack.

> **Decision to confirm.** Self-suppression vs. keep-echo-and-dedup. Recommending
> suppression. The one place echo-back is unavoidable is reconnection (§2), and the
> existing server dedup + a version check handle it there, bounded to the handshake.

### 2. Resync handshake (the core gap)

A client that reconnects after a drop must recover without refetching the whole
wave. Add an incremental open keyed on what the client already has.

| Message | Dir | Fields | Meaning |
|---|---|---|---|
| `Resync` | C→S | `waveletName`, `knownVersion`, `knownHistoryHash` | "I have state through this (version, hash). Catch me up." |
| `ResyncResponse` | S→C | `mode`, `tail[]` \| `snapshotBlob`/`history[]` | `mode=tail`: `tail` is every applied delta with `resultingVersion > knownVersion`, then a live subscription attaches from there (same no-gap/overlap guarantee as `Open`). `mode=reset`: the known point is unusable — full re-open payload; client discards local state and rebuilds. |

Server logic (the container already has every piece — `history.HasSignature`,
`history.DeltaStartingAt`, the `applied` slice, `Subscribe`):

- `knownVersion`/`knownHistoryHash` not on history (fork, or pruned below the
  snapshot floor) → `mode=reset`.
- otherwise → `mode=tail` with `applied[i:]` where `applied[i]` begins at
  `knownVersion`, plus a live subscription. Implement as an atomic
  `OpenAt(knownVersion, hash) (tail, sub, ok)` holding the lock, analogous to
  `Open()`.

**Reconnection echo-back reconciliation.** The hard case: the client submitted a
delta, the server applied+persisted it, but the client dropped before the ack. On
reconnect the client's `knownVersion` is *before* that delta. It does two things
concurrently: re-sends the unacked delta, and receives the resync `tail`. The
server's dedup answers the resubmit idempotently with the original result; the
`tail` also contains that same delta. The client reconciles by matching the tail
delta to its pending delta by (author + ops) — the same equality the server dedup
uses — and treats them as one, advancing once. A delta the client had applied
*optimistically* but appears in the tail is likewise matched and not reapplied.

### 3. Connection generation (client-side)

Each reconnect is a brand-new session server-side (the old session's goroutines
are torn down on its socket close — `shutdown`). The client must tag frames by a
monotonic connection generation and discard late frames from a dead socket. This
is purely client state (#11), not a wire field — noted here so it isn't forgotten.

## The client-CC contract this implies (preview of #11)

What a conforming client does over this channel — the spec #11 implements and the
ported Java tests (`OperationQueueTest`, `OT3Test`, `ClientAndServerTest` recovery
scenarios) validate:

- **One in-flight delta.** At most one unacknowledged delta on the wire; further
  local ops accumulate in a queue (a future optimization may merge consecutive
  same-author ops; the queue is a plain op list today).
- **Optimistic apply.** Local ops apply to the local doc immediately.
- **Transform incoming.** Each `Update` (other participants' deltas) is transformed
  against the in-flight delta and the queued ops before being applied locally; the
  in-flight/queued ops are transformed forward in step.
- **Advance on ack.** On `SubmitResponse{ok}`, advance the server-version pointer to
  `resultingVersion` and send the next queued delta. On nack, follow the
  response-code table.
- **Resync on reconnect.** Reopen via `Resync` at the last confirmed version;
  re-send the unacked delta; reconcile per §2.

This runs **headless** over the existing framed-CBOR transport — no browser editor
needed — and is tested deterministically with `testing/synctest` (fake clock for
backoff/timeouts, `Wait` for goroutine quiescence). The browser editor (Phase 8b)
is a separate consumer of the same client-CC core.

## Open decisions

1. **Self-suppression vs. echo+dedup** (§1) — recommending suppression.
2. **Resync now vs. force full re-open** — recommending the incremental `tail`
   path now; it is cheap given the container internals and it is what makes
   reconnection not-O(history). The Java reference left this a TODO; we close it.
3. **Multiplexing** — deferring; one wavelet per connection until Phase 8 says
   otherwise (browser socket budget).
4. **Keep `Error` as an untyped string?** The `Update`-stream-dropped error
   should arguably become a typed "resync required" signal once `Resync` exists,
   so the client reacts programmatically instead of string-matching.
