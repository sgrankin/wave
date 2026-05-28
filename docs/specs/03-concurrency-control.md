# 03 — Concurrency Control

## Purpose & scope

This spec describes the client/server OT protocol that coordinates concurrent edits to a
wavelet. It sits on top of the raw transform and compose algorithms (see
[02-operational-transform](02-operational-transform.md)) and defines:

- The delta abstraction and its lifecycle states.
- The client-side algorithm: how the client queues, sends, and transforms operations.
- The server-side algorithm: how the server receives, transforms, and broadcasts deltas.
- Version numbers, HashedVersions, and history-hash verification.
- Acknowledgements, commit notifications, and the difference between "applied" and
  "committed to storage".
- Reconnection and resync: how a client recovers from a disconnect without losing edits.
- Error conditions and failure semantics.

This spec does **not** cover the raw OT transform function (see spec 02), federation
(spec 07), or deep server architecture (spec 06). Cross-references to the server delta
pipeline are limited to what is needed to understand the CC protocol.

---

## Concepts & glossary

**Delta** — An ordered list of wavelet operations submitted as a unit. Every delta has an
author (a ParticipantId), a target version (HashedVersion), and one or more operations.

**Client delta** — A delta created locally by the client, targeting the client's best known
server version. Not yet seen by the server.

**Unacknowledged delta** — A client delta that has been sent to the server and is waiting
for an acknowledgement. At most one is in flight at any time.

**Queued operations / client operation queue** — Operations the client has generated but not
yet sent. They will be packaged into the next delta once the in-flight delta is
acknowledged.

**Transformed delta** — A delta whose operations have been OT-transformed against
intervening server operations so that it is semantically correct at the current server
version.

**TransformedWaveletDelta** — The server-side view of an applied delta: the operations as
transformed, the version they were applied at, the resulting version (with history hash),
the author, and an application timestamp.

**Applied delta / AppliedDelta** — The persisted record of a delta: the original signed
wire bytes (`signedOriginalDelta`) plus `operationsApplied`, `applicationTimestamp`, and an
**optional** `hashedVersionAppliedAt` (the version the delta was actually applied at, present
only when the delta was OT-transformed; recovered from the inner delta otherwise).

**HashedVersion** — A monotonically increasing integer `version` paired with a
`historyHash` byte array. The hash is a cryptographic digest of the entire delta history
up to and including that version, enabling clients to detect if they are on the same
history as the server. The "unsigned" form (`historyHash = []`) is used as a sentinel
before any hash is known.

**History hash** — The `historyHash` field of a HashedVersion; a commitment over the full
wavelet history. Two HashedVersions are equal iff both their version numbers and history
hashes are equal.

**Inferred server path** — The sequence of client deltas that the client *believes* the
server has already applied (because each received an acknowledgement), ordered by their
server-assigned version. The client tracks this to support reconnection.

**Commit notification / committed version** — A message from the server informing the
client that the server has persisted all deltas up to and including a given version number
to durable storage. A delta is "committed" only after this notification; it is merely
"applied" (in memory) between acknowledgement and the commit notification.

**Channel / OperationChannel** — The logical bidirectional stream for one wavelet between
one client and the server. Multiplexed over a ViewChannel (see spec 04).

**Connection tag** — An integer that increments on every reconnect; used to discard
orphaned acknowledgements from a previous connection.

---

## Data structures

### HashedVersion

```
HashedVersion {
    version     int64   // monotonically increasing; number of ops applied since genesis
    historyHash []byte  // SHA-256 chain over all applied delta bytes; empty = unsigned
}
```

Two HashedVersions are equal iff both fields are equal. The unsigned sentinel has an empty
hash; it compares less than any real HashedVersion at the same version number.

### WaveletDelta (client delta, on the wire)

```
WaveletDelta {
    author        ParticipantId
    targetVersion HashedVersion  // version this delta was authored against
    operations    []WaveletOperation
}
```

The wire format is `ProtocolWaveletDelta` (see federation.protodevel).

Derived field: `resultingVersion` = `targetVersion.version + len(operations)` (the version
number after all ops are applied, assuming no ops are nullified by transform).

### TransformedWaveletDelta (server → client broadcast)

```
TransformedWaveletDelta {
    author               ParticipantId
    resultingVersion     HashedVersion // version after application (signed)
    applicationTimestamp int64         // Unix millis
    operations           []WaveletOperation
}
```

`size()` = `len(operations)`.

Derived: `appliedAtVersion` = `resultingVersion.version − len(operations)` (a computed
long, **not** a stored or serialized field — the Java `getAppliedAtVersion()` computes it
on demand). Version advance = `resultingVersion.version − appliedAtVersion` = `len(operations)`.

### ProtocolAppliedWaveletDelta (persisted record)

```
ProtocolAppliedWaveletDelta {
    signedOriginalDelta      ProtocolSignedDelta   // original wire bytes + signatures
    hashedVersionAppliedAt   ProtocolHashedVersion // OPTIONAL; see note below
    operationsApplied        int32
    applicationTimestamp     int64
}
```

