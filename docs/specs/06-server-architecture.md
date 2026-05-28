# 06 — Server Architecture & Runtime

## Purpose & scope

This spec covers the Wave server process from startup through steady-state request handling:
configuration loading, dependency wiring, the in-process wavelet store (the _waveserver_ layer),
the client-facing frontend, the notification bus, the executor pool, and the lifecycle of a delta
from client submit through persistence and broadcast.

It references but does not re-derive:
- Storage format and the delta/account/attachment stores → [05-storage-persistence](05-storage-persistence.md)
- Wire framing and protobuf messages → [04-wire-protocol](04-wire-protocol.md)
- OT transform algorithm → [02-operational-transform](02-operational-transform.md)
- Client-server OT protocol (versions, hashed-version chain) → [03-concurrency-control](03-concurrency-control.md)

The server is a single JVM process (no clustering). All wavelets are held in one in-memory map;
persistence is asynchronous and happens on a dedicated executor pool.

---

## Concepts & glossary

| Term | Meaning |
|------|---------|
| **WaveMap** | In-process registry: `WaveId → Wave`, each Wave holds `WaveletId → WaveletContainer`. Backed by a size/time-bounded LRU cache. |
| **WaveletContainer** | Owns a single wavelet's in-memory state plus its serialised delta history. Subtypes: `LocalWaveletContainer` (we are authoritative) and `RemoteWaveletContainer` (another server is authoritative). |
| **WaveletState** | The per-wavelet mutable state object (snapshot + delta log). Not thread-safe; callers must hold the container's write lock. |
| **WaveletProvider** | Public façade over WaveMap, exposed to the frontend and federation. Implemented by `WaveServerImpl`. |
| **WaveBus** | Pub/sub bus for wavelet events (update, commit). Subscribers: `ClientFrontendImpl`, `RobotsGateway`, `PerUserWaveViewDispatcher` (search indexing). |
| **WaveletNotificationDispatcher** | Implements both `WaveBus` and `WaveletNotificationSubscriber`. Receives internal container notifications and fans them out to bus subscribers and remote federation hosts. |
| **WaveletNotificationSubscriber** | Interface called by a `WaveletContainer` after applying and persisting a delta. Takes a snapshot+deltas and a set of remote domains to notify. |
| **ClientFrontend** | Handles `openRequest` and `submitRequest` RPCs from web clients. Tracks subscriptions per user and fans updates to connected channels. |
| **Channel** | A logical stream from a single `openRequest` call. Identified by an opaque string (`ch<N>`). One user can have many concurrent channels. |
| **UserManager** | Per-participant index of active wave subscriptions and pending submits. |
| **WaveViewSubscription** | One channel's subscription to a wave: wave ID + wavelet-ID filter + listener callback. |
| **CertificateManager** | Signs outgoing local deltas, verifies inbound remote deltas, stores signer info. Determines which domains are "local". |
| **ShutdownManager** | JVM shutdown-hook dispatcher; runs registered `Shutdownable`s in priority order. |

---

## Data structures

### Configuration (reference.conf)

The server is configured via Typesafe Config. Layered lookup order (highest wins):
1. JVM system properties / environment
2. `config/application.conf` (operator overrides)
3. `config/reference.conf` (bundled defaults)

All settings live under top-level sections. Key settings:

**`core` section**

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `wave_server_domain` | string | `"local.net"` | Authoritative domain; wavelet IDs are local iff their domain matches this. |
| `http_frontend_addresses` | string list | `["localhost:9898"]` | Bind addresses for the HTTP/WS server. |
| `http_frontend_public_address` | string | `""` | Public-facing host:port reported to clients. |
| `http_websocket_public_address` | string | `""` | WS address clients are told to connect to; defaults to frontend public address. |
| `http_websocket_presented_address` | string | `""` | WS address embedded in served HTML; defaults to ws public address. |
| `gadget_server_hostname` | string | `"gmodules.com"` | Upstream gadget server to proxy. |
| `gadget_server_port` | int | `80` | |
| `resource_bases` | string list | `["./war"]` | Static web resource directories. |
| `signer_info_store_type` | string | `"file"` | `file` or `mongodb`. |
| `signer_info_store_directory` | string | `"_certificates"` | |
| `attachment_store_type` | string | `"disk"` | `disk` or `mongodb`. |
| `attachment_store_directory` | string | `"_attachments"` | |
| `thumbnail_patterns_directory` | string | `"_thumbnail_patterns"` | |
| `account_store_type` | string | `"file"` | `fake`, `memory`, `file`, or `mongodb`. |
| `account_store_directory` | string | `"_accounts"` | |
| `delta_store_type` | string | `"file"` | `memory`, `file`, or `mongodb`. |
| `delta_store_directory` | string | `"_deltas"` | |
| `sessions_store_directory` | string | `"_sessions"` | |
| `search_type` | string | `"memory"` | `memory`, `lucene`, or `solr`. |
| `index_directory` | string | `"_indexes"` | For Lucene only. |
| `profile_fetcher_type` | string | `"initials"` | `gravatar` or `initials`. |
| `solr_base_url` | string | `"http://localhost:8983/solr"` | For Solr only. |
| `enable_profiling` | bool | `true` | Enables timing interceptors and `/stats` endpoint. |
| `mongodb_host` | string | `"127.0.0.1"` | |
| `mongodb_port` | int | `27017` | |
| `mongodb_database` | string | `"wiab"` | |
| `wave_cache_size` | int | `1000` | Max waves held in the WaveMap LRU cache. |
| `wave_cache_expire` | duration | `60m` | Eviction TTL after last access. |

