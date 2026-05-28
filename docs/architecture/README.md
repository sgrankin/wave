# Architecture & Porting

How we take the behavior captured in [`../specs/`](../specs/) and build it as a
modern, single-machine Go server.

1. [01-target-architecture.md](01-target-architecture.md) — the system we're
   building: what to keep/change/drop from the original, the non-negotiable
   invariants, and the designs for storage (SQLite), wire/transport, auth, and
   operability.
2. [02-porting-plan.md](02-porting-plan.md) — the phased, dependency-ordered plan
   to build it, backend-first, with the original Java tests ported as a
   conformance suite.

Both were drafted from the specs, then put through an adversarial panel review
(plan soundness, Go architecture, spec fidelity, simplicity, completeness) with
the load-bearing ecosystem bets web-verified; the must-fix findings are folded
in. A short list of decisions left for the user is under "Open decisions" in
doc 01.
