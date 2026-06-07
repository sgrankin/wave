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

### Progress & decisions (live, as of 2026-05-29)

What's on the fork's `main`, and how execution deviated from the plan above:

- **Done & pushed:** Phase 0; 1a (`op` + transform); 1b (`waveop` + transform);
  2 (`wavelet` apply, `conv` manifest, `doc` reader, worthiness); 3 *server side*
  (`cc` transform-to-head + `MemoryHistory`); **4 fully** (`storage`/`sqlite`
  delta log + lookups/delete/get-by-end-version, accounts store, `blobfs` +
  attachment store, **snapshots** — snapshot+tail load with full-replay fallback,
  snapshot-based join); **5 fully** (`server` apply pipeline + wave map + fan-out
  + join; `transport` framed-CBOR session protocol + `Client`; `cmd/waved` with
  unix/tcp/stdio listeners, slog, expvar/health, graceful drain; `cmd/wavectl`).
- **`internal/codec`** — canonical CBOR (fxamacker, RFC 8949 Core Deterministic)
  for the hash chain + storage blobs + the wire delta payloads. Chosen over
  reproducing Java's `ProtocolAppliedWaveletDelta` (federation dropped → no
  byte-compat need). See architecture invariant #2.
- **`internal/snapshot`** — separate, *non-frozen* snapshot encoding (snapshots
  are a rebuildable cache). Snapshots are **opt-in** (`server.WithSnapshots` /
  `waved --snapshot-every N`); default off keeps the history-replay join. The
  snapshot-based join (current-state snapshot + live stream) is the bootstrap the
  eventual browser client will use.
- **6 fully (auth):** `internal/auth` — stateless HMAC signed-token sessions, a
  pluggable provider chain (`Local` + `TrustedHeader`; tsnet/OIDC/passkey slot in
  behind `Provider`), register-on-first-use `Provisioner`, and HTTP
  `Service`/`Middleware`. Self-contained; live wiring into the transport/HTTP
  path lands with the client phase.
- **7 fully (search + attachments):** `doc` text/title/snippet projection;
  `internal/search` — per-user inbox index maintained off the commit event
  (`server.WithIndexer`, `waved --index`) + FTS5 full-text search with
  in:/with:/creator:/orderby: operators, all inbox-scoped (access-controlled) +
  `Rebuild` from the log; `internal/attachapi` — attachment upload/download/
  thumbnail HTTP serving gated by wavelet participation.
- **Phase 3 / client-side CC (`#11`) — DONE** (2026-06-06). Server CC
  (`cc.TransformToHead` + error taxonomy + double-submit dedup) plus the full
  client side: `internal/clientcc` is a pure (no-I/O) optimistic state machine
  (one in-flight delta + queue, transform-incoming, option-1 ack/gap settling),
  validated by a 50-seed convergence fuzz and adversarially reviewed (two
  criticals fixed: op-count version basis, zero-op-ack settle). The server gained
  self-suppression + a `Resync` handshake; `transport.OptimisticClient` is a
  reconnecting supervisor that resyncs on drop/nack, recognizing its own committed
  delta in the resync tail by a per-submission **nonce** (persisted, so recovery
  is crash-safe) or re-submitting an uncommitted one. Recovery validated with
  `testing/synctest` (reconnect-resync, server-restart) and `-race`. Deferred (not
  blocking): queue merging (optimization), an optimistic snapshot-open test, and
  literal ports of the Java client-CC suites (`OT3Test`/`OperationQueueTest`/
  `ClientAndServerTest` — they don't map 1:1 to our API; their behaviors are
  covered by the resync/reconnect/convergence tests).
- **`internal/doc` is a read projection, not the indexed model** (the lean
  `Apply = Compose` decision — see Phase 2 below and Deferred).
- **Conformance suite (`#10`) — DONE** (2026-06-06, via a fan-out workflow). The
  Java OT/CC tests are ported as Go conformance tests across `op`/`waveop`/`cc`
  (57 pass, 11 documented skips), including an **independent reference-transformer
  oracle** cross-checked against `op.Transform` over 7000 random pairs. One
  low-severity, documented divergence: our normalizing builder omits Java's
  annotation-state elision (`op/builder.go`) — representation-only, does not
  affect apply semantics or convergence. Follow-up `#7`: port the wavelet
  *forward-apply* halves (participant/blip/metadata) into `internal/wavelet`
  (they're implemented there; the `waveop` suite skips them for lack of a data
  model in that package).
