# 01 — Core Wave Data Model

## Purpose & scope

This spec defines the structural entities of Apache Wave and how they relate to
each other. It covers the wave/wavelet/blip/document hierarchy, all identifier
types and their serialization, the conversation model built on top of wavelets,
the persistent document data model, wavelet versioning (including HashedVersion),
participants and roles, and the per-user supplement data structure.

Excluded from this spec: operational-transform operations and compose/transform
algorithms (see `02-operational-transform.md`), the wire protocol and RPC framing
(`04-wire-protocol.md`), storage formats (`05-storage-persistence.md`), and
federation crypto (`07-federation.md`).

---

## Concepts & glossary

| Term | Definition |
|------|-----------|
| **Wave** | A named collection of wavelets. Identified by a WaveId. Carries no state of its own beyond the set of wavelets it contains. |
| **Wavelet** | The fundamental unit of storage, access control, and concurrency. A wavelet holds a participant set, a version counter, a hashed version, a set of named documents, and timestamps. |
| **Blip** | A document within a conversation wavelet that holds user-authored content. Each blip has a document ID, author, contributors, and per-blip version metadata. |
| **Document** | An ordered XML-like tree: elements with typed attributes plus text nodes, plus a parallel layer of annotations. Every piece of state in a wavelet (including conversation structure) is stored as a document. |
| **Conversation** | The logical view of a conversation wavelet. Structured as a tree of threads and blips backed by a manifest document. |
| **Thread** | An ordered list of blips. Threads can be replies to a blip (non-inline or inline). Every conversation has exactly one root thread. |
| **Manifest** | A special document (id = `"conversation"`) within a conversation wavelet that encodes the conversation structure (thread/blip tree) as XML. |
| **User Data Wavelet (UDW)** | A per-user private wavelet within a wave that stores per-user supplement state. |
| **Supplement** | Per-user state stored in the UDW: read/unread markers, folder assignments, archive state, thread collapse state, etc. |
| **WaveRef** | A reference into the wave hierarchy: wave, optionally plus wavelet, optionally plus document (blip). Used in URLs and links. |
| **HashedVersion** | A pair `(version: uint64, historyHash: []byte)` that identifies a specific point in a wavelet's history with cryptographic integrity. |
| **Delta** | An atomic set of operations applied to a wavelet, advancing its version. (See spec 02 and 03.) |
| **Token** | A `+`-separated component within a local ID. The separator `+` is escaped with `~` if it appears inside a token. |

---

## Data structures

### 2.1 Wave

```
Wave {
    id       WaveId
    wavelets map<WaveletId, Wavelet>   // keyed by wavelet local id
}
```

A wave is a container. It has no independent timestamps or participants; those
live on each wavelet.

### 2.2 Wavelet

```
Wavelet {
    waveId            WaveId
    waveletId         WaveletId
    creator           ParticipantId
    creationTime      int64           // ms since Unix epoch
    lastModifiedTime  int64           // ms since Unix epoch
    version           uint64          // monotonically increasing, starts at 0
    hashedVersion     HashedVersion   // authoritative signed version
    participants      ordered set<ParticipantId>
    documents         map<string, Document>   // keyed by document ID
}
```