`hashedVersionAppliedAt` is **optional** (`optional ProtocolHashedVersion hashed_version_applied_at = 2`
in `federation.protodevel`). It is the version the delta was actually applied at after OT
transformation, and is present only when the delta was transformed (applied at a version
different from the one it targeted). When absent, the applied-at version is recovered by
parsing the inner `ProtocolWaveletDelta` (the `delta` bytes inside `signedOriginalDelta`)
and reading its `hashed_version`. Per the federation protocol, when no transformation
occurs the field is omitted as an optimization; a deserializer **MUST** handle its absence.
A writer that mirrors the Java may always set it, but the reader must tolerate older or
foreign applied deltas that omit it. See spec [07](07-federation.md).

### ProtocolSubmitRequest / ProtocolSubmitResponse (wire RPC)

The running box server uses `waveclient-rpc.proto` (generated outer class `WaveClientRpc`,
service `ProtocolWaveClientRpc`), **not** `clientserver.proto`. See "Wire / storage formats"
for the full service description.

```
ProtocolSubmitRequest {
    waveletName  string                // REQUIRED; URI netpath, deserialized to a WaveletName
    delta        ProtocolWaveletDelta  // REQUIRED
    channelId    string                // OPTIONAL; from the first stream update
}

ProtocolSubmitResponse {
    operationsApplied              int32                 // REQUIRED; ops applied (0 = none / failure)
    errorMessage                   string                // OPTIONAL; free text, set on failure
    hashedVersionAfterApplication  ProtocolHashedVersion // OPTIONAL; set on success
}
```

Note: there is a single `waveletName` (a URI netpath that deserializes to a `WaveletName`),
not separate `waveId` + `waveletId`. The response has **no** status wrapper and **no**
`timestampAfterApplication`; failure is signaled purely by setting `errorMessage` with
`operationsApplied = 0` (WaveClientRpcImpl.java:170-171).

### ProtocolWaveletUpdate (server → client streaming messages)

The open RPC returns a stream of `ProtocolWaveletUpdate` messages. This is a **single
multiplexed wave-level** message (not a per-wavelet stream); each instance carries one
of: a channel id only, a marker only, a wavelet snapshot, or one or more deltas for a
named wavelet.

```
ProtocolWaveletUpdate {
    waveletName       string                   // REQUIRED; URI netpath (set when deltas/snapshot present)
    appliedDelta      []ProtocolWaveletDelta    // zero or more deltas, streamed in order
    commitNotice      ProtocolHashedVersion     // OPTIONAL; server committed to disk at this version;
                                                //   mandatory for snapshots
    resultingVersion  ProtocolHashedVersion     // OPTIONAL; version after all deltas; mandatory for snapshots
    snapshot          WaveletSnapshot           // OPTIONAL; full wavelet state for initial load
    marker            bool                      // OPTIONAL, default false; signals end of initial snapshots
    channelId         string                    // OPTIONAL; set only in the first update; echoed in submits
}
```

A `ProtocolWaveletUpdate` must contain either one or more applied deltas or a commit
notice. The `channelId` is wave-level and appears only in the first update; the client
echoes it in submits (RemoteWaveViewService.viewSubmit). There is **no** separate
`OpenWaveletChannelStream`, `WaveletChannelService`, `DeltaSubmissionService`, or
`FetchService` in the running server.

### ResponseCode

| Code | Value | Meaning |
|------|-------|---------|
| OK | 0 | Success |
| BAD_REQUEST | 1 | Malformed delta |
| INTERNAL_ERROR | 2 | Server-side failure |
| NOT_AUTHORIZED | 3 | Author not a participant or access denied |
| VERSION_ERROR | 4 | targetVersion hash does not match history |
| INVALID_OPERATION | 5 | Op invalid before, during, or after transform |
| SCHEMA_VIOLATION | 6 | Op violates document schema |
| SIZE_LIMIT_EXCEEDED | 7 | Delta or resulting document too large |
| POLICY_VIOLATION | 8 | Rejected by namespace policy |
| QUARANTINED | 9 | Object quarantined |
| TOO_OLD | 10 | Version too far behind; reconnect and retry |

