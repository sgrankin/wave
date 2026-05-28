# 11 — Search & Indexing

## Purpose & scope

This spec covers how the server answers the question "which waves is this user
in, and which of them match this query?". That includes:

- The **per-user wave view**: a data structure tracking which (wave, wavelet)
  pairs each participant belongs to, kept current as deltas arrive.
- The **query model**: the syntax accepted by `SearchProvider.search()`, how
  queries are parsed, filtered, and sorted.
- The three **search provider backends** (memory, Lucene, Solr) and what each
  indexes.
- The **indexing pipeline**: how wavelet events flow into index updates.
- The **wire surface**: the `SearchRequest`/`SearchResponse` proto messages used
  by the HTTP search endpoint (cross-reference [spec 04](04-wire-protocol.md)).
- The **digest**: the per-wave summary (title, snippet, unread count, blip
  count, participants) computed on the way out of every search call.

What is NOT covered here: full-text content storage (covered in
[spec 05](05-storage-persistence.md)), OT and delta application (spec 03), the
wave supplement model for read-state (partially covered in spec 01 and 10).

---

## Concepts & glossary

| Term | Meaning |
|---|---|
| **Per-user wave view** | The set `{(WaveId, WaveletId)}` that a given participant is currently a member of. This is the universe of waves eligible to appear in any search result for that user. |
| **WaveBus** | An in-process pub-sub bus that fires on every committed wavelet update. Indexers and the view-dispatcher subscribe to it. |
| **PerUserWaveViewBus** | A narrower bus that fires only the three events that matter to view tracking: `onParticipantAdded`, `onParticipantRemoved`, `onWaveInit`. |
| **PerUserWaveViewHandler** | The interface that both maintains the per-user view and answers queries against it. Implementations: `MemoryPerUserWaveViewHandlerImpl`, `LucenePerUserWaveViewHandlerImpl`. |
| **SearchProvider** | Executes a string query on behalf of a user and returns a `SearchResult`. Implementations: `SimpleSearchProviderImpl` (pairs with either memory or Lucene view handler), `SolrSearchProviderImpl`. |
| **Digest** | A one-wave summary included in a `SearchResponse`. Contains title, snippet, waveId, participants, last-modified time, creation time, unread count, blip count. |
| **WaveDigester** | Computes a `Digest` from a `WaveViewData` + the user's supplement (user-data wavelet). |
| **User-data wavelet (UDW)** | The per-user private wavelet holding the supplement (read/unread state, follow state). Wavelet ID: domain = the participant's domain, local ID = `user+<participantAddress>` (e.g. `user+alice@example.com`). The prefix is the literal string `user` (`IdConstants.USER_DATA_WAVELET_PREFIX`) joined to the address by the token separator `+` (`IdConstants.TOKEN_SEPARATOR`); the `~` character is the escape prefix in `SimplePrefixEscaper`, not a separator. See [spec 01](01-data-model.md). |
| **Shared domain participant** | A synthetic participant `@<domain>` (no username) whose presence on a wavelet makes it a "public" wave visible to all users in the `all` query. |
| **Inbox query** | Query containing `in:inbox` — restricts results to wavelets where the querying user is an explicit participant. |
| **All query** | Any query without `in:inbox` — includes waves where either the user or the shared domain participant is present. |
| **Snippet** | Up to 140 characters of text extracted from the most-recently-modified blip in the wavelet. |
| **NRT (Near-Real-Time)** | Lucene's `NRTManager` pattern: writes are available for search within 25 ms–1 s without a full index commit. |

---

## Data structures

### Per-user wave view

```
PerUserWaveView:
  user        : ParticipantId
  entries     : Multimap<WaveId, WaveletId>   // one wave → many wavelets
```

The view is a flat set of (wave, wavelet) pairs. A single wave can contribute
multiple wavelets (the root conversational wavelet, sub-wavelets, and the UDW).
The search layer always adds the user's UDW for each wave in the view before
filtering, to ensure unread-state data is available when building digests.

### Lucene index document (per-wavelet)

One Lucene document per wavelet. Fields:

| Field name | Stored | Indexed | Type | Content |
|---|---|---|---|---|
| `WAVEID` | yes | not-analyzed | string | `WaveId.serialise()` |
| `WAVELETID` | yes | not-analyzed | string | `WaveletId.serialise()` |
| `LMT` | no | not-analyzed | long-as-string | `wavelet.getLastModifiedTime()` |
| `WITH` | yes | not-analyzed | string (multi-valued) | One value per participant address |