**Invariants:**
- `version == hashedVersion.version` (the plain version is always consistent
  with the hashed version's version field).
- `version` increments by 1 for each operation applied. A delta of N operations
  advances version by N.
- `lastModifiedTime` is the time of the last operation applied.
- The participant set is ordered by time of addition and never has duplicates.
- `creator` is immutable after creation.
- An empty document (one that has never been written to, or was deleted) is
  absent from the `documents` map.

### 2.3 Document

Every piece of wavelet state is a **persistent document** — an XML-like tree plus
annotations. Documents are mutated through DocOp operations (spec 02).

```
Document {
    id          string
    content     DocumentContent   // XML tree
    annotations AnnotationSet     // parallel string key/value layer
}

DocumentContent {
    root        Element           // the single document element
}

Element {
    tagName     string
    attributes  map<string, string>    // sorted by key
    children    []Node
}

Node = Element | TextNode

TextNode {
    data   string   // non-empty; adjacent text nodes are merged
}
```

**Invariants:**
- The document always has exactly one root element (the document element).
- Text nodes are never empty and never adjacent to another text node.
- Attribute maps are sorted lexicographically by key — this is a correctness
  requirement for operation composition and transforms.
- Every attribute name/value is a string; null values are represented by
  absence of the attribute.

**Annotations** are a parallel layer: a map from `(position, key) → string|null`
where position ranges over 0 to `size-1` (positions cover start tags, end tags,
and individual characters). Annotations are used for styling, user presence, and
semantic markup. The annotation layer is part of the document and is mutated by
`annotationBoundary` components in DocOp.

### 2.4 Blip (raw)

A blip is a document in a conversation wavelet with additional metadata tracked
by the wavelet:

```
BlipData {
    id                  string          // document id, prefix "b+"
    wavelet             Wavelet         // back-reference
    author              ParticipantId   // creator of the blip
    contributors        ordered set<ParticipantId>
    lastModifiedTime    int64           // ms since Unix epoch
    lastModifiedVersion uint64          // wavelet version of last modification
    content             Document        // the blip document
}
```

The author and contributor metadata are tracked at the `BlipData`/wavelet level
(the `author`, `contributors`, and `lastModifiedTime` fields above), NOT inside
the blip document. Although the conversation schema permits a `<head>` element,
no code path ever populates it (see the implementation note in section 3.3), so
there is no in-document home for this metadata.

### 2.5 HashedVersion

```
HashedVersion {
    version      uint64
    historyHash  []byte   // may be empty for "unsigned" versions
}
```

`version` is the count of operations applied to the wavelet since creation (not
including the initial empty state). `historyHash` is a cryptographic commitment
to the sequence of deltas that produced this version.

**Version zero** is special: `version=0`, `historyHash = UTF-8 bytes of the
wavelet's URI`. The URI is formed as
`wave://<waveletDomain>/<waveDomainPart><waveLocalId>/<waveletLocalId>` (see
section 4.3 for exact format).

**Subsequent versions:** After applying a delta whose serialized bytes are `D`
to a wavelet at `HashedVersion(v, h)`, the new hashed version is:

```
newVersion = v + operationCount(D)
newHash    = SHA-256(h || D)[0:20]    // first 160 bits of SHA-256
```

where `||` denotes concatenation and `D` is the serialized protobuf bytes of the
applied delta (see spec 04 for the protobuf format).

**Unsigned versions** (`historyHash = []byte{}`) are used when only the version
number matters and cryptographic integrity is not needed (e.g., for read-state
markers in the supplement).

**String serialization:** `"<version>:<base64url-hash>"`, e.g.
`"42:yH+XqIvfwSKHLGqEcSCTWQ=="`. The base64 uses standard base64 (with `+`
and `/`; see `CharBase64`). An unsigned version serializes as `"<version>:"`.

### 2.6 WaveId

```
WaveId {
    domain  string   // RFC 1035 hostname, lowercase, 1–253 chars
    id      string   // non-empty, valid wave identifier characters
}
```

**Serialization (modern):** `"<domain>/<id>"`, e.g. `"example.com/w+abc123"`.

**Serialization (legacy):** `"<domain>!<id>"`, e.g. `"example.com!w+abc123"`.
The legacy form is still encountered in stored data and must be parseable.

### 2.7 WaveletId

Same structure as WaveId:

```
WaveletId {
    domain  string
    id      string
}
```

**Serialization:** The legacy serializer (`LegacyIdSerialiser`) is used for
WaveletId: `"<domain>!<id>"`. The modern serializer also produces
`"<domain>/<id>"`. Implementations must accept both.

**Note:** `WaveId.serialise()` uses the modern (`/`) format. `WaveletId.serialise()`
uses the legacy (`!`) format. This inconsistency is a historical artifact that
the Go rewrite must preserve for wire compatibility.

### 2.8 WaveletName

A globally unique wavelet identifier:

```
WaveletName {
    waveId    WaveId
    waveletId WaveletId
}
```

**Modern string serialization:** `"<waveDomain>/<waveLocalId>/<waveletDomainOrTilde>/<waveletLocalId>"`

When the wavelet domain equals the wave domain, the wavelet domain is encoded
as `"~"` (elided). Example:

```
example.com/w+abc123/~/conv+root        // wavelet domain == wave domain
example.com/w+abc123/other.com/conv+xyz // different wavelet domain
```

This 4-token `/`-separated form must have exactly 4 tokens. The `~` domain may
not be written out as the actual domain (that is, `example.com/w+abc/example.com/conv+root`
is invalid — it must use `~`).

### 2.9 ParticipantId

A participant address looks like an email address:

```
ParticipantId {
    address  string   // "name@domain", always normalized to lowercase
}
```

**Validation:** exactly one `@` character; `@` cannot be the last character;
addresses are always stored in lowercase.

**No domain-format validation** is enforced on the name or domain parts beyond
the above. (TODO: this is a known weakness, see Open Questions.)

### 2.10 WaveRef

A reference into the wave hierarchy, used in permalinks and `link/wave`
annotations:

```
WaveRef {
    waveId     WaveId
    waveletId  WaveletId?   // optional
    documentId string?      // optional; only valid when waveletId is set
}
```

**URI path encoding:** percent-encoded `/`-separated tokens:
```
<waveDomain>/<waveLocalId>[/<waveletDomainOrTilde>/<waveletLocalId>[/<documentId>]]
```

Valid token counts: 2 (wave only), 4 (wave + wavelet), or 5 (wave + wavelet +
document). Exactly 3 tokens is invalid.

---

## Well-known identifiers & naming conventions

### Token structure

Local IDs within wave, wavelet, and document IDs are composed of `+`-separated
tokens. The `+` character is the **token separator**. When `+`, `!`, or `~`
appear literally inside a token, they are escaped by prefixing `~`:
- `+` → `~+`
- `!` → `~!`
- `~` → `~~`

### Wave ID prefixes (local id)

| Prefix | Meaning |
|--------|---------|
| `w` | Conversational wave (`w+<seed>`) |
| `prof` | Profile wave |

### Wavelet ID prefixes (local id)

| Prefix | Meaning |
|--------|---------|
| `conv` | Conversation wavelet |
| `conv+root` | Conversation root wavelet (the conventional primary conversation) |
| `user` | User data wavelet (UDW), format: `user+<participantAddress>` |

A wavelet whose local id begins with `conv` (followed by `+`) is a
**conversational wavelet**. A UDW for participant `alice@example.com` hosted on
domain `example.com` has wavelet id `WaveletId("example.com", "user+alice@example.com")`.

### Document ID conventions

| Prefix/Name | Meaning |
|-------------|---------|
| `b+` | Blip document (user content) |
| `g+` | Ghost blip (not rendered; placeholder for deleted content) |
| `conversation` | Conversation manifest document |
| `tags` | Tags document |
| `roles` | Role assignment document |
| `listing` | Indexability document |
| `attach+` | Attachment metadata document |
| `r+` | Robot data document |
| `m/read` | Supplement: per-blip read state |
| `m/folder` | Supplement: folder assignment |
| `m/archiving` | Supplement: archive state |
| `m/muted` | Supplement: mute state |
| `m/seen` | Supplement: seen version state |
| `m/gadgets` | Supplement: gadget state |

**Classification rules:**
- A document id is a **blip id** if it starts with `b+`, or starts with `g+`
  (ghost), or contains `*` and does not start with `m/`, `attach+`, or `spell+`
  (the last clause is legacy compatibility).
- A document id is the **manifest** if it equals `"conversation"` exactly.

### ID generation

New IDs are generated as `<prefix>+<seed><counter>` where:
- `seed` is a session-specific base64url string (not containing `*`).
- `counter` is a per-generator monotonic counter, base-64 encoded using the
  web-safe alphabet `A-Za-z0-9-_` (minimum-length, big-endian, no padding).

The wave-safe base-64 alphabet maps 0–25 to A–Z, 26–51 to a–z, 52–61 to 0–9,
62 to `-`, 63 to `_`.

---

## The conversation model

### 3.1 Structure overview

A **ConversationView** is the set of conversations in a wave, organized as a
forest. Each **Conversation** maps to one conversation wavelet and is structured as:

```
ConversationView
  └── Conversation (root, wavelet id = "conv+root")
       ├── rootThread : ConversationThread
       │    ├── blip[0] (first/root blip)
       │    │    ├── replyThread[0] (non-inline)
       │    │    │    └── blip[0]
       │    │    └── replyThread[1] (inline, anchored at doc offset N)
       │    │         └── blip[0]
       │    └── blip[1]
       └── Conversation (private reply, wavelet id = "conv+<seed>")
            └── anchor → (rootConversation, blip[0])
```

### 3.2 Manifest document

The conversation structure is encoded in the **manifest document** (id =
`"conversation"`). It is an XML document conforming to the manifest schema:

```xml
<conversation [anchorWavelet="<waveletId>"] [anchorBlip="<blipId>"] [sort="m"]>
  <blip id="b+abc">
    <thread id="b+def">          <!-- non-inline reply thread -->
      <blip id="b+ghi"></blip>
    </thread>
    <thread id="b+jkl" inline="true">  <!-- inline reply thread -->
      <blip id="b+mno"></blip>
    </thread>
  </blip>
  <blip id="b+pqr"></blip>
</conversation>
```

**Schema rules for the manifest:**
- Root element: `<conversation>`.
- Allowed attributes on `<conversation>`: `anchorWavelet`, `anchorBlip`,
  `anchorManifestOffset`, `anchorVersion`, `anchorOffset`, `sort`.
- `anchorWavelet` must be a conversational wavelet id (i.e. prefix `conv`).
- `anchorBlip` must be a blip id (prefix `b+`).
- The `<conversation>` element may contain `<blip>` children (the root thread).
- Each `<blip>` element has attribute `id` (a blip id), optional attribute
  `deleted` (boolean string: `"true"` or `"false"`), and may contain `<thread>`
  children (reply threads) and `<peer>` children.
- Each `<thread>` element has attribute `id` (a blip id used as thread id),
  optional `inline` (boolean string), and may contain `<blip>` children. The
  `inline` value is the string `"true"` (Java `Boolean.toString`); it is written
  **only when the thread is inline** and is **absent otherwise** (a missing
  `inline` attribute reads as `false`). Note: do NOT write `inline="1"` — the
  reader uses `Boolean.parseBoolean`, which parses `"1"` as `false`. (The Java's
  own `DocumentBasedManifest` javadoc loosely shows `inline="1"`, but the
  serializer in `DocumentBasedManifestThread` writes `"true"`.)
- `<peer>` elements have attribute `id` (a wavelet id), linking to another
  conversation wavelet.

**Root thread:** The blips directly inside `<conversation>` form the root thread.
The root thread has no id attribute (it is implicit). The first blip in the root
thread is the **root blip** of the conversation.

**Inline threads:** A `<thread inline="true">` is an inline reply, anchored at a
position within the parent blip's document. The anchoring is tracked via a
`<reply id="<threadId>">` element inside the blip document (see blip schema
below).

**Thread IDs:** The thread ID stored in `<thread id="...">` is the ID of the
thread's first blip. Thread IDs are thus blip IDs (prefix `b+`).

**Anchor:** If a conversation has `anchorWavelet` and `anchorBlip` set on
`<conversation>`, it is anchored inside another conversation. The wavelet id
identifies the parent conversation wavelet; the blip id identifies the blip
within that wavelet.

### 3.3 Blip document structure

A blip document conforms to the following schema-permitted XML structure:

```xml
<head>
  <timestamp>
    <lmt t="<milliseconds>"/>
  </timestamp>
</head>
<body>
  <line/>
  ... user content ...
</body>
```

**IMPLEMENTATION NOTE — the `<head>` is never populated.** Although the
conversation schema (`ConversationSchemas.DefaultDocumentSchema`) permits the
`<head><timestamp><lmt t="<ms>"/></timestamp></head>` structure above, the
original Java never emits it. `Blips.INITIAL_HEAD = XmlStringBuilder.createEmpty()`
(empty), and `Blips.buildBlipHead()` is an unimplemented no-op carrying the TODO
comment "Will be implemented when adding blip heads." The actual content of a
newly created blip is therefore only `<body><line/></body>` with **no `<head>`
element at all** (see section 8.3). The `HEAD_TAGNAME` / `TIMESTAMP_TAGNAME` /
`LAST_MODIFICATION_TIME_TAGNAME` constants are defined but unused on every write
path. A Go reimplementation should NOT create or expect a `<head>` element, and
should treat last-modified timestamp metadata as a `BlipData.lastModifiedTime`
field (section 2.4), not as an in-document element. The schema entries are
retained only so that any historical document that *does* contain a `<head>`
validates.

The `<body>` section is the user-visible content area. The `<body>` is a **line
container**: it must begin with a `<line/>` element, and may contain further
`<line>`, `<image>`, `<gadget>`, `<eqn>`, `<reply>`, and other inline elements.

**`<reply>` element:** An inline reply anchor embedded in a blip body:
```xml
<reply id="<threadId>"/>
```
The `id` attribute is the thread id (= the id of the first blip in that reply
thread). Its position in the document determines the inline anchor location.

**Line element attributes:**
- `t` — line type: `h1`, `h2`, `h3`, `h4`, `li` (list item)
- `i` — indent level (positive integer)
- `a` — text alignment: `l`, `r`, `c`, `j`
- `listyle` — list style: `decimal`
- `d` — direction: `l`, `r`

### 3.4 Conversation invariants

**Invariant C1:** Every blip id referenced in the manifest must have a
corresponding document in the wavelet.

**Invariant C2:** The root conversation wavelet has id `conv+root` (local part).
Private reply wavelets have ids of the form `conv+<seed>`.

**Invariant C3:** A blip may be logically deleted (attribute `deleted="true"` in
the manifest) but remain in the manifest as long as it has non-deleted reply
threads. Once all reply threads are removed, the blip entry itself is removed.

**Invariant C4:** The root thread has no id; all other threads have a non-null
id equal to the id of their first blip.

**Invariant C5:** Thread ids are unique within a conversation.

### 3.5 Document schema constraints (conversation wavelets)

A conversation wavelet's documents are validated against hard-coded schema
constraints (`ConversationSchemas`, a `SchemaProvider`). Schema selection is:

- If the wavelet is conversational (`IdUtil.isConversationalId`) **and** the
  document id is a blip id (`IdUtil.isBlipId`): use the **BLIP** schema below.
- Else if the wavelet is conversational and the document id equals
  `"conversation"`: use the **MANIFEST** schema below.
- Otherwise: no schema constraints (`DocumentSchema.NO_SCHEMA_CONSTRAINTS` —
  anything goes).

These constraint tables define the `DocumentSchema` *contract* (permitted
children, permitted attributes, permitted attribute values, required initial
children, and permitted-character class per element) that the operation validator
consumes. See spec 02's `DocumentSchema` / `DocOpValidator` description for how
the contract is applied during operation validation; this section only enumerates
the conversation-specific constraint data. The value predicates referenced below
are `SchemaUtils.isPositiveInteger`, `SchemaUtils.isNonNegativeInteger`,
`SchemaUtils.isValidInteger(value, min)`, `IdUtil.isBlipId`, and
`IdUtil.isConversationalId`.

**Permitted-character classes** (which characters may appear as text within an
element): `BLIP_TEXT` permits ordinary blip text; line containers and the
"one-liner" elements (`caption`, `label`, `input`) permit `BLIP_TEXT`; by default
elements permit `NONE`.

#### BLIP schema (selected for blip-id documents)

Per `ConversationSchemas.DefaultDocumentSchema`. Principal elements (the schema
also lists many gadget/form/experimental elements omitted here for brevity — see
source for the complete set):

| Element | Permitted children | Attributes (and value constraints) |
|---------|--------------------|------------------------------------|
| (root) | `head`, `body` | — |
| `head` | `timestamp` | — |
| `timestamp` | `lmt` | — |
| `lmt` | — | `t` (non-negative integer) |
| `body` | line-container set (below) + form/experimental/etc. elements | — |
| `line` | — | `t` ∈ {`h1`,`h2`,`h3`,`h4`,`li`}; `listyle` = `decimal`; `a` ∈ {`l`,`r`,`c`,`j`}; `d` ∈ {`l`,`r`}; `i` = positive integer |
| `img` | — | `alt`, `src`, `width`/`height` (non-negative integers) |
| `reply` | — | `id` (inline-reply thread anchor) |
| `caption` / `label` / `input` | one-liner (`BLIP_TEXT`) | — |

A **line container** (`body`, and also `textarea`, `part`, `stanza`, `quote`)
permits children {`line`, `image`, `gadget`, `eqn`, `experimental`,
`mediasearch`, `img`, `reply`, `profile`}, permits `BLIP_TEXT`, and **requires
its first child to be a `line` element** (`addRequiredInitial(..., ["line"])`).
This is the schema basis for the "must begin with `<line/>`" rule in section 3.3.

Note the `head`/`timestamp`/`lmt` entries are schema-permitted but never written
by any code path (see the implementation note in section 3.3).

#### MANIFEST schema (selected for the `"conversation"` document)

Per `ConversationSchemas.MANIFEST_SCHEMA_CONSTRAINTS`:

| Element | Permitted children | Attributes (and value constraints) |
|---------|--------------------|------------------------------------|
| (root) | `conversation` | — |
| `conversation` | `blip` | `anchorWavelet` (must be a conversational wavelet id, parsed via `WaveletIdSerializer.INSTANCE`); `anchorManifestOffset`, `anchorVersion`, `anchorOffset` (non-negative integers); `anchorBlip` (must be a blip id); `sort` |
| `blip` | `thread`, `peer` | `id` (must be a blip id); `deleted` ∈ {`true`,`false`} |
| `thread` | `blip` | `id` (a blip id used as thread id); `inline` ∈ {`true`,`false`} |
| `peer` | — | `id` |

The boolean value sets are `SchemaUtils.BOOLEAN_VALUES` (`true`/`false`). The id
and offset value constraints are enforced in `permitsAttribute` rather than by
the declarative attribute-value lists.

---

## Participants and roles

### 4.1 Participant list

Each wavelet maintains an ordered set of `ParticipantId`s. Participants are
added/removed via wavelet operations (`AddParticipant`, `RemoveParticipant` in
spec 02). Order reflects addition order. Being on the participant list is the
**sole** access-control gate — only participants (plus the shared-domain
participant) can read or write a wavelet. There are no enforced role exceptions;
see section 4.2.

The wavelet creator is immutable and always a participant (at creation time).

### 4.2 Roles

Roles are stored in the `"roles"` data document within the wavelet. The default
role for any participant (if no explicit assignment exists) is `FULL`.

```
Role = FULL | READ_ONLY

FULL     → capabilities: JOIN, INDEX, READ, ADD, WRITE
READ_ONLY → capabilities: READ, INDEX
```

Role assignments are stored as `DocumentBasedRoles` — an XML document encoding
`(participant → role)` pairs. The last assignment for a participant wins if
duplicates exist.

**NOTE — roles are advisory, not enforced.** The Role/Capability model (`FULL`,
`READ_ONLY`) is defined in the data model and surfaced through the Robots/Data
API, but it is **NOT enforced by the server**. The server never reads the `Role`
enum; read AND write authorization is gated solely on participant-set membership
plus the shared-domain participant (`WaveletDataUtil.checkAccessPermission`,
which tests creator/participant/shared-domain only). A `READ_ONLY` participant
can still author and submit deltas. Roles are advisory client/API metadata only.

### 4.3 Indexability

The `"listing"` document controls whether a wavelet appears in search indexes.
This is a separate concern from the participant list. (Details: spec 11.)

---

## Versions

### 5.1 Wavelet version

Version is a `uint64` that starts at 0 and increments by 1 per operation applied.
A wavelet's `version` field reflects the total number of operations ever applied.

`lastModifiedVersion` on a blip is the wavelet version at which that blip was
last modified.

### 5.2 HashedVersion

See section 2.5. Key facts for implementors:

- Version zero hash = `UTF-8(wavelet URI)` where the URI is:
  ```
  wave://<waveletDomain>/<waveDomainPart><waveLocalId>/<waveletLocalId>
  ```
  If the wave domain equals the wavelet domain, `<waveDomainPart>` is empty;
  otherwise it is `<waveDomain>!`. The wave and wavelet local ids are
  percent-encoded. Example:
  ```
  wave://example.com/w+abc/conv+root
  wave://privatereply.com/example.com!w+abc/conv+3sG7
  ```

- Subsequent hashes: `SHA-256(prevHash || appliedDeltaBytes)[0:20]` (first 160
  bits). The `appliedDeltaBytes` are the serialized protobuf of the `AppliedWaveletDelta`
  (see spec 04).

- Implementations that do not validate signatures can use unsigned versions
  (empty hash). The supplement stores read-state version numbers as plain
  integers (not HashedVersion), accepting version mismatch as harmless.

---

## The supplement (per-user state)

### 6.1 Storage

Supplement state lives in the **user data wavelet** (UDW) for each user. The
UDW has wavelet id `user+<participantAddress>` (domain = participant's domain).
Each piece of supplement state is stored in a separate named document within
the UDW.

### 6.2 Supplement documents

| Document | Content |
|----------|---------|
| `m/read` | Per-blip and per-wavelet read versions |
| `m/presentation` | Per-thread collapsed/expanded (presentation) state |
| `m/folder` | Folder assignments |
| `m/archiving` | Per-wavelet archive versions |
| `m/muted` | Mute flag |
| `m/cleared` | Boolean archive-clear override flag (see 6.5a) |
| `m/abuse` | Stored `WantedEvaluation` spam/abuse signals |
| `m/seen` | Per-wavelet seen HashedVersions and notified versions |
| `m/gadgets` | Per-gadget key/value state |

These are exactly the nine documents defined in `WaveletBasedSupplement`
(`READSTATE_DOCUMENT`, `PRESENTATION_DOCUMENT`, `FOLDERS_DOCUMENT`,
`ARCHIVING_DOCUMENT`, `MUTED_DOCUMENT`, `CLEARED_DOCUMENT`, `ABUSE_DOCUMENT`,
`SEEN_DOCUMENT`, `GADGETS_DOCUMENT`).

**Integer width of version values:** Supplement read-state, archive, and
notified version numbers are stored and compared as **32-bit signed integers**
(Java `int`, serialized via `Serializer.INTEGER` with `Integer.parseInt`), NOT
as the wavelet's `uint64` version. The `v="..."` attributes in `m/read`,
`m/archiving`, and `m/seen`'s `<notified>` element are decimal 32-bit ints;
values must lie in `[-1, 2^31-1]` and the sentinel `NO_VERSION = -1` denotes
absence. A Go reimplementation MUST use `int32` (or otherwise store/parse these
as 32-bit) to interoperate with the original XML-encoded values, since the
original would reject larger values. By contrast, the `<seen>` element carries
the full `HashedVersion` (uint64 version + signature hash, via
`HashedVersionSerializer`), so seen versions are *not* width-limited. Because
wavelet versions are genuinely `uint64`, a wavelet exceeding version `2^31-1`
cannot be correctly tracked by the legacy 32-bit read/archive/notified fields;
this is a pre-existing limitation the Go port inherits (or must consciously
widen, breaking on-disk compatibility).

### 6.3 Read state document (`m/read`)

```xml
<data>
  <wavelet i="<waveletId>">
    <all v="<version>"/>              <!-- wavelet-level override version -->
    <participants v="<version>"/>     <!-- participant-set read version -->
    <tags v="<version>"/>             <!-- tags document read version -->
    <blip i="<blipId>" v="<version>"/>  <!-- per-blip read version -->
    ...
  </wavelet>
  ...
</data>
```

The wavelet id attribute `i` uses the **modern** serialization (`domain/localId`,
`/`-separated), produced by `WaveletIdSerializer.INSTANCE`, which delegates to
`ModernIdSerialiser`. This is **NOT** `WaveletId.serialise()`'s legacy `!` form
(see section 2.7) — the supplement deliberately uses a different serializer. On
read, the value is parsed by `ModernIdSerialiser.deserialiseWaveletId`, which
splits on `/` and requires exactly two tokens; a bare legacy `!`-separated value
would be rejected by that path. Implementations must **write** modern
`domain/localId` here, though they may **also accept** legacy `domain!localId`
if it appears in historical data. The same modern serializer (and the same
write-modern caveat) applies to the `i` attribute in every per-wavelet supplement
structure: `m/archiving` (`<archive i=...>`, section 6.5), `m/seen`
(`<seen i=...>` and `<notified i=...>`, section 6.7), and the thread-state
collection (section 6.8).

Multiple `<all>` or `<blip>` entries for the same id are resolved by taking the
**maximum** version (monotonic map semantics — only newer reads are recorded).

**Read/unread logic:** A blip is "read" if its `lastModifiedVersion` is ≤ either
its per-blip read version or the wavelet-level override version. Otherwise it is
"unread". The sentinel `NO_VERSION = -1` means no read state has been recorded.

### 6.4 Folder document (`m/folder`)

```xml
<data>
  <folder i="<folderId>"/>
  <folder i="<folderId>"/>
</data>
```

`folderId` is a non-negative integer. The set of folder ids is the set of `i`
values present. Duplicate entries are allowed (implementation is a set, not a
list).

### 6.5 Archiving document (`m/archiving`)

```xml
<data>
  <archive i="<waveletId>" v="<version>"/>
  ...
</data>
```

A wave is **archived** if the `m/cleared` flag is NOT set (see 6.5a) **and**
every conversation wavelet has an archive version ≥ its current wavelet version.
If the `m/cleared` flag is set, `getArchiveWaveletVersion` returns `NO_VERSION`
for all wavelets, so the wave is always **inbox**. Otherwise, if at least one
conversation wavelet's current version exceeds its archive version (or has no
archive entry), the wave is **inbox**.

### 6.5a Archive-clear override (`m/cleared`)

The per-wavelet archive versions in `m/archiving` are stored in a
`DocumentBasedMonotonicMap`, which cannot be lowered, shrunk, or cleared. To let
a user move an archived wave back to the inbox ("move to inbox" / clear-archive
action — also invoked as a side effect of `unfollow()`), the supplement uses a
separate boolean override stored in `m/cleared`:

```xml
<data>
  <cleared cleared="true"/>
</data>
```

This is a `DocumentBasedBoolean` on tag `cleared` / attribute `cleared`,
mirroring `m/muted`'s structure.

**Semantics:**
- When `cleared="true"`, `getArchiveWaveletVersion(waveletId)` returns
  `NO_VERSION` (-1) for **every** wavelet, ignoring all entries in `m/archiving`.
  The wave is therefore treated as inbox regardless of stored archive versions.
- `clearArchiveState()` sets this flag to `true`. It is the only mechanism to
  "un-archive", because the monotonic archive map cannot be mutated downward.
- `archiveAtVersion(waveletId, v)` writes a new archive version into
  `m/archiving` **and** resets the flag back to `false`. Thus the clear lasts
  only until the next archive operation.

### 6.6 Mute document (`m/muted`)

```xml
<data muted="true"/>
```

A wave is **muted** (MUTE inbox state) if this boolean attribute is set to
`"true"`. Mute overrides the archive state.

### 6.7 Seen/notified document (`m/seen`)

```xml
<data>
  <seen i="<waveletId>" v="<version>" signature="<base64Hash>"/>
  <notified i="<waveletId>" v="<version>"/>
  <notification pending="true"/>
</data>
```

`seen` entries record the last HashedVersion the user has confirmed receiving
(used for federation receipt), serialized with `HashedVersionSerializer` (full
uint64 version + base64 signature hash). `notified` entries record the last
version for which an email/notification was sent, stored as a **32-bit int** (see
6.2). `pending` indicates a pending notification. The `i` attribute on both
`<seen>` and `<notified>` uses the modern `domain/localId` serialization (see
6.3).

### 6.7a Abuse signals (`m/abuse`)

The `m/abuse` document stores the user's `WantedEvaluation` set (spam/abuse
signals), managed by `DocumentBasedAbuseStore`. The exact element structure is
an implementation detail of the abuse store; the supplement exposes it as a raw
`WantedEvaluation` collection rather than typed accessors.

