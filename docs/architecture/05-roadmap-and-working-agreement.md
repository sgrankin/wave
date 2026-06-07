# 05 — Roadmap & Working Agreement

Status: **living** (2026-06-06). Two things: (1) the **working agreement** for how
the build proceeds autonomously, and (2) the **product roadmap** from the current
real-time editor to a fuller Wave-shaped product — including the **agent channel**
(LLM agents as participants). The task list (TaskList) is the live tracker; this doc
is the durable shape. Order within the roadmap is flexible; the working agreement is
the standing contract.

## Part 1 — Working agreement (autonomy contract)

The build runs **batch by batch, top-down**, without per-step approval.

**Proceed without asking:** decompose a batch into tasks; fan out worktree agents
for independent pieces; build; **verify in the browser** (the loop that catches what
unit tests miss); commit per logical unit; push; keep docs/memory/tasks current.
Make the obvious technical/UX calls. For a **load-bearing subsystem, write a short
design doc first** (like [04-auth-model.md](04-auth-model.md)) and proceed on it.

**Stop and surface only when:**
- a genuine **product fork** (e.g. "do we want gadgets at all?", a UX direction that
  changes the product),
- a **load-bearing design decision that's the user's** — write the doc, flag the
  call, and proceed on a recommended *reversible* default if the user is away,
- anything **destructive or outward-facing** (data loss, external services, email),
- the **roadmap itself** wants reshaping based on what's learned.

**Cadence:** push as you go; a short status at each **batch boundary**, not each
step. The user redirects anytime; the tasks are the living plan.

**Conventions** (also in CLAUDE.md): VCS is `jj` (load `/jj:workflow` before VCS
ops); macOS/BSD userland; never modify the original Java (source of truth); specs
under `docs/specs/` stay language-agnostic. `jj git push` needs the sandbox
disabled; `make check` = Go + web type/unit/component, `make check-all` adds the
browser e2e.

## Part 2 — Product roadmap

The kernel (OT/CC, the controlled collaborative editor, threading, formatting,
participants, transport, storage, observability) is **done**. The backend already
exceeds the UI: search/FTS5 + a read-index, attachments, the supplement (read-state)
model, the full hash-chained delta history. The batches expose and extend that.

| # | Batch | Task | Gist |
|---|---|---|---|
| 1 | **Identity & access** | #10 | Real login (session cookie), wavelet membership, server-side seeding. Foundational. Designed in doc 04. |
| 2 | **App shell & wave management** | #14 | Inbox/wave-list, search box, new-wave, navigation, two-pane layout. "One hardcoded URL" → an app. Wires the existing search/index. |
| 3 | **Collaboration completeness** | #15, #12 | Inline replies, read/unread (supplement), live presence (carets/typing), mentions. |
| 4 | **Rich content** | #16 | Inline attachments (drag-drop; attachapi exists), links, embeds. |
| 5 | **Profiles & contacts** | #17 | Display names + avatars, participant picker. Humanize the addresses. |
| 6 | **Signature features** | #18 | Playback (history scrubber — cheap, the history's already stored); the **agent channel** (Part 3); gadgets *evaluated*. |
| 7 | **Operability & deployment** | #19 | Health/metrics/logging, config, single-binary packaging, notifications. |

**Dropped / parked (#6):** federation (XMPP + delta signing — never shipped upstream;
no-op seams + on-disk proto schema kept), and the legacy robots/gadgets HTTP+OAuth
surface (replaced by the agent channel). The "modern single-machine, drop the
last-decade dead weight" lens: 1–5 are the real product, 6 is high-value-but-scoped,
7 is hardening.

## Part 3 — The agent channel (LLM agents as participants)

**Vision:** LLM agents join a wave as ordinary participants — a communication channel
between a wave and a hosted agent harness. Not a bespoke agent framework: the wave
*API/client* is the channel. Event notifications → agent → replies, at minimum.
Imagine an existing harness (openclaw, pi, a Claude Code plugin, …) gaining a "wave"
channel.

**The substrate already exists.** An agent is a **participant** (a `ParticipantId`,
e.g. `assistant@<domain>`; `storage.Account` already has a robot kind). The channel
is the **OT-aware client** we already built: `internal/transport.OptimisticClient`
(and its TS port). This is the "heavier client because of OT" the user named — an
agent **cannot** just POST text to an HTTP endpoint: to contribute it must submit
operations against the live version and converge (transform against concurrent
edits, optimistic apply, resync). The client owns all of that, so driving the client
gives an agent an OT-correct contribution surface for free.

**The loop:**
```
  connect as the agent participant
     → semantic activity events  (new blip / reply / @mention / edit by others)
     → agent harness (the LLM)    (decides what to do)
     → reply intent               (post a blip in a thread, edit, add a participant)
     → submit as ops via the OT client (versioned, transformed, convergent)
```

**A semantic event layer** sits between raw deltas and the harness: replica changes →
conversation events ("new blip by X in thread T", "you were @mentioned", "blip B
edited"), built on the conversation manifest reader + the supplement/read-state. This
is the *same* machinery as Batch 3 (read-state, mentions, presence) — an agent's
"unread / addressed-to-me" is a human's. So Batch 3 is a prerequisite for a good
agent channel, which is why this lives in Batch 6.

**Two deployment shapes:**
- **In-process (Go):** a harness written in Go embeds `OptimisticClient` directly.
- **Agent gateway (out-of-process):** a process that runs the OT client per (wave,
  agent) and exposes a *simple* bridge — activity events out, reply intents in — over
  stdio / WebSocket / SSE, so an **external** harness (openclaw, pi, claude-code
  plugin) plugs in with **no OT or Go knowledge**. The gateway translates a dumb
  "here's my reply text" into a correct OT submit. The harness design is open; the
  gateway's contract (events out, intents in) is the thing to pin first.

**Scope:** build the event→reply loop + the gateway bridge; do **not** port the
legacy robots HTTP/OAuth API. A dedicated design doc precedes the build (when Batch 6
is reached), covering the gateway protocol, the semantic event schema, agent identity
/ provisioning (an agent account + its membership), and rate/loop-safety (an agent
must not echo-amplify its own edits — the self-suppression the client already does
helps here).
