# 06 — Agent channel, playback, and the gadgets decision

Status: **draft** (2026-06-07). Design for Batch 6 ("Signature features", work item
`#18`): the **agent channel** (LLM agents as wave participants), **playback** (a
history scrubber), and the **decision to drop gadgets** and not port the legacy
robots API. This doc precedes the build, per the working agreement
([05-roadmap-and-working-agreement.md](05-roadmap-and-working-agreement.md) §"Part 3").

The agent channel is the load-bearing, novel piece and gets most of this doc.
Playback is a small read-only feature. Gadgets is a one-section decision memo.

References the implementation as it stands after Batch 5:
- `internal/transport/optimistic.go` — `OptimisticClient` (the OT-aware client).
- `internal/clientcc/clientcc.go` — pure, transport-agnostic client concurrency control.
- `internal/server/{container.go,fanout.go}` — the wavelet container: `SubmitFrom`,
  `Open`, `Subscribe`, `replayFrom`; nonce + fan-out `exclude` self-suppression.
- `internal/conv/` — conversation manifest reader/builders (`ReadManifest`,
  `appendBlipToThread`, `initialBlipContent`, mention/anchor readers).
- `internal/storage/{storage.go,accounts.go}` — `DeltasAccess` (delta history),
  `RobotAccount` (the agent account kind, already present).
- `internal/auth/auth.go` — `Provider` chain, `Service`, `Provisioner`.
- `internal/queryapi`, `internal/profileapi` — the `/api/*` JSON handler pattern.

---

## Part A — The agent channel

### A.1 Principle: an agent is a participant; the OT client is the channel

An agent is a **`ParticipantId`** (e.g. `assistant@agents.local`) with a
**`storage.RobotAccount`** (the account kind already exists — `accounts.go`). It joins
a wave like any human: it must be a wavelet participant, and to contribute it must
**submit operations against the live version and converge**. It cannot POST text to
an endpoint — Wave has no "append text" primitive; contributions are `DocOp`s on a
versioned document, transformed against concurrent edits. The OT client we already
built (`OptimisticClient` + `clientcc.CC`) owns all of that. So the design is not a
new agent framework — it is a thin **bridge** that turns the OT client's raw replica
changes into *semantic events* a harness can reason about, and turns a harness's
*reply intents* back into OT submits.

```
  wave server (containers)
        │  deltas in/out (OT, versioned, convergent)
   ┌────┴─────────────────────────────────────────┐
   │ agent runtime  (per wave×agent)               │
   │   OT client  ──►  EventExtractor  ──► events  │ ──►  harness (the LLM)
   │   OT submit  ◄──  IntentTranslator ◄── intents │ ◄──
   └────────────────────────────────────────────────┘
```

The agent runtime lives **inside `waved`** (single-machine deployment). The harness —
the thing that decides what to say — may be in-process (a Go callback) or
out-of-process (an external program over the *gateway protocol*, §A.5). Pinning the
gateway protocol (events out, intents in) is the primary goal: it is the contract an
external harness (openclaw, pi, a Claude Code plugin) codes against with **no OT or
Go knowledge**.

### A.2 Why the substrate is already enough (and what's missing)

Confirmed present (scoping, 2026-06-07):
- `OptimisticClient.Open() / Submit([]waveop.Operation) / SubmitWith(build) /
  BlipContent(id) / BlipIDs() / Version() / Updates() <-chan struct{}` — a complete
  drive surface. `SubmitWith` does read-modify-write against the live blip at submit
  time (needed for "append to the current end of a thread/blip").
- `clientcc.CC` is pure and transport-agnostic (`New/Edit/OnServerDelta/OnAck/Blip`),
  so an in-process client that drives a `server.WaveletContainer` directly via
  `SubmitFrom` (no WebSocket) is viable — there is already a `net.Pipe` dial seam.
- **Loop prevention is solved.** Each client session has a nonce; the container dedupes
  by nonce and *excludes the submitting subscription from fan-out*, so a client never
  observes its own deltas. An agent therefore never reacts to its own writes. This is
  load-bearing and we must not add a second, fragile dedup layer on top.

Missing (what Batch 6 builds):
1. A **semantic event layer**: raw `WaveletUpdate.Delta.Ops` → typed events
   (`BlipAdded`, `BlipEdited`, `ParticipantAdded`, `Mention`). Built on a cached
   manifest + blip snapshots, diffed frame-to-frame.
2. An **intent translator**: `post.blip` / `edit.blip` / `add.participant` → `DocOp`s
   via the existing `conv` builders, submitted through the OT client.
3. **Agent identity/auth**: how a non-browser client authenticates as the agent
   participant and is gated by membership.
