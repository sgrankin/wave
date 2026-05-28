# 04 — Wire Protocol & RPC Transport

## Purpose & scope

This spec covers everything between the browser (or any client) and the Wave
server at the network layer: the WebSocket transport, the JSON message framing
that multiplexes multiple in-flight RPCs over it, and every protobuf message
shape exchanged. It also documents the separate HTTP endpoints used for search,
profiles, attachments, and wave snapshots.

The concurrency-control semantics of deltas (OT, versioning) are out of scope
and are covered in spec 03. This spec focuses only on message shapes, transport
mechanics, and serialization.

---

## Concepts & glossary

| Term | Meaning |
|---|---|
| **Envelope** | A JSON wrapper that carries one protobuf message together with its sequence number and type tag. |
| **Sequence number** | A per-connection monotonically increasing integer assigned by the sender. Used to correlate requests with responses and to cancel in-flight calls. |
| **Streaming RPC** | An RPC call where the server returns 0..N response messages before a final `RpcFinished`. Marked in the proto service definition with `option (rpc.is_streaming_rpc) = true`. |
| **Channel ID** | An opaque string assigned by the server in the first `ProtocolWaveletUpdate` message of an `Open` call. The client echoes it on subsequent `Submit` calls to allow the server to suppress its own updates from the channel's stream. |
| **Snapshot** | A full state image of a wavelet (all documents + participant list + version). |
| **Marker** | A boolean flag in `ProtocolWaveletUpdate` that signals the end of the initial snapshot burst. After the marker, the stream transitions to delta-only updates. |
| **PST (Proto Source Toolkit)** | Wave's internal code generator that produces JSON (Gson) serializers from `.proto` files. Generated code uses **field numbers** (not names) as JSON object keys. |
| **Int52** | An **in-memory / client-side** convenience only; it does **not** change the wire form. JavaScript numbers are 64-bit IEEE 754 doubles and represent integers exactly up to 2^53. The `[(int52)=true]` annotation makes the GWT JSO client overlay read the field as a JS double in memory. On the wire, **all** `int64` fields — int52-annotated or not — are encoded as a two-element JSON array `[lowWord, highWord]` (see Type mapping). |
| **Blob** | A `bytes` proto field serialized as a JSON string (unspecified encoding, opaque). |

---

## Data structures

### RPC envelope (sent over WebSocket, both directions)

Every message exchanged over the WebSocket is a JSON object with exactly three
keys:

```
{
  "sequenceNumber": <integer>,
  "messageType":   <string>,   // proto message simple class name, e.g. "ProtocolOpenRequest"
  "message":       <object>    // the body, serialized with PST-Gson rules (see below)
}
```

The envelope is not itself a protobuf. It is hand-crafted JSON. See
`WebSocketChannel.MessageWrapper` for the canonical definition.

---

### rpc.proto — the RPC control layer

**`CancelRpc`** — client → server  
An empty message. Sent with the sequence number of an active streaming RPC to
request cancellation. The server processes the cancellation but still terminates
the RPC through normal means.

```proto
message CancelRpc {}
```

**`RpcFinished`** — server → client  
Sent to terminate an RPC. For streaming RPCs, sent after the last payload
message. For failed unary or streaming RPCs, `failed = true`.

```proto
message RpcFinished {
  required bool   failed     = 1;
  optional string error_text = 2;
}
```

Invariant: a non-streaming RPC that succeeds sends exactly one payload message
and no `RpcFinished`. A non-streaming RPC that fails sends only `RpcFinished`
with `failed=true`. A streaming RPC sends 0..N payload messages followed by one
`RpcFinished`.

---

### waveclient-rpc.proto — the main client↔server service

**Service `ProtocolWaveClientRpc`**

| Method | Input | Output | Streaming |
|---|---|---|---|
| `Open` | `ProtocolOpenRequest` | `ProtocolWaveletUpdate` | Yes |
| `Submit` | `ProtocolSubmitRequest` | `ProtocolSubmitResponse` | No |
| `Authenticate` | `ProtocolAuthenticate` | `ProtocolAuthenticationResult` | No |

---

**`ProtocolAuthenticate`** — client → server

A workaround for browsers that do not send cookies over WebSocket connections.
The client sends the `JSESSIONID` cookie value as a token immediately after
connecting.

```proto
message ProtocolAuthenticate {
  required string token = 1;   // value of the JSESSIONID session cookie
}
```

Server validates the token. If invalid, closes the connection. Sends
`ProtocolAuthenticationResult` (empty) on success.

**`ProtocolAuthenticationResult`** — server → client (empty)

```proto
message ProtocolAuthenticationResult {}
```

---

**`ProtocolOpenRequest`** — client → server

Opens a streaming view of a wave.

