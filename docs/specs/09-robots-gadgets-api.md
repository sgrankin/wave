# 09 — Robots API, Data API, and Gadgets

## Purpose & scope

This subsystem defines Wave's extensibility surface: automated participants
(robots), third-party applications acting on behalf of users (the Data API),
and embedded mini-apps in blips (gadgets).

Robots are Wave participants that run external code. When something happens in a
wave the robot belongs to, the server delivers an event bundle to the robot over
HTTP; the robot replies with a list of operations to apply. This is called the
**Passive API** because the server drives the interaction.

The **Active API** (also called the robot active API) inverts control: a robot
or any trusted application initiates an HTTP request to the server carrying a
list of operations, and the server returns results. The Active API uses OAuth
two-legged (robot acts as itself). The **Data API** is the same HTTP+JSON
mechanism but with three-legged OAuth so a web app can act on behalf of a logged-in
user.

**Gadgets** are OpenSocial gadgets embedded in blip documents. Their state is
stored as document elements subject to OT; robots can read and write gadget
state; gadget state changes trigger robot events.

This spec does not cover the federated wavelet protocol (see
[07-federation](07-federation.md)), the general OT document model (see
[02-operational-transform](02-operational-transform.md)), or account storage
(see [08-authentication-accounts](08-authentication-accounts.md)), but
cross-references them where needed.

---

## Concepts & glossary

| Term | Meaning |
|---|---|
| **Robot** | An automated Wave participant. Identified by an email-style address. The server calls it via HTTP when events occur. |
| **Robot account** | A server-side record (see spec 08) containing the robot's URL, consumer secret, capabilities, and verification status. |
| **Passive API** | Server-to-robot event delivery. Server POSTs an event bundle; robot replies with operations. |
| **Active API** | Robot-to-server operation submission. Robot POSTs a JSON-RPC operation list; server applies them and returns responses. Active API uses two-legged OAuth (robot signs as itself). |
| **Data API** | Same operation format as Active API but uses three-legged OAuth so a web app can act on behalf of a Wave user. |
| **Event bundle** | JSON payload sent from server to robot containing one or more events plus wavelet/blip context. |
| **Operation request** | A single JSON-RPC-style record with `method`, `id`, and `params` (including `waveId`, `waveletId`, optional `blipId`, and operation-specific params). |
| **capabilities.xml** | XML manifest served by the robot at `/_wave/capabilities.xml`. Declares which events the robot subscribes to, in what context, and the protocol version it speaks. |
| **Capability** | One entry in capabilities.xml: an event type, a set of contexts, and an optional filter regex. |
| **Context** | Which surrounding blips are included in the event bundle: `ROOT`, `PARENT`, `SIBLINGS`, `CHILDREN`, `SELF`, `ALL`. Default is `[ROOT, PARENT, CHILDREN]`. |
| **Protocol version** | Wire format version. Current versions: `0.1` (V1, deprecated), `0.2` (V2), `0.21` (V2_1), `0.22` (V2_2, default). |
| **RobotName** | Parsed form of a robot's participant address: `id[+proxyFor][#version]@domain`. |
| **ProxyFor** | Suffix in a robot's participant address (`+user`) indicating the robot is acting on behalf of another participant. Used to chain robots. |
| **Capabilities hash** | A version token (opaque string) from the robot's `<w:version>` tag. The server uses it to detect when to re-fetch capabilities.xml. |
| **Consumer key / secret** | OAuth 1.0a credentials. For the Active API the robot's email address is the consumer key; the server stores the corresponding secret. For the Data API, anonymous consumer credentials are used during the OAuth dance. |
| **Gadget** | An embedded mini-app in a blip, represented as a `<gadget>` element in the document XML. State is a key/value map stored as `<state>` child elements. |
| **WaveletData** | JSON object summarizing wavelet metadata sent to robots: participants, title, data docs, tags, version, timestamps. |
| **BlipData** | JSON object summarizing a blip sent to robots: content (plain text), annotations, elements (map of position → element), thread/parent/child ids. |

---

## Data structures

### RobotName

```
RobotName {
  id       string   // base robot id, e.g. "echoey"
  domain   string   // e.g. "appspot.com"
  proxyFor string   // optional; set when robot acts as proxy
  version  string   // optional AppEngine version tag
}
```

Addresses encode optional fields: `id[+proxyFor][#version]@domain`.

- `toParticipantAddress()` → full form including proxyFor and version
- `toEmailAddress()` → bare `id@domain` (used as account lookup key)
- Well-formed: matches `^[a-z0-9._%+#-]+?@[a-z0-9.-]+\.[a-z]{2,6}$`

### RobotCapabilities (server-side record)

```
RobotCapabilities {
  capabilitiesMap  map[EventType → Capability]
  capabilitiesHash string          // from <w:version> tag
  protocolVersion  ProtocolVersion // 0.1 / 0.2 / 0.21 / 0.22
}
```

### Capability

```
Capability {
  eventType EventType
  contexts  []Context  // default: [ROOT, PARENT, CHILDREN]
  filter    string     // optional regex; empty means "match all"
}
```

### RobotAccountData (see also spec 08)

```
RobotAccountData {
  id             ParticipantId      // robot's Wave address
  url            string             // base URL, e.g. "https://echoey.appspot.com"
  consumerSecret string             // 48-char random token, used as OAuth secret
  capabilities   RobotCapabilities  // null until first capabilities.xml fetch
  verified       bool               // true after first successful capability fetch
}
```

Endpoint paths derived from `url`:
- Passive API event delivery: `url + "/_wave/robot/jsonrpc"`
- Capabilities manifest: `url + "/_wave/capabilities.xml"`

### ProtocolVersion