- **Remaining: Phase 8** — the browser transport + frontend (`#4`/`#5`). The
  backend is now functionally complete through client-side CC: a headless
  optimistic client converges and recovers over the wire. The client wire contract
  is pinned in [03-delta-channel-protocol.md](03-delta-channel-protocol.md).
  **Transport decided: WebSocket + framed-CBOR** (see Phase 8 below for the full
  rationale + h2/h3 note); 8a is the WebSocket server adapter + a JS port of
  `OptimisticClient`, 8b is the editor rebuild. Follow-ups `#7` and the `#11`
  deferrals are **done** (2026-06-06); the backend is wrapped up. The **8a server
  WebSocket adapter is done** (`#4`, 2026-06-06; see Phase 8 below) — next is the
  JS `OptimisticClient` port (`#9`) — **also done** (2026-06-06; see Phase 8 below):
  the TS client converges with the Go server over a real WebSocket. **8b, the Lit
  editor (`#5`), is in progress** (2026-06-06): the collaborative conversation editor
  — recursive thread/blip view, replies, bold/italic — works browser-to-browser and
  has a committed Playwright convergence test (see Phase 8 below).

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
- **Status:** server CC (`TransformToHead`, `MemoryHistory`, error taxonomy +
  TOO_OLD) done & pushed. Client-side CC, reconnection, and the ported
  `concurrencycontrol` suite are deferred (see Progress & decisions).

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
- **Status:** done & pushed. Delta log (`transformed_blob` = canonical CBOR;
  federation `applied_blob` dropped, re-addable) + lookup/delete/get-by-end-version;
  accounts store (JSON column); `blobfs` + attachment store; snapshots
  (snapshot+tail load with full-replay fallback, opt-in).

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

### Phase 8 — Browser transport + frontend (separate effort)
- **Transport — DECIDED 2026-06-06: WebSocket** (binary frames; Go server via
  `coder/websocket`). Rationale: our logical channel *is* an ordered/reliable/
  bidirectional message stream (open/resync/submit/ack/update), which is exactly
  WebSocket's native contract; and `transport.OptimisticClient`'s
  `dial func() (io.ReadWriteCloser, error)` seam makes the transport a **leaf
  adapter**, so the existing headless client-CC tests carry over to the browser
  client and WebTransport/SSE stay cheap to add later. Second choice
  **WebTransport/HTTP-3** (Baseline since 2026-03, but draft-tracking Go lib +
  UDP/QUIC ops + needs a WS fallback); fallback **SSE↓+POST↑** for proxy-hostile
  networks, behind the same `dial` seam. Long-poll / negotiation: skip.
- **Encoding — keep framed-CBOR** (the `internal/transport/message.go` envelope)
  over the WebSocket, NOT protobuf/JSON: the Go reference client and the JS client
  then speak one wire format, so the headless `OptimisticClient` tests stay
  authoritative. (Hash-chain CBOR is internal and independent of this.) Add a
  dev-only JSON/decode hook since binary frames are opaque in DevTools.
- **h2/h3 coexistence (verified):** WebSocket is its own connection and does NOT
  preclude serving pages/API over h2/h3. Go's `http.Server` over TLS auto-
  negotiates h1.1+h2 on one TCP listener via ALPN; the WS rides an h1.1 connection
  (WS-over-h2 RFC 8441 / WS-over-h3 RFC 9220 aren't implemented by Go stdlib or
  `coder/websocket`, and browsers fall back to h1.1 transparently). h3 for page/
  asset delivery is an optional later add (separate UDP listener via quic-go) and
  never touches the realtime path. Net: one TCP listener / one port covers it.
- **Build order:** (8a) WebSocket `transport` adapter on the server + a JS client
  port of `OptimisticClient` (same envelope, same open/resync/submit/ack/update
  state machine; reconnect+resync+nonce reconciliation per
  [03-delta-channel-protocol.md](03-delta-channel-protocol.md)). (8b) the editor /
  document-model rebuild — its own design+build track (strategy per spec
  [10](../specs/10-web-client.md)), out of scope beyond reserving the seam.