**`network` section**

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `session_cookie_max_age` | int (seconds) | `-1` | `-1` = browser-session cookie. |
| `websocket_max_idle_time` | int (ms) | `0` | `0` = never close. |
| `websocket_max_message_size` | int (MB) | `2` | |

**`administration` section**

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `admin_user` | string | `"@"` (invalid) | User who can change other users' passwords. |
| `welcome_wave_id` | string | `""` | Wave ID of template shown to new users. |
| `disable_registration` | bool | `false` | |
| `disable_loginpage` | bool | `false` | |
| `analytics_account` | string | `""` | |

**`threads` section** — thread-pool sizes

| Key | Default | Executor role |
|-----|---------|---------------|
| `listener_executor_thread_count` | 1 | Federation async callbacks. |
| `wavelet_load_executor_thread_count` | 1 | Loads WaveletState from storage on first access. |
| `delta_persist_executor_thread_count` | 1 | Writes deltas to the delta store. |
| `storage_continuation_executor_thread_count` | 1 | Post-load and post-persist callbacks inside WaveletContainerImpl. |
| `lookup_executor_thread_count` | 1 | Lists wavelet IDs for a wave from storage. |
| `solr_thread_count` | 1 | Solr search/index requests. |
| `contact_executor_thread_count` | 1 | Contacts API. |

**`security` section**

| Key | Default |
|-----|---------|
| `enable_ssl` | `false` |
| `ssl_keystore_path` | `"wiab.ks"` |
| `ssl_keystore_password` | `"changeme"` |
| `enable_clientauth` | `false` |
| `clientauth_cert_domain` | `""` |

**`federation` section**

| Key | Default | Meaning |
|-----|---------|---------|
| `enable_federation` | `false` | If true, use signed deltas; if false, use no-op signer. |
| `certificate_private_key` | `"local.net.key"` | PEM-encoded PKCS#8 private key. |
| `certificate_files` | list | PEM/DER cert chain; first is the leaf. |
| `certificate_domain` | `"local.net"` | |
| `waveserver_disable_verification` | `true` | Skip delta signature verification. |
| `waveserver_disable_signer_verification` | `true` | Skip cert-chain validation. |

---

### WaveMap / Wave / WaveletContainer hierarchy

```
WaveMap
  LRU cache: WaveId → Wave
    Wave
      localWavelets:  WaveletId → LocalWaveletContainer
      remoteWavelets: WaveletId → RemoteWaveletContainer
      lookedupWavelets: Future<Set<WaveletId>>   # from storage

WaveletContainer (abstract base)
  waveletName: WaveletName
  state: {LOADING, OK, DELETED, CORRUPTED}
  waveletState: WaveletState          # set once, after async load
  loadLatch: CountDownLatch(1)        # counts down when state leaves LOADING
  readWriteLock: ReentrantReadWriteLock
  notifiee: WaveletNotificationSubscriber
  sharedDomainParticipantId: ParticipantId | null

LocalWaveletContainer extends WaveletContainer
  # accepts submitRequest; runs OT; signs deltas

RemoteWaveletContainer extends WaveletContainer
  pendingDeltas: NavigableMap<HashedVersion, AppliedDelta>
  pendingCommit: bool
  pendingCommitVersion: HashedVersion | null
  # does not accept client submits; receives federation updates
```

**WaveletState** (interface, implemented by `DeltaStoreBasedWaveletState`):

```
WaveletState
  snapshot: ReadableWaveletData | null     # null = wavelet is at version 0 (empty)
  currentVersion: HashedVersion
  lastPersistedVersion: HashedVersion
  # in-memory delta log (applied+transformed pairs)
  appendDelta(record: WaveletDeltaRecord)  # adds to in-memory log; mutates snapshot
  persist(version: HashedVersion) → Future<Void>  # async write to delta store
  flush(version: HashedVersion)            # evict persisted deltas from memory
```

**WaveletDeltaRecord**:

```
WaveletDeltaRecord
  appliedAtVersion: HashedVersion           # version before the delta
  appliedDelta: ByteStringMessage<ProtocolAppliedWaveletDelta> | null
  transformedDelta: TransformedWaveletDelta
```

**CommittedWaveletSnapshot** (returned by frontend queries):

```
CommittedWaveletSnapshot
  snapshot: ReadableWaveletData
  committedVersion: HashedVersion
```

---

### Frontend data structures

**WaveletInfo** — in-memory index used by `ClientFrontendImpl`:

```
WaveletInfo
  perWavelet: WaveId → (WaveletId → PerWavelet)
    PerWavelet
      currentVersion: HashedVersion      # tracks frontend's view of current version
      explicitParticipants: Set<ParticipantId>
      implicitParticipants: Set<ParticipantId>  # users who opened but aren't in participant list
  perUser: ParticipantId → UserManager
    UserManager
      subscriptions: WaveId → [WaveViewSubscription]
        WaveViewSubscription
          waveId: WaveId
          waveletIdFilter: IdFilter
          channelId: string
          openListener: OpenListener    # callback → wire connection
          channels: WaveletId → WaveletChannelState
            WaveletChannelState
              hasOutstandingSubmit: bool
              heldBackDeltas: [TransformedWaveletDelta]
              submittedEndVersions: Set<long>   # echo-suppression for own deltas
              lastVersion: HashedVersion | null
```

---

