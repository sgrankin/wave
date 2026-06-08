# 01 — Target Architecture (Go rewrite)

Status: **proposed**, ratified through discussion on 2026-05-28, then revised
after an adversarial panel review (2026-05-28). Supersedes the original
Java/WIAB deployment architecture where noted. The retroactive specs in
[`../specs/`](../specs/) define the *behavior* we must preserve; this document
defines the *system we are building* to deliver that behavior on modern,
single-machine infrastructure.

## Design goals

1. **Single machine, single binary.** A personal / small-team wave server that
   runs as one Go process with a local data directory. No external services
   required to start.
2. **Preserve the hard-won correctness, drop the dead weight.** The
   operational-transform core, the data model, and the identity model are the
   irreplaceable value; reproduce them exactly. Everything bolted on for
   2009-era distributed/federated/GWT reasons is reconsidered or cut.
3. **Interface only where a deployment fork is real** (storage backend, auth,
   eventually transport). Everywhere else: one concrete implementation now,
   extract an interface when the second case actually arrives.
4. **Backend-first.** Get the OT server provably correct against a headless
   client before investing in a browser UI.

## What we keep, change, and drop

| Area | Original (Java/WIAB) | Target (Go) | Rationale |
|---|---|---|---|
| **OT core** | DocOp model, transform/compose/invert | **Port faithfully** (TP1, client-biased tie-break) | The irreducible core. Spec [02](../specs/02-operational-transform.md). |
| **Wavelet-op layer** | WaveletDelta, Add/RemoveParticipant, WaveletBlipOperation, wavelet-op transform | **Port faithfully** (distinct from DocOp) | CC transforms *wavelet* deltas; see spec [02](../specs/02-operational-transform.md) §wavelet ops. |
| **Data model** | wave/wavelet/blip/document, conversation manifest, supplement | **Port faithfully** | Spec [01](../specs/01-data-model.md). |
| **Identity** | `ParticipantId` = `name@domain` | **Keep exactly** | Load-bearing across auth, access, storage. |
| **History integrity** | `HashedVersion` + SHA-256 hash chain | **Keep exactly** | Tamper-evident; the client agreement point. |
| **Concurrency control** | client/server CC protocol, deltas, versions | **Port faithfully** | Spec [03](../specs/03-concurrency-control.md). |
| **Wire protocol** | hand-rolled JSON-proto over WebSocket, app-level channels | **Redesign clean** (transport-agnostic logical ops) | No legacy client to stay compatible with. See *Wire & transport*. |
| **Transport** | WebSocket only (behind a channel seam) | **stdio now; one browser transport later** | H2/H3-aware; browser choice deferred. |
| **Storage** | pluggable: memory / file / MongoDB | **SQLite** (default), interface preserved | Single file, ACID, FTS5, pure-Go driver. See *Storage*. |
| **Snapshots** | none (full replay on load) | **Add periodic snapshots** | Bounds load time/memory; spec [05](../specs/05-storage-persistence.md) flagged absence. |
| **Search** | Lucene / Solr / in-memory | **SQLite FTS5** | Spec [11](../specs/11-search-indexing.md) maps cleanly. |
| **Auth** | JAAS password (SHA-512) + X.509 client certs | **Provider seam** + signed-token sessions | See *Authentication*. |
| **Federation** | XMPP + X.509 delta signing (never shipped; runs no-op) | **Drop subsystem, keep no-op seams.** The proto *message types* are retained as the delta-on-disk schema. | Spec [07](../specs/07-federation.md). Re-addable later. |
| **Web client** | GWT (Java→JS) | **Rebuild later** (deferred) | Cannot reuse GWT. Spec [10](../specs/10-web-client.md). |
| **Robots/Gadgets API** | large HTTP/OAuth surface | **Defer** (keep event/op seam) | Spec [09](../specs/09-robots-gadgets-api.md). Not needed for core collaboration. |
| **DI** | Guice | **Hand-rolled constructor wiring** in `cmd/waved` | Simplest for this dependency shape; no codegen. `wire` only if it grows unwieldy. |
| **Config** | Typesafe Config (`reference.conf`) | **Go config** (env + file via `koanf`/stdlib) | Proposed; open. |
| **Build** | Gradle + GWT + GXP + PST codegen | **`go build`** (+ `protoc` for retained proto types) | One toolchain. |