- **8a server adapter — DONE 2026-06-06** (`#4`). `internal/transport/websocket.go`:
  `Server.WebSocketHandler` upgrades via `coder/websocket` and wraps the WS as an
  `io.ReadWriteCloser` (`NetConn`, binary frames) handed to the existing
  `serveConn` loop — framing, fan-out, and the headless client/tests ride it
  unchanged. The upgrade is gated by an `identify(*http.Request)→(participant,ok)`
  hook (401 before Accept; mount behind auth middleware, cf. `attachapi`); the
  authenticated participant is **bound to the session** so a client cannot author
  deltas as another (mismatch → nack). `DialWebSocket`/`WebSocketDialer` are the
  client dial seam; `Server.Shutdown` drains hijacked WS conns; a per-conn
  keepalive ping reaps vanished peers. Tested over a real `httptest` WebSocket
  (round-trip, two-client converge, unauthorized-rejected, author-mismatch-nacked,
  shutdown + concurrent-dial race). Wired into `waved` behind `-ws`. Next: JS
  `OptimisticClient` port (`#9`, now unblocked).
- **Auth + access control + seeding — DONE 2026-06-07** (`#10`; see
  [04-auth-model.md](04-auth-model.md) §9). `/socket` and `/whoami` mount behind
  `auth.Service.Middleware` (session cookie verified before the WS upgrade); the
  dev `-ws-user`/`?user=` stub is gone (the browser learns its address from
  `/whoami` and rides the cookie). `/login` is a dev trust-any endpoint (loopback-
  guarded) or a proxy trusted-header login; the session signing key persists via a
  new `storage.SettingsStore`. Wavelet **membership** is gated at Open and Resync by
  a `transport.AccessChecker` (`MembershipChecker`, strict; nil = dev-permissive),
  with **open-or-create + server-side conversation seeding** (`conv.SeedConversation`
  + atomic `WaveletContainer.SeedIfEmpty`) replacing the client `maybeBootstrap` and
  killing the cold-start race. `-auth dev|proxy`, `-seed-conversations`.
- **App shell & wave management — DONE 2026-06-07** (`#14`). The single-hardcoded-
  wave client became an app: a new `internal/queryapi` serves authenticated
  `GET /api/inbox` and `GET /api/search` (over the existing `internal/search`
  index), turning index hits into wave "digests" (title/snippet/participants) read
  under the container lock via the new `WaveletContainer.Read`. The client gained a
  two-pane `<wave-app>` shell (`<wave-list>` search/inbox on the left, the active
  `<wave-conversation>` on the right, switched via lit `keyed`), `?wave=` URL
  navigation, and client-minted new-wave ids (`waveid.ts`) that the server seeds on
  open. Validated by `internal/queryapi` unit tests + a Playwright app-shell e2e
  (new-wave → seed → index → inbox → search) alongside the convergence e2e.
- **Collaboration completeness — CORE DONE 2026-06-07** (`#12`, `#15`). Three of the
  four Batch-3 features shipped: **inline replies** (a `<reply id>` anchor in the
  parent blip body + manifest `inline=true`; `conv.InsertReplyAnchor`/`ReadReplyAnchors`
  with a TS mirror, surfaced in the editor as a caret-safe marker anchored at the
  caret's line boundary — see the §"inline replies" note below); **read/unread** (a
  dedicated `storage.ReadStateStore`, an `unread` flag on inbox/search digests, and
  `POST /api/read`, with a client unread indicator + mark-on-view + a 5s inbox poll);
  and **mentions** (render-time `@address` highlighting, self emphasized). **Presence**
  (live carets/typing) is deferred (`#20`): the live-content collaboration already
  converges char-by-char, so presence is an awareness overlay that needs a separate
  transient (non-OT) broadcast channel — a distinct subsystem, designed but not yet
  built.
- **Rich content — CORE DONE 2026-06-07** (`#16`). Auto-linking of URLs in blip
  text (a render-time decoration, like mentions); `internal/attachapi` MOUNTED on
  the browser server (`-attach-root`; behind `auth.Service.Middleware` +
  `transport.MembershipChecker`, integration-tested); and inline image attachments
  — an `<image attachment=id>` element (`conv.InsertImage`/`ReadImages` + TS mirror)
  rendered caret-safely as an `<img>` from `/attachments/<id>` (reusing the
  reply-anchor line-boundary marker pattern), with an Attach button that uploads the
  file and inserts the element. A Playwright e2e uploads a real PNG and asserts it
  loads from the server through auth + membership. Deferred (minor): link
  previews/embeds and a manual-link toolbar (auto-linking covers the common case).