## Algorithms & behavior

### Bootstrap sequence

```
ServerMain.main()
  1. Load config: system-props → application.conf → reference.conf (Typesafe Config merge).
  2. Bind Config singleton; bind core.wave_server_domain string.
  3. Instantiate StatModule (profiling interceptors) + ExecutorsModule (thread pools).
  4. Instantiate (in order):
       ServerModule        → WaveServerModule, SessionManager, ServerRpcProvider, ProtoSerializer
       PersistenceModule   → binds DeltaStore, AccountStore, AttachmentStore, SignerInfoStore
       RobotApiModule
       FederationModule    → NoOpFederationModule (or XMPP, if enabled)
       SearchModule        → binds SearchProvider, WaveIndexer, PerUserWaveViewHandler
       ProfileFetcherModule
  5. initializeServer(domain):
       accountStore.initializeAccountStore()
       AccountStoreHolder.init(accountStore, domain)
       (if file signer info store) signerInfoStore.initializeSignerInfoStore()
       WaveletProvider.initialize()   # marks WaveServerImpl ready
  6. initializeServlets(server, config): register all HTTP endpoints on ServerRpcProvider.
  7. initializeRobotAgents(server): register built-in robot servlets.
  8. initializeRobots(injector, waveBus): subscribe RobotsGateway to WaveBus.
  9. initializeFrontend(injector, server, waveBus):
       create WaveletInfo
       create ClientFrontendImpl (subscribes itself to WaveBus)
       wrap in WaveClientRpcImpl (protobuf service adapter)
       register as protobuf service on ServerRpcProvider
  10. initializeFederation(injector): federationManager.startFederation().
  11. initializeSearch(injector, waveBus):
       subscribe PerUserWaveViewDispatcher to WaveBus
       waveIndexer.remakeIndex()   # (re)builds search index from stored deltas
  12. initializeShutdownHandler(server): register server.stopServer() with ShutdownManager.
  13. server.startWebSocketServer(injector): bind HTTP ports, start Jetty, begin accepting connections.
```

**Invariant**: No request can arrive before step 13; `WaveletProvider.initialize()` must be called before any method on `WaveletProvider` (enforced by guard flag in `WaveServerImpl`).

---

### WaveletContainer lifecycle

Each `WaveletContainer` starts in state `LOADING`. State transitions:

```
LOADING  →  OK          (storage load succeeded)
LOADING  →  CORRUPTED   (storage load failed or interrupted)
OK       →  CORRUPTED   (markStateCorrupted() called — e.g., remote signature failure)
OK       →  DELETED     (not yet implemented in this version)
```

Loading is asynchronous:
1. `WaveServerModule.loadWaveletState()` submits a task on `WaveletLoadExecutor` that calls `DeltaStore.open(waveletName)` and returns a `Future<DeltaStoreBasedWaveletState>`.
2. `WaveletContainerImpl` attaches a listener to that future (on `StorageContinuationExecutor`). The listener acquires the write lock, sets `waveletState`, and transitions to `OK` (or `CORRUPTED`), then counts down `loadLatch`.
3. All public methods that read wavelet state call `awaitLoad()` first (blocking on `loadLatch` with a 1000-second timeout). They then acquire the appropriate lock and call `checkStateOk()`.

**Locking rules**:
- `WaveletState` is not thread-safe. All access goes through `WaveletContainerImpl`'s `ReentrantReadWriteLock`.
- Read operations (snapshot copy, history queries, access check) hold the read lock.
- Write operations (apply delta, persist, flush, notifyOfDeltas, notifyOfCommit) hold the write lock.
- `notifyOfDeltas` and `notifyOfCommit` must be called with the write lock held (enforced by assertion).
- `awaitLoad()` must be called without the read or write lock held. **Only the write-lock case is enforced** by an assertion (`checkState(!writeLock.isHeldByCurrentThread())`); the read-lock case is **not** asserted. Calling `awaitLoad()` while holding the read lock, before the initial load completes, deadlocks silently: the load-completion listener cannot acquire the write lock to count down `loadLatch` while a read lock is held (a read/write lock does not permit read→write upgrade), and no assertion catches it. A Go reimplementation should add an explicit check (or design) ensuring no lock is held on the await path.

---

### Delta submit lifecycle (local wavelet)

```
Client → WebSocket → WaveClientRpcImpl.submit()
  → ClientFrontendImpl.submitRequest(loggedInUser, waveletName, delta, channelId, listener)
    1. Verify delta.author == loggedInUser.
    2. UserManager.submitRequest(channelId, waveletName)   # mark outstanding submit in subscription
    3. WaveletProvider.submitRequest(waveletName, delta, wrappedListener)
         → WaveServerImpl.submitRequest()
           a. Reject empty delta.
           b. certificateManager.signDelta(serialized delta)   # wraps in ProtocolSignedDelta
           c. submitDelta(waveletName, delta, signedDelta, resultListener):
                i.  isLocalWavelet(waveletName)?  → check waveletId.domain ∈ localDomains
                ii. wavelet = getOrCreateLocalWavelet(waveletName)
                iii. wavelet.checkAccessPermission(author) — fail if not participant (or empty)
                iv. wavelet.submitRequest(waveletName, signedDelta):
                     → LocalWaveletContainerImpl.submitRequest():
                       a. awaitLoad()
                       b. acquireWriteLock()
                       c. checkStateOk()
                       d. before = getCurrentVersion()
                       e. result = transformAndApplyLocalDelta(signedDelta)
                       f. after = getCurrentVersion()
                       g. if after != before (not a no-op or duplicate):
                            domainsToNotify = participants' domains ∪ removed participants' domains
                            notifyOfDeltas([result], domainsToNotify)   # → WaveletNotificationDispatcher
                            persist(after, domainsToNotify)             # async, non-blocking
                       h. releaseWriteLock()
                       i. return result
                v. report operationsApplied, hashedVersionAfterApplication, applicationTimestamp
    4. On success callback:
         listener.onSuccess(ops, version, timestamp) → client RPC response
         UserManager.submitResponse(channelId, waveletName, version)   # unblock held deltas
    5. On failure callback:
         listener.onFailure(error) → client RPC error
         UserManager.submitResponse(channelId, waveletName, null)   # ORIGINAL JAVA BUG: null trips a checkNotNull; see "Failed-submit NPE" gotcha below — a Go port MUST NOT pass null here
```