### 6.8 Thread state (collapsed/expanded)

Thread state is stored in the `m/presentation` document
(`PRESENTATION_DOCUMENT`), via the per-wavelet `WaveletThreadStateCollection`.
Each thread has a state of `COLLAPSED` or `EXPANDED`. Absence of a record means
the thread uses its default rendering. The per-wavelet `i` attribute in this
collection uses the modern `domain/localId` serialization (see 6.3).

### 6.9 Supplement invariants

**Invariant S1:** The UDW for user `alice@example.com` in wave W is at wavelet id
`WaveletId("example.com", "user+alice@example.com")`.

**Invariant S2:** Version numbers in supplement read-state are monotonically
increasing — lower versions are never written after higher ones (monotonic map
semantics via `DocumentBasedMonotonicMap`). This per-entry monotonicity also
governs the per-wavelet archive versions in `m/archiving`. The **effective**
archive version, however, is gated by the `m/cleared` override (6.5a): when
`cleared="true"`, `getArchiveWaveletVersion` returns `NO_VERSION` for all
wavelets without mutating (and thus without violating the monotonicity of) the
stored archive map.

**Invariant S3:** The supplement is private — other participants cannot read the
UDW. It is not a conversation wavelet.

---

## Wire / storage formats

### 7.1 ID serialization summary

