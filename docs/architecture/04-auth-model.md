# 04 — Authentication & Identity Model

Status: **draft** (2026-06-06). The target identity/auth model for the Go server,
deepening the high-level seam in [01-target-architecture.md](01-target-architecture.md)
§Authentication. Scope: how a request becomes a verified participant, how many
authentication methods coexist, how external credentials map to Wave identities,
and what's enforced for wavelet access. The first increment (work item `#10`,
wiring real auth + access control into the browser server) builds a small slice of
this; the rest is designed here so that slice doesn't paint us into a corner.

References the implementation: `internal/auth/{auth.go,session.go}` (the existing
`Service`/`Provider`/`Provisioner`/`Sessions`, currently unwired in `cmd/`),
`internal/transport/websocket.go` (the `identify` seam on the WS handler),
`internal/storage` (`AccountStore`), `internal/attachapi` (the `AccessChecker`
pattern). Written after a design discussion about supporting trusted-header/tsnet,
OIDC, passkeys, GitHub, account linking, multiple identities, and magic-link email.

## 1. Principle: identity is an address

A Wave identity is a **`ParticipantId` = `name@domain`** — load-bearing across the
data model (wavelet participants, delta authorship), storage, and access. This does
not change. "Everything is an address" is the invariant the auth layer serves: no
matter how you authenticate, you act in Wave **as an address**.

The auth layer's whole job is therefore: *take some proof of who you are, and
resolve it to a verified `ParticipantId`, then mint a session.* Everything below is
about doing that for many proof types without special-casing each through the
system.

## 2. Two kinds of authentication

The existing `Provider` interface (`Authenticate(r) → (ParticipantId, ok, err)`)
models **stateless, per-request** auth — read a verified identity off each request.
That covers some methods but not all. There are two shapes, and conflating them is
the main thing to avoid:

- **Stateless providers** — every request carries the proof: the **session cookie**
  itself, **trusted-header**, **tsnet** (`LocalClient.WhoIs`), **local/dev**. These
  fit the per-request `Provider` chain as-is.
- **Interactive login flows** — a challenge/redirect/callback dance that can't be a
  per-request header read: **OIDC** (OAuth2 code flow), **passkeys** (WebAuthn
  challenge/assert), **magic-link** (email a token, verify it). These are **login
  endpoints**, not chain providers.

Both shapes **converge on one point**: a verified `ParticipantId` → mint a session
cookie (`Service.SetCookie`). After that, every request is the stateless cookie
provider. So:

```
  stateless provider ─┐
                      ├─▶ verified ParticipantId ─▶ Provisioner ─▶ Service.SetCookie ─▶ session cookie
  interactive flow  ──┘        (resolution seam)     (policy)        (convergence)
```

`Service.Authenticate` is the **resolution seam**; `Service.SetCookie` is the
**convergence point**. Both already exist and are correctly factored — interactive
flows are *additive* (new endpoints that end at `SetCookie`), not a reshape. This is
the key reason the current arrangement scales to the full method list.

## 3. Credential ↔ identity separation (the one structural choice)

Where a method's proof **is** (or maps 1:1 to) an address, resolution is trivial:

| Method | Proof | → address |
|---|---|---|
| trusted-header | header value | the value (bare name + default domain) |
| tsnet | WhoIs login name | e.g. `user@github`, `user@tailnet.ts.net` |
| local/dev | configured | the pinned/asserted address |
| OIDC (verified email) | `email` claim w/ `email_verified` | that email |

These are **address-asserting** methods. They need no per-credential storage — the
address is the identity.

But three of the requested features are **credential-bound**, where the proof is
*not* an address and a prior binding is required:

- **passkeys** — a WebAuthn credential has no inherent address; registration must
  bind it to a chosen address, and login must look up *which* address this
  credential belongs to.
- **OIDC by `sub`** — the stable identifier is an opaque `sub` (the `email` may be
  absent, unverified, or mutable); keying on `sub` needs a `sub → account` binding.
- **account linking / multiple credentials per user** — one human with a passkey
  *and* a GitHub login *and* an email, all acting as the same identity.
- **multiple identities per user** — one human owning several addresses.

All of these need a **credential store** separate from the address:

```
  Account ──< owns >── ParticipantId(s)        (today 1:1; one-to-many enables multi-identity)
     ^
     └──< has >── Credential(method, subject, data)   (many-to-one; unique on (method, subject))
```

- `Account` — the human (today `storage.AccountStore`, keyed by `ParticipantId`).
- `Credential` — `(method, subject) → account`, plus method-specific data (WebAuthn
  public key + sign count; OIDC issuer+sub; …). Unique on `(method, subject)`.
- Login (credential-bound): flow yields `(method, subject)` → look up Credential →
  Account → its `ParticipantId`. None found + policy allows → register.