**Submit-error channel.** The client-submit error is a free-text `error_message` string (sourced from `FederationError.error_message`); `ProtocolSubmitResponse` carries only `operations_applied`, `error_message`, and `hashed_version_after_application` — there is **no** structured `ResponseCode` on the live submit RPC. The structured `ResponseCode` taxonomy is internal / client-side only — see specs [03-concurrency-control](03-concurrency-control.md) and [04-wire-protocol](04-wire-protocol.md) for the authoritative statement.

> **Schema enforcement is client-side input validation, NOT a server admission gate.** The box server builds all wavelet documents with `SchemaCollection.empty()` (`WaveletDataUtil.WAVELET_FACTORY`, `RobotWaveletData`, `OperationContextImpl` — each carrying a TODO referencing issue 109 that "schemas should be enforced"). The server therefore never validates submitted deltas against the conversation/blip schema and never rejects a delta for `SCHEMA_VIOLATION`. The live submit RPC (`ProtocolWaveClientRpc.Submit` / `ProtocolSubmitResponse`) returns only `operations_applied` plus a free-form `error_message` — it has no `ResponseCode` enum. `SCHEMA_VIOLATION` and the full `ResponseStatus.ResponseCode` enum exist only in the concurrencycontrol OT layer (`ResponseCode.java`) and `clientserver.proto`, which is not wired to the running server. Schema validity is maintained solely by the GWT editor (`StageTwoProvider` → `ConversationSchemas`). A Go reimplementation should reproduce this: do **not** add server-side schema validation or emit `SCHEMA_VIOLATION` on the submit path, or it will reject deltas the Java server accepts. See [01-data-model](01-data-model.md) and [03-concurrency-control](03-concurrency-control.md), which cross-reference this note.

#### transformAndApplyLocalDelta detail

```
transformAndApplyLocalDelta(signedDelta):
  1. Parse ProtocolWaveletDelta from signedDelta.
  2. Deserialize to WaveletDelta (author + ops + targetVersion).
  3. transformed = maybeTransformSubmittedDelta(delta):
       if delta.targetVersion == currentVersion: return delta unchanged.
       if delta.targetVersion.version == currentVersion.version but hash differs: InvalidHashException.
       else: transformSubmittedDelta(delta):
         load server deltas from waveletState between targetVersion and currentVersion.
         for each serverDelta:
           if clientOps empty: return early (transformed away).
           if clientAuthor == serverAuthor and ops equal: return duplicate marker.
           clientOps = Transform.transform(clientOps × serverDelta)
           targetVersion = serverDelta.resultingVersion
         return new WaveletDelta(author, currentVersion, clientOps)
  4. applicationTimestamp = now().
  5. if transformed.size() == 0: return empty WaveletDeltaRecord (no-op).
  6. if transformed.targetVersion != currentVersion: duplicate — lookup existing applied/transformed delta and return.
  7. Build ProtocolAppliedWaveletDelta (AppliedDeltaUtil.buildAppliedDelta).
  8. applyDelta(appliedDelta, transformed):
       transformedDelta = AppliedDeltaUtil.buildTransformedDelta(appliedDelta, transformed)
       deltaRecord = WaveletDeltaRecord(targetVersion, appliedDelta, transformedDelta)
       waveletState.appendDelta(deltaRecord)   # mutates snapshot; advances currentVersion
       return deltaRecord
```

---

### Notification and fanout

After a delta is applied and the write lock is still held, `notifyOfDeltas()` calls
`WaveletNotificationDispatcher.waveletUpdate(snapshot, [deltaRecord], domainsToNotify)`.

The dispatcher, while processing that call synchronously:
1. For each `WaveBus.Subscriber` (ClientFrontendImpl, RobotsGateway, PerUserWaveViewDispatcher):
   calls `subscriber.waveletUpdate(wavelet, deltaSequence)` synchronously. Exceptions are caught and logged (not fatal).
2. For each remote domain in `domainsToNotify \ localDomains`:
   calls `federationHost.waveletDeltaUpdate(waveletName, serializedAppliedDeltas, callback)`.

**ClientFrontendImpl.waveletUpdate(wavelet, deltas)**:
1. `waveletInfo.syncWaveletVersion(waveletName, deltas)` — verifies contiguity, advances stored version.
2. Computes sets of participants added and removed across all deltas.
3. For each removed participant: `participantUpdate(..., add=false, remove=true)` for deltas up to removal.
4. For all remaining participants (pre-existing + newly added): `participantUpdate(..., add=isNew, remove=false)`.
5. `participantUpdate` calls `waveletInfo.notifyAddedExplicitWaveletParticipant` if `add=true`,
   `UserManager.onUpdate(waveletName, deltas)` always, and `notifyRemovedExplicitWaveletParticipant` if `remove=true`.