| ID Type | Canonical serialization | Example |
|---------|------------------------|---------|
| WaveId | `<domain>/<localId>` | `example.com/w+abc123` |
| WaveletId | `<domain>!<localId>` | `example.com!conv+root` |
| WaveletName (modern) | `<waveDomain>/<waveLocal>/~/waveletLocal` | `example.com/w+abc/~/conv+root` |
| WaveletName (URI) | `wave://<waveletDomain>/[waveDomain!]<waveLocal>/<waveletLocal>` | `wave://example.com/w+abc/conv+root` |
| ParticipantId | `<name>@<domain>` (lowercase) | `alice@example.com` |
| HashedVersion | `<version>:<base64hash>` | `42:yH+XqI==` |
| WaveRef (URL path) | `/wave/<waveDomain>/<waveLocal>[/<waveletDomain>/<waveletLocal>[/<docId>]]` | `/wave/example.com/w+abc/~/conv+root/b+xyz` |

### 7.2 Character constraints for identifiers

**Valid domain:** RFC 1035 hostname — labels of `[a-z0-9]([a-z0-9-]*[a-z0-9])?`
separated by `.`, total length 1–253 characters. Labels may start with a digit.
No trailing dot.

**Valid local ID:** Non-empty string where each character is either:
- ASCII safe: `A-Z`, `a-z`, `0-9`, `-`, `.`, `_`, `~`, `+`, `*`, `@`, or
- UCS char above 0x7F per RFC 3987 (see `WaveIdentifiers.isUcsChar`).