**This is the one piece worth reserving up front.** Doc 01 sketched "credentials in
the accounts JSON column" — a credential as a *field on the address-keyed account*.
That works for passkey-register-on-first-use *only* because that flow already knows
the address. It does **not** support: login-by-credential when you don't yet know
the address (OIDC `sub`), one credential vocabulary across methods, linking, or
merging — those need the credential as a **first-class row with its own index**, not
a column. Adding that table later, after accounts exist, is a migration; reserving
it now makes passkey/OIDC/linking *additive*. Identity schema is the expensive thing
to change, so this is the decision to make deliberately.

`#10` does **not** build the credential store (its providers are all
address-asserting). But it should not assume "address == account key forever" in a
way that blocks the one-to-many account→address or the credential index later.

## 4. Address-minting authority (security boundary)

A method may only assert addresses in a **namespace it controls**. This is a hard
security rule, not hygiene — `TrustedHeader` can assert *any* address, which is why
it is proxy-only (a forgeable header on a public bind is total bypass).

Generalize it: each method declares which address namespace it may mint, and the
service rejects assertions outside it.

| Method | May mint |
|---|---|
| local/dev | anything (dev only) |
| trusted-header | anything — **only on a proxy-exclusive listener** |
| tsnet | the tailnet's identities only |
| GitHub (OAuth) | `*@github` only |
| OIDC | `sub@<issuer-domain>`, or a `email_verified` address only |
| passkey | the address chosen at registration (bound by the credential) |
| magic-link | the email it verified |

**Fake-domain namespacing** (the Tailscale pattern) keeps "everything is an address"
while admitting external IdPs: a GitHub login `foo` becomes `foo@github`; a passkey
user picks `name@<local-domain>`; an OIDC `sub` can be `sub@<issuer>`. Pick the
domain conventions once. The rule above guarantees a GitHub login can never claim
`alice@example.com`.

## 5. Registration: derived vs chosen address

`Provisioner.RegisterOnFirstUse` (exists) auto-provisions an account + ParticipantId
for an unknown verified identity. That assumes a **derived** address (GitHub →
`foo@github`; verified email → that email): the address is known at first contact.

**Chosen-address** methods (passkeys — no inherent name) break that assumption: the
first registration must let the user **pick their Wave address** (subject to the
minting rule and uniqueness). So the provisioner needs a "needs-registration /
choose-address" state for those methods, distinct from the auto-derive path. Worth
modeling up front even though `#10` only uses auto-derive (dev) / no-provision.

## 6. Method catalog & status

| Method | Kind | Address mapping | Status |
|---|---|---|---|
| session cookie | stateless | (carries the resolved id) | **#10** (the convergence) |
| local / dev-trust | stateless | configured / asserted | **#10** (dev-permissive) |
| trusted-header | stateless | header → address (proxy-only) | near-term (provider exists) |
| tsnet | stateless | WhoIs → address | near-term (optional dep / build tag) |
| GitHub | interactive (OAuth) | `foo@github` | later |
| OIDC | interactive (OAuth2/OIDC) | verified email, or `sub@issuer` | later (credential store if by `sub`) |
| passkey / WebAuthn | interactive | chosen address (credential-bound) | later (credential store + chosen-address reg) |
| magic-link email | interactive | verified email | much later (needs outbound email) |
| account linking / merge | — | repoint credentials → one account | later (credential store is the prerequisite) |
| multiple identities | — | account → many addresses | later (schema reservation) |

Per-listener configuration makes "all of them" coherent (doc 01 §Authentication):
trusted-header/tsnet on a private bind, passkey/OIDC on a public bind, one session
layer.

## 7. Sessions

`internal/auth/session.go`: HMAC-signed token (`Sessions`), carried in an
HttpOnly + SameSite=Lax cookie (`Service.SetCookie`). Decisions:

- **Signing key must survive restart** (else every restart logs everyone out).
  Persist a generated key in storage (a settings/keys row; doc 01 reserves
  `keys/`), overridable by config. `NewSessions(key, ttl, clk)` already takes it.
- **TTL + rotation**: a TTL is set; key rotation (accept old, sign new) is a later
  refinement — note it, don't build it.
- The browser carries identity via this cookie on the WebSocket handshake (the
  upgrade is an HTTP request); the dev `?user=` query param goes away.

## 8. Access control (membership) — a separate concern

Authentication answers *who you are*; **access control** answers *what you may
touch*. Today only delta **authorship** is enforced (a submitted delta's author must
equal the authenticated participant; `transport/server.go` `handleSubmit`). There is
**no membership check** — any authenticated user can Open/read any wavelet by name.