```proto
message ProtocolOpenRequest {
  required string         participant_id    = 1;  // logged-in user (redundant — will be removed)
  required string         wave_id           = 2;  // URI-path format, e.g. "example.com/w+abc"
  repeated string         wavelet_id_prefix = 3;  // filter: only wavelets with matching id prefix
  repeated WaveletVersion known_wavelet     = 4;  // client's cached versions (for resync)
}

message WaveletVersion {
  required string                        wavelet_id      = 1;  // URI-path format
  required federation.ProtocolHashedVersion hashed_version = 2;
}
```

`wavelet_id_prefix`: an empty list means "all wavelets in the wave". A prefix
like `"example.com/conv+"` matches `"example.com/conv+root"` and any other
wavelet with that prefix.

`known_wavelet`: intended for resynchronization — the client supplies cached
versions so the server could skip the snapshot and resume the delta stream from
a known version. **This is NOT implemented in the reference server.**
`ClientFrontendImpl.openRequest` rejects the *entire* open request with
`onFailure("Known wavelets not supported")` whenever `known_wavelet` is
non-empty. In practice the field is always sent empty: the GWT client's
`RemoteWaveViewService.viewOpen` accepts a `knownWavelets` argument but never
forwards it into the `ProtocolOpenRequest`. A Go reimplementation only needs to
accept an empty `known_wavelet` list and may reject (or ignore) non-empty lists;
the resync logic remains a TODO in the original code.

---

**`ProtocolWaveletUpdate`** — server → client (streaming response to `Open`)

One message per event. The fields present determine the semantic variant.
`channel_id` is **orthogonal** to the variant: during the initial open phase the
server sets `channel_id` on each per-wavelet message (it is repeated on every one
of them), so a snapshot message in that phase carries both the snapshot fields
*and* `channel_id`.

| Variant | Fields set |
|---|---|
| Snapshot (initial open) | `wavelet_name`, `snapshot`, `resulting_version`, `commit_notice`, **and** `channel_id` |
| Channel-only (no wavelets in view) | `wavelet_name`, `channel_id` — sent as a *single* message **only** when the wave view contains zero visible wavelets (`ClientFrontendImpl` lines 169-172) |
| Marker (end of initial snapshots) | `wavelet_name`, `marker=true` |
| Delta update | `wavelet_name`, `applied_delta` (1+), `resulting_version`, optionally `commit_notice` |

```proto
message ProtocolWaveletUpdate {
  required string                           wavelet_name      = 1;  // URI-path, always present
  repeated federation.ProtocolWaveletDelta  applied_delta     = 2;  // deltas (0 for snapshot/marker)
  optional federation.ProtocolHashedVersion commit_notice     = 3;  // server's durable commit version
  optional federation.ProtocolHashedVersion resulting_version = 4;  // version after all deltas / snapshot version
  optional WaveletSnapshot                  snapshot          = 5;
  optional bool                             marker            = 6 [default=false];
  optional string                           channel_id        = 7;  // set on each per-wavelet update during initial open; standalone only when no wavelets visible
}
```

Stream lifecycle:
1. Initial open: the server sends one `ProtocolWaveletUpdate` per visible
   wavelet, each carrying the snapshot **and** `channel_id` (the client records
   the channel id; it is repeated identically on each message). If the wave view
   has **no** visible wavelets, the server instead sends a single
   channel-id-only message (`wavelet_name` + `channel_id`, no snapshot/marker).
2. One marker message (`marker=true`).
3. Zero or more delta/commit-notice messages, indefinitely, until the server
   terminates the stream with `RpcFinished`.

---

**`ProtocolSubmitRequest`** — client → server

```proto
message ProtocolSubmitRequest {
  required string                          wavelet_name = 1;  // URI-path
  required federation.ProtocolWaveletDelta delta        = 2;
  optional string                          channel_id   = 3;  // from the Open response
}
```

**`ProtocolSubmitResponse`** — server → client

```proto
message ProtocolSubmitResponse {
  required int32                            operations_applied               = 1;
  optional string                           error_message                    = 2;
  optional federation.ProtocolHashedVersion hashed_version_after_application = 3;
}
```

Invariant: exactly one of `(error_message)` or `(hashed_version_after_application)`
is present. `operations_applied` is 0 on error.

`error_message` carries opaque human-readable text sourced from the server-side
`FederationError.error_message` (`WaveServerImpl.submitRequest`'s `onFailure`
forwards only `errorMessage.getErrorMessage()`). The structured
`FederationError.Code` is **discarded** and does **not** reach the client. This
is the live browser submit-error channel; the structured
`ResponseStatus.ResponseCode` taxonomy from `clientserver.proto` is **not** used
on this path — see spec [03](03-concurrency-control.md).

---

**`DocumentSnapshot`**

