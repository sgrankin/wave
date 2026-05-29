# 02 — Go Porting Plan

Status: **proposed** (2026-05-28), revised after adversarial panel review.
Executes the target architecture in
[01-target-architecture.md](01-target-architecture.md) against the behavior specs
in [`../specs/`](../specs/). Strategy: **backend-first**, dependency-ordered,
with the original Java tests ported as a conformance suite.

## Guiding principles

- **Port behavior, not structure.** The specs are the contract. Match Java
  *semantics* (especially OT convergence and the hash chain) exactly; write
  idiomatic Go, not transliterated Java.
- **The Java tests are an executable spec.** 351 test files exist; the OT,
  document, version, wavelet-op, and concurrency-control suites are the
  convergence contract. Port them as Go conformance tests *before/with* each
  subsystem.
- **Each phase ends green and demoable.** A phase isn't done until its tests
  pass and (from Phase 5) it's exercised end-to-end via the stdio client.
- **Vertical slice early.** Reach a working single-wavelet edit loop over stdio
  as fast as the dependency order allows, then broaden.

## Construct mapping (Java/GWT/proto → Go)

| Java construct | Go approach |
|---|---|
| Guice DI / modules | **Hand-rolled constructor wiring** in `cmd/waved`; `google/wire` only if it grows unwieldy (avoid reintroducing codegen). |
| Typesafe Config `reference.conf` | `koanf` (file + env) or stdlib; one `Config` struct. |
| `.proto` (delta-on-disk / retained types) | Keep; regenerate with `protoc-gen-go` into top-level `proto/` (the delta-on-disk **storage/wire schema**, depended on by `internal/storage` and `internal/cc`). **The live wire protocol** is redesigned separately, not these. |
| `.proto` PST / GXP codegen | Drop entirely (build-time Java/GWT tooling). |
| GWT client | Drop; rebuild later (separate effort). |
| Guava collections / `Preconditions` | stdlib + generics; small helpers only where they earn it. |
| Apache Commons (io, codec, fileupload, …) | stdlib (`io`, `encoding/*`, `net/http` multipart). |
| `gson` JSON | `encoding/json` / chosen codec. |
| Checked exceptions (`TransformException`, `PersistenceException`) | `error` returns; sentinel/typed errors for cases callers branch on. The CC layer maps these to wire `ResponseCode`s. |
| JAAS / X.509 client auth | Pluggable auth providers (arch doc). |
| Jetty `HttpSession` | Signed-token session store. |
| `System.currentTimeMillis()` (flagged TODO) | Injectable `Clock` from day one (testability). |
| MongoDB / Lucene / Solr backends | SQLite + FTS5. |
| XMPP federation transport | No-op seam (`internal/federation`); proto types live in `proto/`. |

## Proposed Go module layout

Single module, single server binary. Internal packages mirror the spec
boundaries so a reader can map spec ↔ package 1:1.

```
go.mod  (module github.com/sgrankin/wave)
cmd/
  waved/            # the server binary (wiring, config, listeners, shutdown)
  wavectl/          # stdio/CLI client (test harness + demo client)
internal/
  id/               # WaveId, WaveletId, WaveletName, ParticipantId, IdGenerator   [spec 01]
  version/          # HashedVersion, hash chain                                     [spec 01]
  doc/              # document model: items, annotations, schema/validation         [spec 01,02]
  op/               # DocOp components, apply, compose, invert, transform           [spec 02]   ★ core
  waveop/           # WaveletDelta, Add/RemoveParticipant, WaveletBlipOp,            [spec 02]   ★
                    #   TransformedWaveletDelta, VersionUpdateOp, wavelet-op transform
  wavelet/          # wavelet/blip/conversation model, supplement                   [spec 01]
  cc/               # concurrency control: deltas, client+server CC, versions       [spec 03]
  storage/          # store interfaces + sqlite impl + snapshots + fs blobs          [spec 05]
    sqlite/  blobfs/
  server/           # wavelet container, apply pipeline, wave map, fan-out bus      [spec 06]
  frontend/         # per-client channels, open/submit/subscribe, session binding   [spec 06]
  transport/        # logical session protocol + impls (stdio now)                  [spec 04]
    stdio/          # (ssepost/ webtransport/ websocket/ added at Phase 8)
  auth/             # provider chain, session store, provisioner                    [spec 08]
    providers/      # local, trustedheader (now); tsnet, oidc, passkey (later)
  account/          # account model + store glue                                    [spec 08]
  search/           # per-user view + FTS5 query                                    [spec 11]
  attachment/       # upload/download/thumbnail service                             [spec 12]
  federation/       # no-op provider/listener seams ONLY (proto types are in proto/) [spec 07]
proto/              # retained .proto + generated Go (delta-on-disk schema)
docs/               # specs + architecture (this tree)
```

