# 09 — Readiness backlog (autonomous ship push)

Status: **active** (2026-06-08). Output of a 4-analyst readiness audit (Wave fidelity,
UI polish across iPhone/iPad/desktop, test coverage, auth-methods design) run as a
workflow, merged into one prioritized backlog. This is the working list for the "make it
ship-ready" push. Items are checked off as they land (commits on `main`).

## Done in this push
- ✅ Dev login page responsive (viewport meta, 16px inputs, touch targets).
- ✅ Comment sheet rebuilt for mobile (full-height, scroll-locked, keyboard-anchored).
- ✅ Blip author byline + comment-pill avatars (clientcc authorship tracking).
- ✅ `deleteInlineElement` (Backspace/Delete adjacent to a mid-text widget).
- ✅ Underline + strikethrough in the selection toolbar.
- ✅ Mobile + a11y polish: iOS input-zoom fix, sub-44px touch targets, aria-live on
  status/presence/errors, contenteditable focus ring, playback-bar wrap, comment-sheet
  unified on the coarse signal (dropped the 640px-vs-820px breakpoint disagreement).
- ✅ Access-control predicate test (CanAccess/IsParticipant against a real sqlite store).
- ✅ Auth methods (credential store + AuthMethod registry + MintPolicy + GitHub OAuth +
  OIDC w/ PKCE+nonce). Adversarially reviewed; hardened: closed an IdP account-takeover
  (MintIdP: returning users resolve the bound account, first logins uniqueness-check the
  derived address), atomic insert-only provisioning (no TOCTOU clobber), refuse minting
  the shared-domain participant from an IdP claim, opaque login-denied (no enumeration),
  tolerate string `email_verified`. Passkeys still deferred.
- ✅ **Bounded inbox memory** (was the critical core item): the inbox/search digest
  (title/snippet/creator/participants/version/time) is now projected into the index at
  commit time and served entirely from SQLite — the ~5s inbox poll no longer loads any
  wavelet, so it stops pinning every inbox wave in the WaveMap. Plus SQL-level recency
  ordering + limit, and an idempotent `wavelet_meta` column migration.
- ✅ **WaveMap idle eviction** (`WithEviction`/`-wave-cache-idle`, default 30m): an idle,
  unsubscribed container is reaped and reloads from the delta log on next access —
  bounding the only remaining cache growth (live editing). Subscribed/hot containers are
  never evicted (no split-brain).

## Auth (sequence; shared infra MUST precede the methods)
1. Credential store: `(method, subject) -> account` SQLite table + `storage.CredentialStore`.
2. `AuthMethod` registry + per-method routes in `cmd/waved`; `GET /auth/methods`; re-express
   dev/proxy as AuthMethods; refine `requireSafeAuthBind` per-method.
3. MintPolicy + chosen-address provisioning (the security boundary: github→@github,
   oidc→verified-email-else-sub@issuer, passkey/dev→chosen under -auth-domain).
4. GitHub OAuth (`x/oauth2`); 5. OIDC (`go-oidc/v3`, PKCE+nonce); 6. Passkeys/WebAuthn
   (`go-webauthn`, deferred); 7. coexistence/logout/linking seams + update `04-auth-model.md`.

## Fidelity / Go core (the only items touching the wave-serving core)
- ✅ **critical** Bound the WaveMap — DONE (see "Done in this push"): cached inbox-digest
  projection in the index + paginated/ordered inbox query (queryapi no longer loads
  wavelets) + idle TTL eviction of unsubscribed containers.
- **high** Per-blip read state (today wavelet-granular) → enables "next unread".

## UI polish (remaining, by priority)
- medium: resolve selection-toolbar-vs-comment-sheet top-pin collision on touch;
  contenteditable handled; remote-caret/avatar text contrast (luminance-picked fg);
  CSS design tokens (type scale, brand, greys) — prerequisite for dark mode.
- low: empty/edge states for the right pane; roster chip names recoverable on touch;
  add-participant input flex (not fixed 140px); reduced-motion gates; keep Comment visible
  in the coarse toolbar; dark mode (deferred, needs tokens).

## Tests (remaining)
- ✅ comment-sheet full-height/scroll/footer geometry under a coarse/touch context (browser e2e).
- ✅ inbox digest projection: ordering/limit/participant-aggregation + the `wavelet_meta`
  column migration (legacy→digest) + Open idempotency.
- ✅ auth: 64-way concurrent same-address provisioning (one winner); CreateAccount
  insert-only; shared-domain rejection; takeover 403 carries no enumeration detail.
- medium: remote-caret selection highlight + multi-peer/clamp paths; sqlite still-0% funcs
  (DeleteWaveletIndex, Checkpoint); attachapi setThumbnail 409/413 branches.

Full per-item detail (file:line, actions) is in the audit workflow result.