```proto
message DocumentSnapshot {
  required string                              document_id            = 1;
  required federation.ProtocolDocumentOperation document_operation    = 2;  // initial → current state
  required string                              author                 = 3;  // first contributor
  repeated string                              contributor            = 4;  // all contributors
  required int64                               last_modified_version  = 5;
  required int64                               last_modified_time     = 6;  // ms since epoch
}
```

**`WaveletSnapshot`**

```proto
message WaveletSnapshot {
  required string                           wavelet_id         = 1;
  repeated string                           participant_id     = 2;
  repeated DocumentSnapshot                 document           = 3;
  required federation.ProtocolHashedVersion version            = 4;
  required int64                            last_modified_time = 5;  // ms since epoch
  required string                           creator            = 6;
  required int64                            creation_time      = 7;  // ms since epoch
}
```

**`WaveViewSnapshot`** — a snapshot of a user's view of a wave; contains
snapshots of all wavelets visible to the user. Returned by `/fetch/*` when the
URL path encodes only a wave (no wavelet id).

```proto
message WaveViewSnapshot {
  required string          wave_id = 1;  // URI-path format, e.g. "example.com/w+abc"
  repeated WaveletSnapshot wavelet = 2;  // one per visible wavelet
}
```

Note: per `FetchServlet`, the wave-level fetch currently returns only the
`conv+root` wavelet wrapped in this message (a single-element `wavelet` list in
practice), though the field is repeated. (`FetchServlet` line 142 comment:
fetching all visible wavelets of a wave is not yet implemented, so it defaults
to `conv+root`.)

---

### clientserver.proto — alternative/newer CC service definitions

This file defines a second, more structured service API that is parallel to
`waveclient-rpc.proto`. The Java web client uses `waveclient-rpc.proto`
exclusively; `clientserver.proto` appears to be a newer design that is not yet
wired to the browser client. Document it for completeness.

**`FetchService.Fetch`** — unary RPC

```proto
service FetchService {
  rpc Fetch(FetchWaveViewRequest) returns (FetchWaveViewResponse);
}

message FetchWaveViewRequest {
  required string         waveId       = 1;   // URI path
  repeated WaveletVersion knownWavelet = 2;
}

message FetchWaveViewResponse {
  required ResponseStatus status  = 1;
  repeated Wavelet        wavelet = 2;

  message Wavelet {
    required string          waveletId = 1;
    optional WaveletSnapshot snapshot  = 2;  // absent if client already knew this version
  }
}
```

**`WaveletChannelService`** — streaming RPC

```proto
service WaveletChannelService {
  rpc Open(OpenWaveletChannelRequest) returns (OpenWaveletChannelStream) {
    option (rpc.is_streaming_rpc) = true;
  };
  rpc Close(CloseWaveletChannelRequest) returns (EmptyResponse);
}

message OpenWaveletChannelRequest {
  required string                           waveId        = 1;
  required string                           waveletId     = 2;
  required federation.ProtocolHashedVersion beginVersion  = 3;  // start streaming from here
}

message OpenWaveletChannelStream {
  optional string                           channelId     = 1;  // first message only
  optional WaveletUpdate                    delta         = 2;  // subsequent messages
  optional federation.ProtocolHashedVersion commitVersion = 3;  // may accompany delta
  optional WaveletChannelTerminator         terminator    = 4;  // last message only
}

message WaveletUpdate {
  required federation.ProtocolWaveletDelta  delta               = 1;
  required federation.ProtocolHashedVersion resultingVersion    = 2;
  required int64                            applicationTimpstamp = 3;  // [sic] ms since epoch
}

message WaveletChannelTerminator {
  required ResponseStatus status = 1;
}

message CloseWaveletChannelRequest {
  required string channelId = 1;
}
```

**`DeltaSubmissionService`**

```proto
service DeltaSubmissionService {
  rpc Submit(SubmitDeltaRequest) returns (SubmitDeltaResponse);
}

message SubmitDeltaRequest {
  required string                          waveId    = 1;
  required string                          waveletId = 2;
  required federation.ProtocolWaveletDelta delta     = 3;
  required string                          channelId = 4;
}

message SubmitDeltaResponse {
  required ResponseStatus                   status                        = 1;
  required int32                            operationsApplied             = 2;
  optional federation.ProtocolHashedVersion hashedVersionAfterApplication = 3;
  optional int64                            timestampAfterApplication     = 4;
}
```

**`TransportAuthenticationService`** — mirrors `ProtocolAuthenticate` workaround

```proto
service TransportAuthenticationService {
  rpc Authenticate(TransportAuthenticationRequest) returns (EmptyResponse);
}

message TransportAuthenticationRequest {
  required string token = 1;
}
```