Package names under `internal/` are proposals, refined as the port proceeds.

## Dependency graph

```
   id ─ version ─ doc ─ op(★) ─ waveop(★) ─ wavelet ─ cc ─ server ─ frontend ─ transport
                   │                          │              │          │
                   └─ (schema/validation)     │            storage ─────┤
                                              │            account ─ auth┘   (auth → account)
   search, attachment, federation(seam)  hang off server/storage.
   proto/  is depended on by storage and cc (delta-on-disk types).
```

`op` + `waveop` (the OT engine: document ops *and* wavelet ops) are the critical
nodes — nearly everything depends on them and they carry the highest correctness
risk, so they come first after the primitives. Note **auth depends on account**
(login fetches `AccountData` by ParticipantId), not the reverse.

## Phases

Each phase lists deliverable + the tests that define "done". Phases 0–6 reach a
**working headless wave server**; 7+ broaden and then tackle UI.

### Phase 0 — Skeleton & primitives
- `go.mod`, layout, CI (build + `go test` + `golangci-lint`, **cross-compiled
  release binaries with `CGO_ENABLED=0`, asserting the FTS5 build**), injectable
  `Clock`.
- `internal/id` (all id types + serialization incl. modern `/` + `~` elision +
  **stateful `IdGenerator`**: synchronized counter, web-safe base64),
  `internal/version` (HashedVersion + hash chain).
- **Tests:** port `model/id` + `model/version` tests; hash-chain golden vectors
  (version-0 = raw URI bytes; n>0 = SHA-256[0:20] over the **applied-delta
  serialization**); `IdGenerator` golden vector against `IdGeneratorImpl`.
- **Done when:** id round-trips, IdGenerator matches Java output, hash vectors
  match the spec.

### Phase 1a — DocOp core ★ (highest risk)
- `internal/doc` (items, annotations, schema/validation, char coercion) +
  `internal/op` (DocOp components, apply, compose, invert, **document transform**).
- **Tests:** port `model/document/operation` (12) + `model/operation` (17)
  suites verbatim; add **property/fuzz tests for TP1** convergence and
  compose/invert round-trips.
- **Done when:** the ported DocOp suite is green and fuzz finds no TP1 violation
  over long runs.

### Phase 1b — Wavelet-op layer ★
- `internal/waveop`: WaveletDelta, Add/RemoveParticipant, WaveletBlipOperation,
  NoOp, VersionUpdateOp, TransformedWaveletDelta, and the **wavelet-op transform**
  (Java `model/operation/wave/Transform.java`) that wraps DocOp transform.
- **Tests:** port the `model/operation/wave` transform tests; convergence of two
  concurrent WaveletDeltas (mixed participant + blip ops).
- **Done when:** wavelet-op transform is green — this is the *other half* of the
  transform surface CC rides on.