## Preserved invariants (non-negotiable)

These come straight from the specs' cross-cutting invariants and are **not** open
to redesign — a "clean" rewrite that breaks any of them is wrong:

1. Version counts **operations applied**, not deltas: `resultingVersion =
   appliedAt + opCount`.
2. **History hash chain (structure non-negotiable; encoding chosen below).**
   `hash(0) = UTF-8(wavelet URI)` (raw bytes, no digest);
   `hash(n>0) = SHA-256(prevHash ‖ appliedDeltaBytes)[0:20]`. A version match with a
   hash mismatch is a hard error, and `appliedDeltaBytes` must be a **deterministic,
   frozen, byte-stable** encoding (replay recomputes it and verifies it against the
   stored hash — invariant #7).
   **Encoding — DECIDED 2026-05-28, supersedes the original plan:** `appliedDeltaBytes`
   is our **own canonical CBOR** of (author, applied-at version, applicationTimestamp,
   operations) — `internal/codec` in RFC 8949 Core Deterministic mode — *not* Java's
   `ProtocolAppliedWaveletDelta` protobuf. Federation is dropped, so there is **no**
   Java byte-compat requirement and **no** signed-delta wrapper / signatures; the
   chain only needs to be self-consistent. (ref `version.Apply`, `codec.HashBytes`.)
   See *Wire & transport* and the codec note below.
3. Transform guarantees **TP1**, with the **client biased first** for concurrent
   insertions and overlapping attribute writes. (Single-server ⇒ no TP2.)
4. At most **one client delta in flight per wavelet** channel.
5. Identity is `name@domain` end to end; no opaque user IDs.
6. Everything is a **document mutated only by ops** (conversation structure,
   gadget state, **and the per-user supplement** included). SQL tables derived
   from this state (inbox, read-state) are *caches*, never the authority — see
   *Read-state authority*.
7. Storage is **append-only delta history**; materialized state is derived
   (now: from the latest snapshot + tail replay). Replay must be **bit-identical**
   to the original — so stored timestamps and hashes are *read back*, never
   recomputed.
8. **Schema is client-side input validation, not a server admission gate** — the
   server applies deltas without schema enforcement (matches Java
   `SchemaCollection.empty()`); do not add a server schema gate or emit
   `SCHEMA_VIOLATION`.
9. **Document text is modelled as UTF-8 runes, not UTF-16 code units — a
   deliberate, package-wide divergence from Java.** Java's `Utf16Util` is BMP-only:
   `DocOpAutomaton`'s `firstSurrogate` gate rejects any string containing a
   surrogate *code unit*, so a supplementary (astral) code point — an emoji,
   `U+1F600` — is rejected because in UTF-16 it is a surrogate *pair*. The Go port
   (`internal/op`) instead treats a supplementary code point as a single valid item
   (one rune): `documentItems`, `inputItems`/`outputItems`, `Compose`, `Transform`,
   the structural validator, and the fuzz generators all count and compose it as one
   item. Consequences a reimplementer must keep consistent: (a) the validator
   **accepts** valid supplementary runes (rejecting only surrogate and noncharacter
   code points); (b) item indices/lengths differ from Java for astral text (Go: 1
   item, Java: 2 code units), so a full-document retain over `"a😀b"` is `Retain{3}`
   in Go vs `Retain{4}` in Java. This only matters at a UTF-16 boundary — replaying
   historical UTF-16-indexed deltas, or interop with a UTF-16 client. The box server
   is rune-based end-to-end, so its own ops are self-consistent. If exact Java
   item-index parity is ever required, revisit the rune-vs-code-unit model
   **package-wide**, not only in the validator. Noncharacter code points
   (`U+FDD0..U+FDEF`, low 16 bits `0xFFFE`/`0xFFFF`) and surrogates are rejected in
   text, attribute values, and annotation values, matching Java's `isValidUtf16`.

## Access control & wavelet creation

One predicate gates **both read and write (delta submission)**:
`checkAccessPermission(participant)` = participant is in the wavelet's participant
set, OR the set contains the `@domain` shared participant. There is no separate
write gate and no role enforcement (roles are advisory metadata; spec
[08](../specs/08-authentication-accounts.md)).

**Critical v0 exception:** there is no "create wave" RPC. A new wavelet is created
by the **first submitted delta**, and the access check returns `true` for any
authenticated participant on an empty (version-0, no-snapshot) wavelet so that
first delta can land. A reimplementer who adds a natural "must already be a
participant" guard at the submit path silently breaks wave creation for everyone.

**Open scope.** `Open` operates on a **wave-view**, not a single wavelet: it
returns all wavelets of the wave the participant may see, plus the participant's
own User-Data Wavelet (UDW) and any reply wavelets (matching the Java). The
underlying delta subscription/channel is per-wavelet.

## Storage

**SQLite** via the pure-Go `modernc.org/sqlite` driver (≥ v1.51.0, SQLite 3.53.1):
empirically bundles **FTS5 + JSON1** and builds with `CGO_ENABLED=0` (clean
cross-compilation, single binary). WAL is **not** the default for a file DB —
set `PRAGMA journal_mode=WAL` and `PRAGMA busy_timeout` at open. The store
*interfaces* from spec [05](../specs/05-storage-persistence.md) are preserved, so
SQLite is the default implementation, not a lock-in.

**Schema principle — minimize schema, lean on JSON columns.** Promote a field to
a real, typed column **only** when it is indexed, filtered, joined, or
range-scanned. Everything else rides in a `JSON` column.

### Delta log (the source of truth)

Persist the full **`WaveletDeltaRecord`** per delta (spec 05 §5.1), not just ops —
storing only ops would make snapshot+tail replay produce *different*
timestamps/hashes and break invariants #2 and #7.

```
deltas(
  wave_id            TEXT,
  wavelet_id         TEXT,
  applied_at_version INTEGER,          -- the version this delta applied at
  resulting_version  INTEGER,          -- appliedAt + opCount
  resulting_hash     BLOB,             -- 20-byte history hash at resulting_version
  transformed_blob   BLOB NOT NULL,    -- canonical CBOR (codec.StoredDelta):
                                        --   author, resultingVersion(+hash),
                                        --   applicationTimestamp, operations
  PRIMARY KEY (wave_id, wavelet_id, applied_at_version)
)
-- index on (wavelet_id, resulting_version) for getDeltaByEndVersion (spec 05:158)
```

**As built (Phase 4):** `transformed_blob` is the canonical CBOR encoding
(`internal/codec`), persisted verbatim for bit-identical replay. The federation
`applied_blob` column (signed original bytes + signer-ids) is **dropped** —
federation is gone — and is re-addable as a nullable column with no migration if
the federation seam is ever revived. `applied_at_version` + `resulting_version`
mirror the spec's own SQLite recommendation (05:672).

### Other stores

- **Snapshots** — `snapshots(wave_id, wavelet_id, version, state BLOB)`:
  materialized wavelet state. Load = latest snapshot + replay deltas after it. A
  *derivable cache*; safe to drop/rebuild. Cadence configurable; default policy =
  snapshot when the replay tail exceeds a threshold (≈1000 ops) rather than a
  fixed N.
- **Accounts** — `accounts(participant_id TEXT PK, kind, data JSON)`. `kind`
  (human/robot) + id are columns; the rest (password verifier, locale, robot
  URL/secret/caps, **registered WebAuthn credentials**) lives in JSON.
- **Attachments** — metadata row `attachments(id TEXT PK, wave_id, wavelet_id,
  meta JSON)`; **bytes + thumbnails on the filesystem**, content-addressed by id.
- **Signer info** — seam only (federation dropped); table omitted until needed.

### Derived indexes (caches, not authority)

`wave_participants` (the per-user inbox/wave-view) and the **FTS5** search table
are **rebuildable caches** with the same status as snapshots (invariant #6/#7).
They are maintained **off the commit/WaveBus event, not inside the synchronous
submit transaction** (keeps append cheap; respects the ack-before-durable
ordering). A **backfill/rebuild from the delta log** path must exist and be
re-runnable; the indexes are rebuilt on startup/repair if they lag the log.

- `wave_participants(participant_id, wave_id, wavelet_id, lmt, ...)` — maintained
  by scanning applied deltas for Add/RemoveParticipant (the Java
  `PerUserWaveViewDispatcher` is a WaveBus subscriber).

### Read-state authority

Read/unread/archive state **stays an op-mutated UDW document** (invariant #6);
`wave_participants` and any read-state column are **derived indexes** over it, not
the authority. *(Decision pending confirmation — see "Open decisions".)*

### Write concurrency

SQLite WAL = many concurrent readers + one writer for the whole DB. Wave's model
is one-writer-per-wavelet; at single-machine scale the global write lock is a
non-issue (delta appends are sub-millisecond). Route writes through one
serialized path with WAL + `busy_timeout`; **measure before optimizing.** If a
single DB ever becomes the bottleneck, the store interface permits sharding
per-wavelet or swapping the DeltaStore impl.

### Data directory & backup

```
<data-dir>/
  wave.db, wave.db-wal, wave.db-shm   # SQLite (WAL)
  attachments/<id>                    # content-addressed blobs + thumbnails
  keys/                               # session-signing key, TLS material
```

Backup is **not** "just copy the file" with WAL + a separate blob tree: use
`VACUUM INTO` (or the online backup API) for a consistent DB copy, plus an
`rsync` of `attachments/`, or a stop-and-copy. State this so it isn't discovered
in production.

## Wire & transport

**Redesign the live wire protocol clean.** The OT/delta/version *semantics* are
fixed (specs 02/03); only the live client encoding and transport change. Drop the
legacy field-numbers-as-JSON-keys and int64-as-`[low,high]` encoding. **This does
not touch the applied-delta serialization that feeds the hash chain** (invariant
#2) — that is storage/wire schema, fixed.

**Transport-agnostic logical session protocol.** Define the protocol as a small
set of logical operations, independent of how bytes move:

- `Open(waveView) → channelId(s) + initial wire snapshot` (then a stream of updates)
- `Submit(channelId, delta) → ack(resultingVersion) | Error{code, message}`
- server-pushed `Update(channelId, transformedDelta)` / `Commit(version)`
- `Search(query) → results`, plus profile/attachment fetch

The **initial wire snapshot** is a serialized full-wavelet-state message (the
Java `WaveletSnapshot`), distinct from the storage snapshot blob — don't conflate
the two.

**Wire error model.** `Error` is structured `{code, message}`, not free text. Codes
reuse spec [04](../specs/04-wire-protocol.md)'s `ResponseCode` **minus
`SCHEMA_VIOLATION`** (invariant #8 forbids it): `OK`, `BAD_REQUEST`,
`NOT_AUTHORIZED`, `VERSION_ERROR`, `INVALID_OPERATION`, `TOO_OLD`. Per-code client
reaction is part of the contract (e.g. `VERSION_ERROR` ⇒ resync; `NOT_AUTHORIZED`
⇒ surface to user; `TOO_OLD` ⇒ re-open). The CC layer originates these codes.

**Implementation discipline.** Implement **stdio against the logical ops
directly**. Do **not** build a transport-dispatch/encoding-abstraction layer until
the *second* transport exists — then extract the seam from two real cases.

**Transports.** Two concrete; the browser one is a deferred choice:

| Transport | Status | Maps logical streams to |
|---|---|---|
| **stdio** | **build now** (test harness, CLI client) | length-prefixed frames on stdin/stdout |
| **SSE + POST** | candidate (browser; H2/H3-friendly, proxy-safe, always-works fallback) | server push via `EventSource`; submit via `POST` |
| **WebTransport** (HTTP/3 / QUIC) | candidate (browser; native multiplexing) | one QUIC stream per wavelet |
| **WebSocket** | candidate (browser; compatibility) | hand-framed channels |

Browser WebTransport is now cross-browser (Safari 26.4 closed the gap). The
residual risk is **draft-version coupling** between `quic-go/webtransport-go`
(draft-02) and browser drafts — not "is it ready". **SSE+POST is the
always-works fallback.** (The Java had a transport seam too — `WebSocketChannel`
over a hand-framed envelope — with WS as its only impl; the lesson is "one
logical protocol, one transport at a time; the seam factors cleanly later," not
that WS was a mistake.)

**Encoding.** The *internal* encoding — version hash chain + storage blobs, and
the stdio frame payloads — is **canonical CBOR** (`internal/codec`, RFC 8949 Core
Deterministic), **decided 2026-05-28** (federation dropped → no need to reproduce
Java's protobuf; see invariant #2). The original lean toward binary protobuf
referred to the *browser wire*, which is still open and **pinned at Phase 8** —
but reusing the CBOR codec there (one codec, no translation layer) is now the
natural default unless a browser concern argues otherwise.

## Authentication

**One seam:** every auth method produces a **verified `ParticipantId`** and mints
a **session**. Replace JAAS + X.509 + Jetty `HttpSession` with:

```
  request ──▶ AuthProvider ──▶ verified ParticipantId ──▶ Provisioner ──▶ Session
               (pluggable)                                 (register-       (signed token,
                                                            on-first-use?)   transport-bound,
                                                                             CSRF-safe)
```

**Providers in initial scope:** **local/single-user** and **trusted-header**
(simplest, unblock real use). **Addable later behind the same seam:** tsnet, OIDC,
passkey.

- **Local / single-user** — pin one participant, or trust loopback. Dev/personal.
- **Trusted-header** — trust identity headers from a fronting proxy
  (`tailscale serve`, oauth2-proxy, Cloudflare Access, Authelia/nginx
  forward-auth). Configurable header names. **Hard requirement:** only enabled on
  a listener *exclusively* reachable via that proxy — a forgeable header on a
  public listener is a full auth bypass.
- **Tailscale tsnet** — embed `tsnet`; resolve identity via `LocalClient.WhoIs`
  (no spoofable header). Pulls a large dependency in purely for identity, so make
  it **optional (build tag / separate module)**; trusted-header behind
  `tailscale serve` covers the common tailnet case with zero embedded deps.
- **OIDC** — standard OAuth2/OIDC code flow; verified email/sub → ParticipantId.
- **Passkey / WebAuthn** — passwordless (`go-webauthn/webauthn`, the de-facto Go
  lib; pin a minor version — it is pre-1.0 — and isolate it behind the provider
  interface, with credentials in the accounts JSON column). "Passkey-only,
  register-on-first-use": first visit with no account creates a passkey + mints a
  ParticipantId; later visits assert.

**Register-on-first-use** is a cross-cutting *policy*: the first time a new
verified identity appears (OIDC/passkey/Tailscale), auto-provision **only its
account + ParticipantId** — no UDW seed and no welcome wave. Consumers must
tolerate an absent UDW (treat as empty/all-unread supplement). One config knob.

**Per-listener config** makes "all of them" coherent: e.g. trusted-header/tsnet on
the private tailnet bind, passkey/OIDC on a public bind — same session layer.

## Operability

Goal #1 is a *runnable* binary, so this is in scope, not hygiene:

- **Logging:** `log/slog`, structured, leveled.
- **Metrics:** a small set — loaded-wavelet count, apply latency, submit
  error-rate (by code), and **commit-lag = currentVersion − lastPersistedVersion**
  (the async-persist model makes a storage stall otherwise invisible). Plus a
  health endpoint. (Replaces the Java `/stats` + `@Timed`.)
- **Graceful shutdown (correctness-relevant):** because ack+broadcast happen
  *before* durability (spec 00 flow B), a naive exit drops acked-but-unfsynced
  deltas. On SIGTERM run an **explicit ordered sequence**: stop accepting new
  connections → stop listeners → drain pending persists → WAL checkpoint → close
  DB → exit. (The Java `ShutdownManager` had a documented ordinal-vs-value
  ordering bug, spec 06:486 — redo it correctly.)
- **TLS:** WebTransport/H3 mandates TLS 1.3. State per deployment who terminates:
  trusted-header/tsnet assume a fronting proxy or tailscale-provided certs; a
  directly-exposed transport needs native cert handling (autocert/files).

## Component architecture (high level)

```
        transports (stdio now | sse+post / webtransport / ws later)   auth providers
                          │                                                 │
                          ▼                                                 ▼
                   ┌──────────────┐  session/ParticipantId           ┌───────────┐
                   │   Frontend   │◀────────────────────────────────▶│  Sessions │
                   │ (per-client  │                                   └───────────┘
                   │  channels,   │
                   │  fan-out bus) │──▶ derived-index maintenance (off commit event)
                   └──────┬───────┘
                          │ open / submit / subscribe
                          ▼
                   ┌──────────────┐    apply pipeline    ┌───────────────┐
                   │ Wavelet core │◀──(wavelet-op + OT,─▶│  OT engine    │
                   │ (state, lock,│    version, hash)     │ (DocOp +      │
                   │  participants)│                      │  wavelet-op)  │
                   └──────┬───────┘                       └───────────────┘
                          │ append (full record) / load (snapshot+tail)
                          ▼
                   ┌──────────────┐   ┌──────────┐   ┌─────────────┐
                   │  Delta log   │   │ Accounts │   │ Attachments │   (SQLite + FS)
                   │ + snapshots  │   │  (JSON)  │   │ (meta+bytes)│
                   └──────┬───────┘   └──────────┘   └─────────────┘
                          │ (derived caches, rebuildable)
                          ▼
                   ┌──────────────────────────────┐
                   │ wave_participants  +  FTS5    │   inbox / search
                   └──────────────────────────────┘

   proto/ : retained delta-on-disk message types (depended on by storage + cc).
   internal/federation : no-op provider/listener seams ONLY (depends on proto/).
```

## Open decisions (need confirmation)

A few forks I've leaned on a default for; flagging the ones worth your veto:

1. **New-user experience.** *Decided (2026-05-28): empty inbox.*
   Register-on-first-use creates only account + ParticipantId; no welcome wave.
   A "getting started" wave can be revisited later.
2. **Read-state authority.** Leaning: keep read/unread as an **op-mutated UDW**
   with SQL as a derived index (preserves invariant #6). The alternative (SQL
   authoritative) is simpler but a deliberate invariant break.
3. **tsnet scope.** Keep tsnet as an *optional* provider (build tag) so its
   dependency isn't forced; trusted-header behind `tailscale serve` is the
   default tailnet path. (You asked for both — this keeps both without bloating
   the minimal binary.)

## Deferred

- **Browser transport choice** (SSE+POST vs WebTransport vs WS) — Phase 8.
- **Frontend rebuild strategy** — its own design effort; spec [10](../specs/10-web-client.md).
- **Robots/Gadgets API** — event-out/op-in seam preserved.
- **Federation** — dropped; no-op seams + proto types retained.
- **Client-side render concerns** — the diff/read-unread *rendering* document and
  server-side profile fetch are client/robot-surface, not server core.
- **Browser wire encoding** — pinned at Phase 8. Internal encoding is decided
  (canonical CBOR, `internal/codec`); reusing it for the browser wire is the
  natural default, but the choice is still open if a browser concern argues for
  JSON/protobuf.
- **Config library** and **DI** — `koanf` + hand-rolled wiring; confirm at skeleton.