**Token separator:** `+` (0x2B). Escape prefix: `~` (0x7E). The characters `+`,
`!`, and `~` are escaped with `~` when they appear as data within a token.

### 7.3 Wavelet URI (for HashedVersion zero)

Used to seed the initial hash. Format:

```
wave://<waveletDomain>/[<waveDomain>!]<percentEncode(waveLocalId)>/<percentEncode(waveletLocalId)>
```

If `waveDomain == waveletDomain`, the `[<waveDomain>!]` prefix is omitted.

Percent-encoding follows the RFC 3986 path `segment`/`segment-nz` rules as
implemented by `URIEncoderDecoder`. The following characters are **NOT** escaped
and pass through verbatim:

- `A`–`Z`, `a`–`z`, `0`–`9` (ALPHA, DIGIT), and
- the 17 symbol characters `: @ ! $ & ' ( ) * + , ; = - . _ ~`
  (the literal `NOT_ESCAPED` set is `":@!$&'()*+,;=-._~"`).

In particular, **`+` is NOT percent-encoded** — it appears literally in the URI
(e.g. `w+abc`, `conv+root`). Only characters **outside** that set are converted
to their UTF-8 bytes and `%xx`-escaped. A Go reimplementation MUST percent-encode
only out-of-set characters and MUST emit `+`, `!`, `~`, etc. literally to
reproduce Java's version-zero hash. Note also that the inverse `decode` operation
MUST NOT treat `+` as a space (unlike standard form-URL decoding) — see the
`PercentEncoderDecoder.decode` contract.