**`ResponseStatus`** — used across all `clientserver.proto` services

```proto
message ResponseStatus {
  enum ResponseCode {
    OK                = 0;
    BAD_REQUEST       = 1;
    INTERNAL_ERROR    = 2;
    NOT_AUTHORIZED    = 3;
    VERSION_ERROR     = 4;   // hashed version didn't match history
    INVALID_OPERATION = 5;   // operation invalid before or after transform
    SCHEMA_VIOLATION  = 6;   // operation broke document schema
    SIZE_LIMIT_EXCEEDED = 7;
    POLICY_VIOLATION  = 8;
    QUARANTINED       = 9;
    TOO_OLD           = 10;  // version too old; client must fetch and retry
  }
  required ResponseCode status        = 1;
  optional string       failureReason = 2;  // required when status != OK
}
```

---

### search.proto

Served via HTTP GET, not WebSocket.

```proto
message SearchRequest {
  required string query      = 1;   // e.g. "in:inbox"
  required int32  index      = 2;   // zero-based result offset (pagination)
  required int32  numResults = 3;
}

message SearchResponse {
  required string query        = 1;
  required int32  totalResults = 2;  // -1 if unknown (more results exist beyond numResults)
  repeated Digest digests      = 3;

  message Digest {
    required string title        = 1;
    required string snippet      = 2;   // text excerpt
    required string waveId       = 3;
    required int64  lastModified = 4;   // ms since epoch
    required int32  unreadCount  = 5;
    required int32  blipCount    = 6;
    repeated string participants = 7;   // participants[1..n]
    required string author       = 8;   // participants[0]
  }
}
```

---

### profiles.proto

Served via HTTP GET.

```proto
message ProfileRequest {
  repeated string addresses = 1;   // participant addresses, email format
}

message ProfileResponse {
  repeated FetchedProfile profiles = 1;

  message FetchedProfile {
    required string address    = 1;
    required string name       = 2;
    required string imageUrl   = 3;
    optional string profileUrl = 4;
  }
}
```

---

### attachment.proto

```proto
message AttachmentsResponse {
  repeated AttachmentMetadata attachment = 1;
}

message AttachmentMetadata {
  required string        attachmentId    = 1;
  required string        waveRef         = 2;   // wave ref in URI path format
  required string        fileName        = 3;
  required string        mimeType        = 4;
  required int64         size            = 5;   // bytes
  required string        creator         = 6;
  required string        attachmentUrl   = 7;   // download URL
  required string        thumbnailUrl    = 8;
  optional ImageMetadata imageMetadata   = 9;
  optional ImageMetadata thumbnailMetadata = 10;
  optional bool          malware         = 11;
}

message ImageMetadata {
  required int32 width  = 1;
  required int32 height = 2;
}
```

---

### federation.protodevel — shared primitive types

Used by both `waveclient-rpc.proto` and `clientserver.proto`.

**`ProtocolHashedVersion`**

```proto
message ProtocolHashedVersion {
  required int64 version      = 1 [(int52) = true];
  required bytes history_hash = 2;
}
```

The `[(int52)=true]` annotation only affects the in-memory GWT client overlay
(it reads `version` as a JS double); on the wire `version` is encoded as a
`[lowWord, highWord]` two-element array like every other `int64` field.

`version` is a wavelet version number (non-negative, monotonically increasing).
`history_hash` is an opaque byte string identifying the history up to this
version; used to detect forks.

**`ProtocolWaveletDelta`**

```proto
message ProtocolWaveletDelta {
  required ProtocolHashedVersion    hashed_version = 1;  // version delta applies at
  required string                   author         = 2;  // submitter participant id
  repeated ProtocolWaveletOperation operation      = 3;
  repeated string                   address_path   = 4;  // delegation path (usually empty)
}
```

**`ProtocolWaveletOperation`**

Exactly one field must be set:

```proto
message ProtocolWaveletOperation {
  optional string          add_participant    = 1;
  optional string          remove_participant = 2;
  optional MutateDocument  mutate_document    = 3;
  optional bool            no_op             = 4;

  message MutateDocument {
    required string                    document_id         = 1;
    required ProtocolDocumentOperation document_operation  = 2;
  }
}
```

**`ProtocolDocumentOperation`** — a list of components, each being exactly one of:

```proto
message ProtocolDocumentOperation {
  message Component {
    optional AnnotationBoundary annotation_boundary   = 1;
    optional string             characters            = 2;
    optional ElementStart       element_start         = 3;
    optional bool               element_end           = 4;
    optional int32              retain_item_count     = 5;
    optional string             delete_characters     = 6;
    optional ElementStart       delete_element_start  = 7;
    optional bool               delete_element_end    = 8;
    optional ReplaceAttributes  replace_attributes    = 9;
    optional UpdateAttributes   update_attributes     = 10;
  }
  repeated Component component = 1;
}
```

