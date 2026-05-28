# 00 — System Overview

## Purpose & scope

This document is the connective tissue for the Apache Wave retroactive specs. It
explains what Wave *is*, names the cross-cutting concepts the per-subsystem specs
assume, draws the component map, and traces the end-to-end data flows that span
multiple subsystems. Read this first; then read the subsystem specs in the order
given in [README.md](README.md).

It also records the **cross-cutting decisions** that shape the Go rewrite (which
subsystems are load-bearing, which can be dropped or replaced), so later planning
docs have a single reference.

## What Wave is

Wave is a **real-time collaborative editing** system. The unit of collaboration
is a **wave**: a threaded conversation made of richly-formatted documents that
multiple participants edit simultaneously. Every change is an **operation**;
operations from concurrent editors are reconciled by **Operational Transformation
(OT)** so that all participants converge on identical state without locking.

A running deployment is a **wave server** (the "Wave in a Box" / WIAB server)
plus a **web client**. Servers in different domains could federate over XMPP to
share waves across organizations — though, as the federation spec documents, the
XMPP transport was never actually shipped in this codebase (it runs in no-op
mode).

The three ideas that everything else hangs off:

1. **Everything is a document of operations.** A wave's content, its conversation
   structure, even per-user metadata, are all XML-like documents mutated only by
   OT operations. There is no "edit in place" — you apply an op.
2. **History is a hash chain.** Each wavelet's version is a `(version number,
   history hash)` pair; the hash chains every applied delta, making history
   tamper-evident and giving clients an exact agreement point.
3. **Identity is `name@domain`.** Participants, accounts, robots, and the
   federation model all key off the same `ParticipantId` address form. There are
   no opaque user IDs anywhere in the model.

## Glossary (cross-cutting terms)

These terms appear across many specs. Subsystem specs define their own local
terms; these are the shared vocabulary.

| Term | Meaning |
|---|---|
| **Wave** | Top-level collaboration object; a container of wavelets. Identified by `WaveId`. |
| **Wavelet** | The unit of OT, access control, and storage: an ordered set of documents + a participant set + a version. Identified by `WaveletId`; globally by `WaveletName` = (WaveId, WaveletId). |
| **Blip** | A single message/post within a conversation; backed by one document. |
| **Document** | An XML-like tree (elements, attributes, text) plus an annotation layer. The thing operations mutate. |
| **Conversation** | The threaded structure (threads, blips, replies) encoded in a special `conversation` manifest document within a conversation wavelet. |
| **Operation / DocOp** | A document operation: a sequence of components (retain, characters, elementStart/End, delete…, attribute updates, annotationBoundary) applied over a cursor. See [02](02-operational-transform.md). |
| **Delta** | A signed/authored batch of operations targeting a specific wavelet version. The unit of submission, transfer, and storage. See [03](03-concurrency-control.md). |
| **Version / HashedVersion** | `(long version, historyHash bytes)`. For version >= 1 the `historyHash` is 20 bytes (first 160 bits of a SHA-256); at version 0 it is the raw UTF-8 bytes of the wavelet URI (variable length, no digest). Version counts *operations applied*, not deltas. See [01](01-data-model.md). |
| **Transform** | The OT function reconciling two concurrent operations; guarantees convergence (TP1). See [02](02-operational-transform.md). |
| **Participant / ParticipantId** | An addressable identity `name@domain` (human, robot, or the `@domain` shared-domain wildcard). See [01](01-data-model.md), [08](08-authentication-accounts.md). |
| **Supplement / UDW** | Per-user state (read/unread, folders, archive) stored in a **User Data Wavelet** — a private wavelet visible only to one participant. See [01](01-data-model.md). |
| **Snapshot** | The fully-applied state of a wavelet at a version (vs. the delta history that produces it). The Java server does **not** persist snapshots; it replays deltas. See [05](05-storage-persistence.md). |
| **Robot** | An automated participant that receives events and submits operations via an HTTP/JSON API. See [09](09-robots-gadgets-api.md). |
| **Gadget** | An embedded mini-app represented as a document element with synced state. See [09](09-robots-gadgets-api.md). |

## Component map

```
                          ┌──────────────────────────────────────────┐
                          │                WAVE SERVER                │
   Browser (GWT client)   │                                           │
   ┌──────────────────┐   │   ┌───────────────┐    ┌───────────────┐  │
   │ Editor (OT-aware) │   │   │ ClientFrontend │    │  WaveletState │  │
   │ Conversation view │◄──┼──►│  (per-client   │◄──►│  / WaveMap    │  │
   │ Search/inbox UI   │   │   │  channels,     │    │  (apply+OT,   │  │
   │ Client-side CC    │   │   │  fan-out)      │    │  versions)    │  │
   └──────────────────┘   │   └───────────────┘    └──────┬────────┘  │
        ▲  WebSocket       │          ▲                    │           │
        │  (JSON-encoded   │          │ WaveBus            │ append     │
        │   protobuf)      │          │ (delta events)     ▼           │
        │                  │   ┌──────┴───────┐    ┌───────────────┐  │
        │                  │   │  Robots /     │    │  Persistence  │  │
        │                  │   │  Search /     │    │  (Delta/      │  │
        │                  │   │  Attachments  │    │  Account/     │  │
        │                  │   └───────────────┘    │  Attachment   │  │
        │                  │                        │  stores)      │  │
        │                  │   ┌───────────────┐    └───────────────┘  │
        │                  │   │  Federation   │ (no-op in this build) │
        │                  │   │  (XMPP)       │────────────► remote   │
        │                  │   └───────────────┘             servers   │
        │                  └──────────────────────────────────────────┘
        │
   Auth: login servlet → session cookie → bound to WebSocket connection