> **This enum is NOT on the wire for the live browser path.** The `ResponseCode` /
> `ResponseStatus.ResponseCode` taxonomy belongs to the OT/`clientserver.proto` code
> vocabulary, which is **not** wired into the running box server (see "Wire / storage
> formats"). The live browser protocol is `waveclient-rpc.proto`, whose
> `ProtocolSubmitResponse` reports failure only via a free-text `errorMessage` string
> (sourced from the federation error message). The GWT client
> (`RemoteWaveViewService.viewSubmit`) hardcodes `ResponseCode.OK` on every response and
> treats `errorMessage` as opaque text.
>
> Structured codes seen by the client CC layer (`INVALID_OPERATION` / `SCHEMA_VIOLATION`
> via `onNack`) are generated **client-side** from local transform/validation exceptions,
> not received over the wire. In particular `SCHEMA_VIOLATION` (code 6) is never produced
> by the box server: the server builds documents with `SchemaCollection.empty()` and never
> validates deltas against a schema (see spec [06](06-server-architecture.md)).
>
> A Go rewrite should either reproduce the free-text channel or extend
> `ProtocolSubmitResponse` with a structured code — but must pick one. See spec
> [04](04-wire-protocol.md) (`ProtocolSubmitResponse`) and spec [07](07-federation.md)
> (`FederationError`).

---

## Algorithms & behavior

### Version arithmetic

The server maintains a strictly increasing integer `currentVersion`. Each committed delta
advances it by exactly `operationsApplied`. An empty delta (all ops transformed away) does
not advance the version and is not stored in history.

**Invariant**: `resultingVersion.version = appliedAtVersion + operationsApplied`.

The `historyHash` of a version is computed over the ordered sequence of all
`ProtocolAppliedWaveletDelta` bytes up to and including the delta that produced it (exact
hash algorithm is SHA-256; see spec 07 for federation signing). The hash at version 0 is
not derived from any delta; it is the UTF-8 bytes of the wavelet's URI
(`wave://<waveletDomain>/<waveDomainPart><waveLocalId>/<waveletLocalId>`), which is fixed
for a given wavelet but differs between wavelets. See spec [01](01-data-model.md) §2.5 for
the exact URI format.

### Client-side concurrency control

The client CC module maintains this state per wavelet:

```
startSignature      HashedVersion  // hash at the start of the current connection epoch
endOfStartingDelta  HashedVersion  // used during reconnection handshake; nil otherwise
inferredServerPath  []AckedDelta   // deltas acked by server this session (cleared on server delta)
acks                []AckInfo      // acked but not yet committed (not cleared on server delta)
lastCommitVersion   int64
unacknowledged      *WaveletDelta  // nil when nothing is in flight
clientOperationQueue OperationQueue
serverOperations    []WaveletOperation  // transformed server ops buffered for the UI
```

`AckedDelta = { delta WaveletDelta, ack AckInfo }` where `AckInfo = { numOps int, ackedVersion HashedVersion }`.

#### Sending client operations

1. Client calls `onClientOperations(ops[])`.
2. Ops are first transformed against any buffered `serverOperations` (ops received but not
   yet consumed by the UI): `(clientOps', serverOps') = transform(ops, serverOperations)`.
   `serverOperations ← serverOps'`. The transformed `clientOps'` are enqueued.
3. Each op is appended to `clientOperationQueue`. Consecutive ops from the same author are
   merged into a single delta item; a change of author creates a new item.
4. `sendDelta()` is called.

#### sendDelta()

Only executes if: the server connection is open AND `pauseSendForFlush` is false AND
`unacknowledged == nil` AND the client operation queue is non-empty.

1. After passing the ready/unacknowledged/non-empty-queue guards but before dequeuing, set
   `endOfStartingDelta = nil`. This terminates the reconnection echo-back detection window:
   once the client sends a new delta, the inferred server-path location is established, so
   subsequent server deltas are processed normally rather than being checked for echo-back.
   (Java: ConcurrencyControl.java line 361, inside `sendDelta()`, after the empty-queue
   check at line 352.) After this point `detectEchoBack()` is a no-op (see "Echo-back
   detection").
2. Dequeue one item from `clientOperationQueue.take()`. This merges consecutive same-author
   items and applies `MergingSequence.optimise()` (compose) before extracting.
3. Package ops as `unacknowledged = WaveletDelta(author, getLastSignature(), ops)`.
   `getLastSignature()` returns the last ack's `ackedVersion` if `inferredServerPath` is
   non-empty, else `startSignature`.
4. Send `unacknowledged` to the server.

#### Receiving a server delta (onServerDelta)

Called when the server broadcasts a delta from another client.

1. Sanity check: `serverDelta.appliedAtVersion >= lastVersionInInferredPath`.
2. **Echo-back detection**: if `endOfStartingDelta != nil` and
   `endOfStartingDelta.version > serverDelta.appliedAtVersion`, check if the incoming delta
   matches `unacknowledged` op-for-op (same author, same ops, ignoring timestamp). If so,
   treat it as an implicit ack via `onSuccess(delta.size(), delta.resultingVersion)` and
   return. If we reach `endOfStartingDelta` version without a match, merge pending ops back
   to the queue (see Reconnection).
3. **Clear inferred server path**: `inferredServerPath.clear()`, `startSignature ← delta.resultingVersion`.
4. Transform `unacknowledged` (if non-nil) against the server delta:
   - Precondition: `serverDelta.appliedAtVersion == unacknowledged.targetVersion.version`.
   - `(client', server') = DeltaPair(unacknowledged, serverDelta).transform()`.
   - `unacknowledged ← WaveletDelta(author, serverDelta.resultingVersion, client')`.
   - `transformedServerDelta ← server'`.
5. Transform `transformedServerDelta` against each item in `clientOperationQueue` in order.
   Items whose ops are completely transformed away are discarded.
6. Append transformed server ops to `serverOperations`.
7. Notify the UI (via `ConnectionListener.onOperationReceived()`), pausing `sendDelta`
   during notification to let the UI flush its pending ops first.
8. After notification, call `sendDelta()`.

**Invariant** (CC diamond property): After step 5, the client's document state equals
`apply(serverState, transformedClientOps)` = `apply(clientState, transformedServerOps)`.

#### DeltaPair transform (used in step 4 and 5)

If the two op lists are identical (same author, same ops), they nullify each other:
`client' = []`, `server' = [versionUpdateOps for each serverOp]` (preserving version
advancement). Otherwise, apply `Transform.transform(c, s)` for each pair `(c, s)` in the
Cartesian product, accumulating results left-to-right.

#### Receiving an ack (onSuccess)

Called when the server's submit response indicates success.

Preconditions (any violation throws `TransformException`, closing the channel), checked in
this order:
- `unacknowledged != nil`
- `signature.version == unacknowledged.resultingVersion` (i.e.
  `unacknowledged.targetVersion.version + unacknowledged.size()`) — note this uses the
  delta's own op count (`size()`), **NOT** the server-reported `opsApplied`
- `opsApplied == unacknowledged.size()`

The two checks together are behaviorally equivalent regardless of order: they only diverge
when `opsApplied != size()`, which both reject.

Steps:
1. Record `AckInfo(opsApplied, signature)` in `acks`. If `unacknowledged.size() > 0`, also
   record an `AckedDelta(unacknowledged, ack)` in `inferredServerPath`.
2. Generate *fake version-update ops* for each unacknowledged op:  
   For each op in `unacknowledged`, create a `versionUpdateOp(versionIncrement=1,
   hashedVersion=nil)`. The last op gets `hashedVersion=signature`. Transform these against
   `clientOperationQueue` — this advances the queued client ops past the version increment;
   the returned (transformed) ops are **discarded**. Append the **original** version-update
   ops list to `serverOperations` (the ack path discards the transform return value, unlike
   the server-delta path which appends the captured transform result). Because
   `VersionUpdateOp` has an identity transformation, the original ops and the transformed
   ops are content-identical, so this is behaviorally equivalent. Notify UI.  
   (This is how the client model learns of the version advance for its own ops.)
3. `unacknowledged ← nil`.
4. `sendDelta()` — sends the next queued delta if any.

#### Receiving a nack (onNack)

The server rejected the delta. The channel is torn down as NOT_RECOVERABLE. Callers must
reconnect.

Exception: `TOO_OLD` is RECOVERABLE — the client should reconnect, which will re-transform
its ops against the current history.

#### Receiving a commit notification (onCommit)

Called with `committedVersion int64`.

1. Walk `inferredServerPath` from the front, removing entries whose `delta.resultingVersion
   <= committedVersion`, updating `startSignature` to the last removed entry's
   `ack.ackedVersion`.
2. Remove entries from `acks` whose `ackedVersion.version <= committedVersion`.
3. `lastCommitVersion ← committedVersion`.
4. Notify `UnsavedDataListener`.

**Invariant**: `lastCommitVersion <= lastAckVersion <= serverVersion`. Everything between
`lastCommitVersion` and `lastAckVersion` is acknowledged but not yet durably stored.

#### Operation states on the client

```
┌──────────┐  onClientOperations  ┌──────────┐  sendDelta()  ┌─────────────────┐
│ generated │ ──────────────────► │  queued  │ ────────────► │  in-flight (one │
│  by UI   │                      │ in queue │               │   at a time)    │
└──────────┘                      └──────────┘               └────────┬────────┘
                                                                       │ onSuccess
                                                                       ▼
                                                              ┌────────────────────┐
                                                              │ acked / inferred   │
                                                              │ server path        │
                                                              └──────┬─────────────┘
                                                                     │ onCommit
                                                                     ▼
                                                              ┌────────────────────┐
                                                              │  committed         │
                                                              └────────────────────┘
```

### Server-side concurrency control

The server CC (ConcurrencyControlCore) manages one wavelet and holds a `DeltaHistory`
(read-only reference to the committed delta log).

#### Receiving a client delta (onClientDelta)

1. If `delta.targetVersion.version > currentVersion`: error (client is ahead of server —
   impossible under correct protocol).
2. While `delta.targetVersion.version < currentVersion`:
   a. Fetch `serverDelta = deltaHistory.getDeltaStartingAt(delta.targetVersion.version)`.
   b. `(client', server') = DeltaPair(delta, serverDelta).transform()`.
   c. `delta ← WaveletDelta(originalAuthor, serverDelta.resultingVersion, client')`.
3. Return the transformed `delta` targeting `currentVersion`.

The returned delta is then applied to the wavelet state, which advances `currentVersion`
by `delta.size()`.

#### Server version check and hash validation (WaveletContainerImpl)

Before calling `onClientDelta`:

1. If `targetVersion.version == currentVersion`: no transform needed. But check
   `targetVersion.historyHash == currentVersion.historyHash`. If version numbers match but
   hashes differ, return `VERSION_ERROR` (hash mismatch at same version).
2. If `targetVersion.version < currentVersion`: transform as above.
3. If `targetVersion.version > currentVersion`: impossible; return `VERSION_ERROR`.

**Invariant**: The server never applies a delta targeting a version it hasn't seen.

#### Broadcasting applied deltas

After applying the delta, the server:
1. Notifies all subscribed clients via their open streams with a `ProtocolWaveletUpdate`
   whose `applied_delta[]` carries the `TransformedWaveletDelta` and whose
   `resulting_version` is the new version.
2. The submitting client's channel receives a `ProtocolSubmitResponse` (the ack) instead of
   seeing the delta on its stream. The stream for that channel excludes the client's own
   submitted deltas.
3. Persists the delta asynchronously. When persistence completes, broadcasts a commit
   notification (a `ProtocolWaveletUpdate` carrying `commit_notice`) to all subscribers.

**Invariant**: Broadcast and submission response are sent before persistence, so the ack
reaches the submitting client before the commit notification.

### Reconnection and resync

When a client reconnects, it must re-synchronize its local state with the server's
authoritative history.

#### Client side: preparing to reconnect

The client collects `reconnectionVersions`:
- Always starts with `startSignature` (the version at the beginning of the current epoch,
  or the initial version for a new connection).
- Followed by each `ackedDelta.ack.ackedVersion` in `inferredServerPath` order.

This is the list of HashedVersions the client claims to have observed.

#### Server side: reopen (ConcurrencyControlCore.reopen)

Given a list of client-known signatures (oldest to newest):
1. Walk the list from newest to oldest.
2. Find the first signature that appears in the server's `deltaHistory.hasSignature()`.
3. From that matching signature, collect all subsequent deltas from history up to the
   current version and return them, along with the matched start signature.
4. If no signature matches, return nil — the client cannot recover.

#### Client side: processing onOpen response

On reconnect, the channel layer calls `onOpen(connectVersion, currentVersion)`:

- `connectVersion` = the server's matched start signature.
- `currentVersion` = the server's current HashedVersion (may equal `connectVersion`).

Client logic:

1. Find `startResend`: the index into `inferredServerPath` where `connectVersion` matches.
   - If it matches `startSignature` (the epoch root): `startResend = 0`.
   - If it matches some `inferredServerPath[i].ack.ackedVersion`: `startResend = i + 1`.
   - If no match: throw `ChannelException` NOT_RECOVERABLE (server has diverged).
2. If `startResend < inferredServerPath.size()`:  
   Call `mergeToClientQueue(startResend)` — push all deltas from `startResend` onward (plus
   `unacknowledged` if set) back into `clientOperationQueue` as pre-sent items, clearing
   them from `inferredServerPath`.
3. Else if `startResend == inferredServerPath.size()` AND `connectVersion == currentVersion`:  
   Also call `mergeToClientQueue(startResend)` — server is exactly at where the client
   thought, but the in-flight delta may not have arrived; re-queue it.
4. Else (all signatures matched but `currentVersion > connectVersion`):  
   No re-queueing is done now; the server will stream intermediate deltas and the client
   will compare each against `unacknowledged` looking for an echo-back. (Java only logs a
   trace here — it performs no mutation in this branch.)
5. `forgetAcksAfter(startSignature.version)` — drop any stale ack records.
6. `endOfStartingDelta ← currentVersion` (for echo-back detection). This assignment is
   **unconditional**: it executes after `forgetAcksAfter`, regardless of which branch in
   steps 1-4 was taken. (Java: `endOfStartingDelta` is assigned exactly once, at
   ConcurrencyControl.java line 292; no branch of the if/else block assigns it.)
7. Call `sendDelta()` — resend queued ops if connection is open.

#### Echo-back detection

During the reconnection handshake, the server streams all deltas between `connectVersion`
and `currentVersion`. The client receives them via `onServerDelta`. The CC logic checks:

- If `endOfStartingDelta == nil` or `endOfStartingDelta.version <= serverDelta.appliedAtVersion`:
  normal processing (not a reconnect replay).
- If the incoming delta **matches** `unacknowledged` (same author, same ops, ignoring
  timestamp): treat as an ack (`onSuccess`). This handles the case where the server
  received the delta but the ack was lost.
- If we reach `endOfStartingDelta.version` without a match: the in-flight delta was not
  received by the server; merge it back into the queue via `mergeToClientQueue`.

#### mergeToClientQueue(startMerge)

Moves deltas at index `startMerge` and above from `inferredServerPath` into the head of
`clientOperationQueue` (in original order, tagged as `SENT` so they are not re-merged).
If `unacknowledged != nil`, it is also prepended and cleared. These ops will be re-sent to
the server.

#### Reconnection failure

If no client-known signature matches the server history:
- The client cannot recover without a full snapshot re-fetch.
- The channel throws `ChannelException(NOT_RECOVERABLE)`.
- The UI must close the channel and open a new one from scratch.

If two concurrent clients are both reconnecting after a server crash (where the server lost
some history), only the first reconnecting client can be recovered; the second will fail
with no matching signatures (described in `testRecoveryServerCrash2Clients`).

### Open wavelet handshake

A fresh connection (not reconnect) works as follows. The open RPC
(`Open(ProtocolOpenRequest)`) returns a stream of `ProtocolWaveletUpdate` messages:

1. Client calls `ViewChannel.open(waveletFilter, knownWavelets)`, which maps to a
   `ProtocolOpenRequest` (participant id, wave id, wavelet-id-prefix filter, known wavelet
   versions for resync).
2. The first `ProtocolWaveletUpdate` carries the `channelId`. The client echoes this id in
   subsequent submits.
3. For each wavelet in view, the server sends a `ProtocolWaveletUpdate` that is either:
   - A **snapshot** (`snapshot` set to a `WaveletSnapshot`): the current complete state,
     used for initial load. For a snapshot, `commitNotice` and `resultingVersion` are
     mandatory.
   - A **delta-based resync**: an empty (`appliedDelta` zero-length) update specifying the
     resynchronization version, used when the client already has a matching known version.
4. A `ProtocolWaveletUpdate` with `marker = true` signals that all current snapshots have
   been sent (the end of the initial state batch); real-time deltas follow as further
   `ProtocolWaveletUpdate` messages carrying `appliedDelta[]` and `resultingVersion`.

On receiving the first wavelet message, the `WaveletDeltaChannelImpl` calls
`receiver.onConnection(connectVersion, currentVersion)`, which routes to `cc.onOpen()`.

### Unsaved data tracking

The CC module exposes `UnsavedDataInfo` to the UI:

| Metric | Meaning |
|--------|---------|
| `inFlightSize` | Number of ops in the unacknowledged delta |
| `estimateUnacknowledgedSize` | In-flight + queued ops |
| `estimateUncommittedSize` | Unacknowledged + acked-but-uncommitted |
| `lastAckVersion` | Version of most recent ack |
| `lastCommitVersion` | Version confirmed durable |

The UI uses this to show "unsaved changes" warnings. On channel close, `onClose(allCommitted)` is called.

### Message ordering (WaveletDeltaChannelImpl)

The channel layer handles out-of-order delivery between the delta submission response and
the stream:

- All incoming server messages are sorted by `startVersion` before delivery.
- A pending message whose `startVersion > lastDeliveredVersion` is held until earlier
  messages arrive.
- Only one outbound delta is in flight at a time. If there are queued incoming messages,
  the next outbound is deferred until the queue drains (to ensure transform ordering).
- A **connection tag** (integer, incremented on each reconnect) guards against orphaned
  ack callbacks from a prior connection.

---

## Wire / storage formats

> **Which protocol is on the wire.** The running box server speaks the protocol defined in
> `wave/src/proto/proto/org/waveprotocol/box/common/comms/waveclient-rpc.proto` (generated
> outer class `WaveClientRpc`), via the service `ProtocolWaveClientRpc`
> (`ServerMain.java:227-228`, `WaveClientRpcImpl.java`). The `clientserver.proto` file
> (with its `Fetch`/`WaveletChannel`/`DeltaSubmission` RPCs, `SubmitDeltaRequest`,
> `OpenWaveletChannelStream`, and the `ResponseStatus.ResponseCode` enum) also exists in
> the tree but has **zero references in the main Java code** — it is dead code and **MUST
> NOT** be used as the porting target. Build against `ProtocolWaveClientRpc`.

`ProtocolWaveClientRpc` has three RPCs:

```
Open         (ProtocolOpenRequest)   returns (stream ProtocolWaveletUpdate)
Submit       (ProtocolSubmitRequest) returns (ProtocolSubmitResponse)
Authenticate (ProtocolAuthenticate)  returns (ProtocolAuthenticationResult)
```

### Client → server: delta submission

`ProtocolSubmitRequest`:
```
wavelet_name  string                  // REQUIRED; URI netpath, deserializes to a WaveletName
delta         ProtocolWaveletDelta    // REQUIRED; see federation.protodevel
channel_id    string                  // OPTIONAL; the channel id from the first update
```

Note the single `wavelet_name` netpath rather than separate `waveId`/`waveletId`, and that
`channel_id` is optional.

`ProtocolWaveletDelta`:
```
hashed_version  ProtocolHashedVersion  // targetVersion
author          string
operation       []ProtocolWaveletOperation
address_path    []string               // for proxy authors; usually empty
```

`ProtocolHashedVersion`:
```
version       int64
history_hash  bytes
```

### Server → client: submit response

`ProtocolSubmitResponse`:
```
operations_applied                 int32                 // REQUIRED; ops applied (0 = none / failure)
error_message                      string                // OPTIONAL; free text, set on failure
hashed_version_after_application   ProtocolHashedVersion // OPTIONAL; set on success
```

There is **no** `ResponseStatus`/status wrapper and **no** `timestampAfterApplication` on
this message. Failure is signaled purely by setting `error_message` with
`operations_applied = 0` (WaveClientRpcImpl.java:170-171); the `error_message` text is
sourced from the federation error message (see spec [07](07-federation.md)). The structured
`ResponseStatus.ResponseCode` taxonomy is **not** transmitted on this path — the GWT client
(`RemoteWaveViewService.viewSubmit`, line 271) hardcodes `ResponseCode.OK` on success and
treats `error_message` as opaque text. See spec [04](04-wire-protocol.md). A Go rewrite
should either reproduce the free-text channel or extend `ProtocolSubmitResponse` with a
structured code — but must pick one.

### Server → client: stream messages

The `Open` RPC returns a stream of `ProtocolWaveletUpdate` messages (a single multiplexed
wave-level message, not a per-wavelet stream). Each instance carries one of: a channel id
only, a marker only, a snapshot, or one or more deltas for a named wavelet:

```
ProtocolWaveletUpdate {
  wavelet_name       string                   // REQUIRED; URI netpath (set when deltas/snapshot present)
  applied_delta      []ProtocolWaveletDelta    // zero or more deltas, in order
  commit_notice      ProtocolHashedVersion     // OPTIONAL; committed-to-disk version; mandatory for snapshots
  resulting_version  ProtocolHashedVersion     // OPTIONAL; version after deltas; mandatory for snapshots
  snapshot           WaveletSnapshot           // OPTIONAL; full wavelet state for initial load
  marker             bool                      // OPTIONAL, default false; end-of-initial-snapshots marker
  channel_id         string                    // OPTIONAL; first update only; echoed in submits
}
```

A `ProtocolWaveletUpdate` must contain either one or more applied deltas or a commit
notice. The first update of a stream carries `channel_id`; a `marker = true` update signals
the end of the initial snapshot batch, after which real-time delta updates follow.

### Persisted form

`ProtocolAppliedWaveletDelta` (stored per delta in the wavelet store):
```
signedOriginalDelta    ProtocolSignedDelta     // wire bytes + domain signatures
hashedVersionAppliedAt ProtocolHashedVersion   // OPTIONAL; present only when OT transformed the delta;
                                               //   when absent, recover the applied-at version by
                                               //   parsing the inner ProtocolWaveletDelta
                                               //   (signedOriginalDelta.delta) and reading its hashed_version
operationsApplied      int32
applicationTimestamp   int64
```

A reader **MUST** handle the absence of `hashedVersionAppliedAt` (older/foreign applied
deltas, or deltas that were not OT-transformed). See spec [07](07-federation.md).

`ProtocolSignedDelta`:
```
delta      bytes                   // serialized ProtocolWaveletDelta
signature  []ProtocolSignature     // domain signatures
```

---

## Interfaces / APIs

### Client CC (ConcurrencyControl)

```
// Initialize (call once after construction):
initialise(serverConnection ServerConnection, clientListener ConnectionListener)

// Feed operations from the local editor:
onClientOperations(ops []WaveletOperation) -> error

// Receive server events (called by channel layer):
onOpen(connectVersion HashedVersion, currentVersion HashedVersion) -> error
onServerDelta(delta TransformedWaveletDelta) -> error
onServerDeltas(deltas []TransformedWaveletDelta) -> error
onSuccess(opsApplied int, signature HashedVersion) -> error
onCommit(committedVersion int64)

// Consume transformed server ops (called by UI):
receive() -> WaveletOperation (nil if empty)
peek()    -> WaveletOperation (nil if empty)

// Reconnection support:
getReconnectionVersions() -> []HashedVersion
```

### Server CC (ConcurrencyControlCore)

```
// Transform a client delta to the current server version:
onClientDelta(delta WaveletDelta) -> (WaveletDelta, error)

// Compute what to send to a reconnecting client:
reopen(clientKnownSignatures []HashedVersion) -> ReOpenInfo
```

`ReOpenInfo`:
```
startSignature  HashedVersion           // matched reconnection point
deltas          []TransformedWaveletDelta  // history from that point to current
```

### OperationChannel (client-facing)

```
send(ops ...WaveletOperation) -> error
receive() -> WaveletOperation
peek()    -> WaveletOperation
getReconnectVersions() -> []HashedVersion
reset()   // prepare for reconnect
close()   // permanent close
setListener(l Listener)
```

`Listener.onOperationReceived()` — called when transformed server ops are available to consume.

### DeltaHistory (server interface)

```
getCurrentVersion() -> int64
getDeltaStartingAt(version int64) -> TransformedWaveletDelta (nil if not found)
hasSignature(hv HashedVersion) -> bool
```

---

## Edge cases & failure modes

### Same version, different hash (VERSION_ERROR)

Client claims `targetVersion.version == serverVersion` but the hashes differ. This means
the client is on a different history. The server returns `VERSION_ERROR`. The client must
reconnect from scratch.

### Transform failure (INVALID_OPERATION)

If `Transform.transform(clientOp, serverOp)` throws, the channel is marked NOT_RECOVERABLE
and torn down. This indicates a bug in the transform function or corrupted ops.

### Server delta at unexpected version (client)

If the server sends a delta at a version older than the end of `inferredServerPath`, or if
a server delta arrives when an unacknowledged delta exists but the versions don't line up
(`serverDelta.appliedAtVersion != unacknowledged.targetVersion.version`), CC throws
`TransformException` and the channel closes.

### Ack mismatch (client)

`onSuccess` throws if (checked in this order):
- `unacknowledged == nil` (spurious ack).
- `signature.version != unacknowledged.targetVersion.version + unacknowledged.size()` —
  the version check uses the delta's own op count (`size()`), not the server-reported
  `opsApplied`.
- `opsApplied != unacknowledged.size()`.

(The two checks together are behaviorally equivalent regardless of order, since they only
diverge when `opsApplied != size()`, which both reject.)

### Double-submit / ghost delta

A delta may reach the server twice (once from a previous session, once as a resend after
reconnect). The server detects this via `transformSubmittedDelta`: after transform, if the
client's author matches the server delta's author and the ops are identical, it is a
duplicate. The server returns the previously applied result idempotently and does not
re-apply.

On the client, the same situation is handled by echo-back detection: if the server streams
back the client's own delta (same author, same ops, ignoring timestamp) during the initial
reconnect replay, the client treats it as an ack rather than a real server delta.

### TOO_OLD response

If the client targets a version the server's history no longer has (the delta history was
truncated or the server was rebooted to a much older version), the server returns
`TOO_OLD`. This is RECOVERABLE. The channel layer should reconnect; the client will
re-transform its ops from whatever server state it can match.

### Missing messages (version gap)

The `WaveletDeltaChannelImpl` holds a sorted queue of server messages. If an incoming
message's `startVersion` is more than the in-flight delta's `size()` ahead of
`lastServerVersion`, there is an unaccountable gap and the channel is torn down as
NOT_RECOVERABLE.

### Wavelet corrupt / quarantined

The server marks a wavelet `CORRUPTED` if loading from storage fails or an invariant is
violated. All subsequent requests return `INTERNAL_ERROR` or `QUARANTINED`. Clients must
not retry indefinitely.

### Empty delta (all ops transformed away)

In the current (and only) OT algorithm the server **never** transforms a submitted delta
entirely away during a normal ack, so this path does not occur. See
`LocalWaveletContainerImpl.java:148`: *"This is always false right now because the current
algorithm doesn't transform ops away."*

For completeness: if the server *did* ack with `operationsApplied = 0` and a resulting
version equal to the target version (no advance), the client's `onSuccess` would **not**
succeed — it would throw `TransformException` at the first version precondition. That check
is `unacknowledged.getResultingVersion() != signature.version`, i.e.
`unacknowledged.targetVersion.version + unacknowledged.size() != signature.version`. Since
the client's `unacknowledged` delta still holds its original ops (`size() > 0`; it is not
zeroed client-side in this scenario) while `signature.version == targetVersion.version`,
the inequality holds and CC closes the channel.