**UserManager.onUpdate(waveletName, deltas)**:
1. For each matching `WaveViewSubscription`: call `subscription.onUpdate(waveletName, deltas)`.

**WaveViewSubscription.onUpdate(waveletName, deltas)**:
1. Check update is contiguous with `state.lastVersion`.
2. If `hasOutstandingSubmit`: queue deltas into `heldBackDeltas`.
3. Else: filter out deltas whose end version is in `submittedEndVersions` (echo suppression), then call `openListener.onUpdate(...)` for remaining deltas.

**Persist callback** (fires on `StorageContinuationExecutor` after the delta store write completes):
1. Acquire write lock.
2. `waveletState.flush(version)` — evict the now-persisted deltas from the in-memory buffer.
3. `notifyOfCommit(version, domainsToNotify)` → `WaveletNotificationDispatcher.waveletCommitted`.
4. Release write lock.

The dispatcher fans `waveletCommitted` to all bus subscribers and to remote federation hosts.

**ClientFrontendImpl.waveletCommitted(waveletName, version)**:
For each tracked participant on that wavelet, calls `UserManager.onCommit(waveletName, version)`.
`WaveViewSubscription.onCommit` calls `openListener.onUpdate(..., committedVersion=version, ...)`.

---

### openRequest flow

```
Client → WaveClientRpcImpl.open() → ClientFrontendImpl.openRequest(user, waveId, filter, knownWavelets, listener)
  1. Reject if user == null ("Not logged in").
  2. Reject if knownWavelets non-empty ("Known wavelets not supported" — partial resync not implemented).
  3. waveletInfo.initialiseWave(waveId):
       if waveId not yet seen: fetch wavelet IDs from WaveletProvider, load snapshots, populate perWavelet cache.
  4. channelId = "ch" + incrementing counter.
  5. userManager.subscribe(waveId, filter, channelId, listener) → WaveViewSubscription.
  6. waveletIds = waveletInfo.visibleWaveletsFor(subscription, user):
       returns wavelet IDs that pass the filter and for which user has access permission.
  7. For each visible waveletId:
       waveletInfo.notifyAddedImplicitParticipant(waveletName, user)   # track implicit subscribers
       snapshot = waveletProvider.getSnapshot(waveletName)
       if snapshot == null:
         listener.onUpdate(waveletName, null, [], null, null, channelId)
       else:
         listener.onUpdate(waveletName, snapshot, [], snapshot.committedVersion, null, channelId)
  8. dummyWaveletName = WaveletName.of(waveId, WaveletId.of(waveId.domain, "dummy+root"))
     if waveletIds is empty:
       listener.onUpdate(dummyWaveletName, null, [], null, null, channelId)   # channel id only
     listener.onUpdate(dummyWaveletName, null, [], null, marker=true, null)   # end-of-snapshot marker
```

The dummy wavelet name (`<waveid-domain>/dummy+root`) carries two protocol-level messages: the channel-ID assignment and the snapshot-complete marker. These are not real wavelets; clients use them as control messages.

---

### Wavelet locality determination

A wavelet is **local** if `waveletId.domain ∈ CertificateManager.getLocalDomains()`.

`CertificateManager.getLocalDomains()` returns the set of domains for which this server holds signing keys. In the default (non-federated) configuration this is `{core.wave_server_domain}`.

- **Local wavelet**: submits go through OT in `LocalWaveletContainerImpl`.
- **Remote wavelet**: updated by incoming federation deltas; no client submits accepted. Remote containers buffer out-of-order deltas in `pendingDeltas` and apply them once contiguous.

---

### Executor pool

All executors wrap `RequestScopeExecutor` (propagates request scope for profiling).
Thread-count `0` means same-thread execution. Thread-count `-1` means unbounded cached pool (used by `ClientServerExecutor` which handles client RPC).

| Executor annotation | Config key | Default | Purpose |
|--------------------|-----------|---------|---------|
| `ClientServerExecutor` | (none; unbounded) | cached pool | Client WebSocket RPC threads (Jetty HTTP handler). |
| `WaveletLoadExecutor` | `wavelet_load_executor_thread_count` | 1 | Loads `DeltaStoreBasedWaveletState` from storage. |
| `StorageContinuationExecutor` | `storage_continuation_executor_thread_count` | 1 | Post-load / post-persist callbacks inside `WaveletContainerImpl`. |
| `DeltaPersistExecutor` | `delta_persist_executor_thread_count` | 1 | Passed to `DeltaStoreBasedWaveletState`; runs persistence writes. |
| `ListenerExecutor` | `listener_executor_thread_count` | 1 | Federation update/commit async result callbacks in `WaveServerImpl`. |
| `LookupExecutor` | `lookup_executor_thread_count` | 1 | `WaveMap`: async lookup of wavelet IDs per wave from storage. |
| `IndexExecutor` | (hardcoded to 1) | 1 | Search index updates. |
| `SolrExecutor` | `solr_thread_count` | 1 | Solr HTTP calls. |
| `ContactExecutor` | `contact_executor_thread_count` | 1 | Contacts API (scheduled). |
| `RobotConnectionExecutor` | `robot_connection_thread_count` | (not in reference.conf) | Robot active-API HTTP calls (scheduled). |
| `RobotGatewayExecutor` | `robot_gateway_thread_count` | (not in reference.conf) | Passive robot gateway dispatch. |

