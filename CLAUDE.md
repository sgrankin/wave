# CLAUDE.md

## What this repo is

Apache Wave (a.k.a. "Wave in a Box" / WIAB) — the open-source reference
implementation of **Google Wave**: a real-time collaborative editing system
built on **Operational Transformation (OT)**. A standalone wave server plus a
rich web client, with an XMPP-based federation protocol for server-to-server
sharing. The project is retired (Apache Attic); this is a frozen snapshot of the
Java codebase. ~2300 Java files.

## The goal of work in this directory

We are **reverse-engineering the project into retroactive specs** so it can be
**rewritten from scratch in Go**. The original Java is the source of truth for
behavior; we are not modifying it. Workflow:

1. Init this CLAUDE.md (done).
2. Write language-agnostic specs under `docs/` (subagents explore subsystems).
3. Write & review a Go porting plan.
4. Adjust the target architecture for a modern single-machine deployment
   (SQLite instead of MongoDB; updated auth; drop dead weight from the last 10+
   years).
5. Scaffold the Go project.
6. Port.

Track progress via the task list (TaskList).

## Original architecture (what we're porting)

- **Language/build**: Java 7, **Gradle** (`./gradlew`). Client is **GWT**
  (Java compiled to JS). **Guice** for DI. Config via Typesafe Config
  (`wave/config/reference.conf`).
- **Wire format**: Protocol Buffers over WebSocket/socket RPC. `.proto` files
  in `wave/src/proto/`. `pst/` ("Protobuf String Templating") and GXP are
  build-time codegen tools.
- **Federation**: server-to-server over **XMPP**, with X.509 / crypto signing
  of deltas (`wave/.../wave/crypto`, `wave/.../wave/federation`).
- **Auth**: JAAS-based password login + optional X.509 client-cert auth.
- **Storage**: pluggable — in-memory, file-based, or **MongoDB** — for deltas,
  accounts, and attachments.

## Where the code lives (`wave/src/main/java/`)

Core model & protocol (`org/waveprotocol/wave/`):
- `model/` (~524 files) — wave/wavelet/document data model, blips, OT operations.
- `concurrencycontrol/` (~50) — OT client/server concurrency.
- `federation/`, `crypto/` — XMPP federation + delta signing.
- `client/` (~755) — GWT web client (editor, rendering, UI).
- `communication/`, `common/`, `util/` — shared plumbing.

Server & app (`org/waveprotocol/box/`):
- `server/waveserver/` (~61) — core wave store & serving.
- `server/frontend/` — client-facing frontend.
- `server/rpc/` — protobuf RPC transport.
- `server/persistence/` (~30) — storage backends.
- `server/authentication/`, `server/account/` — auth & accounts.
- `server/robots/` (~58) — Robots API server side.
- `webclient/` (~79) — web client wiring/bootstrap.

APIs (`com/google/wave/api/`, ~108 files) — Robots & Gadgets / Data API.

Tooling: `org/waveprotocol/pst/` (codegen), `org/waveprotocol/examples/` (sample robots).

## Conventions for this effort

- **VCS is `jj` (Jujutsu)**, colocated with git. Load the `/jj:workflow` skill
  before any VCS operation or commit — including in subagent prompts.
- Specs go under `docs/` and must be **language-agnostic and detailed enough to
  reimplement without reading the Java**. Cite Java source paths as references,
  not as the spec itself.
- Platform is macOS (BSD userland).
