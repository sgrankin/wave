# 05 — Storage & Persistence

## Purpose & scope

This spec defines how Apache Wave persists state to disk. It covers:

- The four abstract store interfaces: `DeltaStore`, `AccountStore`, `AttachmentStore`,
  and `SignerInfoStore`, plus the data types they traffic in.
- What exactly is persisted: the dual-record (applied + transformed) delta format, account
  data, attachments, and certificate info.
- Three backend implementations: in-memory (testing), file-based (default), and MongoDB.
- The file-based format in full byte-level detail — this is the format a Go rewrite needs
  to read or produce for migration compatibility.
- The protobuf message shapes used as serialization format.
- The `DataMigrationTool` command-line utility.
- Guidance on which abstractions are backend-agnostic vs implementation-specific, to make
  it straightforward to add a SQLite backend.

Excluded: the concurrency-control protocol (see [03-concurrency-control](03-concurrency-control.md)),
the wire protocol / federation protos (see [04-wire-protocol](04-wire-protocol.md)), and
the search/indexing subsystem (spec 11). Wavelet snapshots are **not** persisted by any
backend in the current implementation — the wavelet is always reconstructed by replaying
deltas on startup.

---

## Concepts & glossary

| Term | Definition |
|------|-----------|
| **WaveletName** | A compound key `(WaveId, WaveletId)` uniquely naming one wavelet. |
| **WaveletDeltaRecord** | A bundle of the applied delta (wire bytes) and the transformed delta (logical operations), plus the `appliedAtVersion`. |
| **Applied delta** | The serialized `ProtocolAppliedWaveletDelta` wire message: the original signed client submission plus how the server applied it. |
| **Transformed delta** | The server-canonical view: operations as OT-transformed, author, timestamp, resulting hashed version. |
| **HashedVersion** | `(version: uint64, historyHash: []byte)` — see [01-data-model](01-data-model.md). |
| **DeltaCollection / DeltasAccess** | A handle for reading and appending the delta history of one wavelet. Returned by `DeltaStore.open()`. |
| **ParticipantId** | `user@domain` address string identifying an account. |
| **AttachmentId** | An opaque identifier for an attachment blob, serialized as a string. |
| **SignerId** | The byte-array primary key for a certificate chain (PkiPath hash). |
| **AccountData** | Union of `HumanAccountData` (password digest) and `RobotAccountData` (OAuth, capabilities). |

---

## Data structures

### 5.1 WaveletDeltaRecord

```
WaveletDeltaRecord {
    appliedAtVersion HashedVersion   // version at which this delta was applied
    applied          bytes           // serialized ProtocolAppliedWaveletDelta (may be nil for local-origin deltas)
    transformed      TransformedWaveletDelta
}

TransformedWaveletDelta {
    author               ParticipantId
    resultingVersion     HashedVersion
    applicationTimestamp int64          // Unix milliseconds
    operations           []WaveletOperation
}
```

The `applied` field holds the raw wire bytes of the federation
`ProtocolAppliedWaveletDelta` message (see [04-wire-protocol](04-wire-protocol.md)). It
may be nil for local-origin deltas that were never signed.

**Key invariant**: `appliedAtVersion.version` is the start version; `transformed.resultingVersion.version`
is the end version. Consecutive records must be contiguous: end version of record N equals
the start version of record N+1. Record N=0 always starts at version 0.

### 5.2 AccountData

Two concrete types, discriminated by type tag:

```
HumanAccountData {
    id             ParticipantId
    passwordDigest PasswordDigest | nil
}

PasswordDigest {
    salt   []byte
    digest []byte   // salted SHA hash
}

RobotAccountData {
    id              ParticipantId
    url             string
    consumerSecret  string
    isVerified      bool
    capabilities    RobotCapabilities | nil
}

RobotCapabilities {
    capabilitiesHash string
    protocolVersion  string
    capabilities     map<EventType, Capability>
}

Capability {
    eventType string
    contexts  []string
    filter    string
}
```

### 5.3 AttachmentData

An attachment consists of three independent blobs stored under the same `AttachmentId`:

- **Data blob** — raw attachment bytes (any MIME type).
- **Thumbnail blob** — thumbnail image bytes; may be absent.
- **Metadata blob** — serialized `AttachmentProto.AttachmentMetadata` protobuf.

### 5.4 SignerInfo