| Enum | String | Notes |
|---|---|---|
| V1 | `"0.1"` | Legacy; no `<w:protocolversion>` tag in XML |
| V2 | `"0.2"` | |
| V2_1 | `"0.21"` | |
| V2_2 | `"0.22"` | Default; current |

### WaveletData (JSON)

```
WaveletData {
  waveId            string
  waveletId         string
  rootBlipId        string
  rootThread        BlipThread
  creator           string
  creationTime      long    // milliseconds since epoch
  lastModifiedTime  long
  version           long
  participants      []string
  participantRoles  map[string → string]  // only non-default roles
  dataDocuments     map[string → string]  // named data docs (XML strings)
  tags              []string
  title             string
}
```

### BlipData (JSON)

```
BlipData {
  blipId          string
  waveId          string
  waveletId       string
  creator         string
  contributors    []string
  content         string              // plain-text rendering; starts with "\n"
  lastModifiedTime long
  version         long
  parentBlipId    string
  childBlipIds    []string
  threadId        string
  replyThreadIds  []string
  annotations     []Annotation        // {name, value, range: {start, end}}
  elements        map[int → Element]  // position → element
}
```

### BlipThread (JSON)

```
BlipThread {
  id      string
  location int     // character offset into parent blip; -1 for non-inline
  blipIds []string
}
```

### Element (JSON)

```
Element {
  type       string              // see ElementType enum
  properties map[string → string]
}
```

Element types: `INPUT`, `PASSWORD`, `CHECK`, `LABEL`, `BUTTON`,
`RADIO_BUTTON`, `RADIO_BUTTON_GROUP`, `TEXTAREA`, `INLINE_BLIP`,
`GADGET`, `INSTALLER`, `IMAGE`, `LINE`, `ATTACHMENT`.

### Gadget (Element subtype)

A Gadget is an Element with `type = "GADGET"` and these well-known property keys:

| Key | Meaning |
|---|---|
| `url` | URL of the gadget XML spec |
| `ifr` | Cached iframe URL (set by gadget container) |
| `title` | Gadget title |
| `author` | Participant who added the gadget |
| `pref` | Gadget user preferences (JSON-escaped string) |
| `thumbnail` | Thumbnail URL |
| `category` | Gadget category |

Additional properties are gadget state entries (arbitrary key/value pairs).
The gadget's mutable shared state is stored as `<state name="key" value="val"/>`
child elements in the document XML (see Gadget document format below).

### EventMessageBundle (server→robot JSON payload)

```
EventMessageBundle {
  robotAddress string
  rpcServerUrl string          // optional; server's Active API endpoint (see note)
  proxyingFor  string          // optional; set if robot was added as proxy
  wavelet      WaveletData
  blips        map[string → BlipData]
  threads      map[string → BlipThread]
  events       []EventObject
}
```