---

## Algorithms & behavior

### 8.1 Wavelet creation

1. Assign `version = 0`.
2. Compute `hashedVersion = HashedVersion(0, UTF-8(waveletURI))`.
3. Set `creator = <creating participant>`, `creationTime = now`.
4. Add creator to participant set.
5. For conversation wavelets: create the `"conversation"` manifest document with
   an empty `<conversation>` root element.

### 8.2 Applying a delta

When a delta of N operations is applied at `hashedVersion(v, h)`:
1. Apply each operation in order to the wavelet's documents and participant set.
2. Advance `version` by N.
3. Compute `newHash = SHA-256(h || serializedDelta)[0:20]`.
4. Set `hashedVersion = HashedVersion(v+N, newHash)`.
5. Update `lastModifiedTime`.

(See spec 02 for document-level operation semantics; spec 03 for delta
concurrency control.)

### 8.3 Creating a blip

1. Generate new blip id: `"b+" + seed + base64(counter)`.
2. Create the wavelet document with initial content:
   ```xml
   <body><line/></body>
   ```
3. Set author = creating participant, contributors = {creating participant}.
4. Add entry to manifest: append `<blip id="<blipId>"/>` to the appropriate
   thread element.
5. `lastModifiedVersion` is the wavelet version at which the blip was created.