- **Profiles & contacts — DONE 2026-06-07** (`#17`). Addresses are humanized into
  display names + initials avatars everywhere they surface. Server: a `DisplayName`
  field on `HumanAccount` (zero-migration — the account record is JSON-encoded in one
  column) and a new `internal/profileapi` serving `GET /api/profiles?addr=…` (batch
  resolve, one entry per valid address incl. unknowns so the client caches and never
  refetches) and `POST /api/profile` (set **own** name; identity from the session,
  never the body; trimmed, 128-rune cap, auto-provisions a human account, refuses
  robot accounts). Mounted at the specific `/api/profile(s)` paths so they win over
  queryapi's `/api/` subtree. Client: `wave/profiles.ts` — a `ProfileCache` that
  coalesces lookups (components call `ensure()` per render; one batched fetch per
  microtask; converges) and fires `"change"`; deterministic pure helpers
  (`displayNameFor`/`initialsFor`/`colorFor`); `editor/participant.ts` avatar+chip
  render helpers; a `<wave-identity>` widget (avatar + name + inline editor) in the
  shell. Roster chips, the inbox meta line, and the identity widget all humanize and
  subscribe to the cache; the add-participant box gained a contact-picker `datalist`.
  The address stays the identity (empty name ⇒ falls back to the address). Validated:
  `profileapi` Go tests, `profiles.ts` unit tests (cache coalescing/fallback/setOwn),
  the roster + identity component tests, and a Playwright e2e (set name → identity +
  roster humanize with a derived avatar → persists across reopen). **Deferred
  (minor):** @-mention display-name tooltips — left out deliberately so the
  caret-rune-mapped blip editor's visible text stays literal (the cited invariant);
  and uploaded-image avatars (initials avatars need no upload/serve infra).
- **Signature features — DONE 2026-06-07** (`#18`; design doc
  [06-agent-channel-and-playback](06-agent-channel-and-playback.md)). Three pieces:
  **Playback** — `WaveletContainer.StateAt(version)` reconstructs a past delta-boundary
  state by replaying the log onto a fresh wavelet (independent of live state,
  hash-verified per step; `server.ErrNoVersion` sentinel distinguishes a bad version
  from a storage fault) + `DeltaHeaders()` timeline; `internal/playbackapi`
  (`/api/playback/deltas`, `/api/playback/state`, membership-gated, renders to plain
  text so no DocOp wire codec); a read-only `<wave-playback>` scrubber toggled by an
  Edit/History switch. **Agent channel** (LLM agents as participants over the OT
  client) — `internal/agent`: a semantic event layer (`Extract` ops→events:
  BlipAdded/Edited, Participant±, Mention), an intent translator (`Translate`
  intents→ops via conv builders), an in-process `LocalClient` (subscribe + `SubmitFrom`
  with self-exclude = loop-safe self-suppression, + a defense-in-depth submit rate
  limiter), a `Runtime` loop + `EchoHarness` reference agent, and a `Gateway`
  bridge (wave.opened snapshot + events out / newline-JSON intents in over any io);
  `internal/agentgw` exposes it over WebSocket (`/agent/socket`, bearer-token auth via
  `StrictMembershipChecker` — no open-or-create for agents — wired by `-agents`).
  **Gadgets:** dropped (dead OpenSocial spec; `<gadget>` treated as opaque); the legacy
  robots JSON-RPC/OAuth API is superseded by the agent channel, not ported. Validated:
  Go race tests at every layer (StateAt boundaries, event/intent units, the echo loop
  proving mention→reply + self-suppression, the gateway over pipes, a real
  coder/websocket harness end-to-end) + a Playwright playback e2e; then an adversarial
  review (4 dims → verify, 23 confirmed/0 refuted, no critical) whose findings were
  fixed (open-or-create authz hole, stale event version, snapshot double-report,
  404-vs-500, rate limit + corrected loop-safety doc, mention word-boundary,
  header-only token). Deferred (see doc §A.9): stdio gateway transport, hashed
  per-account tokens, container-cache eviction, agent add-participant allowlist,
  mid-session membership revocation, wire `seq`.