`rpcServerUrl` identifies the server's Active API endpoint. **WIAB's passive
`EventGenerator` always sets this to the empty string**, so the GSON serializer
omits the property entirely from passive-delivery JSON (it is only emitted when
non-empty/non-null). On the robot SDK side (`AbstractRobot.deserializeEvents`),
this URL is the key used to look up the OAuth consumer secret and verify the
incoming POST's signature; because WIAB never sets it and never signs passive
POSTs (see open question #1), the robot's OAuth verification path is effectively
bypassed for WIAB-delivered events. A Go reimplementation of the passive sender
should therefore omit `rpcServerUrl` (matching WIAB), or — if it adds OAuth
signing to passive delivery — populate it with the server's Active API endpoint
so the robot can locate the matching consumer key.

### EventObject (within bundle)

```
EventObject {
  type       string   // EventType name, e.g. "WAVELET_SELF_ADDED"
  modifiedBy string   // participant address that triggered the event
  timestamp  long     // milliseconds since epoch
  properties {
    blipId   string   // blip the event is about (may be null for some events)
    // ... event-type-specific fields (see event catalog below)
  }
}
```

### OperationRequest (robot→server JSON)

```
OperationRequest {
  method string   // e.g. "wavelet.appendBlip"
  id     string   // correlates response to request
  params {
    waveId    string   // optional
    waveletId string   // optional
    blipId    string   // optional
    // ... operation-specific params (see operation catalog below)
  }
}
```

Wire format: JSON array of OperationRequest objects. May be a bare object
for single operations, which is coerced to a single-element array.

### JsonRpcResponse (server→robot response)

```
JsonRpcResponse {
  id    string   // matches the request id
  // exactly one of:
  data  map[ParamsPropertyKey → value]   // on success
  error string                           // on failure
}
```

Response list is a JSON array ordered to match the request array.

---

## Algorithms & behavior

### Robot registration

1. An admin submits `POST /robot/register/create` with `username` and `location`.
2. Server validates: `username@domain` must not already exist; `location` must be a valid HTTP/HTTPS URI.
3. Server creates a `RobotAccountData`: `isVerified=true`, `consumerSecret` = random 48-char token, `capabilities=null`.
4. The registration page displays the consumer secret. This is the only time it is shown.
5. The server does NOT fetch `capabilities.xml` at registration time — that is deferred to first event delivery.

**Invariant**: A robot account always has `isVerified=true` after successful registration. Only verified robots receive passive events.

### capabilities.xml fetch

Triggered when: (a) a robot's capabilities are null (first event), or (b) a
robot sends `robot.notify` with a capabilities hash that differs from the stored one.

Steps:
1. HTTP GET `robotUrl + "/_wave/capabilities.xml"`.
2. Parse XML in namespace `http://wave.google.com/extensions/robots/1.0`.
3. Extract `<w:capabilities>/<w:capability>` elements. Each has:
   - `name` → EventType name (case-insensitive)
   - `context` → comma-separated Context values (default: ROOT,PARENT,CHILDREN)
   - `filter` → optional regex string
4. Always add `WAVELET_SELF_ADDED` capability even if not declared.
5. Extract `<w:version>` text → capabilitiesHash.
6. Extract `<w:protocolversion>` text → ProtocolVersion (default V1 if absent).
7. Extract `<w:consumer_keys>/<w:consumer_key for="activeApiUrl">` → consumerKey for Active API verification (only used if `for` matches the server's Active API URL).
8. Store new `RobotCapabilities` on the account record.

### Passive API: event delivery lifecycle

The server component `RobotsGateway` subscribes to the `WaveBus`. On every
committed wavelet update:

```
for each participant in (current participants ∪ newly-added participants):
  if participant address parses as a RobotName:
    look up RobotAccountData
    if account.isRobot and account.isVerified:
      robot.waveletUpdate(wavelet, deltas)
      schedule robot.run() if not already queued
```

`Robot.run()` processes one queued wavelet at a time (Runnable submitted to an
executor):

```
loop:
  wavelet = dequeue next WaveletAndDeltas
  if none: signal doneRunning; return

  if capabilities == null:
    fetchCapabilities()        // may drop wavelet on failure

  events = eventGenerator.generateEvents(wavelet, capabilities)
  if events is empty: continue

  ops = robotConnector.sendMessageBundle(events, robot, protocolVersion)
  operationApplicator.applyOperations(ops, wavelet.snapshotAfterDeltas, ...)

  doneRunning(); requeue self
```

The wavelet queue is a `ListMultimap<WaveletName, WaveletAndDeltas>`. Contiguous
deltas for the same wavelet are merged into one entry. A gap (non-contiguous
delta versions) creates a new entry.

### EventGenerator: mapping deltas to events

For each batch of deltas to process:

1. Replay deltas on a copy of the wavelet snapshot (from before the deltas).
2. Attach listeners to the copy: `ConversationListener` and per-blip `DocumentHandler`.
3. For each delta:
   - Notify listeners of `deltaBegin(author, timestamp)`.
   - Apply each operation to the snapshot copy (listeners fire synchronously).
   - Notify `deltaEnd()`.
4. Per-delta aggregation: `WAVELET_PARTICIPANTS_CHANGED` fires once per delta accumulating all adds/removes.

**Event filtering rules**:
- Self-generated events are dropped (event.modifiedBy == robot's participant address).
- Events before `WAVELET_SELF_ADDED` are suppressed (robot not yet a member).
- Events after `WAVELET_SELF_REMOVED` are suppressed (processing suspended until next `WAVELET_SELF_ADDED`).

After all deltas are replayed:
- `ContextResolver.resolveContext` populates the bundle's `blips` and `threads`
  maps using the required-blip list accumulated during event generation. Each
  event's capability context specifies which surrounding blips to include.

### Event → HTTP call

`RobotConnector.sendMessageBundle`:

1. Serialize `EventMessageBundle` to JSON using `RobotSerializer` at the
   robot's declared `ProtocolVersion`.
2. HTTP POST to `robotUrl + "/_wave/robot/jsonrpc"` with `Content-Type: application/json`.
3. Parse response as JSON array of `OperationRequest`. On connection error or
   deserialization failure, return empty list (silent failure, robot is not
   penalized).

### Operation application (passive API response)

`RobotOperationApplicator.applyOperations` for each returned `OperationRequest`:

1. Look up the service in the passive API operation registry.
2. Execute the service against an `OperationContext` bound to the robot's account and the post-delta wavelet snapshot.
3. Collect deltas generated by the operations.
4. Submit deltas to `WaveletProvider` asynchronously.

### Active API: request handling

Endpoint: `POST /robot/rpc` (served by `ActiveApiServlet`).

Auth: Two-legged OAuth 1.0a. The consumer key is the robot's participant
address (%-encoded `@` in the OAuth message). The consumer secret is the
robot's stored `consumerSecret`. The server validates the signature using
`OAuthValidator`.

Processing:
1. Validate OAuth signature.
2. Deserialize request body as JSON array of `OperationRequest`.
3. Determine protocol version from the first operation (if it is `robot.notify` with a `protocolVersion` param; otherwise use default V2_2).
4. Create `OperationContextImpl`.
5. Execute each operation in order using the Active API operation registry.
6. Submit all pending deltas via `WaveletProvider`.
7. Return JSON array of `JsonRpcResponse` in the same order as requests.

### Data API: OAuth dance

Endpoint base: `/robot/dataapi/oauth` with sub-paths:

```
/request  → issue unsigned request token (anonymous consumer)
/authorize → user authorization page (requires Wave login session)
/access   → exchange authorized request token for access token
/all      → convenience endpoint: full dance + display token
```

The Data API uses a token container (`DataApiTokenContainer`) that maps
request tokens → accessor objects (which record the authorized user). Access
tokens are opaque random strings.

Data API operation endpoint: `POST /robot/dataapi/rpc` (`DataApiServlet`).
Auth: OAuth 1.0a with access token. The `USER_PROPERTY_NAME` on the accessor
carries the `ParticipantId` of the authorizing user. Operations execute with
that participant's identity.

**Invariant**: The Data API and Active API share the same operation format and
register the identical set of operations. The only difference is that on the
Data API `robot.notify` and `robot.notifyCapabilitiesHash` are wired to a no-op
(`DoNothingService`), whereas the Active API wires them to the real
`NotifyOperationService`.

### Protocol version negotiation

The first operation in any batch may be `robot.notify` with a
`protocolVersion` param. The server uses this to select the correct Gson
serializer version for the response. If absent, V2_2 is used.

`robot.notify` also carries a `capabilitiesHash`. If the hash differs from the
stored one, the server re-fetches `capabilities.xml` and updates the account.

### Gadget document storage

A gadget is stored in the blip document XML as:

```xml
<gadget url="https://example.com/gadget.xml"
        title=""
        prefs=""
        state=""
        author="user@example.com"
        ifr="https://...cached-iframe..."
        height="200"
        width="400">
  <state name="key1" value="val1"/>
  <state name="key2" value="val2"/>
  <pref  name="prefKey" value="prefVal"/>
  <category name="game"/>
  <title value="My Gadget Title"/>
</gadget>
```

Gadget state mutations go through OT the same as any other document
modification (see spec 02). Each `<state>` child element is an independent
key/value pair; the gadget container updates them via `DOCUMENT_MODIFY`
operations.

When a `<state>` element's `value` attribute changes, the document event
listener detects an `ATTRIBUTES` modification on the `<state>` element. The
server reads the `name` attribute from the modified `<state>` element and the
`oldValue` from `oldValues.get("value")`. If `name != null` **OR**
`oldValue != null` (i.e. at least one is non-null — it does **not** require
both), it records a single `oldState` entry `{name -> oldValue}` (a null name
becomes a null map key; a null oldValue becomes a null map value). The
`GADGET_STATE_CHANGED` event then fires only if that `oldState` map is non-empty
**and** the modified element is matched to a gadget element index (`index != -1`).

Gadget state as presented to robots in `BlipData.elements`: the gadget's
position in the blip text is the element key (integer); the element value is
the `Gadget` object with all properties (including state k/v pairs) in the
`properties` map.

---

## Wire / storage formats

### capabilities.xml

```xml
<?xml version="1.0" encoding="utf-8"?>
<w:robot xmlns:w="http://wave.google.com/extensions/robots/1.0">
  <w:capabilities>
    <w:capability name="WAVELET_SELF_ADDED"         context="ROOT,SELF" />
    <w:capability name="BLIP_SUBMITTED"             context="SELF"      filter=".*foo.*" />
    <w:capability name="DOCUMENT_CHANGED"           />
    <w:capability name="GADGET_STATE_CHANGED"       />
    <w:capability name="WAVELET_PARTICIPANTS_CHANGED" />
  </w:capabilities>
  <w:version>1</w:version>
  <w:protocolversion>0.22</w:protocolversion>
  <w:consumer_keys>
    <w:consumer_key for="https://wave.example.com/robot/rpc">mykey</w:consumer_key>
  </w:consumer_keys>
</w:robot>
```

- If `<w:protocolversion>` is absent, the server assumes V1 (`0.1`).
- `WAVELET_SELF_ADDED` is always injected by the server even if not declared.
- `context` is comma-separated; unknown values fall back to default context.
- `filter` is a Java regex; absent or empty means match all.

### Event bundle JSON (server → robot, Passive API)

In the WIAB passive path the `rpcServerUrl` key is **absent** from the emitted
JSON: `EventGenerator` always constructs the bundle with an empty
`rpcServerUrl`, and the GSON serializer omits the property when it is empty or
null. (`proxyingFor` is likewise omitted when empty.)

```json
{
  "robotAddress": "echoey@appspot.com",
  "proxyingFor": "user@example.com",
  "wavelet": {
    "waveId": "example.com!w+abc",
    "waveletId": "example.com!conv+root",
    "rootBlipId": "b+1",
    "rootThread": {"id": "", "location": -1, "blipIds": ["b+1"]},
    "creator": "alice@example.com",
    "creationTime": 1234567890000,
    "lastModifiedTime": 1234567890001,
    "version": 42,
    "participants": ["alice@example.com", "echoey@appspot.com"],
    "participantRoles": {},
    "dataDocuments": {"robot-data": "<doc/>"},
    "tags": [],
    "title": "Hello Wave"
  },
  "blips": {
    "b+1": {
      "blipId": "b+1",
      "waveId": "example.com!w+abc",
      "waveletId": "example.com!conv+root",
      "creator": "alice@example.com",
      "contributors": ["alice@example.com"],
      "content": "\nHello world",
      "lastModifiedTime": 1234567890001,
      "version": 5,
      "parentBlipId": null,
      "childBlipIds": [],
      "threadId": "",
      "replyThreadIds": [],
      "annotations": [{"name": "lang", "value": "en", "range": {"start": 0, "end": 11}}],
      "elements": {
        "3": {"type": "GADGET", "properties": {"url": "https://example.com/g.xml", "mykey": "myval"}}
      }
    }
  },
  "threads": {
    "": {"id": "", "location": -1, "blipIds": ["b+1"]}
  },
  "events": [
    {
      "type": "DOCUMENT_CHANGED",
      "modifiedBy": "alice@example.com",
      "timestamp": 1234567890001,
      "properties": {
        "blipId": "b+1"
      }
    }
  ]
}
```

### Operation request JSON (robot → server)

```json
[
  {
    "method": "robot.notify",
    "id": "op1",
    "params": {
      "protocolVersion": "0.22",
      "capabilitiesHash": "1"
    }
  },
  {
    "method": "wavelet.appendBlip",
    "id": "op2",
    "params": {
      "waveId": "example.com!w+abc",
      "waveletId": "example.com!conv+root",
      "blipData": {
        "blipId": "TBD",
        "waveId": "example.com!w+abc",
        "waveletId": "example.com!conv+root",
        "content": "\nHello from robot",
        "elements": {},
        "annotations": [],
        "contributors": [],
        "childBlipIds": [],
        "replyThreadIds": [],
        "creator": null,
        "lastModifiedTime": -1,
        "version": -1
      }
    }
  }
]
```

### Response JSON (server → robot, Active/Data API)

```json
[
  {"id": "op1", "data": {}},
  {"id": "op2", "data": {"blipId": "b+new", "waveletId": "example.com!conv+root", "waveId": "example.com!w+abc"}}
]
```

Error response:
```json
{"id": "op3", "error": "Blip not found: b+missing"}
```

---

## Interfaces / APIs

### Event catalog

| EventType | Trigger | Extra properties in `properties` |
|---|---|---|
| `WAVELET_SELF_ADDED` | Robot added to wavelet | — |
| `WAVELET_SELF_REMOVED` | Robot removed from wavelet | — |
| `WAVELET_PARTICIPANTS_CHANGED` | Any participant added/removed | `participantsAdded: []string`, `participantsRemoved: []string` |
| `WAVELET_BLIP_CREATED` | New blip created | `newBlipId: string` |
| `WAVELET_BLIP_REMOVED` | Blip deleted | `removedBlipId: string` |
| `WAVELET_TITLE_CHANGED` | NOT fired by server (TBD in `EventGenerator.java`; never constructed) | `title: string` (the **new** title) |
| `WAVELET_TAGS_CHANGED` | NOT fired by server (TBD in `EventGenerator.java`; tags data docs are ignored) | — (no extra properties; `WaveletTagsChangedEvent` declares no fields beyond the base) |
| `WAVELET_CREATED` | Wavelet newly created | — |
| `WAVELET_FETCHED` | Response to `robot.fetchWave` | — |
| `DOCUMENT_CHANGED` | Blip document content changed | — |
| `BLIP_SUBMITTED` | Blip submitted (phased out; may not fire) | — |
| `BLIP_CONTRIBUTORS_CHANGED` | NOT fired by server (TBD in `EventGenerator.java`; never constructed) | `contributorsAdded: []string`, `contributorsRemoved: []string` |
| `ANNOTATED_TEXT_CHANGED` | Annotation key/value changed | `name: string`, `value: string` (full `properties`: `blipId`, `name`, `value`) |
| `GADGET_STATE_CHANGED` | Gadget state entry changed | `index: int` (element position), `oldState: map[string→string]` |
| `FORM_BUTTON_CLICKED` | Form button `<click>` element inserted | `buttonName: string` |
| `OPERATION_ERROR` | Operation processing failure | `operationId: string`, `message: string` |

All events share base fields: `type`, `modifiedBy`, `timestamp`, `blipId`
(the blip related to the event, or the root blip for wavelet events).

**Defined-but-never-fired events.** `WAVELET_TITLE_CHANGED`, `WAVELET_TAGS_CHANGED`,
`BLIP_CONTRIBUTORS_CHANGED`, and `BLIP_SUBMITTED` are registered in the API
(`EventType` enum, `AbstractRobot` dispatch, `EventHandler` interface) but the
server's `EventGenerator` never constructs them — the title/tags/contributors
three are marked `TBD` in `EventGenerator`'s class comment, and `BlipSubmitted`
is documented there as "will not be supported" (submit ops are being phased
out). The only `new *Event(...)` constructions in `EventGenerator` are
`WaveletSelfAdded`, `WaveletSelfRemoved`, `WaveletBlipCreated`,
`WaveletBlipRemoved`, `WaveletParticipantsChanged`, `AnnotatedTextChanged`,
`GadgetStateChanged`, `FormButtonClicked`, and `DocumentChanged`. A Go rewrite
can omit firing logic for the never-fired events (or implement it as a
deliberate improvement). The "Extra properties" columns above for these events
describe the fields the event class *would* serialize if it were ever
constructed (via reflection on the declared field names), not a payload any
WIAB server actually emits.

**Serialization note.** Each event's extra properties are serialized by
`EventSerializer` using Java reflection over the event class's declared fields,
emitting the **raw field name** as the JSON key. This is why the wire key for
`ANNOTATED_TEXT_CHANGED` is `value` (not `newValue`) and for
`WAVELET_TITLE_CHANGED` is `title` — they match the Java field names exactly.

### Operation catalog

All operations take `waveId` and `waveletId` in params. Blip operations also
take `blipId`. **Not every method named in the SDK is backed by a server-side
`OperationService`.** The "Reg." column marks which registries actually
implement each method: **P** = Passive, **A** = Active, **D** = Data API.
A method with no registry mark (—) is part of the SDK/wire vocabulary
(`OperationType`) but is rejected as unknown by every WIAB server registry.
See [Active API vs Passive API operation sets](#active-api-vs-passive-api-operation-sets)
below for the authoritative per-registry enumeration.

**Wavelet operations**:

| Method | Reg. | Description | Key params |
|---|---|---|---|
| `wavelet.appendBlip` | P A D | Append new blip to root thread | `blipData: BlipData` |
| `wavelet.create` | P | Create new wavelet (deprecated; Active/Data use `robot.createWavelet`) | — |
| `wavelet.setTitle` | P A D | Set wavelet title | `waveletTitle: string` |
| `wavelet.addParticipant` | P A D | Add participant | `participantId: string` |
| `wavelet.removeParticipant` | P A D | Remove participant | `participantId: string` |
| `wavelet.removeSelf` | — | Remove robot from wavelet (SDK vocabulary; no server service) | — |
| `wavelet.modifyParticipantRole` | — | Change participant role (no server service) | `participantId: string`, `participantRole: string` |
| `wavelet.appendDatadoc` | — | Append named data document (no server service) | `datadocName: string`, `datadocValue: string` |
| `wavelet.setDatadoc` | — | Set named data document (no server service) | `datadocName: string`, `datadocValue: string` |
| `wavelet.modifyTag` | — | Add or remove a tag (no server service) | `name: string`, `modify_how: string` ("add"/"remove") |

**Blip operations**:

| Method | Reg. | Description | Key params |
|---|---|---|---|
| `blip.continueThread` | P A D | Append reply to same thread | `blipData: BlipData` |
| `blip.createChild` | P A D | Create child blip in new thread | `blipData: BlipData` |
| `blip.delete` | P A D | Delete blip | — |
| `blip.setAuthor` | — | Override blip author (no server service) | `blipAuthor: string` |
| `blip.setCreationTime` | — | Override blip creation time (no server service) | `blipCreationTime: long` |

**Document operations**:

| Method | Reg. | Description |
|---|---|---|
| `document.appendMarkup` | P A D | Append HTML markup |
| `document.modify` | P A D | Structured modify (see below) |
| `document.appendInlineBlip` | P A D | Append inline blip |
| `document.insertInlineBlip` | P A D | Insert inline blip at index |
| `document.insertInlineBlipAfterElement` | P A D | Insert inline blip after a matched element |
| `document.append` | — | Append plain text (no server service) |
| `document.appendStyledText` | — | Append text with style (no server service) |
| `document.delete` | — | Delete range (no server service) |
| `document.insert` | — | Insert text at index (no server service) |
| `document.replace` | — | Replace content (no server service) |
| `document.appendElement` | — | Append an Element (no server service) |
| `document.insertElement` | — | Insert Element at index (no server service) |
| `document.deleteElement` | — | Delete an Element (no server service) |
| `document.replaceElement` | — | Replace an Element (no server service) |
| `document.modifyElementAttrs` | — | Modify an Element's properties (no server service) |
| `document.setAnnotation` | — | Set annotation on range (no server service) |
| `document.deleteAnnotation` | — | Remove annotation (no server service) |

> The legacy text/element/annotation document operations above are part of the
> SDK vocabulary but carry no server-side service in WIAB; most of their effect
> is achievable through `document.modify`, which is the only structured
> document operation any registry implements.

**`document.modify`** is the most powerful operation. It combines:
- **Query** (`modifyQuery`): where to act — range, index, element type+restrictions, or annotation key/value
- **Action** (`modifyAction`): what to do — `DELETE`, `REPLACE`, `INSERT`, `INSERT_AFTER`, `ANNOTATE`, `CLEAR_ANNOTATION`, `UPDATE_ELEMENT`
- Action carries: `values []string`, `elements []Element`, `annotationKey string`, `bundledAnnotations []BundledAnnotation`, `useMarkup bool`

**Robot utility operations**:

| Method | Reg. | Description | Key params |
|---|---|---|---|
| `robot.notify` | P A (D=no-op) | Announce capabilities hash; triggers re-fetch if changed | `capabilitiesHash: string`, `protocolVersion: string` |
| `robot.notifyCapabilitiesHash` | P A (D=no-op) | Legacy alias of `robot.notify` | `capabilitiesHash: string` |
| `robot.folderAction` | P A D | Move wave to folder/archive/mute | `modify_how: string` |
| `robot.createWavelet` | A D | Create new wave | `waveletData: WaveletData`, optional `message: string` |
| `robot.fetchWave` | A D | Fetch wavelet state | `waveId`, `waveletId` (response via `WAVELET_FETCHED` event or `waveletData` in response data) |
| `robot.search` | A D | Search waves | `query: string`, `numResults: int` (response: `searchResults`) |
| `robot.fetchProfiles` | A D | Fetch participant profiles | `fetchProfilesRequest` |
| `robot.exportSnapshot` | A D | Export wavelet XML snapshot | `rawSnapshot: string` in response |
| `robot.exportDeltas` | A D | Export raw deltas | `fromVersion`, `toVersion` |
| `robot.exportAttachment` | A D | Export attachment data | `attachmentId` |
| `robot.importDeltas` | A D | Import raw deltas | `rawDeltas`, `targetVersion` |
| `robot.importAttachment` | A D | Import attachment | `attachmentData` |

In the Data API registry, `robot.notify` and `robot.notifyCapabilitiesHash` are
wired to a no-op (`DoNothingService`): they parse and return success but perform
no action. In the Passive and Active registries they use the real
`NotifyOperationService`.

### Active API vs Passive API operation sets

There are exactly three server-side registries, each mapping `OperationType`
values to an `OperationService`. Any method not registered is rejected as an
unknown operation. The enumerations below are authoritative (derived directly
from the three registry classes).

**Passive registry** (`OperationServiceRegistryImpl`) — 16 entries:

```
robot.notify                         (real NotifyOperationService)
robot.notifyCapabilitiesHash         (real NotifyOperationService)
wavelet.addParticipant               (NEWSYNTAX)
wavelet.appendBlip
wavelet.removeParticipant            (NEWSYNTAX)
blip.continueThread
blip.createChild
blip.delete
document.appendInlineBlip
document.appendMarkup
document.insertInlineBlip
document.insertInlineBlipAfterElement
wavelet.create                       (deprecated WAVELET_CREATE — NOT robot.createWavelet)
document.modify
wavelet.setTitle
robot.folderAction
```

**Active registry** (`ActiveApiOperationServiceRegistry`) — the same
blip / inline-document / participant / `setTitle` / `folderAction` / `modify`
set as Passive, but with two differences and several additions:
- uses `robot.createWavelet` (`ROBOT_CREATE_WAVELET`) **instead of** the
  deprecated `wavelet.create`;
- `robot.notify` / `robot.notifyCapabilitiesHash` also use the real
  `NotifyOperationService`;
- adds: `robot.fetchWave`, `robot.search`, `robot.fetchProfiles`,
  `robot.exportSnapshot`, `robot.exportDeltas`, `robot.exportAttachment`,
  `robot.importDeltas`, `robot.importAttachment`.

**Data API registry** (`DataApiOperationServiceRegistry`) — identical to the
Active registry **except** `robot.notify` and `robot.notifyCapabilitiesHash`
are both wired to `DoNothingService` (no-ops).

Methods that appear in the SDK `OperationType` vocabulary and in the catalog
tables above but are registered in **no** server registry: `wavelet.removeSelf`,
`wavelet.appendDatadoc`, `wavelet.setDatadoc`, `wavelet.modifyTag`,
`wavelet.modifyParticipantRole`, `blip.setAuthor`, `blip.setCreationTime`,
`document.append`, `document.appendStyledText`, `document.delete`,
`document.insert`, `document.replace`, `document.appendElement`,
`document.insertElement`, `document.deleteElement`, `document.replaceElement`,
`document.modifyElementAttrs`, `document.setAnnotation`,
`document.deleteAnnotation` (plus the deprecated `*.NORANGE` / dotted-name
variants). A server receiving one of these returns an unknown-operation error
for that operation (other operations in the batch still execute).

### HTTP endpoints (server)

| Path | Method | Servlet | Notes |
|---|---|---|---|
| `POST /robot/rpc` | POST | `ActiveApiServlet` | Active API; two-legged OAuth |
| `POST /robot/dataapi/rpc` | POST | `DataApiServlet` | Data API; three-legged OAuth (access token) |
| `GET/POST /robot/dataapi/oauth/request` | — | `DataApiOAuthServlet` | Issue request token |
| `GET/POST /robot/dataapi/oauth/authorize` | — | `DataApiOAuthServlet` | Authorize token (user must be logged in) |
| `GET/POST /robot/dataapi/oauth/access` | — | `DataApiOAuthServlet` | Exchange for access token |
| `GET/POST /robot/register/create` | — | `RobotRegistrationServlet` | Web form to register a robot |

Robot-side endpoints (the robot must serve these):

| Path | Notes |
|---|---|
| `/_wave/capabilities.xml` | Robot's capability manifest |
| `/_wave/robot/jsonrpc` | Passive API event receiver |

---

## Edge cases & failure modes

**Capability fetch failure**: If fetching `capabilities.xml` fails on first
event, the wavelet is dropped for that robot (no events delivered). The robot
will retry on the next wavelet update.

**Robot HTTP call failure**: On any connection error or deserialization failure
when calling `/_wave/robot/jsonrpc`, the server logs the error, returns an
empty operation list, and continues without penalty. The robot does not receive
a retry.

**Self-generated events**: Events where `modifiedBy == robot's participant
address` are dropped before delivery. Prevents infinite loops where a robot
triggers events by its own operations.

**Non-contiguous deltas**: If the server receives a delta batch that is not
contiguous with the robot's current queue entry for a wavelet, it creates a
new queue entry. This can happen under high concurrency. The robot processes
both entries sequentially.

**Race between add and events**: When a robot is added to a wavelet, the delta
containing the `AddParticipant` op is included in the initial event delivery.
The `EventGenerator` filters out events that occur before `WAVELET_SELF_ADDED`
in that batch.

**Operation ordering**: Operations within a response are executed in array
order. All operations in one response share one `OperationContext`; deltas are
batched and submitted together after all operations complete.

**Unknown operation type**: Returns `OperationType.UNKNOWN`; the registry
returns a 400-equivalent error response for that operation; other operations
in the batch continue.

**Protocol version mismatch**: If the robot sends a `protocolVersion` string
the server does not recognize, it falls back to the default (V2_2). The server
selects the Gson serializer by taking the nearest lower version in its map.

**OAuth signature failure**: Returns HTTP 401 immediately. No operations are
executed.

**Data API token expiry**: Request tokens and XSRF tokens expire (12 hours for
XSRF tokens). The `DataApiTokenContainer` uses an expiring in-memory cache;
tokens do not survive server restarts.

**Gadget state identity**: `GADGET_STATE_CHANGED` fires when an `ATTRIBUTES`
modification on a `<state>` child element has a non-null `name` attribute **OR**
a non-null `oldValue` for the `value` key (at least one non-null; it does not
require both). Concretely, the server reads `name` from the modified `<state>`
element and `oldValue` from `oldValues.get("value")`, and if
`name != null || oldValue != null` it records a single `oldState` entry
`{name -> oldValue}` (a null name becomes a null map key, a null oldValue a null
map value). The event then fires only if that `oldState` map is non-empty
**and** the modified element is matched to a gadget element index
(`index != -1`). Changes to `<gadget>` element attributes directly (e.g.
`title`, `ifr`) do not fire this event; they may fire `DOCUMENT_CHANGED`.

---

## Open questions / ambiguities

1. **Passive API auth**: The current server does not sign or authenticate the
   HTTP POST to `/_wave/robot/jsonrpc`. Any party that knows the robot URL and
   event format can forge events. The `capabilities.xml` `<w:consumer_key>`
   is parsed but in the passive RobotConnector it is not used for request
   signing. Clarify whether a Go rewrite should add OAuth signing to passive
   delivery.

2. **Capabilities hash semantics**: The `<w:version>` tag is opaque. Robots
   can put any string. There is no hash validation — the server just compares
   for equality. Confirm that this is intentional and sufficient.

3. **Timestamp accuracy**: `EventGenerator` sets `timestamp = 0L` for all
   generated events with a comment that the `WaveBus` does not send timestamps.
   A Go rewrite should decide where the authoritative timestamp comes from
   (delta commit time is available from `TransformedWaveletDelta`).

4. **Defined-but-never-fired events**: The event classes `WaveletTitleChangedEvent`,
   `WaveletTagsChangedEvent`, and `BlipContributorsChangedEvent` are defined in
   the API (`com/google/wave/api/event/`) and dispatchable, but `EventGenerator`
   never constructs them (marked `TBD` in its class comment), so they are never
   delivered to passive robots. The same applies to `BlipSubmitted`, whose code
   comment says it "will not be supported" because submit ops are being phased
   out. Verification basis: the only `new *Event(...)` constructions in
   `EventGenerator` are `WaveletSelfAdded`/`WaveletSelfRemoved`,
   `WaveletBlipCreated`/`WaveletBlipRemoved`, `WaveletParticipantsChanged`,
   `AnnotatedTextChanged`, `GadgetStateChanged`, `FormButtonClicked`, and
   `DocumentChanged`. A Go rewrite can omit firing logic for these four events
   (or implement it as a deliberate improvement).

5. **Data API token persistence**: Tokens are in-memory only (Guava cache).
   Server restart invalidates all issued tokens. A production Go rewrite likely
   needs persistent token storage.

6. **Active API consumer key vs robot address**: The `activeRobotApiUrl` passed
   to the capabilities parser is always `""` in the current code (see
   `RobotsGateway.updateRobotAccount`). This means the `<w:consumer_key
   for="...">` tag is never matched in the passive path. The Active API servlet
   reads the consumer key from the robot account's `consumerSecret`, not from
   capabilities.xml. This is asymmetric and potentially confusing.

7. **Port/priority of this subsystem**: The Robots API is a significant surface
   area (108 API classes + 58 server classes). For a Go rewrite, consider
   deferring the passive event delivery (which requires a running event bus and
   full wavelet awareness) and implementing the Active/Data APIs first, since
   they are request/response without the event loop complexity.

8. **Gadget server/proxy**: The code does not contain a gadget server or proxy
   component. Gadgets are rendered by the web client directly via the OpenSocial
   gadget container. The server only stores and replicates gadget state via OT.
   A Go rewrite does not need to implement gadget serving.

9. **FormButton clicked event**: Generated by detecting a `<click>` element
   inserted into a document. This is a client-side synthetic operation; the
   exact semantics of when and by whom this element is inserted are in the web
   client (spec 10), not the server.

---

## Source references

| Path | Role |
|---|---|
| `wave/src/main/java/com/google/wave/api/OperationType.java` | Canonical list of all operation method names |
| `wave/src/main/java/com/google/wave/api/event/EventType.java` | Canonical list of all event types |
| `wave/src/main/java/com/google/wave/api/JsonRpcConstant.java` | All JSON-RPC field names (RequestProperty, ResponseProperty, ParamsProperty) |
| `wave/src/main/java/com/google/wave/api/impl/EventMessageBundle.java` | EventMessageBundle data structure |
| `wave/src/main/java/com/google/wave/api/impl/EventMessageBundleGsonAdaptor.java` | JSON serialization of event bundle |
| `wave/src/main/java/com/google/wave/api/event/EventSerializer.java` | JSON serialization/deserialization of individual events |
| `wave/src/main/java/com/google/wave/api/impl/WaveletData.java` | WaveletData structure |
| `wave/src/main/java/com/google/wave/api/BlipData.java` | BlipData structure |
| `wave/src/main/java/com/google/wave/api/Gadget.java` | Gadget element well-known properties |
| `wave/src/main/java/com/google/wave/api/ProtocolVersion.java` | Protocol version enum (0.1/0.2/0.21/0.22) |
| `wave/src/main/java/com/google/wave/api/Context.java` | Context enum (ROOT/PARENT/SIBLINGS/CHILDREN/SELF/ALL) |
| `wave/src/main/java/com/google/wave/api/robot/Capability.java` | Capability (event type + contexts + filter) |
| `wave/src/main/java/com/google/wave/api/robot/RobotCapabilitiesParser.java` | capabilities.xml parsing |
| `wave/src/main/java/com/google/wave/api/robot/RobotName.java` | Robot address parsing (id[+proxy][#ver]@domain) |
| `wave/src/main/java/com/google/wave/api/RobotSerializer.java` | JSON serialization with protocol-version dispatch |
| `wave/src/main/java/org/waveprotocol/box/server/robots/passive/RobotsGateway.java` | WaveBus subscriber; routes deltas to Robot instances |
| `wave/src/main/java/org/waveprotocol/box/server/robots/passive/Robot.java` | Per-robot runnable; queues and processes wavelet updates |
| `wave/src/main/java/org/waveprotocol/box/server/robots/passive/EventGenerator.java` | Converts deltas to EventMessageBundle |
| `wave/src/main/java/org/waveprotocol/box/server/robots/passive/RobotConnector.java` | HTTP calls to robot + capability fetching |
| `wave/src/main/java/org/waveprotocol/box/server/robots/RobotCapabilities.java` | Server-side capability record |
| `wave/src/main/java/org/waveprotocol/box/server/robots/RobotRegistrationServlet.java` | Robot registration web form |
| `wave/src/main/java/org/waveprotocol/box/server/robots/register/RobotRegistrarImpl.java` | Robot account creation logic |
| `wave/src/main/java/org/waveprotocol/box/server/robots/active/ActiveApiServlet.java` | Active API HTTP endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/robots/active/ActiveApiOperationServiceRegistry.java` | Active API operation registry |
| `wave/src/main/java/org/waveprotocol/box/server/robots/dataapi/DataApiServlet.java` | Data API HTTP endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/robots/dataapi/DataApiOAuthServlet.java` | Data API OAuth dance (3-legged) |
| `wave/src/main/java/org/waveprotocol/box/server/robots/dataapi/DataApiOperationServiceRegistry.java` | Data API operation registry |
| `wave/src/main/java/org/waveprotocol/box/server/robots/dataapi/BaseApiServlet.java` | Shared Active+Data API request processing |
| `wave/src/main/java/org/waveprotocol/box/server/robots/operations/` | Individual operation service implementations |
| `wave/src/main/java/org/waveprotocol/wave/model/gadget/GadgetConstants.java` | Gadget XML tag/attribute names |
| `wave/src/main/java/org/waveprotocol/wave/model/gadget/GadgetXmlUtil.java` | Gadget XML construction helpers |