The Lucene index is used solely as a fast membership lookup: given a user, find
all (wave, wavelet) pairs that have the user in their `WITH` field. No full-text
content is stored in the Lucene per-user-view index.

### Solr index document (per-blip)

One Solr document per *blip* within a conversational wavelet. Fields:

| Solr field | Type | Content |
|---|---|---|
| `id` | string | URI-encoded `WaveRef(waveId, waveletId, blipId)` |
| `waveId_s` | string | Serialized wave ID |
| `waveletId_s` | string | Serialized wavelet ID |
| `docName_s` | string | Blip document ID |
| `lmt_l` | long | Wavelet last-modified timestamp (ms since epoch) |
| `with_ss` | string[] | Participant addresses (exact-match field) |
| `with_txt` | text[] | Same participant addresses (analyzed, for fuzzy search) |
| `creator_t` | text | Wavelet creator address |
| `text_t` | text | Full plain-text of the blip (extracted from doc-op character events) |
| `in_ss` | string[] | Hardcoded to `["inbox"]` for all blips |

Non-blip documents (manifests, tags, user-data wavelets) are skipped during
Solr indexing.

### Query token types

All query tokens have the form `<key>:<value>`. Every token in the query string
must match one of these known keys — bare text search terms are **not
supported** by the simple/Lucene providers (the parser throws
`InvalidQueryException` on unrecognized tokens). Solr passes unrecognized
prefix-colon tokens through to Lucene's query parser against the `text_t` field.

| Key | Meaning | Multi-valued |
|---|---|---|
| `in` | Folder filter. Only recognized value is `inbox`. Absence of `in:inbox` = "all" query. | no |
| `with` | Restrict to wavelets containing this participant. `@` is a shortcut for the shared-domain participant. Bare username (no `@`) gets the local domain appended. | yes |
| `creator` | Restrict to wavelets whose root wavelet creator equals this address. | yes |
| `orderby` | Sort order for results. | yes |
| `id` | Filter by wave ID (parsed but not implemented in the filter logic of the simple provider). | yes |

### Sort orders (orderby values)

| Token | Meaning |
|---|---|
| `dateasc` | LMT ascending |
| `datedesc` | LMT descending (default) |
| `createdasc` | Creation time ascending |
| `createddesc` | Creation time descending |
| `creatorasc` | Creator address ascending |
| `creatordesc` | Creator address descending |

Default ordering when `orderby` is absent: LMT descending. The sort is always
made stable by appending a secondary sort on WaveId lexicographic order.

### Digest

```
Digest:
  title        : string    // first line of root blip; empty string if none
  snippet      : string    // up to 140 chars from newest blip; title prefix stripped
  waveId       : string    // API-serialized wave ID (legacy "wavy" format)
  participants : []string  // first 5 participant addresses (participant-set order)
  lastModified : int64     // ms since epoch; -1 if unknown
  creationTime : int64     // ms since epoch; -1 if unknown
  unreadCount  : int32     // number of unread blips per the user's supplement
  blipCount    : int32     // total blips in the root conversation thread (breadth-first)
```

The digest is computed per-user from the live wavelet data at query time — it is
not cached in the index.