- **Operability & deployment — DONE 2026-06-07** (`#19`). Deployment-readiness
  hardening on top of the existing structured `slog` logging + expvar metrics +
  signal-driven graceful shutdown. **Packaging:** `web/embed.go` embeds the built
  client behind a `-tags embed` build tag (`web/embed_stub.go` is the default
  no-embed half); waved serves the embedded client (`http.FileServerFS`) when present,
  else `-webroot`. `make release` builds `web/dist` then a single CGO-free
  `waved` binary — verified it serves the client with no `-webroot`. **Health:**
  `/readyz` pings the DB (`sqlite.Store.Ping`) → 503 when unreachable, distinct from
  `/healthz` liveness; added `/version` + a `wave_build_version` expvar. **Config:**
  env-var fallback for any unset flag (`WAVED_<FLAG>`), explicit flags win — 12-factor
  friendly. **Notifications:** a guarded browser desktop notification for newly-unread
  inbox waves (permission on a user gesture, dedup, silent first-load seed); email/push
  is an outward-facing concern, deferred. Validated: Go tests (readyHandler 200/503,
  env-default vs explicit-flag-wins), default + `-tags embed` builds, the embed binary
  serving the client, web typecheck/unit/component + the Playwright e2e, and a
  fresh-eyes code review (one notification-dedup footgun pre-empted with a guard +
  doc — notifications are inbox-only by construction).
- **Presence — DONE 2026-06-07** (`#20`; design doc
  [07-presence](07-presence.md)). A **transient** awareness channel (who is here /
  typing / focused blip), deliberately **separate** from the OT delta socket so a lossy
  signal can never perturb convergence. Server `internal/presence`: a `Hub` (room per
  wavelet, non-blocking drop-on-full fan-out, nothing persisted) + a `/presence`
  WebSocket that binds the authenticated participant (server-stamped, client identity
  ignored), gates by the same access policy as the OT socket (nil dev-permissive /
  `MembershipChecker` in proxy), sends a join snapshot, and keepalive-pings to reap
  half-open sockets; a server-lifetime base context drains all sockets on shutdown.
  Client `wave/presence.ts` (throttled sends, reconnect) + a `<wave-conversation>`
  presence bar (avatar per online peer, dimmed unless typing, "… is typing"). Validated:
  hub white-box + a real two-client WebSocket round-trip (online/typing/snapshot/depart,
  401/403) under `-race`, a Playwright e2e (a peer seen present + typing), and a
  fresh-eyes review (the writer/reader lifecycle verified leak-free; the two flagged
  gaps — half-open reaping + shutdown drain — fixed). **Deferred (flagged, doc §5):**
  pixel-exact remote caret bars — they touch the editor's caret-mapping invariant and
  warrant a separate reviewed increment; v1 ships the lower-risk blip-granular awareness
  + typing indicators.
- **8a JS client — DONE 2026-06-06** (`#9`). A TypeScript port of the whole client
  stack under `web/` (esbuild + Lit + `node --test` via Node 26 type-stripping):
  the shared model (`types.ts`), CBOR wire subset, op algebra (compose/transform/
  invert/normalize), wavelet-op + delta transform, codec, the framed-CBOR envelope,
  the `clientcc` state machine, and the reconnecting `OptimisticClient` over the
  browser `WebSocket`. **The client needs no hash chain** (`clientcc` takes resulting
  `HashedVersion`s from the server; it never runs `version.Apply`) — a major scope
  cut. Validated three ways: (1) 196 property-based fixture vectors generated by the
  Go reference (`cmd/genfixtures`, canonical-CBOR hex — tests the TS codec AND
  algebra against the oracle); (2) a 40k-case adversarial differential review of the
  OT port (transform/compose/waveop/docop) vs the Go — **zero mismatches**, faithful
  incl. annotation null-vs-absent, surrogate-pair rune boundaries, updateAttributes
  owner-wins, participant resolution; (3) a Node↔Go integration test (`web/test/`):
  builds + spawns real `waved -ws`, connects TS clients over a real WebSocket, and
  passes single-client round-trip, history replay, and **two-client concurrent
  convergence** (alice prepends / bob appends → `AhiB`). One integration-found wire
  bug fixed (Go nil `[]byte` → CBOR null, not empty bytes). Built via a
  contract-driven background workflow (fan-out of 8 modules against a fixed
  `types.ts` + fixtures). Next: 8b — the editor / document-model (`#5`), Lit-based.