---

### Profiling / monitoring

Controlled by `core.enable_profiling` (default `true`).

`RequestScopeFilter` (servlet filter on `/*`) is registered **unconditionally**, regardless of `enable_profiling`. It begins/ends a request-scope context per HTTP request, providing the scope that `TimingFilter` (and any other scope-dependent code) writes into. In `ServerMain.initializeServlets()` it is added *before* the `if (enableProfiling)` block.

When enabled (in addition to the always-on `RequestScopeFilter`):
- `TimingFilter` (servlet filter on `/*`) records per-request timing in the current request scope.
- `StatuszServlet` at `/stats` (path constant `StatService.STAT_URL`) returns an HTML page of aggregate timing data.

When disabled: `TimingFilter` and the `/stats` endpoint are not registered. `RequestScopeFilter` still runs, so the request scope is still established for every request.

Note: `StatModule`'s `TimingInterceptor` (the method interceptor bound to methods annotated `@Timed`) is wired in the core module unconditionally — its installation does not depend on `enable_profiling`. `enable_profiling` gates only the `TimingFilter` and `/stats` servlet registration above.

---

### Shutdown sequence

`ShutdownManager` is a singleton registered as a JVM shutdown hook (via `Runtime.addShutdownHook`). It stores registered tasks in a `TreeMap<ShutdownPriority, Set<Shutdownable>>` with **no custom comparator**, so iteration order is the enum's **natural (ordinal / declaration) order**, *not* the numeric `.value` field. The `ShutdownPriority` enum is declared `Server(1), Waves(3), Task(2), Storage(3)`, giving ordinals `Server=0, Waves=1, Task=2, Storage=3`. The shutdown loop never reads `.value`.

On JVM shutdown signal the manager iterates priorities in ordinal order:
1. Priority `Server`: stops Jetty (`server.stopServer()`).
2. Priority `Waves`: wave-layer cleanup.
3. Priority `Task`: application tasks.
4. Priority `Storage`: storage cleanup.

> **Latent bug — `.value` does not match run order.** The `.value` field (`Server=1, Waves=3, Task=2, Storage=3`) is dead/unused: `ShutdownManager` orders purely by enum ordinal. Because of this, `Waves` (intended value 3) actually runs **before** `Task` (intended value 2) — the run order contradicts the integer values. A reader who trusts the `.value` numbers will get the order wrong. A Go reimplementation should use an explicit ordered list reflecting the intended semantics (e.g. Server → Task → Waves → Storage, or whatever the intended phase order is), not replicate the ordinal-vs-value mismatch.

Multiple tasks registered at the same priority are kept in an unordered `Set`, so they run in unspecified order within that level. Exceptions during shutdown are logged but do not abort the remaining tasks.

---

## Wire / storage formats

This layer produces no new wire or storage formats. It:
- Receives `ProtocolWaveletDelta` (protobuf) from clients over the wire protocol (see [04-wire-protocol](04-wire-protocol.md)).
- Constructs `ProtocolSignedDelta` and `ProtocolAppliedWaveletDelta` (see [04-wire-protocol](04-wire-protocol.md)).
- Persists delta records via the delta store (see [05-storage-persistence](05-storage-persistence.md)).

---

## Interfaces / APIs

### WaveletProvider (waveserver façade)

```
interface WaveletProvider:
  initialize()                                     # must be called before any other method
  submitRequest(waveletName, delta, listener)      # listener: onSuccess(ops, version, ts) | onFailure(msg)
  getHistory(waveletName, start, end, receiver)    # streams TransformedWaveletDelta
  checkAccessPermission(waveletName, participant)  # → bool
  getWaveIds()                                     # → Iterator<WaveId>
  getWaveletIds(waveId)                            # → Set<WaveletId>
  getSnapshot(waveletName)                         # → CommittedWaveletSnapshot | null
```

### ClientFrontend

```
interface ClientFrontend:
  openRequest(user, waveId, filter, knownWavelets, openListener)
  submitRequest(user, waveletName, delta, channelId, submitListener)

interface OpenListener:
  onUpdate(waveletName, snapshot?, deltas, committedVersion?, marker?, channelId?)
  onFailure(errorMessage)
```

### WaveBus

```
interface WaveBus:
  subscribe(subscriber)
  unsubscribe(subscriber)

interface Subscriber:
  waveletUpdate(wavelet: ReadableWaveletData, deltas: DeltaSequence)
  waveletCommitted(waveletName, version: HashedVersion)
```

### WaveletContainer (core state object)

```
interface WaveletContainer:
  getWaveletName() → WaveletName
  copyWaveletData() → ObservableWaveletData         # deep copy; safe outside lock
  getSnapshot() → CommittedWaveletSnapshot
  applyFunction(fn: ReadableWaveletData → T) → T
  requestHistory(start, end, receiver)              # streams ProtocolAppliedWaveletDelta
  requestTransformedHistory(start, end, receiver)   # streams TransformedWaveletDelta
  checkAccessPermission(participant) → bool
  getLastCommittedVersion() → HashedVersion
  hasParticipant(participant) → bool
  getCreator() → ParticipantId | null
  getSharedDomainParticipant() → ParticipantId | null
  isEmpty() → bool

interface LocalWaveletContainer extends WaveletContainer:
  submitRequest(waveletName, signedDelta) → WaveletDeltaRecord
  isDeltaSigner(version, signerId) → bool

interface RemoteWaveletContainer extends WaveletContainer:
  update(deltas, domain, federationProvider, certificateManager) → Future<Void>
  commit(version)
```