4. The **gateway boundary**: serialize events out / intents in over a transport so an
   external harness can plug in.

### A.3 Agent identity, provisioning, and membership

- **Account.** An agent has a `storage.RobotAccount` keyed by its `ParticipantId`.
  Provisioned out-of-band (a CLI/admin action: "create agent `assistant@agents.local`
  with token T"), not on first login. Repurpose `RobotAccount.ConsumerSecret` as the
  agent's bearer token (or add a dedicated `Token`/`TokenHash` field — see decisions).
- **Authentication.** A new `auth.Provider` — `AgentToken` — reads
  `Authorization: Bearer <token>` (or an `X-Agent-Token` header), looks up the
  matching robot account, and resolves it to the agent's `ParticipantId`. It slots
  into the existing provider chain and mints the same session machinery, so the agent
  runtime's OT client authenticates exactly like a browser (cookie/session), just with
  a token instead of an interactive login. Tokens are stored hashed (reuse the
  `PasswordDigest` salted-hash, or a dedicated token hash).
- **Membership = access.** An agent only acts on waves it is a participant of. The
  existing `transport.MembershipChecker` already gates Open/Resync/submit by
  membership — no change. A human adds the agent via the existing add-participant flow
  (Batch 5's roster picker already lists known addresses); or an admin pre-adds it.
  An agent is never implicitly in every wave.
- **Self-mention / "addressed to me".** "An agent's *@mentioned / addressed-to-me* is
  a human's unread" (roadmap). The mention detection (Batch 3 render-time `@address`)
  becomes a first-class event so a harness can choose to act only when addressed.

### A.4 The semantic event layer (events out)

The `EventExtractor` sits between the OT client's replica changes and the harness. On
each replica change (`OptimisticClient.Updates()` tick, or the in-process container
subscription), it diffs the new state against its cached view and emits typed events.
It is **agent-local and derived** — the canonical truth is still the op log; events
are a convenience projection (so a buggy extractor can never corrupt a wave).

Event taxonomy (v1), each carrying `wave`, `version`, and a monotonic `seq`:

| Event | Fields | Source |
|---|---|---|
| `wave.opened` | participants, blips: [{id, threadId, author, text}] | initial snapshot — the agent's starting context |
| `blip.added` | blipId, threadId, author, text | manifest gained a blip |
| `blip.edited` | blipId, author, text | blip content changed (debounced — coalesce a typing burst) |
| `participant.added` | participant, by | addParticipant op |
| `participant.removed` | participant, by | removeParticipant op |
| `mention` | blipId, author, target | `@address` appears in added/edited text (target may be the agent or anyone) |

Built on `conv.ReadManifest` (manifest structure) + blip-content reads (the same
readers the editor and queryapi use). `blip.edited` is **debounced**: char-by-char OT
deltas would otherwise flood the harness; coalesce by `(blipId)` over a short window
and emit the settled text. Authorship comes from the op context (`waveop` creator).

### A.5 Reply intents (intents in) and the gateway protocol

The harness sends back **intents** — high-level, OT-free requests. The
`IntentTranslator` turns each into ops via the existing `conv` builders and submits
through the OT client (`SubmitWith` for read-modify-write so it lands at the live
version):

| Intent | Fields | Translation |
|---|---|---|
| `post.blip` | threadId (default root), text | `appendBlipToThread` + `initialBlipContent` + set text — one delta |
| `reply.blip` | blipId, text, inline? | `replyToBlip` (new thread under blipId, thread id == new blip id) + `blipContentWithText`; if `inline`, also `insertReplyAnchor` in the parent body just before `</body>` — one delta |
| `edit.blip` | blipId, text | replace the blip body content op |
| `add.participant` | address | `addParticipant` op |

`reply.blip` is the inline-reply path: an agent that was @mentioned in a specific
blip can answer *that* blip (a reply thread on it) instead of appending to the root.
The new blip's id is also the reply thread's id and the inline `<reply>` anchor's id,
so the manifest mutation, the new blip content, and the anchor all agree.

The **gateway protocol** is one canonical JSON schema (the events of §A.4 out, the
intents above in), carried over a transport. The schema is the thing to pin; the
transport is a thin frame. Proposed framing: newline-delimited JSON, events and
intents distinguished by a `type` field, each event carrying `seq` so a harness can
detect gaps.

Transports (the schema is identical across them):
- **stdio** — `waved` spawns the harness as a child process, writes events to its
  stdin, reads intents from its stdout. Simplest for a local "agent plugin".
- **WebSocket** — `/agent/socket?wave=…` (token-authed): bidirectional JSON for a
  remote harness.
- (later) **SSE + POST** — events over SSE, intents via POST, for HTTP-only harnesses.

### A.6 Loop and rate safety

- **Echo:** solved by the client's nonce self-suppression (§A.2) — no agent-layer
  dedup.
- **Agent↔agent loops:** by default, an agent's runtime drops events authored by
  *other agents* (a `RobotAccount`-kind author), unless explicitly configured to
  engage. Prevents two assistants amplifying each other.
- **Rate limit:** per-(wave, agent) cap on intents/minute, and a minimum cooldown
  between an event and the agent's own reply, enforced in the runtime (not the
  harness). A hard cap on ops per intent.
- **Backpressure:** if the harness is slow, coalesce/drop superseded `blip.edited`
  events (keep only the latest per blip) rather than queueing unboundedly.

### A.7 Build plan (Batch 6, agent channel)

1. **Identity:** `auth.AgentToken` provider + agent-account provisioning (CLI/admin
   path) + token storage. Smallest first slice; reuses the session machinery.
2. **EventExtractor:** ops/manifest → typed events, with the debounce. Pure, unit-
   testable against synthetic delta sequences (no network).
3. **IntentTranslator:** intents → ops via `conv` builders, submitted via the OT
   client. Unit-tested by asserting the resulting manifest/blip state.
4. **In-process reference agent:** an "echo" agent (Go callback harness) that replies
   to a `mention` with a blip — proves the full loop end-to-end against a real server
   (a browser-less convergence test, like the existing Node↔Go interop tests).
5. **Gateway:** expose events/intents over WebSocket (and/or stdio); a reference
   external echo harness drives it. Browser-verify a human and an agent collaborating
   in one wave.

Each piece: built, fresh-eyes-reviewed, tested, committed, pushed — same cadence as
Batches 1–5.

### A.8 Decisions to confirm (load-bearing — defaults chosen so work can proceed)

These are the user's-call forks. The recommended default lets the build start; the
user can redirect when they return.

1. **Agent transport for v1.** *Default:* build the gateway on the **network**
   `OptimisticClient` over the existing loopback socket (battle-tested, already
   converges), with an in-process `LocalAgentClient` (direct `container.SubmitFrom`)
   as a later latency optimization. *Alternative:* go in-process first.
2. **Gateway wire transport to ship first.** *Default:* **WebSocket** (`/agent/socket`)
   — it matches the browser transport and supports remote harnesses; add **stdio** for
   spawned child harnesses next. *Alternative:* stdio first (simplest local plugin).
3. **Token storage.** *Default:* a dedicated hashed `Token` on `RobotAccount` (don't
   overload `ConsumerSecret`, which is legacy-OAuth-shaped). Zero-migration (JSON
   account record).
4. **Event taxonomy granularity.** *Default:* the six events in §A.4 with debounced
   `blip.edited`. *Open:* whether to emit presence/typing (deferred — Batch-deferred
   `#20`) or annotation-level events (deferred).

### A.9 Status — what shipped (2026-06-07)

The agent channel is **built and externally usable**, end-to-end tested (event
extraction, intent translation, in-process echo loop, gateway-over-pipes, and a real
WebSocket harness), then adversarially reviewed (4 dimensions → skeptic-verify: 23
confirmed, 0 refuted, no critical) and hardened. The §A.8 decisions resolved as:

1. **Transport for v1:** the agent runs **in-process** (`agent.LocalClient` over the
   live container, raw deltas via the container subscription, self-suppression via
   `SubmitFrom(exclude=ownSub)`). The external harness drives it over the gateway —
   so we got the in-process path *and* external reachability in one shape, without an
   OptimisticClient delta-hook.
2. **Gateway wire transport:** **WebSocket** (`internal/agentgw`, `/agent/socket`)
   shipped; the gateway core is transport-agnostic (`io.Reader/Writer`) so **stdio**
   is a trivial add when a spawned-child harness is wanted.
3. **Token storage:** v1 uses a **config token→agent map** (`agentgw.StaticAuth`, the
   `-agents` flag) rather than a hashed `Token` on `RobotAccount`. Simpler for a
   single machine; the hashed-on-account store is a clean later swap (the `Auth`
   interface already abstracts it). Tokens are header-only bearer secrets — serve over
   TLS.
4. **Event taxonomy:** the six events shipped (no debounce yet — `blip.edited` fires
   per applied delta; coalescing is a later refinement). `wave.opened` carries the
   connect-time snapshot.

**Intents shipped:** `post.blip`, `reply.blip` (sibling or inline reply to a specific
blip — closes the top extensibility gap so an agent can answer the blip that mentioned
it, not just append to the root), `edit.blip`, and `add.participant`.

**Loop-safety, as built:** self-suppression (an agent never sees its own writes) plus
a defense-in-depth **submit rate limiter** on `LocalClient`. The runtime does **not**
prevent higher-level loops (two agents mutually reacting); that is the harness's job
(the `Harness` doc says so). The §A.6 "drop other agents' events" rule is **not**
implemented in the runtime (it needs account-kind lookup) — left to the harness.

**Deferred / known limitations** (most overlap Batch 7 operability): an unbounded
container cache — `StrictMembershipChecker` denies non-members but still instantiates
an empty container per looked-up name (bound/evict in Batch 7); `add.participant` by
an agent is unrestricted (no per-agent allowlist) — acceptable while agents are
operator-configured/trusted; membership is checked once at connect, not re-checked
mid-session (matches the transport's once-at-Open model; ties into the
RemoveParticipant revocation work in doc 04 §8); the wire `seq` field from §A.5 is not
yet emitted (gap-detection deferred); blip text in events is read live (may be newer
than the event's version under concurrent edits — fine for a reactive harness, since
the mention *decision* is delta-accurate).

---

## Part B — Playback (history scrubber)

Read-only and mechanical: the delta history is already stored and the forward-apply
path already exists.

### B.1 Data path

- **`Container.StateAt(version uint64) (*wavelet.Data, error)`** (new, public): load
  the base (snapshot at ≤ version, or empty at v0) and `replayFrom` until the target
  version, returning the reconstructed `wavelet.Data`. Reuses the existing replay/apply
  logic — no new OT code. Errors if the version doesn't exist.
- **`internal/playbackapi`** (new package, mirrors queryapi/profileapi), mounted behind
  `auth.Service.Middleware` + membership:
  - `GET /api/playback/deltas?wave=…&from=&to=` → `[{author, version, timestamp, opCount}]`
    — lightweight delta digests for the scrubber timeline (extracted from
    `DeltaRecord`; no new codec).
  - `GET /api/playback/state?wave=…&version=N` → the rendered `SnapshotState` JSON the
    client can display read-only.
- **Access:** reuse `transport.MembershipChecker` — only participants can scrub.

### B.2 Client

A scrubber control in the conversation view: a slider over the delta timeline
(`/api/playback/deltas`), and on change fetch `/api/playback/state` and render the
conversation **read-only** at that version (reuse the existing read-side conversation
renderer; disable the editor). "Live" returns to the OT client. Keep it a distinct
read-only mode so it never submits ops.

### B.3 Deferred

Snapshot caching for nearby-version jumps; a per-blip / per-participant timeline index;
streaming large delta ranges as ndjson. None needed for a usable v1.

---

## Part C — Gadgets: drop. Robots: superseded, not ported.

**Gadgets — drop entirely.** Gadgets were OpenSocial/iGoogle-era embedded iframes
(`<gadget url=…>` elements + cross-iframe RPC + external gadget servers + OpenSocial
security tokens). That entire ecosystem is defunct; the container sandbox and token
model cannot be reproduced, and there is no modern gadget ecosystem to host. Gadget
support in the original is overwhelmingly **client-side rendering** (~32 Java files);
the server merely stores `<gadget>` state as document content and emits a
`GADGET_STATE_CHANGED` robot event. Decision:
- Treat `<gadget>` document elements as **opaque content** in the Go codec (they round-
  trip as unknown elements; the data model is unharmed).
- The client renders a non-interactive **placeholder** ("gadget not supported").
- No gadget types, no rendering, no OpenSocial. Revisit only if a concrete modern
  embed use case appears (unlikely).

**Legacy robots API — superseded by the agent channel, not ported.** The original
robots API is an HTTP JSON-RPC + OAuth surface (~58 Java files): passive robots
receive `EventMessageBundle` POSTs at a registered callback URL and return operation
lists; active robots POST signed `OperationRequest`s. The agent channel replaces this
with a single OT-native model — an agent is a participant on the OT transport, sees
ops in order (no missed-event/retry webhook semantics), and contributes via normal
versioned submits (no separate RPC endpoint, no per-robot OAuth). We therefore **do
not port** `RobotConnector`, `EventGenerator`, `ActiveApiServlet`, capabilities.xml
fetching, or the OAuth validation. What carries over conceptually: the *idea* of an
event taxonomy and capability filtering (reborn as §A.4 events and the runtime's event
filter), and the `RobotAccount` storage kind (already present). This matches the
roadmap's "do not port the legacy robots HTTP/OAuth API."

---

## Summary

Batch 6 ships, in order: **playback** (Part B — small, uncontroversial, first), then
the **agent channel** (Part A — identity → event extractor → intent translator →
in-process reference agent → gateway), recording the **gadgets/robots decision**
(Part C — drop/supersede, no code). The agent channel's load-bearing forks (§A.8) have
chosen defaults so the build can proceed AFK; they are the points to steer on.