A participation predicate (an `AccessChecker`, `CanAccess(participant, wavelet)
(bool, error)`) is wired into `transport.Server` and checked at **Open** and
**Resync** (`handleOpen`/`handleResync`, before subscribing). It reads the
wavelet's participant set via `WaveletContainer.HasParticipant` (which holds the
container lock — reading the live wavelet directly would race a concurrent submit).

- **Strict by default for production**, with a **dev-permissive override** (an
  allow-all checker for the dev/test path) so the local demo and the browser
  convergence tests keep working without an invite dance. Confirmed direction.
- **Open-or-create + server-side seeding**: first open of a non-existent wavelet
  creates it, adds the opener as the first participant, and seeds the conversation
  (manifest + root blip) **server-side**. An existing wavelet requires membership.
  This removes the client-side `maybeBootstrap` and **kills the cold-start race**
  (the container is created once under lock; a concurrent second opener sees it
  exists and goes through the membership check instead of writing a second
  manifest). The participants UI (`#13`) is the invite path that lets a second user
  in under strict mode.
- **Enforced once, at Open/Resync — not per delivered delta.** After a session is
  subscribed, membership is not re-evaluated, so a participant *removed* mid-session
  keeps receiving the live stream until they disconnect. Revoking an in-flight
  subscription on `RemoveParticipant` is a deliberate future refinement (it would
  close the removed participant's subscriptions on that commit), out of scope for
  the first slice. Both Open and Resync are gated, so a non-member cannot bypass
  the gate by reconnecting via Resync.

## 9. Incremental plan

`#10` builds the **address-asserting + dev** slice and the access/seeding layer.
**Status: shipped.** What landed:

1. `/socket` (and `/whoami`) mounted behind `auth.Service.Middleware` (identify =
   `auth.ParticipantFrom`): the cookie is verified before the WS upgrade. The dev
   `?user=` WS override and `-ws-user` flag are gone; the browser learns its own
   address from `GET /whoami` and rides the cookie on the handshake.
2. Login endpoints (`internal/auth/handlers.go`): dev `DevLoginHandler` trusts the
   submitted address (no password) + register-on-first-use → `SetCookie` (and
   serves an address-entry form when none is given); `LoginHandler` runs the
   provider chain (`TrustedHeader`) for proxy deployments. `sanitizeRedirect`
   restricts the post-login redirect to a local path (open-redirect defence).
3. Session signing key persisted via a new `storage.SettingsStore`
   (`auth.SigningKey` load-or-generate; ephemeral under an in-memory DB).
4. `AccessChecker` on `transport.Server` (`internal/transport/access.go`), checked
   at **both** `handleOpen` and `handleResync` (so a non-member cannot bypass the
   gate by reconnecting): `MembershipChecker` (strict) reads the wavelet's
   participant set; nil = dev-permissive (allow-all). The `-auth` mode selects it:
   `dev` (permissive) / `proxy` (strict).
5. Server-side open-or-create + conversation seeding (`conv.SeedConversation` +
   `WaveletContainer.SeedIfEmpty`, atomic under the container lock): the client
   `maybeBootstrap` is removed and the cold-start double-manifest race is gone.
   Gated by `-seed-conversations` (default on); the raw OT/CC interop test opts out.

**Deferred, kept additive by the seams above:** the credential store (§3), GitHub/
OIDC/passkey/magic-link flows (§2, all end at `SetCookie`), chosen-address
registration (§5), account linking/merge and multiple identities (§3), key
rotation, outbound email.

## 10. Open decisions

Resolved by `#10`:

- **Membership predicate location + denial response** — RESOLVED. The predicate is
  an `AccessChecker` on `transport.Server` (the transport layer owns it, since it
  holds the authenticated participant and the container), checked at `handleOpen`
  and `handleResync`. Denial is an **explicit "access denied" error frame** the
  client surfaces, not a silent not-found: wavelet names are shared deliberately in
  a single-machine collaborative app, so existence is not a meaningful secret and
  an explicit reason is friendlier. (Reversible: the checker could later return a
  not-found-shaped error without touching callers.)
- **`-auth` bundles auth-method + access-policy** — for the `#10` slice, one `-auth`
  flag picks a coherent bundle (`dev` = trust-any login + permissive access + plain
  cookie; `proxy` = trusted-header + strict membership + Secure cookie). Doc §8
  keeps them conceptually separate; the bundling is a deliberate simplification for
  the two deployments that exist today, not a constraint — per-listener / split
  flags can be added when a third shape appears.

Still open (future increments):

- Domain conventions for fake-namespaced identities (`@github`? `@passkey`? `sub@`
  vs verified-email for OIDC).
- Whether the credential store lands as a dedicated table or a typed JSON column
  with a secondary `(method, subject)` index (doc 01 leaned JSON; §3 argues the
  index is the load-bearing part either way).
- Default `RegisterOnFirstUse` per listener (open registration vs invite-only;
  `#10` uses register-on-first-use everywhere).