Maps a `signerId` (byte array) to a `ProtocolSignerInfo` wire message (federation proto).
The `ProtocolSignerInfo` contains the hash algorithm, hash bytes, and the X.509 certificate
chain for a federated server.

---

## Algorithms & behavior

### 5.5 DeltaStore operations

`DeltaStore` is the central persistence interface for the delta log.

**open(WaveletName) → DeltasAccess**
- Returns a handle for the named wavelet.
- If the wavelet does not exist yet, creates it implicitly when the first delta is appended.
- The caller is responsible for serializing all concurrent calls to `open` and `delete` for
  the same wavelet name.

**delete(WaveletName)**
- Removes a non-empty wavelet permanently. Throws `FileNotFoundPersistenceException` if
  the wavelet does not exist.

**lookup(WaveId) → Set\<WaveletId\>**
- Returns the set of wavelet IDs belonging to the wave that have at least one delta
  (i.e. `endVersion.version > 0`). Empty if the wave does not exist.

**getWaveIdIterator() → Iterator\<WaveId\>**
- Returns all wave IDs that have at least one non-empty wavelet. The iterator is not
  thread-safe. Concurrent modifications to the store may or may not be reflected.

### 5.6 DeltasAccess operations

**append([]WaveletDeltaRecord)**
- Atomically appends a batch of contiguous delta records.
- On return (no exception), the deltas are durably stored (fsync'd for file backend).
- The caller guarantees: applied and transformed deltas are consistent, hashes are correct,
  and the batch starts from the collection's current end version.

**getDelta(version) → WaveletDeltaRecord | nil**
- Returns the record whose `appliedAtVersion.version == version`, or nil.

**getDeltaByEndVersion(version) → WaveletDeltaRecord | nil**
- Returns the record whose `transformed.resultingVersion.version == version`, or nil.

**getEndVersion() → HashedVersion | nil**
- The resulting version of the most recently appended delta. Nil if empty.

**isEmpty() → bool**

**getAppliedDelta(version)**, **getTransformedDelta(version)**,
**getAppliedAtVersion(version)**, **getResultingVersion(version)** — convenience accessors
that delegate to `getDelta`.

**close()** — Releases any file handles or connections.

### 5.7 AccountStore operations

**initializeAccountStore()** — Validates configuration, establishes connections, loads
data if necessary. Called once at startup.

**getAccount(ParticipantId) → AccountData | nil**

**putAccount(AccountData)** — Upsert (overwrites if participant already exists).

**removeAccount(ParticipantId)** — Deletes the account record.

### 5.8 AttachmentStore operations

**getMetadata(AttachmentId) → AttachmentMetadata | nil**

**getAttachment(AttachmentId) → AttachmentData | nil** — Returns handle with `getInputStream()` and `getSize()`.

**getThumbnail(AttachmentId) → AttachmentData | nil**

**storeMetadata(AttachmentId, AttachmentMetadata)** — Throws if already exists.

**storeAttachment(AttachmentId, InputStream)** — Throws if already exists.

**storeThumbnail(AttachmentId, InputStream)** — Throws if already exists.

**deleteAttachment(AttachmentId)** — Also removes thumbnail and metadata where the backend
supports it (MongoDB). File backend only deletes the data blob; thumbnail/metadata files
remain.

### 5.9 SignerInfoStore operations

Extends `CertPathStore`:

**initializeSignerInfoStore()** — Startup validation.

**getSignerInfo([]byte signerId) → SignerInfo | nil**

**putSignerInfo(ProtocolSignerInfo)** — Upsert, keyed by the signer ID derived from the
cert chain hash.

---

## Wire / storage formats

### 5.10 File-based delta store

#### Directory layout

```
<delta_store_directory>/
  <encoded-wave-id>/
    <encoded-wavelet-id>.deltas   ← delta data file
    <encoded-wavelet-id>.index    ← seek index file
```

**ID encoding** — Both wave and wavelet IDs are encoded as `<hex(domain)>_<hex(id)>`
where `hex(s)` is the UTF-8 bytes of string `s` in lowercase hex. Each component of the
wave and wavelet ID is encoded separately, then joined with a literal `_`. Examples:

```
WaveId{domain="example.com", id="w+abc"} →
    hex("example.com") + "_" + hex("w+abc")
    = "6578616d706c652e636f6d" + "_" + "772b616263"

WaveletName path =
    waveIdSegment + "/" + waveletIdSegment
```

Decoding: split on the single `_`, hex-decode each half to bytes, interpret as UTF-8.

#### Delta data file format (`.deltas`)

```
File = FileHeader DeltaRecord*

FileHeader (8 bytes):
    magic[4]   = 0x57 0x41 0x56 0x45   ("WAVE")
    version[4] = 0x00 0x00 0x00 0x01   (big-endian int32 = 1)

DeltaRecord:
    DeltaRecordHeader (12 bytes):
        protoVersion[4]          big-endian int32, must be 1
        appliedDeltaLength[4]    big-endian int32, byte count of applied blob
        transformedDeltaLength[4]big-endian int32, byte count of transformed blob
    appliedBytes[appliedDeltaLength]    serialized ProtocolAppliedWaveletDelta
    transformedBytes[transformedDeltaLength] serialized ProtoTransformedWaveletDelta
```

- All integers are big-endian (Java `DataOutputStream` / `RandomAccessFile.writeInt`).
- `appliedDeltaLength` may be 0 if the applied delta is absent (local-origin ops not
  signed by federation).
- `transformedBytes` is a serialized `ProtoTransformedWaveletDelta` protobuf (see §5.13).
- Records are written sequentially; there are no gaps and no alignment padding between
  records.
- On `append`, the file pointer is seeked to `file.length()` before writing. After all
  records are written, `fsync` is called.
- On open, trailing junk (incomplete record) is truncated: the file is truncated to the
  position of the file pointer after reading the last complete record (as indexed by the
  index file).

#### Index file format (`.index`)

The index is an array of `int64` values (8 bytes each, big-endian), one entry per wavelet
**operation** (not per delta). Entry `i` corresponds to wavelet version `i`.

```
Index[version] = int64 value
```

Interpretation:
- If `value >= 0`: the byte offset into the `.deltas` file at which the `DeltaRecord` for
  the delta applied at `version` begins.
- If `value < 0`: this version is within a multi-operation delta; the offset of that
  delta's `DeltaRecord` is `~value` (bitwise complement, i.e. `-(value+1)`).

**Writing** — When appending a delta that starts at version V and contains N operations:

```
index[V]   = offset_of_delta_record   (positive)
index[V+1] = ~offset_of_delta_record  (negative, for each subsequent op)
...
index[V+N-1] = ~offset_of_delta_record
```

This means the index file has exactly as many 8-byte entries as the total number of
operations across all deltas.

**Reading** — To find the record for version V:
- Read `index[V]`. If negative, there is no record starting at that version (return nil).
- If non-negative, seek the `.deltas` file to that offset.

To find the record whose **end version** is V (i.e. `getDeltaByEndVersion(V)`):
- Read `index[V-1]`. If negative, `offset = ~index[V-1]`. If positive, `offset = index[V-1]`.
- Then verify the entry at `index[V]` is non-negative (i.e. a new delta starts at V, not
  mid-delta) unless V is past end of index.

**Rebuilding** — On every open, the index is always rebuilt from scratch by scanning the
`.deltas` file sequentially. The `.index` file acts as a cache; its contents on disk are
discarded and replaced. A Go implementation may preserve this behavior or only rebuild on
corruption.

**length()** — `file.length() / 8` gives the number of index entries, i.e. total operations
ever applied to the wavelet.

#### File-based account store

```
<account_store_directory>/
  <participant-address-lowercase>.account
```

Each `.account` file is a raw serialized `ProtoAccountData` protobuf (no framing, no
length prefix). The filename is `participant.address().toLowerCase() + ".account"`.
The in-memory cache is populated on first access and invalidated on write/delete.

#### File-based attachment store

```
<attachment_store_directory>/
  <base64(attachmentId.serialise())>           ← data blob (no extension)
  <base64(attachmentId.serialise())>.meta      ← serialized AttachmentProto.AttachmentMetadata
  <base64(attachmentId.serialise())>.thumbnail ← thumbnail blob
```

`attachmentId.serialise()` produces a string; it is encoded with standard Base64
(CharBase64, not URL-safe) using the UTF-8 bytes of that string.

`storeAttachment`, `storeMetadata`, `storeThumbnail` each throw `IOException` if the target
file already exists. `deleteAttachment` only deletes the data blob file.

#### File-based signer-info store

```
<signer_info_store_directory>/
  <hex(signerId)>.signer
```

`signerId` is the raw byte array; `hex()` is lowercase hex encoding. Each `.signer` file
is a raw serialized `ProtocolSignerInfo` protobuf (no framing). An in-memory
`DefaultCertPathStore` acts as a write-through cache.

---

### 5.11 In-memory store

The in-memory backend stores everything in heap data structures. It is used for testing and
scenarios where durability is not required.

- `MemoryDeltaStore` — nested `Map<WaveId, Map<WaveletId, MemoryDeltaCollection>>`.
- `MemoryDeltaCollection` — two maps: `Map<Long, WaveletDeltaRecord>` keyed by start
  version, and another keyed by end version. `endVersion` tracked as a field.
- `MemoryStore` — implements both `SignerInfoStore` and `AccountStore`. Delegates signer
  info to `DefaultCertPathStore`; accounts in a `ConcurrentHashMap<ParticipantId, AccountData>`.
- No separate attachment store in memory — the `AttachmentStore` interface has no
  in-memory implementation; tests use the file store with a temp directory.

---

### 5.12 MongoDB store

#### DeltaStore — `deltas` collection

One document per `WaveletDeltaRecord`. Document schema:

```
{
  waveid:          string,       // WaveId.serialise()
  waveletid:       string,       // WaveletId.serialise()
  appliedatversion: {
    version:     int64,
    historyhash: binary
  },
  applied:         binary,       // raw ProtocolAppliedWaveletDelta bytes
  transformed: {
    author:              { address: string },
    resultingversion:    { version: int64, historyhash: binary },
    appliedatversion:    int64,  // the version number only (not full HashedVersion)
    applicationtimestamp:int64,
    ops: [
      { type: "NoOp" }
      | { type: "AddParticipant",    participant: { address: string } }
      | { type: "RemoveParticipant", participant: { address: string } }
      | { type: "WaveletBlipOperation",
          blipid: string,
          blipop: { contentop: { bytes: binary } } }
    ]
  }
}
```

`contentop.bytes` is the serialized `ProtocolDocumentOperation` protobuf.

Write concern: `JOURNALED` for all inserts and deletes.

`lookup(WaveId)` — queries `{waveid: X}`, projects `{waveletid: 1}`, returns distinct
wavelet IDs. Note: it does **not** filter out empty wavelets the way the file backend does.

`getWaveIdIterator()` — calls `distinct("waveid")` on the collection.

**Nil `applied` delta — backend asymmetry (constraint).** The data model (§5.1, §5.6)
permits a nil `applied` field for local-origin deltas that were never signed. The **file**
backend honours this: it encodes a nil applied delta as `appliedDeltaLength = 0` and reads
it back as nil. The **MongoDB** backend does **not** null-check the `applied` field:
`MongoDbDeltaStoreUtil.serialize()` calls `getAppliedDelta().getByteArray()` unconditionally
(NPE on nil), and `deserializeWaveletDeltaRecord()` unconditionally wraps the stored
`applied` bytes (fails on a missing field). The MongoDB backend is therefore incompatible
with a nil-applied record. A Go reimplementation that aims for the documented data-model
property (`applied` may be nil) must add explicit nil handling on **both** the serialize and
deserialize paths for **every** backend, and should treat a nil/absent applied delta as a
first-class case. Note that in the current Java OT algorithm no nil-applied record is ever
actually persisted — the only nil-applied `WaveletDeltaRecord` (the "transformed away" case
in `LocalWaveletContainerImpl`) is excluded from the delta history — so this is a latent
inconsistency rather than a guaranteed runtime failure.

#### AccountStore, SignerInfoStore, AttachmentStore — `MongoDbStore`

**Account collection** (`account`):
```
{
  _id: string,   // ParticipantId.getAddress()
  human: {
    passwordDigest: { salt: binary, digest: binary } | absent
  }
  | robot: {
    url:          string,
    secret:       string,
    verified:     bool,
    capabilities: {
      capabilitiesHash: string,
      version:          string,
      capabilities: {
        "<EventType>": { contexts: [string, ...], filter: string },
        ...
      }
    } | absent
  }
}
```

Only one of `human` or `robot` is present per document.

**SignerInfo collection** (`signerInfo`):
```
{
  _id:       binary,   // signerId bytes
  protoBuff: binary    // serialized ProtocolSignerInfo
}
```

**Attachments** — stored in MongoDB GridFS:
- Grid bucket `attachments` — data blobs, filename = `attachmentId.serialise()`.
- Grid bucket `thumbnails` — thumbnails.
- Grid bucket `metadata` — serialized `AttachmentProto.AttachmentMetadata` bytes.

`deleteAttachment` removes from all three GridFS buckets.

---

### 5.13 Persistence protobufs

#### `delta-store.proto` — `ProtoTransformedWaveletDelta`

Package `protodeltastore`. Used as the serialized form of the transformed delta inside
each `.deltas` record.

```protobuf
message ProtoTransformedWaveletDelta {
  required string author = 1;           // ParticipantId address
  required ProtocolHashedVersion resulting_version = 2;
  required int64 application_timestamp = 3;  // Unix ms
  repeated ProtocolWaveletOperation operation = 4;
}
```

`ProtocolHashedVersion` and `ProtocolWaveletOperation` are from
`federation.protodevel` (see [04-wire-protocol](04-wire-protocol.md)).

Note: the applied-at version is **not** stored in this proto — it is recovered from the
`ProtocolAppliedWaveletDelta` in the applied blob, or from context.

#### `account-store.proto` — `ProtoAccountData` et al.

Package `protoaccountstore`. Used as the serialized form in `.account` files.

```protobuf
message ProtoAccountData {
  enum AccountDataType { HUMAN_ACCOUNT = 1; ROBOT_ACCOUNT = 2; }
  required AccountDataType account_type = 1;
  required string account_id = 2;           // ParticipantId address
  optional ProtoHumanAccountData human_account_data = 3;
  optional ProtoRobotAccountData robot_account_data = 4;
}

message ProtoHumanAccountData {
  optional ProtoPasswordDigest password_digest = 1;
}

message ProtoPasswordDigest {
  required bytes salt   = 1;
  required bytes digest = 2;
}

message ProtoRobotAccountData {
  required string url              = 1;
  required string consumer_secret  = 2;
  optional ProtoRobotCapabilities robot_capabilities = 3;
  required bool is_verified        = 4;
}

message ProtoRobotCapabilities {
  required string capabilities_hash  = 1;
  required string protocol_version   = 2;
  repeated ProtoRobotCapability capability = 3;
}

message ProtoRobotCapability {
  required string event_type = 1;
  repeated string context    = 2;
  required string filter     = 3;
}
```

---

## Interfaces / APIs

### 5.14 Backend-agnostic contracts (implement these for SQLite)

The following interfaces are completely backend-agnostic and form the stable surface that
any new backend (e.g. SQLite) must implement:

```
WaveletStore<T extends WaveletDeltaRecordReader> {
    open(WaveletName) → T
    delete(WaveletName)
    lookup(WaveId) → Set<WaveletId>
    getWaveIdIterator() → Iterator<WaveId, PersistenceException>
}

DeltaStore extends WaveletStore<DeltasAccess> { }

DeltasAccess extends WaveletDeltaRecordReader, Closeable {
    append([]WaveletDeltaRecord)
}

WaveletDeltaRecordReader {
    getWaveletName() → WaveletName
    isEmpty() → bool
    getEndVersion() → HashedVersion | nil
    getDelta(version) → WaveletDeltaRecord | nil
    getDeltaByEndVersion(version) → WaveletDeltaRecord | nil
    getAppliedAtVersion(version) → HashedVersion | nil
    getResultingVersion(version) → HashedVersion | nil
    getAppliedDelta(version) → bytes | nil
    getTransformedDelta(version) → TransformedWaveletDelta | nil
}

AccountStore {
    initializeAccountStore()
    getAccount(ParticipantId) → AccountData | nil
    putAccount(AccountData)
    removeAccount(ParticipantId)
}

AttachmentStore {
    getMetadata(AttachmentId) → AttachmentMetadata | nil
    getAttachment(AttachmentId) → AttachmentData | nil
    getThumbnail(AttachmentId) → AttachmentData | nil
    storeMetadata(AttachmentId, AttachmentMetadata)
    storeAttachment(AttachmentId, InputStream)
    storeThumbnail(AttachmentId, InputStream)
    deleteAttachment(AttachmentId)
}

SignerInfoStore extends CertPathStore {
    initializeSignerInfoStore()
    getSignerInfo([]byte signerId) → SignerInfo | nil
    putSignerInfo(ProtocolSignerInfo)
}
```

### 5.15 Configuration

Store types are selected by config keys:

| Config key | Valid values | Default |
|---|---|---|
| `core.delta_store_type` | `memory`, `file`, `mongodb` | `file` |
| `core.account_store_type` | `memory`, `file`, `fake`, `mongodb` | `file` |
| `core.attachment_store_type` | `disk`, `mongodb` | `disk` |
| `core.signer_info_store_type` | `memory`, `file`, `mongodb` | `file` |
| `core.delta_store_directory` | filesystem path | — |
| `core.account_store_directory` | filesystem path | — |
| `core.attachment_store_directory` | filesystem path | — |
| `core.signer_info_store_directory` | filesystem path | — |
| `core.mongodb_host` | hostname | — |
| `core.mongodb_port` | port string | — |
| `core.mongodb_database` | DB name | — |

The `fake` account store accepts any login without checking credentials — for development
only.

---

## Edge cases & failure modes

**Truncation on crash** — On `open()`, recovery happens in two steps. (1) The index is
rebuilt by scanning the delta file forward record-by-record (`getOffsetsIterator`); the
scan stops at the **first** record that fails to read (a partial/corrupt write), so only
complete leading records are indexed. (2) `initializeEndVersionAndTruncateTrailingJunk()`
then does **not** scan: it takes `numRecords = index.length()`, and if `>= 1` calls
`getDeltaByEndVersion(numRecords)` — a single seek (via the index) to the **last** indexed
record — which leaves the file pointer just past that record. It then calls
`file.setLength(file.getFilePointer())` to discard trailing junk. Consequently, only
trailing corruption (after the last complete record) is detected/discarded; the truncation
step itself reads exactly one record. Corruption in the **middle** of the file is not
repaired — the index scan simply treats the first unreadable record and everything after it
as junk to be truncated.

**Index rebuild** — The index file is always rebuilt from scratch by scanning the delta
file on open. The on-disk `.index` file is written during the scan; if it existed
previously it is deleted first. This means there is no risk of index/data divergence
across crashes, but the first open after a large crash may be slow.

**Missing applied delta** — `appliedDeltaLength` of 0 is legal. `readAppliedDelta(0)`
returns nil. Callers must handle nil applied deltas.

**Concurrent access** — The file backend is documented as **not** multithread-safe per
`FileDeltaCollection` instance. The server must ensure at most one collection instance
per wavelet is open at a time, and external serialization is required. The memory backend
is likewise not thread-safe at the collection level. The MongoDB backend inherits
thread-safety from the MongoDB driver.

**AccountStore caching** — `FileAccountStore` holds a `Map<ParticipantId, AccountData>`
in memory. On `putAccount`, the cache is updated synchronously. On `getAccount`, a cache
miss triggers a file read, then caches the result. Concurrent access is serialized with a
lock on the map. The MongoDB backend has no in-memory cache.

**Duplicate attachment write** — `FileAttachmentStore.storeAttachment` (and
`storeMetadata`, `storeThumbnail`) throws `IOException("Attachment already exist")` if
the file already exists. Callers must check before writing.

**Wavelet not found** — `WaveletStore.delete` throws `FileNotFoundPersistenceException`
(a subtype of `PersistenceException`) when the wavelet does not exist.

**MongoDB getDeltaByEndVersion bug** — The Java `MongoDbDeltaCollection.getDeltaByEndVersion`
contains a bug: it calls `deserializeWaveletDeltaRecord(result)` but assigns the return
value to a local that shadows `waveletDelta`, so it always returns nil. A Go reimplementation
should fix this.

---

## Open questions / ambiguities

1. **No snapshots** — Wave currently reconstructs wavelet state by replaying the full
   delta log on every load. For large wavelets this is slow. The spec deliberately omits
   snapshot persistence because it doesn't exist; a Go rewrite should design snapshot
   support from scratch.

2. **SQLite schema recommendation** — For a Go + SQLite backend, the natural schema is:
   - `deltas(wave_id TEXT, wavelet_id TEXT, applied_at_version INTEGER, resulting_version INTEGER,
     history_hash BLOB, applied_delta BLOB, transformed_delta BLOB)`
     with a PRIMARY KEY on `(wave_id, wavelet_id, applied_at_version)`.
   - `accounts(_id TEXT PRIMARY KEY, account_type TEXT, data BLOB)`
   - `signer_info(signer_id BLOB PRIMARY KEY, proto BLOB)`
   - Attachments: continue using the filesystem (same as `FileAttachmentStore`), or store
     BLOBs in SQLite with a separate table. Large BLOBs in SQLite perform best when the
     page size is tuned.

3. **lookup() semantics differ between backends** — The file backend filters out empty
   wavelets (endVersion.version == 0). The MongoDB backend does not — it returns any
   wavelet that has at least one document in the collection (even if all ops were no-ops
   leaving version at 0). A Go implementation should follow the file-backend semantics
   (filter out version-0 wavelets).

4. **Thread safety contract** — The spec says callers must serialize `open` and `delete`
   for the same wavelet. It is unclear whether concurrent `open` calls for different
   wavelets are safe in the file backend. In practice the server uses a per-wavelet lock
   at a higher layer.

5. **Index file purpose** — The index is rebuilt on every open anyway. Its primary value
   is for fast random-access during a session (O(1) seek to any version rather than O(n)
   scan). A SQLite backend gets this for free via indexed queries; no separate index file
   needed.

6. **Attachment delete incompleteness** — `FileAttachmentStore.deleteAttachment` only
   deletes the data blob, leaving `.meta` and `.thumbnail` files behind. This is likely
   a bug. MongoDB's delete correctly removes all three. A Go implementation should clean
   up all three files.

7. **DeltaMigrator ordering** — `DeltaMigrator` collects records in reverse (from
   `endVersion` back to 0) then calls `append` with the reversed list. The in-memory and
   file backends handle this because `append` is called with a correctly-ordered slice.
   Verify any Go backend can accept a reversed-order list passed as one batch, or change
   the migrator to pass forward order.

---

## Source references

| File | Role |
|------|------|
| `persistence/AccountStore.java` | AccountStore interface |
| `persistence/AttachmentStore.java` | AttachmentStore interface |
| `persistence/SignerInfoStore.java` | SignerInfoStore interface |
| `waveserver/DeltaStore.java` | DeltaStore + DeltasAccess interfaces |
| `waveserver/WaveletStore.java` | WaveletStore generic interface |
| `waveserver/WaveletDeltaRecordReader.java` | Reader interface |
| `waveserver/WaveletDeltaRecord.java` | Applied + transformed delta bundle |
| `persistence/file/FileDeltaStore.java` | File DeltaStore — directory layout, lookup, enumeration |
| `persistence/file/FileDeltaCollection.java` | File DeltasAccess — byte format, index usage, fsync |
| `persistence/file/DeltaIndex.java` | Index file format and seek logic |
| `persistence/file/FileUtils.java` | ID-to-path encoding |
| `persistence/file/FileAccountStore.java` | File account store — filenames, cache, proto I/O |
| `persistence/file/FileAttachmentStore.java` | File attachment store — filenames, Base64 encoding |
| `persistence/file/FileSignerInfoStore.java` | File signer store — hex filenames, write-through cache |
| `persistence/memory/MemoryDeltaStore.java` | In-memory DeltaStore |
| `persistence/memory/MemoryDeltaCollection.java` | In-memory DeltasAccess |
| `persistence/memory/MemoryStore.java` | In-memory AccountStore + SignerInfoStore |
| `persistence/mongodb/MongoDbDeltaStore.java` | MongoDB DeltaStore |
| `persistence/mongodb/MongoDbDeltaCollection.java` | MongoDB DeltasAccess (note getDeltaByEndVersion bug) |
| `persistence/mongodb/MongoDbDeltaStoreUtil.java` | MongoDB delta serialization — field names and schema |
| `persistence/mongodb/MongoDbStore.java` | MongoDB AccountStore + AttachmentStore + SignerInfoStore |
| `persistence/protos/ProtoAccountDataSerializer.java` | Account proto ↔ domain object mapping |
| `persistence/protos/ProtoDeltaStoreDataSerializer.java` | TransformedWaveletDelta proto ↔ domain object mapping |
| `persistence/PersistenceModule.java` | Dependency-injection wiring and config key names |
| `persistence/migration/DeltaMigrator.java` | Cross-backend delta copy logic |
| `DataMigrationTool.java` | CLI entry point for migration |
| `src/proto/.../account-store.proto` | Account persistence protobuf schema |
| `src/proto/.../delta-store.proto` | Delta persistence protobuf schema |