The internal model (`com.google.wave.api.SearchResult.Digest`) has **no
author/creator field**. It holds a single `participants` list — the first up to
5 addresses from the conversational wavelet's participant set, in participant-set
order. The `author`/`participants` split (author = first participant, participants
= the rest) happens only at wire serialization; see [Wire / storage formats](#wire--storage-formats).

---

## Algorithms & behavior

### Startup indexing (remakeIndex)

When the server starts and the index does not yet exist (Lucene: directory is
empty), `AbstractWaveIndexer.remakeIndex()` runs:

1. `waveMap.loadAllWavelets()` — force-loads every wave into memory.
2. Iterate over all (WaveId, WaveletId) pairs from `WaveletProvider`.
3. For each wavelet, call `waveletProvider.getSnapshot()` (loads into memory),
   then call the backend-specific `processWavelet()`:
   - **Lucene**: delegates to `LucenePerUserWaveViewHandlerImpl.onWaveInit()`.
   - **Solr**: delegates to `SolrWaveIndexerImpl.onWaveInit()`.
   - **Memory**: no-op; the memory handler rebuilds on first access.
4. The base `remakeIndex()` ends after the loop. Note: although each backend
   defines a `postIndexHook()` (Lucene/Solr call `waveMap.unloadAllWavelets()`;
   Memory is a no-op), this hook is **never invoked** by `remakeIndex()` — it is
   dead code for all three backends. Consequently the wavelets force-loaded in
   step 1 stay **resident in memory** after indexing; memory is not freed. This
   is what the memory backend's lazy view-build relies on (see "Memory handler
   cold start"). A Go reimplementation should NOT add a post-indexing unload step.

**Invariant**: After `remakeIndex` completes, every wavelet that existed on disk
is represented in the index.

If the Lucene index directory already exists and is non-empty, `NoOpWaveIndexer`
is bound instead — the existing index is assumed correct.

### Live update pipeline

```
delta applied to wavelet
        │
        ▼
  WaveBus.waveletUpdate()
        │
        ├─► PerUserWaveViewDispatcher.waveletUpdate()
        │         │ scans delta ops for AddParticipant/RemoveParticipant
        │         │
        │         ├─► PerUserWaveViewHandler.onParticipantAdded(waveletName, user)
        │         └─► PerUserWaveViewHandler.onParticipantRemoved(waveletName, user)
        │
        └─► SolrWaveIndexerImpl.waveletUpdate()  [no-op; waits for commit]

delta persisted to disk
        │
        ▼
  WaveBus.waveletCommitted()
        │
        └─► SolrWaveIndexerImpl.waveletCommitted()
                  │ if wavelet.version == committed version:
                  └─► updateIndex(waveletData) → POST to Solr
```

For Lucene:
- `onParticipantAdded` → re-read wavelet from store → `updateIndex()`:
  delete-by-(waveId+waveletId), then add new document.
- `onParticipantRemoved` → re-read wavelet → search for existing doc → rebuild
  participant list without the removed user → `updateDocument`.
- `onWaveInit` → same as `onParticipantAdded` (full reindex of the wavelet).

All Lucene index writes execute on a dedicated `IndexExecutor` thread pool.
The `NRTManager` keeps the index readable within 25 ms of a write, reopening
the reader on a background thread with bounds `[MIN_STALE_SEC=0.025,
MAX_STALE_SEC=1.0]`.

For Memory:
- `onParticipantAdded` → if the user's view cache is present, add the entry.
- `onParticipantRemoved` → if present, remove the entry.
- The view is built lazily on first `retrievePerUserWaveView(user)` by scanning
  all in-memory wavelets for the user's participation. The cache expires 5
  minutes after last access.

### Search execution — simple provider (memory or Lucene view)

```
search(user, query, startAt, numResults):
  1. parseQuery(query) → tokensMap            // throws on invalid token
  2. isAllQuery = !tokensMap.containsKey(IN)
  3. withParticipantIds  = buildValidatedParticipantIds(WITH, domain)
  4. creatorParticipantIds = buildValidatedParticipantIds(CREATOR, domain)
  5. currentView = waveViewProvider.retrievePerUserWaveView(user)
     if isAllQuery: currentView += waveViewProvider.retrievePerUserWaveView(sharedDomainParticipant)
  6. ensureWavesHaveUserDataWavelet(currentView, user)
  7. for each (waveId, waveletIds) in currentView:
       for each waveletId:
         load wavelet from waveMap
         if isWaveletMatchesCriteria(wavelet, user, isAllQuery, withList, creatorList):
           add wavelet to WaveViewData for this wave
       if WaveViewData has at least one conversational wavelet: add to results
  8. results = sort(results, queryParams)
  9. return results[startAt : startAt+numResults]
  10. digester.generateSearchResult(user, query, results)
```

`isWaveletMatchesCriteria`:
- If the wavelet is the user's own UDW: always include.
- If not `isAllQuery`: wavelet must contain `user` in participants.
- If `isAllQuery`: wavelet must contain `user` OR `sharedDomainParticipant`.
- For each address in `creatorList`: wavelet creator must match.
- For each address in `withList`: wavelet participants must contain the address.

**Invariant**: Only wavelets with at least one conversational wavelet ID
(`IdUtil.isConversationalId()`) are included in search results. Waves with only
UDWs are silently dropped.

### Search execution — Solr provider

```
search(user, query, startAt, numResults):
  1. isAllQuery = query does not contain "in:" token (regex check, not parser)
  2. Build filter query (fq). Every fq begins with the Solr local-params prefix
     FILTER_QUERY_PREFIX = "{!lucene q.op=AND df=text_t}with_ss:" which switches
     the fq parser to the Lucene parser, sets the default boolean operator to AND,
     and sets the default field to text_t:
     if isAllQuery: fq = "{!lucene q.op=AND df=text_t}with_ss:(user OR sharedDomainParticipant)"
     else:          fq = "{!lucene q.op=AND df=text_t}with_ss:user"
     if query nonempty: fq += " AND (" + translateQueryTokens(query) + ")"
  3. Loop: query Solr /select?wt=json&sort=lmt_l+desc&fq=<fq>&q=<Q>
     collecting (waveId, waveletId) pairs until < ROWS results returned
  4. ensureWavesHaveUserDataWavelet(view, user)
  5. Re-verify membership via isWaveletMatchesCriteria (access control re-check)
  6. computeSearchResult(user, startAt, numResults, results)
  7. digester.generateSearchResult(user, query, results)
```

Token translation from Wave query to Solr:
- `in:` → `in_ss:`
- `with:@` → `with_ss:<sharedDomainParticipant.address>`
- `with:` → `with_txt:` (fuzzy/analyzed field)
- `creator:` → `creator_t:`
- Unrecognized text → passed through as Lucene query against `text_t`

The `df=text_t` in the FILTER_QUERY_PREFIX (above) is exactly what makes the
unrecognized-text passthrough work: bare terms in the fq are parsed against
`text_t` as the default field. Without this prefix the query would parse with
different semantics.

The base `Q` filter that every Solr query includes ensures only documents with
all required fields set are considered.

### Digest computation

`WaveDigester.build(participant, waveViewData)`:

1. Find the root conversational wavelet (ID matches `conv+root` prefix), or if
   absent, any other conversational wavelet.
2. Find the user's UDW within the wave view.
3. If no conversational wavelet: return an empty or unknown digest.
4. Build `ObservableConversationView` from the conversational wavelet.
5. Build `SupplementedWave` from the UDW (or a mock supplement if UDW absent).
6. Title: extract from first blip of root thread using `TitleHelper.extractTitle`
   (reads the `blip/title` annotation; falls back to first line of text).
7. Snippet: `Snippets.renderSnippet(wavelet, 140)` — finds the most-recently-
   modified blip, extracts its plain text, then appends text from blips
   referenced in the conversation manifest. Truncated to 140 chars. Title prefix
   is stripped from the snippet if present.
8. Participants: first 5 addresses from the conversational wavelet's participant
   list.
9. `lastModified`: max over all blips of `blip.getLastModifiedTime()`.
10. `unreadCount`: count of blips where `supplement.isUnread(blip)` is true.
11. `blipCount`: count of blips in breadth-first traversal of root conversation.

---

## Wire / storage formats

### Search proto (search.proto)

```protobuf
message SearchRequest {
  required string query      = 1;  // query string, e.g. "in:inbox"
  required int32  index      = 2;  // 0-based offset into result set
  required int32  numResults = 3;  // how many results to return
}

message SearchResponse {
  message Digest {
    required string title        = 1;
    required string snippet      = 2;
    required string waveId       = 3;  // API-serialized (legacy "wavy" format)
    required int64  lastModified = 4;  // ms since epoch (wire: [low,high] array, see below)
    required int32  unreadCount  = 5;
    required int32  blipCount    = 6;
    repeated string participants = 7;  // the participants after the first — internal
                                       // digest participants[1..], up to 4 entries.
                                       // The first participant is emitted in `author`.
    required string author       = 8;  // first participant of the internal digest
                                       // participant list (participants.get(0)); falls
                                       // back to the literal "nobody@example.com" if the
                                       // wavelet has no participants. NOT derived from any
                                       // wavelet-creator field.
  }
  required string query        = 1;
  required int32  totalResults = 2;  // pagination hint, NOT a true total (see below)
  repeated Digest digests      = 3;
}
```

The search HTTP endpoint is a JSON-over-HTTP GET request to `/search`. The
proto is serialized using the PST-generated JSON format (field numbers as keys
— see [spec 04](04-wire-protocol.md) for the encoding rules). The `waveId`
field uses the legacy API serialization (`wavy+<domain>!<localId>`), not the
modern URI format.

**`lastModified` wire encoding**: `lastModified` is an `int64` with no
int52-style annotation. The PST/Gson serializer encodes *every* `int64` as a
two-element `[lowWord, highWord]` JSON array (there is no int52 wire exception —
see [spec 04](04-wire-protocol.md)). Decode with `toLong(arr[1], arr[0])`. This
applies to `lastModified`; the other digest numeric fields (`unreadCount`,
`blipCount`, `totalResults`) are `int32` and serialize as plain JSON numbers.

**`author` / `participants` split (from `SearchServlet.serializeDigest()`)**:
the internal `com.google.wave.api.SearchResult.Digest` has no author/creator
field. `WaveDigester` builds a single `participants` list (the first up to 5
addresses from the wavelet's participant set, in participant-set order). At wire
serialization, `SearchServlet` splits that list: `participants[0]` becomes the
wire `author` field, and `participants[1..]` become the wire `participants` field.
If the internal list is empty, `author` is set to the literal
`"nobody@example.com"` and `participants` is empty. There is no wavelet-creator
lookup.

**`totalResults` is a pagination hint, not a true total**
(`SearchServlet.computeTotalResultsNumberGuess()`). The Data API does not expose
the true match count, so the server computes a heuristic guess:
- If the number of returned digests is `>=` the requested `numResults` (the page
  is full), `totalResults = -1` (`SearchService.UNKNOWN_SIZE`). The client
  interprets this sentinel as "more results exist beyond this page; total
  unknown".
- Otherwise `totalResults = index + numReturned` — the absolute position of the
  last result, signalling the end of the result set has been reached.

The `digests` list contains up to `numResults` entries starting at `index`.

### Lucene index on disk

Stored in a directory given by `core.index_directory` (default `_indexes`).
Standard Lucene 3.5 FSDirectory format. One document per wavelet. Opened with
`CREATE_OR_APPEND` — safe to reuse across restarts.

### Configuration (reference.conf)

```
core.search_type       : "memory" | "lucene" | "solr"   // default: memory
core.index_directory   : "_indexes"                      // lucene only
core.solr_base_url     : "http://localhost:8983/solr"    // solr only
```

---

## Interfaces / APIs

### SearchProvider

```
interface SearchProvider:
  search(user: ParticipantId, query: string, startAt: int, numResults: int)
    → SearchResult
```

Returns empty results (not an error) if the query is syntactically invalid or
the user has no waves.

### PerUserWaveViewProvider

```
interface PerUserWaveViewProvider:
  retrievePerUserWaveView(user: ParticipantId)
    → Multimap<WaveId, WaveletId>
```

### PerUserWaveViewBus.Listener

```
interface Listener:
  onParticipantAdded(waveletName: WaveletName, participant: ParticipantId)
    → Future<Void>
  onParticipantRemoved(waveletName: WaveletName, participant: ParticipantId)
    → Future<Void>
  onWaveInit(waveletName: WaveletName)
    → Future<Void>
```

### WaveIndexer

```
interface WaveIndexer:
  remakeIndex() throws WaveletStateException, WaveServerException
```

Called once at startup if the index is absent. Blocks until complete.

### WaveBus.Subscriber (for Solr)

```
interface Subscriber:
  waveletUpdate(wavelet: ReadableWaveletData, deltas: DeltaSequence)
  waveletCommitted(waveletName: WaveletName, version: HashedVersion)
```

Solr uses `waveletCommitted` to trigger indexing (not `waveletUpdate`) as an
optimization to avoid redundant updates.

---

## Edge cases & failure modes

**Query parse errors**: `QueryHelper.parseQuery` rejects any token that does not
match `<key>:<value>` where `<key>` is a known `TokenQueryType`. On parse error
or invalid participant address, `search()` returns an empty `SearchResult` (no
error propagated to client).

**Missing wavelet in WaveMap**: If a wavelet listed in the per-user view cannot
be loaded (e.g., concurrently deleted), it is silently skipped with a log
warning. The result set is smaller than expected but no error is thrown.

**Lucene index stale after crash**: The `NRTManager` writes to a single index
writer with `IndexWriterConfig.OpenMode.CREATE_OR_APPEND`. On restart the
existing index is used. If the index is corrupt or partially written, the server
will fail to open the `IndexWriter` at startup with an `IndexException`.

**Solr unavailable**: If the Solr HTTP request fails or returns non-200, the
search returns an empty result set (logged at WARNING). Indexing failures on
commit are also logged and silently dropped.

**Memory handler cold start**: The first call to `retrievePerUserWaveView(user)`
requires scanning all in-memory wavelets. If `waveMap` is not fully loaded, only
currently-loaded wavelets are visible. Startup ordering must ensure `remakeIndex`
(which loads all wavelets) completes before the server begins serving search
requests.

**Participant removal race**: The Lucene handler's `removeParticipantFromIndex`
reads the existing document, mutates the participant list in memory, then writes
back. If two removals race, the second may re-add a participant the first
removed. Both run on the single `IndexExecutor`, so this is not a real race in
the current implementation; but the pattern is fragile.

**`in:inbox` vs no `in:` — behavior difference**: With `in:inbox`, only wavelets
where the querying user is an *explicit* participant appear. Without it (the
"all" query), wavelets carrying the shared-domain participant also appear — even
if the requesting user was never explicitly added.

**UDW always included**: `ensureWavesHaveUserDataWavelet` adds the user's UDW
wavelet ID to every wave in the view before filtering. If the UDW does not
actually exist on disk, `buildSupplement` falls back to a mock supplement
(treating all blips as unread).

---

## Open questions / ambiguities

### SQLite-backed implementation assessment

The Go rewrite targets a single-machine SQLite store. Here is how each part of
the search contract maps:

**Per-user wave view** maps cleanly to a SQL table:
```sql
CREATE TABLE wave_participants (
    wave_id    TEXT NOT NULL,
    wavelet_id TEXT NOT NULL,
    user_addr  TEXT NOT NULL,
    PRIMARY KEY (wave_id, wavelet_id, user_addr)
);
CREATE INDEX wp_user ON wave_participants(user_addr, wave_id);
```
`retrievePerUserWaveView(user)` = `SELECT DISTINCT wave_id, wavelet_id WHERE user_addr = ?`.
Updates on `onParticipantAdded/Removed` are single-row INSERTs/DELETEs.
This is strictly simpler than the Lucene implementation and fully durable.

**Structured query filters** (`with:`, `creator:`, `in:inbox`) map to SQL JOINs
and WHERE clauses — no FTS needed. These can be implemented without SQLite FTS5.

**Full-text search** (Solr's `text_t` field) requires SQLite FTS5:
```sql
CREATE VIRTUAL TABLE blip_fts USING fts5(
    wave_id UNINDEXED, wavelet_id UNINDEXED, blip_id UNINDEXED,
    creator UNINDEXED, participants UNINDEXED,
    content
);
```
Blip text is extracted from doc-ops the same way Solr does it (character events
from `DocOp`). Since participant filtering is done in the SQL layer, FTS5 only
needs to handle full-text matching on `content`.

**Sorting** (LMT desc, creation time, creator) is a SQL `ORDER BY`. LMT must be
stored in the participants/waves table or a separate `wave_metadata` table.

**Digest computation** is not stored anywhere — it is computed at query time from
live wavelet data in all three Java implementations. The Go rewrite should do
the same: load the wavelet snapshot, extract the title/snippet/unread counts at
search time. Read state (unread counts) requires joining against the supplement
data stored in the UDW; this is currently opaque XML-in-a-wavelet.
A SQLite rewrite should consider storing read state explicitly in a table rather
than decoding it from the UDW document format.

**Cleanest reference implementation**: `SimpleSearchProviderImpl` paired with
`MemoryPerUserWaveViewHandlerImpl` is the simplest to follow — it's
straightforward in-memory filtering with no external dependencies. Replace the
in-memory Multimap with the SQL table above and the logic is unchanged.
The Lucene handler is the best reference for the indexing event pipeline
(`onParticipantAdded/Removed/onWaveInit`) because its async-executor pattern
translates directly to Go goroutines.

### Unresolved questions

1. **`id:` filter**: `TokenQueryType.ID` exists in the enum but the
   `isWaveletMatchesCriteria` logic in `SimpleSearchProviderImpl` never checks
   it. It is unclear if this was intended as a future feature or was removed.
   The Go rewrite should either implement it (filter by wave ID) or drop the
   token from the query language.

2. **`totalResults` semantics**: The proto field `totalResults` is NOT a true
   total and NOT the page size. `SearchServlet.computeTotalResultsNumberGuess()`
   computes a heuristic pagination hint: if the page is full (returned digests
   `>=` requested `numResults`), `totalResults = -1` (`SearchService.UNKNOWN_SIZE`),
   which the client reads as "more pages exist, total unknown"; otherwise
   `totalResults = index + numReturned`, the absolute position of the last result.
   The Data API deliberately does not expose the real match count. A Go rewrite
   must replicate this `-1` / `(index + count)` logic exactly, or deliberately
   decide to return a real count and document the divergence.

3. **Snippet freshness**: The snippet is computed from live wavelet data at query
   time, which is expensive for large result sets. The Solr provider stores
   full-text in the index but the Java code still re-derives the snippet from
   the live wavelet. The Go rewrite could cache snippets in the index.

4. **Read state and the UDW**: Unread counts depend on decoding the supplement
   model stored in the user-data wavelet document. This format is complex (see
   spec 01 and 10). A SQLite rewrite that stores read state in a dedicated table
   (`wave_read_state(user, wave_id, blip_id, read_version)`) would significantly
   simplify digest computation.

5. **Shared domain participant semantics**: The "all" query includes waves where
   `@<domain>` is a participant. The handling of this synthetic participant is
   checked in multiple places. A clean rewrite should make this a first-class
   concept with a single access-control function rather than the scattered checks
   in the current code.

6. **Search query language is strictly key:value**: Bare search terms cause an
   `InvalidQueryException` in the simple/Lucene providers. Only the Solr provider
   passes them through to Lucene query syntax. The Go rewrite should decide which
   behavior to standardize on.

---

## Source references

| File | Role |
|---|---|
| `waveserver/SearchProvider.java` | Top-level search interface |
| `waveserver/AbstractSearchProviderImpl.java` | Shared wave-view filtering and WaveViewData assembly |
| `waveserver/SimpleSearchProviderImpl.java` | Memory/Lucene search: query parsing, filtering, sorting |
| `waveserver/SolrSearchProviderImpl.java` | Solr search: query translation, HTTP calls, Solr field names |
| `waveserver/QueryHelper.java` | Query parsing, sort order definitions |
| `waveserver/TokenQueryType.java` | Enum of valid query token keys |
| `waveserver/WaveDigester.java` | Builds per-wave Digest from live wavelet + supplement |
| `waveserver/PerUserWaveViewBus.java` | Bus interface for view-update events |
| `waveserver/PerUserWaveViewDistpatcher.java` | WaveBus → PerUserWaveViewBus adapter (scans delta ops) |
| `waveserver/PerUserWaveViewHandler.java` | Combined view-handler interface |
| `waveserver/PerUserWaveViewProvider.java` | Read-only view-provider interface |
| `waveserver/MemoryPerUserWaveViewHandlerImpl.java` | In-memory view with 5-minute expiring cache |
| `waveserver/LucenePerUserWaveViewHandlerImpl.java` | Lucene-backed view: NRT index, field definitions, async updates |
| `waveserver/LuceneWaveIndexerImpl.java` | Startup indexer for Lucene |
| `waveserver/SolrWaveIndexerImpl.java` | Solr indexer: blip-level JSON docs, waveletCommitted trigger |
| `waveserver/AbstractWaveIndexer.java` | Startup remakeIndex loop (load all → iterate → processWavelet) |
| `waveserver/MemoryWaveIndexerImpl.java` | No-op startup indexer for memory mode |
| `waveserver/IndexFieldType.java` | Enum of Lucene field names |
| `waveserver/WaveBus.java` | Raw wavelet-update pub-sub bus |
| `server/SearchModule.java` | Dependency injection wiring for the three backends |
| `common/Snippets.java` | Plain-text extraction from wavelet documents |
| `persistence/lucene/FSIndexDirectory.java` | Lucene FSDirectory adapter |
| `persistence/lucene/IndexDirectory.java` | Directory abstraction |
| `proto/.../search/search.proto` | SearchRequest / SearchResponse wire format |
| `config/reference.conf` | `search_type`, `index_directory`, `solr_base_url` settings |