- **8b editor — IN PROGRESS** (`#5`, 2026-06-06). Built and validated live in
  headless Chrome (via Playwright):
  - **Collaborative editor, browser-to-browser**: two clients on one wavelet, edits
    converge on open (history replay) and live, through the real Go server. First a
    flat-text MVP (contenteditable + diff→op), then a **rich line-based editor**.
  - **Document model** (`web/src/editor/blipdoc.ts`): project a blip content DocOp
    (Wave `<body>`/`<line>` line-container + `style/*` annotations) into paragraphs
    of styled spans with caret↔doc-offset mapping; pure command builders
    (insert/replace/delete text, splitLine=Enter, deleteLineMarker=line-merge),
    verified through the ported composer. `blip-view.ts` is a **controlled
    contenteditable** Lit element: render the projection, translate `beforeinput`
    into content ops (no diffing), preserve the caret across re-renders.
    `cmd/waved -webroot` serves the esbuild bundle same-origin with `/socket`.
  - **Conversation view — DONE** (`web/src/editor/{controller,wave-conversation,
    wave-thread,wave-blip}.ts`): the manifest drives a recursive
    `<wave-conversation>`→`<wave-thread>`→`<wave-blip>` tree. `<wave-conversation>`
    owns the `OptimisticClient`, bootstraps an empty wavelet into a one-blip
    conversation, and authors edits/replies through a `ConvController` the tree edits
    through (so the views never touch the connection). Reply (start a reply thread on
    a blip) and continue-thread (append a blip to a thread / new root message) each
    submit one delta = manifest mutation + new-blip init. Built on the ported
    **conversation/manifest model** (`web/src/wave/doc.ts` reader + `conversation.ts`,
    port of `internal/doc` + `internal/conv`), which gained general thread authoring
    (`AppendBlipToThread`/`ReplyToBlip`, Go + TS, symmetric, both tested).
  - **Rich-text formatting**: bold/italic via Cmd/Ctrl+B/I (the `formatBold`/
    `formatItalic` `beforeinput` events), toggling `style/*` annotations over the
    selection using pure command builders (`setStyleRange`/`clearStyleRange`/
    `setLineType`/`rangeStyle` in `blipdoc.ts`). A line-type toolbar UI is still TODO.
  - **Testing — three layers, all wired**: pure logic (`node --test` vs the Go
    composer/fixtures); **components** via the vendored harness (`web/testing/`,
    sgrankin/cs) — Lit rendering in headless Chromium (`npm run test:web`), incl.
    conversation-tree render + controller-wiring tests; **end-to-end** browser
    convergence (`npm run test:browser`) — a committed Playwright test driving the
    real UI against a real `waved`, asserting a fresh client converges (covers the
    create-then-edit-a-blip case + threaded reply). Node vs browser tests separated
    by their `node:` imports. Scenarios use a small harness
    (`web/test/browser-harness.ts`: `client`/`typeInto`/`clickReply`/
    `waitForBlipTexts`); `make check` runs Go + web type/unit/component, `make
    check-all` adds the browser e2e.
  - **Observability** (added so the next convergence bug is one grep, not ten
    steps): the server logs the delta flow at `debug` — `delta submitted/applied/
    delivered`, keyed by `wavelet` + `nonce` + version (the nonce correlates a
    submit through apply to fan-out). The client mirrors it: `?debug=1` turns on a
    console delta-trace (submit → cc.edit → sendDelta → ack) and a state overlay
    (`<wave-debug>`: version/inflight/queue/blips), and exposes the client at
    `window.__wave`. (Deliberately NOT OTel — single machine; the existing nonce is
    the correlation id, structured logs get ~80% of the value.)
  - **Four real bugs found via the browser loop** (none caught by unit tests):
    empty-doc projection/DOM mismatch (offset null → native edit), a Lit
    reactive-field-initializer shadow that killed re-render, a Lit comment marker
    counted as text when mapping the caret, and — the big one — a controlled
    contenteditable that returned from `beforeinput` *without* `preventDefault` on a
    mapping miss, letting the browser edit natively so the edit showed locally but
    was never submitted (surfaced as: typing into a just-created blip never reached
    other clients; the caret had landed in a stray text node at the editable root).
  - **Formatting toolbar — DONE**: a focus-shown toolbar in `<blip-view>` (B/I/H1/
    H2/H3/bullet/plain), controlled (ops via the emit path, no execCommand), with the
    mousedown-preventDefault fix and selection-preserving formatting (a `pendingSelection`
    range restored in `updated()`). Validated live (H1 applies + converges).
  - **Participants UI — DONE**: a roster + add-participant in `<wave-conversation>`
    on `OptimisticClient.participants()` / `ConvController.addParticipant`; bootstrap
    adds the creator as the first participant (a 3-op delta). The membership basis for
    `#10`. Validated live (add converges to a fresh client).
  - **Remaining for #5/editor**: **inline replies** (`#12`, anchored within blip
    text — the meatier one: needs an anchor element in the doc model + `project()`
    surfacing it + a Go mirror). A cold-start race (two clients bootstrapping one
    empty wavelet at once) is noted in `wave-conversation`; the real fix is
    server-side conversation seeding (folded into `#10`).

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
