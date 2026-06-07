# 07 — Presence (live awareness)

Status: **draft → building** (2026-06-07). Design for work item `#20`: ephemeral
awareness — who else is in a wave, who is typing, and which blip they are focused
on. Presence is **transient**: not an OT operation, not versioned, not persisted,
not part of the hash chain. It is a separate realtime channel so it cannot perturb
the convergence-critical delta path.

References: `internal/transport` (the OT WebSocket, for the auth/membership pattern to
mirror), `internal/auth` (`Service.Middleware`, `ParticipantFrom`),
`internal/server.WaveletContainer.HasParticipant` (membership), the browser
`<wave-conversation>`/`<wave-blip>` components.

## 1. Why a separate channel (not the OT protocol)

The OT transport is framed-CBOR with strict version/hash continuity; a dropped or
reordered frame there is a correctness bug. Presence is lossy by nature (a stale
"typing" just expires). Folding presence into the OT stream would couple a lossy,
high-frequency signal to the convergence path and risk the delta channel. So
presence gets its **own WebSocket endpoint** (`/presence?wave=<name>`), its own hub,
and a plain-JSON wire — completely decoupled from the delta channel. The
[Phase 8 transport decision](../specs/.. ) (WebSocket + framed-CBOR) is unchanged;
this adds a parallel, independent socket.

## 2. Model

- An **identity is the session's authenticated participant** (from the auth
  middleware) — never trusted from the client payload, exactly like delta authorship.
- Presence is **per (wave, participant)**, last-write-wins, with no history.
- **Liveness by connection:** a participant is "present" while they hold a presence
  socket for the wave. On disconnect the hub broadcasts their departure. A half-open
  socket (laptop sleep, NAT drop) is reaped by a **server keepalive ping**: an
  unanswered ping closes the socket, the read loop returns, and the hub broadcasts the
  departure — so a vanished peer is not shown "present/typing" forever.

## 3. Wire (JSON, both directions)

Client → server (the client's own state; participant is ignored if sent — the server
stamps it):
```
{ "typing": bool, "blipId": string }   // blipId: the blip the user is focused on ("" = none)
```

Server → client (one per other participant's change, and a snapshot on join):
```
{ "participant": "addr", "typing": bool, "blipId": "...", "online": bool }
```
`online:false` is a departure (drop this participant). On join the server sends the
current roster's presence so a late joiner sees who is already active.

The client throttles its sends (coalesce rapid typing/selection changes to a few per
second) — the hub does not rate-limit beyond dropping a slow consumer.

## 4. Server — `internal/presence`

- **Hub**: `map[waveletName]map[*conn]` (a room per wave). `join` registers a conn and
  returns the current room snapshot; `broadcast` fans a participant's state to the
  rest of the room; `leave` deregisters and broadcasts `online:false`.
- **HTTP**: `/presence` mounted behind `auth.Service.Middleware` (so the participant
  is bound) and gated by `MembershipChecker` (only members of an existing wave). The
  handler upgrades to a WebSocket (text), joins the hub as the authenticated
  participant, sends the join snapshot, then loops reading the client's state and
  broadcasting it (participant stamped server-side); on close it leaves the room.
- Transient only: the hub touches no store, no container state, no OT.

## 5. Client + editor

- `web/src/wave/presence.ts` — a `PresenceClient` opening `/presence`, sending the
  local state (throttled) and exposing the remote roster (participant → {typing,
  blipId}) via a change callback; auto-reconnect like the OT client.
- `<wave-conversation>` owns the `PresenceClient`, updates local state on focus/typing
  (which blip is focused; typing = recent input), and re-renders on remote changes.
- **Rendering (v1, blip-granular):** show a typing indicator and a colored
  participant badge on the blip a remote participant is focused on ("● Alice"), reusing
  the Batch-5 avatar/color. This delivers "who is here / who is typing / where" without
  the contenteditable coordinate math.
- **Deferred (load-bearing, flagged):** a **pixel-exact remote caret** (a colored caret
  bar at the remote participant's rune offset within a blip) needs a coordinate overlay
  driven by the editor's offset→DOM mapping, updated on every edit/scroll/resize. It
  touches the caret-mapping invariant the editor is built around, so it is a separate,
  carefully-reviewed increment — v1 ships blip-granular presence (focused-blip + typing)
  which is the lower-risk, already-useful slice of "live carets / typing indicators".

## 6. Build order

`internal/presence` hub + `/presence` endpoint (tested) → mount in `cmd/waved` →
`presence.ts` client → `<wave-conversation>` wiring + blip badges/typing → browser e2e
(two clients see each other's typing + focused blip). Pixel-exact carets deferred (§5).