(Sub-message definitions omitted for brevity; see `federation.protodevel` for
`ElementStart`, `AnnotationBoundary`, `ReplaceAttributes`, `UpdateAttributes`.)

---

## Algorithms & behavior

### Connection lifecycle

```
Client                                      Server
  |                                            |
  |--- HTTP Upgrade (ws:// /socket) ---------> |
  |<-- 101 Switching Protocols ---------------- |
  |                                            |
  |--- ProtocolAuthenticate (seqno=0) -------> |  (if JSESSIONID cookie present)
  |<-- ProtocolAuthenticationResult ---------- |
  |                                            |
  |--- ProtocolOpenRequest (seqno=1) --------> |  (Open is streaming)
  |<-- ProtocolWaveletUpdate (channel_id + snapshot) | message 1..N: one per wavelet,
  |  ...                                       |    each carries channel_id + snapshot
  |<-- ProtocolWaveletUpdate (marker=true) --- |  message N+1: end of snapshots
  |<-- ProtocolWaveletUpdate (delta) --------- |  subsequent: live updates
  |  ...                                       |
  |--- ProtocolSubmitRequest (seqno=2) ------> |
  |<-- ProtocolSubmitResponse (seqno=2) ------ |
  |  ...                                       |
  |--- CancelRpc (seqno=1) -----------------> |  (cancel the open stream)
  |<-- RpcFinished (seqno=1, failed=false) --- |
```

**Reconnect behavior (client-side):** The GWT client retries the WebSocket
connection on disconnect, up to `MAX_INITIAL_FAILURES=2` times within 5-second
intervals. After reconnect, the client re-sends queued messages (including
re-opening the wave view).

### RPC dispatch (server-side)

1. Server receives an envelope; extracts `sequenceNumber`, `messageType`,
   `message`.
2. Deserialization happens in two lookups:
   - `ProtoSerializer.fromJson` looks up the `messageType` **name** in its
     `byName` registry. If that name is unknown it throws `SerializationException`
     ("Unknown proto class: ..."), which `WebSocketChannel.handleMessageString`
     catches, logs, and swallows — the message is dropped and the connection
     survives (see Edge cases).
   - If deserialization succeeds, `ServerRpcProvider.Connection.message()` looks
     up the resulting proto **descriptor** in the service registry
     (`registeredServices`). If the descriptor is recognized but not registered,
     it throws `IllegalStateException` ("Got expected but unknown message ...");
     this is a server-side misconfiguration path, not normal client input.
3. Checks that `sequenceNumber` is not already active; if it is, throws.
4. Creates a `ServerRpcController` and dispatches the service call on a thread
   pool.
5. For each response message the service produces, the server sends an envelope
   with the same `sequenceNumber`.
6. For non-streaming RPCs: exactly one response, then done.
7. For streaming RPCs: N≥0 response messages, then either `RpcFinished` (normal
   end or error). When `RpcFinished` is sent, the sequence number is freed.

**Cancellation:** Client sends `CancelRpc` with the active sequence number.
Server sets the controller's cancelled flag; the service implementation checks
`controller.isCanceled()` and stops. The server still sends a `RpcFinished` to
close the sequence number.

### Open wave — server behavior

1. Server receives `ProtocolOpenRequest`. If `known_wavelet` is non-empty it
   fails the whole request with `onFailure("Known wavelets not supported")`
   (resync is not implemented); otherwise it generates a `channel_id`.
2. For each wavelet currently in view, the server sends one
   `ProtocolWaveletUpdate` (`wavelet_name` always present) carrying the
   `channel_id`:
   - The server unconditionally fetches a snapshot (there is no resync branch).
     When `getSnapshot` returns a snapshot, it sends the full snapshot in
     `ProtocolWaveletUpdate.snapshot` with `resulting_version` and
     `commit_notice`, plus `channel_id`.
   - When `getSnapshot` returns null, it sends an empty delta-sequence update
     (no snapshot) carrying just `channel_id`.
   If there are **no** visible wavelets, the server instead sends a single
   channel-id-only message on a dummy wavelet name.
3. Sends marker (`marker=true`).
4. Thereafter, sends delta updates (`applied_delta` list + `resulting_version`)
   as the wavelet is modified.
5. Also sends `commit_notice` (without deltas) when the server commits a version
   to durable storage.

### Submit delta — flow

1. Client sends `ProtocolSubmitRequest` with `channel_id` (from step 2 above)
   and the delta.