### CertificateManager

```
interface CertificateManager:
  getLocalDomains() → Set<String>
  getLocalSigner() → SignatureHandler
  signDelta(serializedDelta) → ProtocolSignedDelta
  verifyDelta(signedDelta) → ByteStringMessage<ProtocolWaveletDelta>  # throws on bad sig
  storeSignerInfo(signerInfo)
  retrieveSignerInfo(signerId) → ProtocolSignerInfo | null
  prefetchDeltaSignerInfo(provider, signerId, waveletName, deltaEndVersion, callback)
```

---

## Edge cases & failure modes

**Wavelet load failure**: If `DeltaStore.open()` throws, the container transitions to `CORRUPTED`. All subsequent calls that invoke `checkStateOk()` throw `WaveletStateException`. The container is never removed from the cache and cannot recover — process restart is required.

**Load timeout**: `awaitLoad()` times out after 1000 seconds and throws `WaveletStateException`. This is extremely long; in practice it signals a storage hang.

**Duplicate submit**: If OT transforms a delta to a version already in history (same author, same ops), `LocalWaveletContainerImpl` returns the existing `WaveletDeltaRecord` without re-applying. This makes submit idempotent on retry.

**Transformed-away delta**: If OT produces an empty operation list (currently never happens with the implemented algorithm), the container returns an empty `WaveletDeltaRecord` and does not persist or notify.

**Hash mismatch at same version**: If `delta.targetVersion.version == currentVersion.version` but hashes differ, `InvalidHashException` is thrown and the submit is rejected.

**Overlapping submit on a channel**: `WaveViewSubscription.submitRequest()` asserts `!hasOutstandingSubmit`. Overlapping submits from the same client on the same channel are a protocol violation.

**Failed-submit NPE / stuck channel (original Java bug)**: On a failed submit, `ClientFrontendImpl.onFailure` calls `UserManager.submitResponse(channelId, waveletName, null)`. `UserManager.submitResponse` forwards to the matching `WaveViewSubscription.submitResponse`, whose **first statement** is `Preconditions.checkNotNull(version, "Null delta application version")`. For the channel that issued the submit a matching subscription always exists, so this throws `NullPointerException` **before** clearing `hasOutstandingSubmit` and **before** flushing `heldBackDeltas`. Consequences in the original Java: the channel's `hasOutstandingSubmit` stays `true` forever, all later wavelet updates are queued into `heldBackDeltas` and never delivered, and the next submit on that channel trips the overlapping-submit assertion (`checkState(!hasOutstandingSubmit)`). Note the contradiction: `UserManager.submitResponse`'s javadoc documents the version arg as nullable on failure, but the callee rejects null. A Go reimplementation **MUST NOT** faithfully pass null here. Correct behavior on submit failure: skip the version-recording / echo-suppression step but **still** clear the outstanding-submit flag and flush/deliver the held-back deltas so the channel is unblocked (e.g. route the failure path through an unblock routine that takes no version, or use a sentinel, rather than recording a `submittedEndVersion`).

**Remote domain notification failure**: Federation fanout errors from `WaveletNotificationDispatcher` are logged but do not roll back the applied delta. The delta is still persisted and local subscribers are still notified.

**Bus subscriber exception**: `WaveletNotificationDispatcher` catches and logs `RuntimeException` from bus subscribers but does not remove the subscriber and does not halt fanout to remaining subscribers.

**WaveMap cache eviction**: When a wave is evicted from the LRU cache, its `WaveletContainer` instances are discarded but the data remains in storage. On next access the wavelet is loaded from storage again (LOADING → OK). There is no in-process notification of eviction; any ongoing subscriber for that wavelet would have lost its reference.

**Access to non-existent wavelet**: `getSnapshot()` returns `null` for a wavelet that has never had a delta applied. `WaveletProvider.submitRequest()` creates the container via `getOrCreateLocalWavelet()`, so the first submit against a new wavelet implicitly creates it. A `checkAccessPermission()` on an empty (version-0) wavelet returns `true` for any participant — anyone can write the first delta.

---

## Open questions / ambiguities

1. **WaveMap eviction vs. active subscribers**: The WaveMap uses a timed LRU cache. If a wave is evicted while a client has it open, the WaveletContainer is discarded. On the next delta that arrives from federation the wave is re-created from storage. But what happens to the in-memory `WaveletInfo` state in the frontend? The eviction is silent; this may cause a version-contiguity assertion failure in `WaveletInfo.syncWaveletVersion`. The Java server appears not to handle this case gracefully. A Go rewrite should decide whether to pin waves in cache as long as they have active subscriptions, or to handle the resync explicitly.

2. **`knownWavelets` parameter in openRequest**: The code immediately rejects any non-empty `knownWavelets` with "Known wavelets not supported". This is a partial-resync feature that was never implemented. A Go rewrite can either remove the parameter or implement it.

3. **`ShutdownPriority` ordering**: The `TreeMap` sorts by enum ordinal (declaration order), not by the `.value` field, which is never read. Declaration order is `Server(1), Waves(3), Task(2), Storage(3)`, so the actual run order is Server → Waves → Task → Storage. This means `Task` is interleaved *between* `Waves` and `Storage`, and `Waves` (value 3) runs before `Task` (value 2) — the run order contradicts the intended `.value` numbers. This is a bug. A Go rewrite should use an explicit ordered list reflecting the intended phase semantics rather than relying on enum ordinal.