The server-side empty-delta handling is a separate, server-only concern (the server returns
`operationsApplied = 0`, stores no delta, and adds no history entry —
`LocalWaveletContainerImpl.java:149-159`) and must not be conflated with the client
`onSuccess` precondition logic above.

---

## Open questions / ambiguities

1. **History-hash computation**: The exact algorithm for computing `historyHash` from
   `ProtocolAppliedWaveletDelta` bytes is not described in the CC package — it is
   implemented in the federation layer. The Go rewrite must replicate it exactly for
   interoperability. See spec 07.

2. **Snapshot vs. delta reconnect**: The initial connection can deliver either a full
   `WaveletSnapshot` or a stream of deltas starting from a known reconnect version. The
   spec describes both paths, but the decision logic (when to use snapshot vs. deltas) is
   in the server frontend, not in CC. See spec 06.

3. **TOO_OLD recovery**: The spec says the client should reconnect and retry, but the
   recovery path when the server has truncated history past what the client knows is not
   fully specified. In practice, if `reopen()` returns nil (no matching signature), the
   client must do a full snapshot fetch.

4. **Commit notification ordering**: The current code broadcasts commit notifications after
   persistence completes asynchronously. There is no guarantee about the ordering of commit
   notifications relative to delta broadcasts across different clients. The Go rewrite
   should preserve the invariant that a commit notification is never received before the
   corresponding ack.