### 8.4 Creating a reply thread

1. Generate thread id (same mechanism as blip id).
2. Add `<thread id="<threadId>"/>` inside the parent `<blip>` element in the
   manifest.
3. For inline threads: add `<thread id="..." inline="true"/>` and insert a
   `<reply id="<threadId>"/>` element at the anchor location in the parent blip's
   body.

### 8.5 Read/unread determination

```
isBlipRead(blip, supplement) =
    blip.lastModifiedVersion <= supplement.getLastReadBlipVersion(waveletId, blipId)
    OR
    blip.lastModifiedVersion <= supplement.getLastReadWaveletVersion(waveletId)
```

`NO_VERSION = -1` means the relevant read version is absent; comparison with a
non-negative version always returns false, so the blip is unread.

---

## Interfaces / APIs

This spec is structural; the mutation API is in spec 02. The key read-only
interfaces:

**WaveView** — access to wavelets by id, iterate all wavelets.

**Wavelet** — get/set participants, get documents by id, read version and hashed
version, timestamps.

**Conversation** — get root thread, get blip by id, get thread by id, get
data documents.

**ConversationThread** — iterate blips in order, get parent blip (null for root
thread).

**ConversationBlip** — get content document, get reply threads, check
`isRoot()`, get author/contributors/timestamps.

**PrimitiveSupplement** — get/set per-blip and per-wavelet read versions, folder
state, archive state, seen versions. Read/archive/notified versions are 32-bit
`int` (sentinel `NO_VERSION = -1`); seen versions are full `HashedVersion`. Key
archive methods: `getArchiveWaveletVersion(waveletId)` (returns `NO_VERSION` for
all wavelets while the `m/cleared` flag is set, see 6.5a), `archiveAtVersion(
waveletId, version)` (writes a new archive version and clears the `m/cleared`
flag), and `clearArchiveState()` (sets the `m/cleared` flag to force the wave
back to the inbox).

---

## Edge cases & failure modes

- **Malformed IDs:** WaveId and WaveletId construction validates domain (RFC 1035)
  and local id (wave identifier charset). Invalid IDs throw `InvalidIdException`
  (checked) or `IllegalArgumentException` (unchecked, depending on call site).

- **Legacy IDs:** Some stored wavelets have legacy document IDs containing `*`
  (e.g., `"8fJd77*2"`) that were generated before the prefix convention. These
  must be recognized as blip IDs. The classifier checks for `*` as a heuristic
  (see `IdUtil.isBlipId`).

- **Empty manifest:** A newly created conversation wavelet has an empty manifest
  (`<conversation/>`). Attempting to get the root blip before any blip has been
  appended returns null.

- **Deleted blips with live threads:** A blip with `deleted="true"` remains in
  the manifest as long as at least one reply thread exists. It is not usable
  for content operations, only as an anchor.

- **Unsigned HashedVersion comparison:** Unsigned versions (empty hash) sort
  before all hashed versions at the same version number (lexicographic byte
  comparison; empty array is the smallest).

- **Supplement version monotonicity:** The `DocumentBasedMonotonicMap` ensures
  that once a read version is written, a lower version cannot overwrite it.
  Concurrent writes always advance to the higher value.

