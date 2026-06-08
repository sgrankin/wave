# 10 — Fidelity gaps: original Wave vs the Go/TS port

Status: **active** (2026-06-08). Output of a 5-analyst parity review comparing the
frozen Java (the behavioral source of truth, `wave/src/`) against the Go (`internal/`)
+ TypeScript (`web/`) reimplementation, subsystem by subsystem. Deliberately-dropped
scope is NOT counted as a gap: XMPP federation, delta crypto/signing, the GWT
rendering layer, MongoDB, OAuth-for-third-party-robots.

Each gap: severity for a single-machine deployment, status (missing / partial /
divergent), and a one-line action. Ordered within each subsystem by severity.

## Top priorities (cross-subsystem)

Status key: ✅ shipped 2026-06-08.

1. ✅ **[OT] No DocOp validator → silent document corruption** — DONE. `op.Validate` +
   `wavelet.ValidateDelta` gate the submit path; reviewed (no bypass/false-positive).
2. ✅ **[Editor] Undo/redo entirely absent** — DONE. Transform-based per-blip undo
   (`web/src/wave/undo.ts` + clientcc), Cmd-Z/Cmd-Shift-Z; reviewed (OT-correct, 80k
   fuzz). (Task #42.)
3. ✅ **[Conversation] Read state is wavelet-granular, not per-blip** — DONE. Backend
   (`storage blip_read_state` + queryapi GET/POST `/api/read?blip=`) and client UI
   shipped: clientcc tracks each blip's last-modified version from REMOTE deltas;
   `<wave-blip>` paints an unread accent + dot; an IntersectionObserver dwell marks
   blips read on view; a floating pill jumps to the next unread. Reviewed (SHIP — no
   correctness/race/OT-safety defects; both read stores monotonic). (Task #52.) Note:
   blip/thread DELETE authoring — also a conversation gap — is likewise ✅ DONE via
   `conv.SetBlipDeleted` + the delete UI (a logically-deleted tombstone that keeps
   reply threads).
4. ✅ **[Server] A removed participant keeps receiving the live stream** — DONE (cut at
   the removal boundary; reviewed). Original text: until they
   disconnect (membership enforced only at Open/Resync) — an access-control leak.
5. **[Agent] One wave per socket, no create/discovery** — blocks the flagship "wave as
   shareable agent memory" use case (task #34); no structured-state primitive either.

## OT core (internal/op, internal/doc, web/src/wave) — overall fidelity HIGH
Transform (all 4 sub-transforms), Compose, Invert, the asymmetric annotation algebra,
attribute-conflict table, and wavelet-level transform are faithful near-line-for-line
ports; annotations are fully general (no key allowlist). Gaps:
- **high / missing — DocOp validator** (see #1 above). `DocOpValidator.java` +
  `DocOpAutomaton.java` (~1750 lines) absent; `op/component.go:NewDocOp` disclaims
  well-formedness "until the validator arrives" — it never did.
- **medium / missing — structural well-formedness** (element nesting/balance, no
  insert-inside-delete, no lone UTF-16 surrogates, retain-past-end). Folds into the
  validator port.
- **medium / partial — replaceAttributes/deleteElementStart old-attrs not checked**
  against the live element on compose (updateAttributes IS checked, and panics on
  mismatch). Same corruption class as the delete-content gap, lower frequency.
- low / divergent — char counting is by rune (Go/TS) vs UTF-16 code unit (Java);
  Go↔TS agree so convergence holds; only breaks Java byte-compat (out of scope).
- low — N-ary tree compose (`DocOpCollector`) left-folded instead (perf only); cross-port
  conformance is indirect (Go and TS each validate vs the Java reference, not vs one
  shared fixture run — pointing Go at `web/.../fixtures.json` would close it cheaply).

## Conversation / blip model (internal/conv, web conversation.ts) — structure faithful, supplement absent
Manifest/threads/inline-anchors/contributors/last-modified are a faithful port. The
per-user **supplement** is the systematic hole (spec §6 documents it fully):
- ✅ **per-blip read state — DONE** (#3 above). `storage/readstate.go` now also keys a
  `blip_read_state` table per (participant, wavelet, blip); the client computes unread
  from clientcc's per-blip last-modified vs the fetched read versions, marks-on-view,
  and offers next-unread nav.
- high / missing — participant-set / tags read state (separately-unread events).
- high / missing — blip / thread **deletion authoring** (the read path parses
  `deleted="true"` but nothing writes it; no tombstone-vs-remove logic). Users can't
  delete a blip.
- high / missing — archive / mute / "move to inbox" (inbox is just "all waves you're
  in"; no way to archive a wave out of it). Possibly a conscious scope cut — decide.
- medium / missing — folders; seen-version (distinct from read); thread collapse/expand
  persistence; pending-notification/notified-version (notif dedup is per-browser today).
- medium / partial — orphaned inline-reply threads (anchor text deleted) aren't
  reconciled against the manifest in a defined order; needs a tested contract.

## Server serving + concurrency control (internal/server, cc, transport) — overall fidelity HIGH
Server transform-to-head, version/hash validation, dup-elimination, hash chain,
snapshot+tail load, and fan-out faithfully reproduce OG; the client CC even tolerates
the ack/delta race OG assumes away. The idle-eviction + digest-projection additions
were sanity-checked: **no CC-invariant violation**. Gaps:
- high / partial — **single-signature reconnect**: resync sends only the latest
  confirmed version; OG carries a ladder (`getReconnectionVersions`/`reopen`) and can
  recover from an older acked point. Port falls back to a full reset (losing unacked
  edits) when the one point is gone. Small blast radius (synchronous commit, no log
  truncation except snapshot pruning).
- medium / divergent — **removed participant keeps streaming** (#4 above). Cut the
  subscription when a delta removes the subscriber, after delivering that delta.
- medium / divergent — with snapshots on, a submit/resync targeting a pre-snapshot
  version is `TooOld`/reset where OG would transform it forward (scoped to `-snapshot-every`).
- low — committed-vs-applied separation / `UnsavedDataListener` UI signal absent (benign:
  ack == durable commit here); progressive dup-elimination only checks the exact target
  version; persistence fsync is under the per-wavelet lock; dropped-subscriber relies on
  client reconnect; one wavelet per connection (no multiplexed view).
- **Load-bearing undocumented invariant** (worth a code comment): a caller must hold a
  subscription or not retain a `*WaveletContainer` — the eviction sweep can drop an
  unsubscribed container after `idleTTL`, and a stale retained reference would submit to
  an evicted instance. No such caller exists today.

## Client editor features (web/src/editor) — core writing path solid, formatting thin
Faithful/ahead: controlled-DOM convergence, live remote carets, inline-reply +
comment-sheet UX, floating selection toolbar, @mention/URL decoration. Gaps (by user value):
- **high / missing — undo/redo** (#2 above).
- high / divergent — **links are render-only** (regex on bare URLs); can't link arbitrary
  text, no `link/*` annotation, doesn't round-trip. OG: `LinkAnnotationHandler` + toolbar.
- high / missing — font color / highlight (`spanStyle()` would render them; only the
  command+UI are missing).
- high / missing — numbered lists + "Enter continues the list" (Enter always inserts a
  plain `<line>`, breaking out of a list).
- high / missing — **IME / composition input** (CJK + mobile dictation broken; the editor
  preventDefaults all beforeinput with no composition handling). Correctness, not polish.
- medium — font size/family; indent/outdent commands (model reads indent but can't set
  it); text alignment; rich/semantic paste (plain-text only today); spellcheck hard-off;
  H4; super/subscript; clear-formatting.
- low — read-only/permission-gated rendering (every viewer can type); find/replace;
  drag-drop; gadget insertion (needs the retired gadget server — out of scope).

## Agent / programmatic surface (internal/agent, agentgw) — clean push model, memory primitives missing
The gateway is architecturally cleaner than OG's HTTP-poll robots (event push + OT
intents). Gaps, ranked for the agent-first goal:
- high / missing — **lifecycle & discovery**: one wave per socket; no create-wave,
  list/search-my-waves, or leave-wave intent. Blocks "wave as memory" bootstrap (#34, #5).
- high / missing — **structured state**: no gadget-state revival and no datadoc — the
  agent has *no* per-wave key/value or private store, only prose blips. Best revived in
  reduced form (a state doc + `state.changed` event + `set.state` intent).
- medium / missing — annotation ops/events (agent reads/writes flat text only, though the
  OT layer supports annotations); title/tag ops + change events (snapshot lacks both).
- medium / partial — `blip.edited` fires per delta with no debounce/coalesce → floods an
  LLM harness one event per keystroke (the design called for it; unshipped).
- medium / partial — no remove-participant / remove-self intent (add-only lifecycle).
- low — no capability/subscription filter (all-or-nothing per wave); missing event kinds
  (blip-removed, self-added, **operation-error** — a failed intent is fire-and-forget with
  no in-band failure signal for the harness to retry); no agent-token request/response
  channel (queryapi/profileapi/playbackapi are human-cookie-auth only — bridging them to
  agent tokens is cheap and serves discovery).

## Notes
Most gaps are implementation gaps against an ALREADY-FAITHFUL spec (`docs/specs/`), so
closing them is build-to-spec, not re-derivation. Several "missing" inbox/organization
features (archive, folders, multi-conversation anchoring) may be deliberate
single-machine scope cuts — flag for an explicit product decision rather than assuming.
