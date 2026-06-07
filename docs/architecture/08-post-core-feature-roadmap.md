# 08 — Post-core feature roadmap

Status: **active** (2026-06-07). The functional core + QA/styling pass are done
(see [05-roadmap](05-roadmap-and-working-agreement.md), the shipped batches, and
`docs/qa-styling-backlog.md`). This roadmap is the prioritized "what's worth building
next" derived from a 7-area feature-gap audit of the original Google Wave vs our
Go/TS reimplementation (93 gaps; full result archived in the session transcript).

Scope is unchanged: a **modern single-machine** Wave. We deliberately dropped XMPP
federation + delta signing, the GWT client, MongoDB/multi-datacenter, and Google-infra
bits — those are **not** on this roadmap (re-adding them is out of scope).

Prioritization = user value × (1/effort) for a small-team collaborative editor.

## Tier 1 — build now

### 1a. Flagship: live remote carets / selections  (effort L — the headline)
Wave's signature feel: see each peer's caret (a colored bar at their rune offset)
and selection, live. Today presence is blip-granular only (a badge + a 2s "typing"
window — `07-presence.md` §5 deferred the pixel-exact caret). The hard part is owned
code we already have: `blip-view.ts` `offsetToDom()` + a robust offset↔DOM rune
mapping. Plan: extend the presence wire with `{blipId, anchor, focus}`; the focused
`<blip-view>` renders an absolutely-positioned colored caret (+ faint range highlight)
at `offsetToDom(focus)`, repositioned on `updated()` / scroll / resize.
**Known risk:** presence carries *raw* offsets (not OT-transformed — deliberately, to
keep presence off the convergence path per `07-presence.md` §1), so a remote offset
goes stale on every local edit. v1 accepts brief staleness (peer re-publishes within
the throttle window) and clamps to the current blip length; it must NOT OT-transform
presence. Prerequisite (cheap, also Tier 1): publish the focused blip **on focus**,
not only while typing (`wave-conversation.ts markTyping` is currently the only
`setLocal` caller), so idle readers still show "who is where". Load-bearing (touches
the caret invariant) → design-doc-first + clean-context review.

### 1b. Quick high-value wins (S effort each — a follow-on batch)
- **Remove participant** from the roster (the op already round-trips; only the UI 'x'
  is missing; self-removal = "leave wave").
- **Logout** — we mint session cookies but have no `/logout` route or UI; a shared
  machine can't switch users.
- **Online backup** — a `waved backup <path>` subcommand (SQLite `VACUUM INTO`);
  today the single .db is the only copy and copying it live risks a torn backup.
- **Agent reply / inline-reply intent** — the top extensibility gap; an agent can
  only append to a thread, not reply to the blip that mentioned it (conv builders
  already exist; only the intent wiring is missing).
- **Honest reconnect/offline indicator** — the transport reconnects silently while
  the status bar still says "connected"; surface offline/reconnecting/error.
- **Attachment upload size limit** — `http.MaxBytesReader` + `-attach-max-bytes`;
  today any participant can fill the disk.
- **Editor formatting** (model already supports these via style/line annotations):
  underline, strikethrough, ordered lists, indent/outdent, and re-test `spellcheck`.

## Tier 2 — build later
- **Blip delete** (M) — misposts are currently permanent (manifest honors `deleted`
  but there's no authoring path/UI).
- **Blip-level read/unread + "new since last read"** (L) — we track one read version
  per wavelet, so unread is wave-granular only.
- **Blip byline / edit attribution** (M) — the Go model tracks author/contributors but
  the TS client drops blip metadata (`clientcc.ts`).
- **@mention autocomplete picker** (M) and **link-insertion UI + persisted link
  annotation** (M) — pieces exist (profiles/contactSuggestions; style annotations).
- **Archive / mute waves** (M) + **mark-as-unread** (S) + **notification controls**
  (per-wave mute + global toggle, needs a small per-user prefs store) (M).
- **Agent discovery/inbox** (M) and **wave creation by an agent** (M) — unlock
  proactive bots (CI→wave, standup bot).
- **Bound the in-memory wavelet cache** (M) — `WaveMap` never evicts (a slow leak;
  original used a sized/expiring cache).
- **Text/background color** (M), **HTML paste fidelity** (L), **richer profiles**
  (avatar upload, status) (M).

## Explicitly skipped (not worth it for this target)
Gadgets / embedded mini-apps; tables; password-login UI (proxy-auth covers real
deploys, dev login covers local); multi-account switching; i18n/locale; roles/ACLs
beyond participant membership. (Plus the already-dropped federation/GWT/Mongo.)

## Build order
1b is a fast, low-risk value burst; 1a is the marquee. We lead with **1a (live
carets)** as the flagship, then sweep 1b, then Tier 2 by value. Each feature: design
note if load-bearing → implement → browser-verified e2e guard → clean-context review →
commit/push per unit.