4. **`delta_persist_executor_thread_count` vs. `DeltaPersistExecutor`**: `DeltaStoreBasedWaveletState` receives a `persistExecutor` constructor argument, but `WaveServerModule` actually passes the `WaveletLoadExecutor` again rather than `DeltaPersistExecutor` (the binding exists but the factory lambda uses `waveletLoadExecutor`). This looks like a bug. A Go rewrite should wire persist and load to separate goroutine pools.

5. **Thread safety of `ClientFrontendImpl.waveletUpdate`**: This method is called synchronously from `WaveletNotificationDispatcher.waveletUpdate`, which is itself called while the wavelet write lock is held (from `notifyOfDeltas`). The frontend then calls `UserManager.onUpdate` which is `synchronized`. This creates a lock-ordering dependency: wavelet write lock → UserManager lock. Verify there is no reverse ordering elsewhere before adopting this in Go.

6. **Implicit vs. explicit participant tracking**: `WaveletInfo` tracks both `explicitParticipants` (in the wavelet participant list) and `implicitParticipants` (users who opened a wave but are not listed participants, e.g., via a shared-domain participant). The `getImplicitWaveletParticipants()` method actually returns `explicitParticipants` (same field) — this looks like a copy-paste bug. Clarify the intended semantics before rewriting.

7. **Federation is a no-op by default**: The Java server ships `NoOpFederationModule`. A Go rewrite targeting a single-server deployment can omit federation entirely. XMPP federation logic is in spec [07-federation](07-federation.md).

8. **Clock injection**: `LocalWaveletContainerImpl.transformAndApplyLocalDelta()` uses `System.currentTimeMillis()` directly with a TODO noting that a `Clock` should be injected. A Go rewrite should make the time source injectable for testability.

---

## Source references

| File | Role |
|------|------|
| `box/server/ServerMain.java` | Entry point; bootstrap sequence; initialization order. |
| `box/server/ServerModule.java` | Main Guice module: session manager, RPC provider, ID generator, robot registrar. |
| `box/server/StatModule.java` | Profiling interceptor wiring; `@Timed` AOP binding. |
| `box/server/SearchModule.java` | Search backend selection (memory / Lucene / Solr). |
| `box/server/CoreSettingsNames.java` | Config key constants. |
| `wave/config/reference.conf` | Default configuration values and documentation. |
| `box/server/executor/ExecutorAnnotations.java` | Executor binding annotation definitions. |
| `box/server/executor/ExecutorsModule.java` | Thread-pool wiring; size from config. |
| `box/server/waveserver/WaveServerModule.java` | Waveserver Guice bindings; wavelet state loader factories. |
| `box/server/waveserver/WaveMap.java` | LRU cache: WaveId → Wave; wavelet lookup. |
| `box/server/waveserver/Wave.java` | Per-wave local/remote wavelet caches. |
| `box/server/waveserver/WaveletContainerImpl.java` | Abstract base: locking, load/corrupt state machine, OT helper, persist/notify. |
| `box/server/waveserver/LocalWaveletContainerImpl.java` | Local delta submit, OT, apply, sign, notify, persist. |
| `box/server/waveserver/RemoteWaveletContainerImpl.java` | Remote delta accept; pending-delta buffer; history fetch. |
| `box/server/waveserver/WaveletState.java` | Interface: in-memory+persisted delta log. |
| `box/server/waveserver/WaveletDeltaRecord.java` | Triple: appliedAtVersion + appliedDelta + transformedDelta. |
| `box/server/waveserver/WaveServerImpl.java` | `WaveletProvider` + `WaveletFederationProvider` impl; submit routing; cert verification. |
| `box/server/waveserver/WaveBus.java` | Pub-sub interface for wavelet events. |
| `box/server/waveserver/WaveletNotificationDispatcher.java` | WaveBus + remote federation fanout. |
| `box/server/waveserver/CertificateManager.java` | Delta signing/verification interface. |
| `box/server/frontend/ClientFrontend.java` | Frontend RPC interface. |
| `box/server/frontend/ClientFrontendImpl.java` | Frontend implementation; WaveBus subscriber; open/submit handling. |
| `box/server/frontend/WaveletInfo.java` | Per-wavelet version tracking and participant sets. |
| `box/server/frontend/UserManager.java` | Per-user subscription management and submit-hold logic. |
| `box/server/frontend/WaveViewSubscription.java` | Single channel subscription; delta queuing; echo suppression. |
| `box/server/shutdown/ShutdownManager.java` | JVM shutdown hook; priority-ordered task execution (orders by enum ordinal via `TreeMap`, ignores `.value`). |
| `box/server/shutdown/ShutdownPriority.java` | Shutdown priority enum; declared `Server(1), Waves(3), Task(2), Storage(3)` — `.value` is dead, ordinal drives order. |
| `box/server/util/WaveletDataUtil.java` | Builds wavelet documents with `SchemaCollection.empty()` (TODO issue 109: schemas not enforced server-side). |
| `box/server/robots/RobotWaveletData.java`, `box/server/robots/OperationContextImpl.java` | Also build documents with `SchemaCollection.empty()` (no server-side schema enforcement). |
| `wave/concurrencycontrol/common/ResponseCode.java` | OT-layer response codes incl. `SCHEMA_VIOLATION` — client-side / internal only; not on the live submit RPC. |