5. **Empty initial delta**: During reconnect, the server wraps the reconnect version in a
   zero-op `TransformedWaveletDelta` as the first stream message. This is a protocol
   convention not enforced by the data model. Implementors should be explicit about this.

6. **Operation queue SENT state**: Ops that were previously sent but not acked are
   re-inserted into the queue head with a `SENT` tag, preventing them from being merged
   with adjacent same-author ops. This preserves the serialization boundary that the
   original delta had. The Go rewrite should preserve this behavior.

7. **Timestamp in echo-back**: The Java code explicitly ignores timestamp differences when
   comparing ops for echo-back detection. The Go rewrite should do the same (compare only
   author and operation content, not timestamps).

---

## Source references

| File | Role |
|------|------|
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/client/ConcurrencyControl.java` | Client CC state machine: send, ack, transform, reconnect |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/client/OperationQueue.java` | Client-side delta queue with transform and merge |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/client/MergingSequence.java` | Per-author op list with compose optimization |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/server/ConcurrencyControlCore.java` | Server CC: transform-to-head and reopen |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/server/DeltaHistory.java` | Interface for server delta log |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/common/DeltaPair.java` | OT transform of two delta lists; echo-back detection |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/channel/OperationChannelImpl.java` | Wires CC to WaveletDeltaChannel; manages channel state |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/channel/WaveletDeltaChannelImpl.java` | Serializes submissions; reorders server messages; connection tag |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/channel/ViewChannelImpl.java` | Wave-level stream; channel-id lifecycle; snapshot/delta routing |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/channel/OperationChannelMultiplexerImpl.java` | Multiplexes multiple wavelets over one ViewChannel |
| `wave/src/main/java/org/waveprotocol/wave/concurrencycontrol/common/UnsavedDataListener.java` | Unsaved data tracking interface |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/WaveletContainerImpl.java` | Server: hash validation, OT, apply, persist, broadcast |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/LocalWaveletContainerImpl.java` | Local wavelet: submit, transform, notify, persist |
| `wave/src/proto/proto/org/waveprotocol/box/common/comms/waveclient-rpc.proto` | **Live** wire protocol: `ProtocolWaveClientRpc` service (Open/Submit/Authenticate), `ProtocolSubmitRequest/Response`, `ProtocolWaveletUpdate` |
| `wave/src/main/java/org/waveprotocol/box/server/frontend/WaveClientRpcImpl.java` | Server RPC adapter: maps Open/Submit to the client frontend; builds `ProtocolWaveletUpdate`/`ProtocolSubmitResponse` |
| `wave/src/main/java/org/waveprotocol/box/webclient/client/RemoteWaveViewService.java` | GWT client: `viewSubmit` (echoes channel id, hardcodes `ResponseCode.OK`, treats `error_message` as opaque) |
| `wave/src/proto/proto/org/waveprotocol/wave/concurrencycontrol/clientserver.proto` | **Unused dead code** (zero references in main Java): Fetch/WaveletChannel/DeltaSubmission RPCs, `ResponseStatus.ResponseCode`. Not the porting target. |
| `wave/src/proto/proto/org/waveprotocol/wave/federation/federation.protodevel` | Core message types: ProtocolWaveletDelta, HashedVersion, ProtocolAppliedWaveletDelta (`hashed_version_applied_at` is optional) |
| `wave/src/test/java/org/waveprotocol/wave/concurrencycontrol/client/OT3Test.java` | Exhaustive CC unit tests: ack semantics, transform, reconnect |
| `wave/src/test/java/org/waveprotocol/wave/concurrencycontrol/client/ClientAndServerTest.java` | Integration tests: multi-client, concurrent edits, ghost deltas |