```

Mapping to specs: editor/client [10]; wire/RPC [04]; ClientFrontend + WaveletState
[06]; OT [02]; concurrency control [03]; persistence [05]; robots/gadgets [09];
search [11]; attachments [12]; federation/crypto [07]; auth/accounts [08]; the
data model underpinning all of it [01].

## End-to-end flows

These are the flows that no single subsystem spec owns in full. Each step cites
the spec that details it.

### A. Opening a wave

1. Client authenticates; the session is bound to the WebSocket connection ([08]).
2. Client sends an open request (channel open) for a wavelet ([04]).
3. Server's ClientFrontend resolves the wavelet, loads its state (replaying the
   delta history from storage if not cached), and sends an initial **snapshot**
   plus a channel id ([06], [05]).
4. Server registers the client on the WaveBus so subsequent deltas stream to it
   ([06]).
5. Client builds its model and renders the conversation; the editor binds the
   document model to the DOM ([10]).

### B. A local edit becomes a broadcast, then committed, delta

1. User types/formats; the editor's typing extractor turns the DOM change into a
   **DocOp** and applies it optimistically to the local model ([10], [02]).
2. Client concurrency control queues the op, assigns it the client's current
   wavelet version, and sends it as a **delta** (at most one delta in flight per
   wavelet) ([03]).
3. Server receives the delta at a claimed version. It **transforms** the delta
   against any deltas applied since that version, checks the history hash, and
   applies it — advancing the version by the number of ops ([03], [02], [06]).
4. Server broadcasts the transformed delta to all connected WaveBus subscribers
   (which drives client fan-out, search reindexing, and robot events) and
   acknowledges the submitter with the resulting version. This happens
   immediately after apply, **before** the delta is durable; the broadcast and
   ack do not wait for storage ([06], [03]).
5. Server then persists the applied delta to storage **asynchronously**; only on
   the persist callback does it mark the wavelet committed to that version and
   emit a separate **commit notification** (a distinct, later WaveBus event, not
   part of the synchronous submit path) ([05], [06]).
6. Each receiving client transforms the incoming server delta against its own
   unacknowledged ops and applies it, converging ([03]).
7. Side-effect subscribers react: search reindexes the wave for affected
   participants ([11]); robots in the wavelet get events ([09]).

### C. Identity & access

- Every connection carries a `ParticipantId` established at auth time ([08]).
- A wavelet's access check is pure participant-set membership; the `@domain`
  shared-domain participant grants domain-wide access (both read and
  delta-authorship), since the submit/write path uses the same participant-set
  check as reads (`WaveServerImpl.submitDelta` → `WaveletDataUtil.checkAccessPermission`)
  ([06], [08], [01]).
- A version-0 (empty) wavelet grants access to any authenticated participant: the
  access check requires a non-null `ParticipantId`, so an unauthenticated (null)
  requester is rejected even for empty wavelets. The first delta creates the
  wavelet and its participant set implicitly ([06]).

## The invariants that span subsystems

A correct reimplementation must preserve these regardless of how it factors the
code. Each is detailed in the cited spec; collected here because violating one
breaks behavior far from where the bug lives.

1. **Version counts operations, not deltas.** `resultingVersion = appliedAt +
   opCount`. Every layer (client CC, server apply, storage index, wire) assumes
   this. ([01], [03], [05])
2. **History hash chains every applied delta.** `hash(0) = UTF-8(wavelet URI)` —
   the raw UTF-8 bytes of the wavelet URI string, with **no** digest applied. The
   URI has the form `wave://<encoded wavelet name>` (scheme =
   `IdConstants.WAVE_URI_SCHEME` = `"wave"`), e.g.
   `wave://privatereply.com/wave.com!w+4Kl2/conversation+3sG7`; those bytes are
   stored verbatim in `HashedVersion.historyHash` at version 0. For `n > 0`,
   `hash(n) = SHA-256(prevHash ‖ appliedDeltaBytes)[0:20]` (the first 160 bits of
   the digest). Because version 0 applies no hash, its `historyHash` is
   variable-length (the URI byte length); the fixed 20-byte form only holds for
   version >= 1. A version-number match with a hash mismatch is a hard error, not
   a merge. ([01], [03], [07])
