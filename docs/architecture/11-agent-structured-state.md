# 11 — Agent structured state (design)

Status: **design / not yet implemented** (2026-06-08). The remaining piece of the
flagship "wave as shareable agent memory" goal (task #34, doc 10 §Agent). The wave
**lifecycle/discovery** primitives (create / list / leave) are shipped (agentgw REST
endpoints); this doc designs the **structured key-value state** primitive — the part
that lets a wave hold machine-readable memory, not just prose blips.

This is a design to review/steer before implementing: the value model and the OT
mapping have real forks. It does NOT change anything yet.

## Problem

An agent using a wave "as memory" today can only read/write **prose blips**. There is
no per-wave key/value store — OG Wave had per-participant **gadget state** and
**datadocs**, both deliberately dropped in this port (doc 10). So an agent cannot, e.g.,
record `status=processing`, `last_run=2026-06-08T…`, `summary={…json…}` as fields it can
later read back deterministically; it must parse them out of prose. That makes a wave a
chat log, not a memory store.

Goal: a small, OT-native, **shared** key→value document per wave that an agent (and a
human client, eventually) can read and write through the agent channel.

## Model: a `state` document in the wavelet

Add ONE reserved document to the conversation wavelet, alongside the existing
`conversation` manifest and the blip docs:

- Document id: `state` (a reserved id, like `conv.ManifestDocumentID = "conversation"`).
- Structure: a flat element list of key/value entries:

  ```xml
  <state>
    <e k="status" v="processing"/>
    <e k="last_run" v="2026-06-08T19:00:00Z"/>
    <e k="summary" v="{\"n\":3,\"ok\":true}"/>
  </state>
  ```

  - `k` is the key (a string). `v` is the value, **always a string** (see Value typing).
  - Entries are keyed by `k`; at most one entry per key (the writer enforces it).
  - Order is not semantically meaningful (it is a map); the writer keeps it sorted by
    `k` for deterministic diffs / snapshots.

This reuses the existing **DocOp / OT machinery** unchanged — `<state>` is just another
document, so it gets the same hash-chain, transform, validate (the DocOp validator),
snapshot, and persistence as blips. No new storage, no new transform code.

### Why a document (not a side table)

- It is **collaborative + convergent** for free (two agents, or an agent + a human,
  writing state concurrently transform correctly — the whole point of OT).
- It is **shared** (in the wave, visible to all participants) — matching "shareable
  memory". A private per-agent store would be the dropped per-user supplement; out of
  scope here.
- It rides existing snapshot/replay, so an agent reconnecting sees current state with no
  new code path.

## Operations (OT)

All three reuse the attribute/element builders already in `internal/conv` / `op`:

- **set(k, v)**: if an `<e k=K>` exists, `updateAttributes` its `v` (oldValue = current);
  else insert a new `<e k=K v=V/>` element at the sorted position. (Mirrors how
  `setLineMarkers` / the manifest builders emit `updateAttributes` / element inserts.)
- **delete(k)**: `deleteElementStart`+`deleteElementEnd` for the `<e k=K>` element,
  echoing its exact attributes (like `deleteInlineElement`).
- **read**: project the `state` doc into a `map[string]string` (walk elements, collect
  `k`→`v`). A pure function in `internal/conv` (e.g. `conv.ReadState(doc) map[string]string`)
  with a builder counterpart (`conv.SetStateValue` / `conv.DeleteStateValue`).

The `state` doc is created lazily on the first `set` (like a blip is created by its first
content op), or seeded empty (`<state></state>`) in `SeedConversation` — TBD (see Forks).

## Agent channel surface

Extend the gateway (`internal/agent`) symmetrically with the existing blip intents:

- Intent in — **`set.state`**: `{kind:"set.state", key:"status", value:"processing"}`
  → `conv.SetStateValue` op, submitted like any intent (rate-limited, validated).
- Intent in — **`delete.state`**: `{kind:"delete.state", key:"status"}`.
- Event out — **`state.changed`**: emitted when the `state` doc changes, carrying the
  new full state `{kind:"state.changed", state:{status:"…", …}}` (full map, not a delta —
  simplest for the harness; the map is small).
- Snapshot — the `wave.opened` event already carries a snapshot; include the current
  `state` map in it so an agent reads memory immediately on connect.

(REST mirror, optional follow-up: `GET/PUT /agent/waves/{wave}/state` for an agent that
wants to read/write state without holding a socket — same handlers, like the management
API. Not required for v1.)

## Value typing (FORK — decide before implementing)

`v` is a string on the wire and in the doc. Options for structured values:

1. **Strings only; the harness JSON-encodes** complex values into the string and decodes
   on read. Simplest; OT-trivial; the server never parses values. **Recommended for v1.**
2. Typed values (number/bool/string/json) with a `t` attribute. More ergonomic for the
   harness, but adds a type system and validation the server must enforce. Defer.

Recommendation: ship **#1** (opaque strings). It is the smallest correct surface and
matches "the agent owns the schema."

## Other forks / open questions

- **Seed empty vs lazy-create** the `state` doc: lazy-create (first `set` makes it) avoids
  an empty `<state/>` in every wave but means `read` returns `{}` when absent — fine.
  Recommend lazy-create.
- **Key/value size + count limits**: a malicious/buggy agent could bloat the state doc.
  Add a soft cap (e.g. ≤256 keys, ≤4 KB/value) enforced in the builder, rejecting
  oversize `set` (the agent submit path is already rate-limited).
- **Concurrent set of the same key**: OT `updateAttributes` transform resolves it (last
  writer in the serialized order wins the value; both converge). Confirm the attribute
  transform handles two `updateAttributes` on the same `v` (it does — same class as the
  manifest attribute edits).
- **Human-client surface**: out of scope for v1 (agent-first), but the same `state` doc is
  trivially readable by the TS client later (project it like the manifest).

## Implementation sketch (when approved)

1. `internal/conv`: `StateDocumentID` const; `ReadState(doc) map[string]string`;
   `SetStateValue(doc, k, v) []op.Component`; `DeleteStateValue(doc, k) []op.Component`;
   builder size-cap. Unit tests (set/overwrite/delete/read round-trips + cap).
2. `internal/agent`: `IntentSetState` / `IntentDeleteState` translation; a `state.changed`
   event derived from a `state`-doc delta; include state in the `wave.opened` snapshot.
3. Gateway wire types + tests (intent in → state doc changes; state.changed out).
4. Doc 10 + this doc: mark shipped.

Estimated one feature-sized change, fully OT-native and testable with the existing
patterns — no new transform/storage/transport code.