- **User data wavelet address derivation:** The UDW domain is the participant's
  domain (not the wave's domain). A user from a federated domain has their UDW
  on their own domain's server.

---

## Open questions / ambiguities

1. **`WaveletId.serialise()` uses the legacy serializer, `WaveId.serialise()`
   uses modern.** This inconsistency (WaveId → `/`, WaveletId → `!`) is preserved
   in the Java code and appears in stored data and protobuf fields. Note this is
   the behavior of the *type's own* `serialise()` method only; other code paths
   choose a serializer explicitly — e.g. the supplement documents serialize
   embedded wavelet ids with `WaveletIdSerializer.INSTANCE`, which uses the
   **modern** `/` form (see section 6.3), not `WaveletId.serialise()`'s `!` form.
   The Go rewrite must handle both separators when parsing, and emit the correct
   one for each specific serialization context. Needs explicit test cases.

2. **ParticipantId normalization.** The address is lowercased on construction
   but no further validation of name or domain format is done. The comment
   `TODO: Check the validity of the username and domain part` is present in the
   Java. The Go rewrite should define whether to enforce stricter validation.

3. **HashedVersion hash algorithm.** The implementation uses the first 160 bits
   of SHA-256 (HASH_SIZE_BITS = 160), which is unusual. This value is an
   internal `@VisibleForTesting` override, suggesting it may have been changed
   for tests. Verify whether 160 bits is the canonical production value before
   implementing.

4. **Version zero URI encoding — RESOLVED.** The percent-encoding scheme used
   for the wavelet URI (which seeds the version-zero hash) is now precisely
   documented in section 7.3: it is the fixed `NOT_ESCAPED` set (ALPHA, DIGIT,
   and `:@!$&'()*+,;=-._~`) implemented by `URIEncoderDecoder`. The only
   platform-dependent part is the standard RFC 3986 UTF-8 `%xx` encoding applied
   to characters outside that set, which is well-defined. Notably `+` is emitted
   literally. The Go rewrite must reproduce exactly this set to interoperate with
   existing stored hashes.

5. **Thread IDs vs. blip IDs.** Thread IDs are (currently) identical to the ID
   of the thread's first blip. The Java code has a comment:
   `// TODO(user): stop using the blip id when wave panel and rusty doesn't rely on it`.
   The Go rewrite can treat thread IDs as opaque strings but should generate them
   consistently.

6. **Ghost blips.** The `g+` prefix designates ghost blips, which are not
   rendered. Their exact purpose (placeholders for deleted content in the OT
   history?) is unclear from the model layer alone. Needs investigation in the
   operational-transform spec.

7. **Supplement `m/presentation` (thread state location) — RESOLVED.** Thread
   collapsed/expanded state is stored in `PRESENTATION_DOCUMENT = "m/presentation"`
   (the `WaveletThreadStateCollection`), now listed in the section 6.2 table and
   described in 6.8. The detailed XML element/attribute layout of that collection
   is still worth pinning down against `WaveletThreadStateCollection` for the Go
   port.

8. **Conversation anchoring (`anchorManifestOffset`, `anchorVersion`,
   `anchorOffset`).** The manifest schema allows several anchor attributes that
   are not fully used by the current model layer. Their semantics for private
   reply positioning are unclear.

---

## Source references

| Path | Role |
|------|------|
| `model/id/WaveId.java` | WaveId type, serialization dispatch |
| `model/id/WaveletId.java` | WaveletId type, legacy serialization |
| `model/id/WaveletName.java` | Combined wave+wavelet identifier |
| `model/id/ModernIdSerialiser.java` | Modern `/`-separated serializer |
| `model/id/LegacyIdSerialiser.java` | Legacy `!`-separated serializer |
| `model/id/WaveIdentifiers.java` | Character validity for domains and IDs |
| `model/id/IdConstants.java` | Prefixes, well-known IDs, token separator |
| `model/id/IdUtil.java` | ID classification helpers (isBlipId, isConversationalId, etc.) |
| `model/id/IdGeneratorImpl.java` | ID generation algorithm, web-safe base64 |
| `model/id/SimplePrefixEscaper.java` | `~` escaping for token separators |
| `model/id/IdURIEncoderDecoder.java` | Wavelet URI format (for version-zero hash) |
| `model/id/URIEncoderDecoder.java` | Percent-encoding `NOT_ESCAPED` set (incl. literal `+`) |
| `model/id/WaveletIdSerializer.java` | Modern `/` serializer used by supplement docs |
| `model/wave/ParticipantId.java` | Participant address type |
| `model/wave/data/ReadableWaveletData.java` | Wavelet data interface |
| `model/wave/data/ReadableBlipData.java` | Blip data interface |
| `model/wave/data/WaveViewData.java` | Wave view data interface |
| `model/version/HashedVersion.java` | HashedVersion type and comparator |
| `model/version/HashedVersionSerializer.java` | HashedVersion string serialization |
| `model/version/HashedVersionFactoryImpl.java` | Hash computation (SHA-256, 160-bit truncation) |
| `model/version/HashedVersionZeroFactoryImpl.java` | Version-zero hash (URI bytes) |
| `model/waveref/WaveRef.java` | WaveRef type |
| `model/waveref/WaverefEncoder.java` | WaveRef URI encoding/decoding |
| `model/conversation/Conversation.java` | Conversation interface |
| `model/conversation/ConversationThread.java` | Thread interface |
| `model/conversation/ConversationBlip.java` | Blip interface (conversation layer) |
| `model/conversation/ConversationView.java` | Wave-level conversation view |
| `model/conversation/DocumentBasedManifest.java` | Manifest XML structure and parsing |
| `model/conversation/DocumentBasedManifestThread.java` | Thread XML encoding |
| `model/conversation/DocumentBasedManifestBlip.java` | Blip XML encoding in manifest |
| `model/conversation/Blips.java` | Blip document constants (BODY, HEAD, reply anchor) |
| `model/conversation/AnnotationConstants.java` | Well-known annotation key prefixes |
| `model/document/ReadableDocument.java` | Document tree traversal interface |
| `model/document/ReadableAnnotationSet.java` | Annotation query interface |
| `model/document/MutableAnnotationSet.java` | Annotation mutation interface |
| `model/document/operation/DocOp.java` | Document operation interface |
| `model/document/operation/DocInitialization.java` | Initialization-only op subset |
| `model/document/operation/DocOpCursor.java` | Op component visitor |
| `model/document/operation/AnnotationBoundaryMap.java` | Annotation boundary component |
| `model/document/operation/Attributes.java` | Immutable attribute map |
| `model/schema/conversation/ConversationSchemas.java` | Blip and manifest XML schemas |
| `model/schema/SchemaUtils.java` | Value predicates (integer/boolean) for schema constraints |
| `model/account/Role.java` | Role enum (FULL, READ_ONLY) |
| `model/account/Capability.java` | Capability enum (JOIN, INDEX, READ, ADD, WRITE) |
| `model/account/Roles.java` | Role assignment interface |
| `model/supplement/PrimitiveSupplement.java` | Supplement ADT interface |
| `model/supplement/WaveletBasedSupplement.java` | Supplement document names and XML structure |
| `model/supplement/ThreadState.java` | Thread collapse state enum |
| `model/util/Serializer.java` | `INTEGER`/`LONG` serializers (supplement version width) |
| `box/server/util/WaveletDataUtil.java` | `checkAccessPermission` (participant-only access gate) |