2. Server applies/transforms the delta via the concurrency-control layer.
3. Server sends `ProtocolSubmitResponse` (success or error).
4. The submitted delta is broadcast to other open streams for the same wavelet
   (but excluded from the submitter's own channel, identified by `channel_id`).

---

## Wire / storage formats

### Transport

- **Protocol**: WebSocket (RFC 6455). The server listens on the same HTTP port
  as the web client, path `/socket`.
- **Message framing**: Each WebSocket message is exactly one UTF-8 text frame.
  Messages must not span frames. (The Jetty server is configured with a 1 MB
  buffer; see `BUFFER_SIZE = 1024*1024`.)
- **Subprotocol**: None declared. The connection is plain WebSocket.
- **TLS**: Optional. Configured via `security.enable_ssl` in server config.
- **Idle timeout**: Configurable (`network.websocket_max_idle_time`); defaults
  to `Integer.MAX_VALUE` (effectively infinite).

### JSON encoding of protobuf messages (PST-Gson format)

This is the format used by **all** WebSocket messages and all HTTP JSON
endpoints (search, profiles, attachments, fetch). It is NOT binary protobuf.

**Object structure:** A proto message serializes to a JSON object. Keys are the
**proto field number** as a decimal string (e.g., `"1"`, `"2"`), not the field
name.

**Type mapping:**

| Proto type | JSON encoding |
|---|---|
| `bool` | `true` / `false` |
| `int32`, `uint32`, `sint32`, `float` | JSON number |
| `string`, `enum` (as integer value) | JSON number (enum) or JSON string |
| `int64` | Two-element JSON array `[lowWord, highWord]` (each a 32-bit signed int) — applies to **all** `int64` fields, including those annotated `[(int52)=true]` |
| `bytes` | JSON string (opaque; the `Blob` type holds whatever encoding the server used — likely base64 but not specified) |
| nested `message` | Nested JSON object (same rules) |
| `repeated` field | JSON array |
| `optional` absent field | Key is omitted entirely |

**No int52 wire exception:** There is *no* wire-format special case for
`[(int52)=true]` fields. The PST Gson templates (`toGsonFieldInner.st` →
`GsonUtil.toJson(long)`) encode **every** `int64` field — int52-annotated or not
— as a two-element `[lowWord, highWord]` array. The `[(int52)=true]` annotation
only changes how the in-memory GWT JSO client overlay reads the value (as a JS
double); it does not affect the bytes on the wire. Encoding splits the long via
`JsonLongHelper`: `arr = [getLowWord(v), getHighWord(v)]`. Decode rule for a Go
reimplementation: `value = toLong(highWord=arr[1], lowWord=arr[0])` — i.e. the
low word is at index 0 and the high word at index 1.

**Enum encoding:** Enum fields are written as their integer value (not name).

**Key example:** For `ProtocolOpenRequest`:
```json
{
  "1": "user@example.com",
  "2": "example.com/w+abc",
  "3": ["example.com/conv+"],
  "4": []
}
```
Field `1` = `participant_id`, `2` = `wave_id`, `3` = `wavelet_id_prefix`
(repeated), `4` = `known_wavelet` (empty repeated).

### Envelope wire format example

```json
{
  "sequenceNumber": 3,
  "messageType": "ProtocolOpenRequest",
  "message": {
    "1": "alice@example.com",
    "2": "example.com/w+Xabcdef",
    "3": ["example.com/conv+"]
  }
}
```

### HTTP endpoints (non-WebSocket)

All HTTP responses use the same PST-Gson JSON encoding as WebSocket payloads.

| Endpoint | Method | Input | Output | Notes |
|---|---|---|---|---|
| `/socket` | WebSocket upgrade | — | — | Main RPC channel |
| `/fetch/*` | GET | URL path encodes waveref | `WaveletSnapshot` or `WaveViewSnapshot` or `DocumentSnapshot` | Path: `/{waveDomain}/{waveId}[/{waveletDomain}/{waveletId}[/{docId}]]` |
| `/search` | GET | Query params: `query`, `index`, `numResults` | `SearchResponse` | Content-Type: `application/json` |
| `/profile` | GET | Query param: `addresses` (comma-separated, URL-encoded) | `ProfileResponse` | Content-Type: `application/json` |
| `/attachmentsInfo` | GET | Query param: `attachmentIds` (comma-separated) | `AttachmentsResponse` | Content-Type: `application/json` |
| `/attachment/*` | GET/POST | attachment id in path | Binary file data | Download/upload |
| `/thumbnail/*` | GET | attachment id in path | Image data | Thumbnail |
| `/auth/signin` | POST | Form: username, password | Session cookie | Login |
| `/auth/signout` | GET | — | — | Logout |
| `/auth/register` | POST | Form | — | Account creation |

All JSON endpoints set `Cache-Control: no-store`.

### Wavelet name format

Wavelet names in the wire protocol use **URI path format** as serialized by
`ModernIdSerialiser`:

```
{waveDomain}/{waveLocalId}/{waveletDomain}/{waveletLocalId}
```

Example: `"example.com/w+abc123/~/conv+root"`

When the wavelet domain equals the wave domain, the wavelet domain token **MUST**
be elided to `~`; writing the actual matching domain is invalid and is rejected
by the deserializer (`ModernIdSerialiser` throws `InvalidIdException`,
"un-normalised domains"). Use the actual domain only when the wavelet domain
differs, e.g. `example.com/w+abc123/other.com/conv+xyz`. See spec
[01](01-data-model.md) §2.8.

Wave IDs and wavelet IDs each consist of a domain and a local identifier
separated by `/`. The local ID often has a type prefix separated by `+` (e.g.,
`w+`, `conv+`, `b+`).

---

## Interfaces / APIs

### Service registration

The server maintains a map from **proto message descriptor → registered service
method**. When a WebSocket message arrives, the type tag (`messageType`) is
matched against the registry by simple class name. Services are registered via
`ServerRpcProvider.registerService(Service)`, which iterates all methods of the
service and registers each input type.

The set of registered services for the main web client:
- `ProtocolWaveClientRpc` (Open, Submit, Authenticate) — registered in
  `ServerMain`.

### Client-side connection interface (`WaveWebSocketClient`)

```java
// Open a connection
void connect();

// Subscribe to updates
void attachHandler(WaveWebSocketCallback callback);

// Open a wave view (streaming); no callback — updates arrive via callback
void open(ProtocolOpenRequestJsoImpl message);

// Submit a delta; callback called with ProtocolSubmitResponse
void submit(ProtocolSubmitRequestJsoImpl message, SubmitResponseCallback callback);
```

The client maintains a per-submit sequence number map to route
`ProtocolSubmitResponse` messages back to the right callback.

### `WaveWebSocketCallback` (client-side event sink)

```java
void onWaveletUpdate(ProtocolWaveletUpdate message);
```

---

## Edge cases & failure modes

- **Unknown message type (unrecognized `messageType` tag on the wire):** the
  server cannot deserialize it — `ProtoSerializer.fromJson` has no `byName` entry
  for that type name and throws `SerializationException`.
  `WebSocketChannel.handleMessageString` catches this, logs a warning, and
  **drops** the message; the connection stays open and no error is returned to
  the client. A Go server should likewise log-and-ignore an unknown
  `messageType`, **not** terminate the connection.
- **Recognized-but-unrouted message:** if a message deserializes to a known
  proto whose descriptor is not in the service registry (`registeredServices`),
  `ServerRpcProvider.Connection.message()` throws `IllegalStateException` ("Got
  expected but unknown message"). This is a server-side misconfiguration path,
  not normal client input.
- **Duplicate sequence number:** Client sends a new RPC with an already-active
  sequence number → `IllegalStateException`. Server should reject.
- **Auth before open:** If `ProtocolAuthenticate` arrives with a token for a
  different user than the already-authenticated session, the server throws a
  precondition error and the connection should be closed.
- **Cancellation of completed RPC:** Sending `CancelRpc` for a sequence number
  that is no longer active throws `IllegalStateException`. The client must track
  active RPCs to avoid this.
- **Reconnect with queued messages:** The GWT client queues outgoing messages
  while disconnected and flushes them on reconnect. This means a previously
  submitted delta might be retried; the server's OT layer handles idempotency
  via version checking.
- **Large messages:** Server enforces `websocket_max_message_size` (MB, configurable).
  Messages over this limit are rejected by Jetty.
- **Resynchronization (`known_wavelet`):** Not supported. Any non-empty
  `known_wavelet` list causes outright failure of the open request
  (`onFailure("Known wavelets not supported")`) — there is no matching path and
  no fallback. The server always sends a full snapshot for each visible wavelet.
- **`marker` semantics:** A new wavelet coming into view after the marker (e.g.,
  a new conversation thread is added) is sent as another snapshot, not a delta.
  This means the client must handle snapshot messages at any point in the stream.

---

## Open questions / ambiguities

1. **`bytes` encoding:** The `Blob` class comment says "encoding is unspecified."
   In practice, `history_hash` and similar fields are likely base64 (since
   `ByteString.toStringUtf8()` would be lossy). Needs verification against an
   actual captured wire trace or the PST template output for `bytes` fields.

2. **`clientserver.proto` usage:** This proto defines a cleaner service API
   (`FetchService`, `WaveletChannelService`, `DeltaSubmissionService`) but the
   browser client exclusively uses `waveclient-rpc.proto`. Is
   `clientserver.proto` used for server-server or robot communication? A Go
   rewrite should clarify which services to implement.

3. **Authentication flow ordering:** The `ProtocolAuthenticate` RPC is described
   as a workaround for browsers that don't send cookies. If the session is
   established via normal cookie auth, `ProtocolAuthenticate` may still be sent
   (the server checks that the authenticated user matches). The exact interaction
   when both are present needs careful handling.

4. **`participant_id` field in `ProtocolOpenRequest`:** Marked TODO for removal
   in the source. A Go rewrite can ignore this field and use the session's
   authenticated identity instead.

5. **`applicationTimpstamp` [sic]:** Field 3 of `WaveletUpdate` in
   `clientserver.proto` has a typo ("Timpstamp"). Use `applicationTimestamp` in
   any new implementation; the wire number (3) is what matters.

6. **Delta exclusion by channel ID:** The semantics of "exclude own submits from
   the stream" are implemented in `ClientFrontendImpl`, not specified here in
   detail. Spec 03 or 06 should cover this; just note it as a constraint on the
   Open stream.

7. **`int52` annotation in `clientserver.proto`:** Fields in the newer
   `clientserver.proto` messages (`lastModifiedTime`, `creationTime`, etc.) are
   not annotated with `[(int52)=true]` in the same way as in `federation.proto`.
   Per the PST Gson rules above this annotation does not affect the wire form —
   every `int64` (annotated or not) is `[lowWord, highWord]` — so the JSON shape
   is the same regardless. The annotation would only matter for an in-memory GWT
   overlay, and `clientserver.proto` is not currently browser-facing.

---

## Source references

| File | Role |
|---|---|
| `wave/src/proto/proto/org/waveprotocol/box/server/rpc/rpc.proto` | RPC control messages (`CancelRpc`, `RpcFinished`) and `is_streaming_rpc` option |
| `wave/src/proto/proto/org/waveprotocol/box/common/comms/waveclient-rpc.proto` | Main client↔server service and message definitions |
| `wave/src/proto/proto/org/waveprotocol/wave/concurrencycontrol/clientserver.proto` | Newer service API (not currently browser-facing) |
| `wave/src/proto/proto/org/waveprotocol/box/search/search.proto` | Search request/response |
| `wave/src/proto/proto/org/waveprotocol/box/profile/profiles.proto` | Profile fetch request/response |
| `wave/src/proto/proto/org/waveprotocol/box/attachment/attachment.proto` | Attachment metadata response |
| `wave/src/proto/proto/org/waveprotocol/wave/federation/federation.protodevel` | Shared primitives: `ProtocolHashedVersion`, `ProtocolWaveletDelta`, `ProtocolDocumentOperation` |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/ServerRpcProvider.java` | WebSocket server setup, service registry, connection lifecycle, RPC dispatch loop |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/WebSocketChannel.java` | Envelope framing (`MessageWrapper`), JSON serialization of messages |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/WebSocketChannelImpl.java` | Jetty WebSocket adapter (text frames, on connect/close/message) |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/ServerRpcControllerImpl.java` | Per-call RPC lifecycle, streaming/unary handling, cancellation |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/ProtoSerializer.java` | Registry of proto↔Gson DTO mappings; `toJson`/`fromJson` entry points |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/SearchServlet.java` | `/search` HTTP endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/FetchProfilesServlet.java` | `/profile` HTTP endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/AttachmentInfoServlet.java` | `/attachmentsInfo` HTTP endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/FetchServlet.java` | `/fetch/*` HTTP endpoint |
| `wave/src/main/java/org/waveprotocol/box/webclient/client/WaveWebSocketClient.java` | GWT browser client: connection management, envelope send/receive, reconnect logic |
| `wave/src/main/java/org/waveprotocol/box/webclient/client/WaveSocketFactory.java` | Wraps the GWT `WebSocket` object |
| `wave/src/main/java/org/waveprotocol/box/server/frontend/WaveClientRpcImpl.java` | Adapts RPC calls to the `ClientFrontend` interface; builds `ProtocolWaveletUpdate` messages |
| `wave/src/main/java/org/waveprotocol/wave/communication/gson/GsonUtil.java` | `int64` → `[lowWord, highWord]` encoding |
| `wave/src/main/java/org/waveprotocol/wave/communication/json/JsonLongHelper.java` | Low/high word splitting for 64-bit ints |
| `wave/src/main/java/org/waveprotocol/wave/communication/Blob.java` | Opaque `bytes` type for JSON serialization |
| `wave/src/main/java/org/waveprotocol/pst/templates/gson/toGsonFieldInner.st` | PST template showing per-type JSON encoding rules |
| `wave/src/main/java/org/waveprotocol/pst/templates/gson/fromGsonFieldInner.st` | PST template showing per-type JSON decoding rules |
| `wave/src/main/java/org/waveprotocol/box/server/ServerMain.java` | All servlet and service registrations (URL map) |