### Phase 2 — Wavelet & conversation model
- **Document representation — decided 2026-05-28 (lean backend model):** a blip's
  content is held as a **`DocInitialization`** (an insertion-only `DocOp`) and
  operations are applied via **`op.Compose`** (`Apply = Compose`, already built in
  1a). The backend does **not** port the Java indexed mutable document model
  (`model/document/`). This also lands the `apply()` deferred from 1b: blip ops →
  compose into the target blip's content; Add/RemoveParticipant → participant-set
  mutation; version + hash chain (`version.HashedVersion.Apply`) advance per op.
  See [Deferred → indexed-document model](#deferred) for why this is mostly a
  *drop*, not hidden debt, and what small piece is genuinely deferred.
- `internal/wavelet`: `WaveletData` (participants, blips, version + hashed-version
  chain, creator/timestamps), `BlipData` (id, content `DocInitialization`, author,
  contributors, last-modified version/time), conversation manifest, supplement,
  participants/roles (roles = advisory metadata, unenforced).
- **Tests:** port `model/conversation` + supplement tests; apply round-trips
  (wavelet-op apply == compose on the target blip); participant add/remove.
- **Done when:** can (a) read back manifest + blip structure, (b) **apply a
  `WaveletDelta` to wavelet state** (blip + participant ops, version/hash
  advance), and (c) **generate the ops that create a manifest, append a blip, and
  initialise a blip doc (`<body><line/></body>`)** — the client-authoring
  capability Phase 5 needs.

### Phase 3 — Concurrency control
- `internal/cc`: delta types (client/transformed/applied), client CC (one
  in-flight, queue, ack, transform incoming), server CC (transform-to-head,
  apply, version advance), hash verification, and the **mapping from
  transform/validation errors to wire `ResponseCode`s** (origin of the error
  taxonomy). Owns a lightweight **in-memory `DeltaHistory` + apply core** (reused
  by Phase 5's WaveletContainer, not duplicated).
- **Tests:** port `concurrencycontrol` (14) suite; multi-client convergence
  simulation in-process; version/hash math under reordering.
- **Done when:** simulated concurrent clients converge; error codes are produced
  for the right conditions (VERSION_ERROR on hash/version mismatch, etc.).

### Phase 4 — Storage
- `internal/storage` interfaces + `sqlite` impl: delta log (**full
  `WaveletDeltaRecord`**, applied + transformed blobs, `(wavelet_id,
  resulting_version)` index for `getDeltaByEndVersion`), **snapshots**
  (threshold-based), accounts (JSON column); `blobfs` for attachment bytes.
- **Tests:** store conformance suite (append/range/current-version/enumerate/
  by-end-version); **snapshot+tail replay == full replay, bit-identical**
  (timestamps/hashes read back, not recomputed); crash/restart durability;
  JSON-column round-trips.
- **Done when:** a wavelet persists, reloads via snapshot+tail to identical
  state, survives restart.

### Phase 5 — Server core + stdio transport (first vertical slice)
- `internal/server`: wavelet container (load via snapshot+tail), apply pipeline
  (wavelet-op + OT + version + hash + persist **full record**), wave map, fan-out
  bus. `internal/frontend`: `Open` (per-wave-view; **serialize the wire-level
  initial snapshot**, distinct from the storage blob), submit/subscribe, channel
  mgmt, session binding. `internal/transport/stdio` (logical ops directly — **no
  dispatch-abstraction layer yet**) + `cmd/wavectl`.
- **Derived-index maintenance** (moved here from Phase 7): the `wave_participants`
  view is maintained **off the fan-out/commit event** by scanning applied deltas
  for Add/RemoveParticipant; include a **backfill/rebuild-from-log** path.
- **Graceful shutdown:** signal-driven, explicit ordered sequence (stop listeners
  → drain pending persists → WAL checkpoint → close DB → exit).
- **Operability:** `slog`, the metric set (loaded-wavelet count, apply latency,
  submit error-rate, commit-lag), health endpoint.
- **Tests:** end-to-end over stdio — two `wavectl` clients edit one wavelet and
  converge; **a fresh wavelet is created by the first submitted delta from any
  authenticated participant** (v0 open-access); reconnect/resync; persistence
  across restart; kill mid-stream loses no acked delta after drain.
- **Done when:** **the headless real-time edit loop works end-to-end.** Project
  de-risking milestone.

### Phase 6 — Auth & sessions
- `internal/auth`: session store (signed tokens, CSRF-safe), provider chain,
  provisioner (register-on-first-use → account + ParticipantId only). Providers:
  **local/single-user + trusted-header** first (trusted-header rejects on a
  public listener); tsnet/OIDC/passkey behind the same seam later.
- **Tests:** session lifecycle; each provider maps to the right ParticipantId;
  trusted-header rejected on a public listener; first-use provisioning creates
  account+id with no UDW; consumers tolerate absent UDW.
- **Done when:** a connection authenticates and binds a ParticipantId across
  local + trusted-header.

### Phase 7 — Search & attachments
- `internal/search`: FTS5 query over the per-user view + blip text (`in:`,
  `with:`, `creator:`, `orderby:`); **consumes** the Phase-5 maintenance hook and
  adds its own backfill/rebuild. `internal/attachment`: upload/download/thumbnail,
  bytes on disk, access via wavelet participation.
- **Tests:** query semantics + sort/paginate; "drop view + FTS, rebuild from log,
  inbox/search identical"; upload→download round-trip + access control.
- **Done when:** inbox/search and attachment up/download work end-to-end.

### Phase 8 — Browser transport(s) + frontend (separate effort)
- Pick the browser transport (SSE+POST / WebTransport / WS); build that
  `transport` impl and **extract the transport/encoding seam from the two real
  cases** (stdio + browser). Commit the wire encoding (leaning: protobuf binary).
- **Frontend rebuild** is its own design+build track (editor strategy per spec
  [10](../specs/10-web-client.md)). Out of scope here beyond reserving the seam.

### Deferred
- **Indexed mutable document model** (Java `model/document/`: live editable doc
  with annotation-range indexing, cursors, doc-based views). **Mostly *dropped*,
  not deferred 1:1-port debt** — answering "is this work we'll need anyway?":
  - The **backend doesn't need it.** Blip content is a `DocInitialization` and ops
    apply via `op.Compose` (Phase 2 decision); storing/serving deltas + snapshots
    needs nothing more.
  - The **GWT editor that drove it is being rebuilt** (Phase 8 is a clean frontend
    track, not a port), so the new client gets its *own* editor/document model on
    a modern framework — we will not port `model/document/` verbatim there either.
  - **What is genuinely deferred** is a *lightweight read-only projection* over a
    `DocInitialization` — extract plain text / title, read annotation values —
    needed for **search/indexing (Phase 7)** and possibly client snapshots. That
    is far smaller than the full model and is the *alternative* we build when we
    get there, not a resurrection of the Java model. **Net: no large hidden port
    debt; a small projection replaces a large subsystem.**
- ~~Blip-change "worthiness" (`WorthyChangeChecker`)~~ — **done in Phase 2b**
  (`waveop.IsWorthyChange`/`IsWorthyBlipID`/`UpdatesBlipMetadata`, gating
  metadata updates in `wavelet.applyBlipOp`).
- Robots/Gadgets API (event-out/op-in seam kept).
- Real federation (no-op seams + proto types kept).
- Diff/read-unread *rendering* doc; server-side profile fetch (client/robot
  surface).

## Testing strategy

1. **Conformance suite** — port the Java OT/wavelet-op/model/CC tests as Go
   tests; they are the convergence contract. Keep them beside the package.
2. **Property/fuzz** — TP1 (`apply(apply(D,c),s') == apply(apply(D,s),c')`) for
   both DocOp and wavelet-op transform, compose associativity, invert round-trip.
3. **Golden vectors** — hash chain (over applied-delta bytes), id serialization,
   IdGenerator output, snapshot equivalence.
4. **End-to-end simulation** — multi-client convergence, wave-creation-by-first-
   delta, reconnect, over the stdio transport (from Phase 5).
5. **Durability** — kill/restart mid-stream; drain loses nothing; snapshot+tail
   == full replay; drop-and-rebuild derived indexes == identical.

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| OT transform (DocOp **or wavelet-op**) diverges from Java | Port both test suites *first*; fuzz TP1 on both; Java tests are ground truth. |
| Hash-chain byte-incompatibility (version-0; wrong bytes hashed) | Golden vectors in Phase 0 over the applied-delta serialization; invariant #2 pins the bytes. |
| Replay cost / memory on hot wavelets | Snapshots from Phase 4; never ship full-replay-only. |
| Storing ops-only ⇒ non-bit-identical replay | Persist the full `WaveletDeltaRecord`; replay reads stored timestamps/hashes. |
| Derived index drifts from the log | Indexes are caches maintained off the commit event + rebuildable from log; rebuild test. |
| v0 "must be participant" guard breaks wave creation | Explicit Phase-5 test: first delta from any authed participant creates the wavelet. |
| SQLite single-writer contention | Single serialized write path + WAL; measure; sharding seam exists. |
| Lost acked-but-unfsynced deltas on exit | Ordered graceful-shutdown drain before close (Phase 5). |
| Scope creep into frontend/robots/federation early | Hard phase gates; deferred behind seams. |
| Premature transport/encoding abstraction | stdio against logical ops directly; extract the seam only at the 2nd transport (Phase 8). |
| Trusted-header auth misconfig = bypass | Enforce private-listener requirement in code; prefer tsnet/WhoIs. |

## Definition of done (this plan's scope)

A single Go binary that: serves real-time collaborative wavelets with correct OT
convergence (DocOp + wavelet-op) and a verifiable hash chain; creates wavelets on
first delta; persists the full delta record to SQLite with snapshots and
rebuildable derived indexes; authenticates via at least local + trusted-header
(others pluggable); reports structured wire errors; shuts down without losing
acked deltas; emits logs/metrics; supports inbox/search and attachments; and is
driven end-to-end by a headless stdio client. The browser frontend and the
deferred subsystems build on the seams this leaves in place.
