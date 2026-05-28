# Apache Wave — Retroactive Specs

These documents reverse-engineer the Apache Wave (Google Wave) Java reference
implementation into **language-agnostic specifications**, detailed enough to
reimplement the system from scratch (target: Go) **without reading the original
Java**.

The Java source is the source of truth for *behavior*. These specs describe that
behavior; they cite Java paths as **references**, never as a substitute for the
spec itself. If a reader would have to open the Java to understand what to build,
the spec is incomplete.

## Reading order

1. [00-overview](00-overview.md) — system concept, glossary, how the pieces fit.
2. [01-data-model](01-data-model.md) — waves, wavelets, blips, documents, IDs.
3. [02-operational-transform](02-operational-transform.md) — operations, transform, compose. **The core.**
4. [03-concurrency-control](03-concurrency-control.md) — client/server OT protocol, versions, deltas.
5. [04-wire-protocol](04-wire-protocol.md) — protobuf messages, RPC, websocket framing.
6. [05-storage-persistence](05-storage-persistence.md) — delta/account/attachment stores, on-disk formats.
7. [06-server-architecture](06-server-architecture.md) — waveserver, frontend, request flow, lifecycle.
8. [07-federation](07-federation.md) — XMPP federation, remote wavelets, delta signing/crypto.
9. [08-authentication-accounts](08-authentication-accounts.md) — login, sessions, accounts, participants.
10. [09-robots-gadgets-api](09-robots-gadgets-api.md) — Robots API, Data API, gadgets, events.
11. [10-web-client](10-web-client.md) — client architecture, editor, rendering, client-side OT.
12. [11-search-indexing](11-search-indexing.md) — search backends and indexing.
13. [12-attachments-media](12-attachments-media.md) — attachments, thumbnails, media.

## Spec template

Every spec file MUST follow this structure (omit a section only if truly N/A,
and say so):

```markdown
# <NN> — <Subsystem Name>

## Purpose & scope
One paragraph: what this subsystem does and where its boundaries are.

## Concepts & glossary
Define every domain term used. A reader new to Wave should be able to follow.

## Data structures
The types/entities, their fields, types, invariants, and relationships.
Use language-neutral descriptions (or pseudo-structs), not Java classes.

## Algorithms & behavior
Step-by-step behavior, state machines, and the **invariants** that must hold.
This is where the real work is — be precise about ordering, edge cases,
concurrency, and error handling.

## Wire / storage formats
Exact serialization: protobuf message shapes, JSON, byte framing, DB schemas.
Enough to interoperate with the original at the bytes level if relevant.

## Interfaces / APIs
Public surface: RPCs, methods, events, extension points — signatures + contracts.

## Edge cases & failure modes
What happens on malformed input, conflicts, partial failure, version mismatch.

## Open questions / ambiguities
Anything you could not determine from the source, or that needs a human decision
for the Go rewrite. Be honest here — flag, don't paper over.

## Source references
Key Java files/packages this spec was derived from (path + one-line role).
```

## How these specs were verified

The specs were drafted by subsystem and then **adversarially reviewed against the
Java**, not just self-checked:

1. Per-spec reviewers and cross-cutting consistency checkers re-read each spec
   against the cited Java source, flagging factual errors, blocking gaps,
   internal contradictions, and cross-spec inconsistencies (shared facts like
   version arithmetic, the history-hash computation, ID serialization, the
   identity/access model, and the JSON wire encoding).
2. Every flagged finding was handed to an independent skeptic that defaulted to
   *refute* and only confirmed what it could substantiate from the source — so a
   chunk of speculative findings were dropped.
3. Confirmed corrections were applied, then the previously-inconsistent shared
   facts were **re-verified** across all specs that mention them until they
   converged.

Residual unknowns that could not be resolved from the source are recorded in each
spec's **Open questions** section — treat those as decisions for the porting plan,
not as settled behavior.

## Conventions

- Audience: an engineer implementing this in Go who has never seen Wave.
- Prefer diagrams-as-text (ASCII / mermaid), tables, and pseudo-code.
- Call out **invariants** explicitly — these are the contracts the rewrite must
  preserve.
- When the Java does something for legacy/GWT/Java-specific reasons that a Go
  rewrite need not replicate, note it under "Open questions" rather than
  baking it into the spec.
- Keep "what it does" separate from "how Java happens to do it."