3. **Transform guarantees TP1 convergence**, with the **client biased first** for
   concurrent insertions and for overlapping attribute writes. Single-server
   Wave needs only TP1, not TP2. ([02])
4. **At most one client delta is in flight per wavelet channel.** Client ops past
   the in-flight one wait, merged per author. ([03])
5. **Identity is `name@domain` end to end.** No opaque IDs; sessions, robots,
   access checks, and federation all key off `ParticipantId`. ([01], [08])
6. **Everything is a document mutated only by ops** — conversation structure,
   gadget state, and per-user supplement state included. There is no privileged
   side channel that mutates wavelet content. ([01], [09])
7. **Storage is append-only delta history; state is derived by replay.** The
   on-disk index is a rebuildable cache, never the source of truth. ([05])

## Cross-cutting decisions for the Go rewrite

These are recorded here so the porting plan ([../](.) and task list) can reference
a single source. They are derived from the subsystem specs' "Open questions"
sections; the planning docs will ratify them.

- **Federation: drop it (cleanly).** The XMPP transport was never shipped; the
  server already runs with a no-op federation module. Keep the provider/listener
  interface seams and the federation protobuf types (they appear in stored applied
  deltas), make signing/verification pass-throughs, and defer real federation
  indefinitely. ([07])
- **Storage: SQLite single-machine.** The store interfaces are fully
  backend-agnostic; implement them over SQLite. Likely add **snapshots** (the
  Java server replays full history on every load — fine for a frozen project,
  wasteful for a fresh one). ([05])
- **Auth: modernize.** Preserve the `name@domain` identity model, type-distinct
  robot accounts, and the session→connection binding; replace SHA-512 password
  digests with bcrypt/Argon2, drop JAAS for direct Go logic, and replace the
  Jetty session cookie with signed tokens (which also removes the
  `ProtocolAuthenticate` workaround). ([08])
- **Wire: keep the protocol, reconsider the encoding.** The browser speaks a
  custom JSON encoding of protobufs (field numbers as string keys) over a
  WebSocket text frame with a `{sequenceNumber, messageType, message}` envelope.
  A Go server can serve this exact encoding; whether to keep it or move the new
  frontend to a cleaner encoding is a planning decision. ([04])
- **Frontend: rebuild required, strategy open.** The GWT client cannot be reused.
  The editor (model↔DOM dual representation, IME/composition correctness,
  selection preservation across remote ops) is the irreducible hard part. The
  three live options — port the editor to TypeScript, build a TS SPA over the
  existing protocol, or adopt ProseMirror/Tiptap with a Wave document adapter —
  are weighed in the web-client spec and must be decided in the porting plan. ([10])
- **Robots/gadgets: defer.** Large surface, not needed for core collaboration.
  The interface seam (events out, operations in) is clean enough to add later. ([09])

## Open questions / ambiguities

System-level questions that cross subsystem boundaries (each subsystem spec also
has its own list):

- **Snapshot strategy.** No snapshots exist today. The rewrite should decide
  snapshot cadence vs. pure replay before the storage schema is fixed, because it
  changes the schema. ([05], [06])
- **Cache eviction vs. live subscriptions.** The Java WaveMap can evict a wavelet
  that still has subscribed clients; the consequences for client version tracking
  are not fully characterized. The rewrite should define this explicitly. ([06])
- **Collaborative-cursor transmission.** Whether selection annotations travel
  over the op stream to other clients or stay client-local was not conclusively
  determinable; it affects both the wire protocol and the server's op filter. ([10], [04])
- **Read-state location.** Unread counts currently require decoding the supplement
  from UDW document XML. A dedicated SQL table is likely cleaner; this is both a
  storage and a search decision. ([11], [05])
- **`bytes` field encoding** in the JSON wire format is unverified (likely
  base64). Must be pinned down if the new frontend reuses the encoding. ([04])

## Source references

This overview synthesizes the twelve subsystem specs in this directory; it cites
no Java directly. For Java references, see the "Source references" section of each
subsystem spec. Top-level entry points worth knowing:

- `wave/src/main/java/org/waveprotocol/box/server/ServerMain.java` — process boot.
- `wave/src/main/java/org/waveprotocol/wave/model/` — the data model and OT.
- `wave/src/main/java/org/waveprotocol/box/server/waveserver/` — wavelet state & apply.
- `wave/src/proto/` — all wire and storage protobuf definitions.
- `wave/config/reference.conf` — the full configuration surface.
