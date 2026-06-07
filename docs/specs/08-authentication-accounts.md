# 08 — Authentication, Sessions & Accounts

## Purpose & scope

This spec covers everything between a user (human or robot) presenting
credentials and the Wave server treating an in-flight RPC as authorized for a
particular ParticipantId.  It includes:

- The **account data model**: what an account record contains and the two account
  types (human vs. robot).
- **Authentication mechanisms**: form-based password login, X.509 client-
  certificate auto-login.
- **Sessions**: how an authenticated HTTP session is established, persisted, and
  how the WebSocket/RPC connection maps back to a ParticipantId.
- **Account registration**: the self-service registration flow, admin account,
  welcome-wave side-effect, and lockdown options.
- **Authorization**: how wavelet read/write access is decided.

Out of scope: robot OAuth flow (spec 09), federation signatures (spec 07),
wire-protocol framing (spec 04).

---

## Concepts & glossary

| Term | Definition |
|------|-----------|
| **ParticipantId** | The canonical user identity: `localname@domain`. Defined in spec 01. Every account maps 1-to-1 with a ParticipantId. |
| **AccountData** | The persisted record for one account. Has exactly one subtype: HumanAccountData or RobotAccountData. |
| **HumanAccountData** | Account owned by a human. Carries a PasswordDigest and a locale string. |
| **RobotAccountData** | Account owned by a robot agent. Carries the robot's callback URL, OAuth consumer secret, capabilities, and a verification flag. |
| **PasswordDigest** | Salted SHA-512 hash of a password. Stored as (salt bytes, digest bytes). |
| **HttpSession** | Server-side session object (Jetty). Tied to a browser cookie (JSESSIONID). Stores the authenticated ParticipantId under the key `"user"`. |
| **SessionManager** | Server component that reads/writes the authenticated participant on an HttpSession and looks up sessions by token. |
| **ProtocolAuthenticate** | A protobuf message the client can send over WebSocket to bind the WebSocket connection to a session token (workaround for cookie-less environments). |
| **Shared-domain participant** | A synthetic ParticipantId of the form `@example.com` (no local part). Used as a wildcard: any wavelet that lists this address as a participant is readable AND writable (delta-submittable) by all authenticated users of that domain. |
| **Admin user** | A configured ParticipantId that has elevated privileges (e.g., can change passwords for other users via the PasswordAdmin robot). |

---

## Data structures

### AccountData (abstract)

```
AccountData {
    id          ParticipantId   // primary key; non-null
    isHuman     bool
    isRobot     bool
}
```

Exactly one of `isHuman` or `isRobot` is true.

### HumanAccountData

```
HumanAccountData : AccountData {
    passwordDigest  PasswordDigest | null   // null ⇒ password auth disabled
    locale          string | null           // BCP-47, e.g. "en"
    displayName     string                  // Go-rewrite addition; "" ⇒ unset
}
```

`passwordDigest` is null when the account was created without a password (e.g.,
auto-created via cert login), meaning the user can only authenticate through
non-password mechanisms.

`displayName` is a **Go-rewrite addition** (not present in the Java reference):
the human-readable name shown by the client in place of the raw address. It is
presentation metadata only — the ParticipantId address is always the identity,
and an empty `displayName` means the client falls back to the address. It is
served and edited through the **Profile API** (below). Because the Go rewrite
encodes the whole HumanAccountData as JSON in a single column (see
[05-storage-persistence](05-storage-persistence.md)), adding this field is
zero-migration: existing records decode with `displayName == ""`.

### Profile API (Go rewrite)

The browser client humanizes addresses (rosters, inbox, the identity widget,
@-mention tooltips) by resolving display names through two authenticated JSON
endpoints, mounted behind the session middleware on the same origin as the
client:

| Method & path | Purpose |
|---|---|
| `GET /api/profiles?addr=<a>&addr=<b>…` | Batch-resolve display names. Returns `{"profiles":[{"address","displayName"},…]}` with one entry per *valid* requested address (display name possibly empty, so the client caches unknowns and does not refetch); malformed addresses are skipped. The address is canonicalized in the response. |
| `POST /api/profile` (body `{"displayName":"…"}`) | Set **the caller's own** display name (resolved from the session, never from the body). Trimmed and length-capped (128 runes); empty clears it. Auto-provisions a minimal human account if none exists; refuses (`400`) to write onto a robot account. Returns `204`. |

A profile read carries no authorization beyond a valid session: display names are
not secret, and the whole client is already behind login. Writes are restricted
to the authenticated participant's own account — there is no API to edit another
participant's profile.

### PasswordDigest

```
PasswordDigest {
    salt    []byte  // 16 bytes (default); minimum 10 bytes
    digest  []byte  // SHA-512(salt || UTF-8(password))
}
```

**Algorithm**: concatenate `salt` then the UTF-8 encoding of the password, run
through SHA-512.  There is no iteration (PBKDF2, bcrypt, etc.) — just a single
SHA-512 pass.

**Verification**: recompute `SHA-512(salt || UTF-8(candidate))` and compare
byte-for-byte with the stored digest.

### RobotAccountData

```
RobotAccountData : AccountData {
    url             string              // base URL of robot endpoint; no trailing /
    consumerSecret  string              // OAuth 1.0a consumer secret
    capabilities    RobotCapabilities | null  // null until fetched
    isVerified      bool                // true once ownership verified
}
```

The consumer key (OAuth) equals the robot's ParticipantId address string.

### Storage format (protobuf)

Defined in `account-store.proto` (package `protoaccountstore`):

```proto
message ProtoAccountData {
    required AccountDataType account_type = 1;  // HUMAN_ACCOUNT=1, ROBOT_ACCOUNT=2
    required string account_id = 2;             // full participant address
    optional ProtoHumanAccountData human_account_data = 3;
    optional ProtoRobotAccountData robot_account_data = 4;
}

message ProtoHumanAccountData {
    optional ProtoPasswordDigest password_digest = 1;
    // NOTE: locale is NOT in the proto (it is in-memory / runtime only in the
    // Java reference implementation; LocaleServlet updates it on the live object)
}

message ProtoPasswordDigest {
    required bytes salt   = 1;
    required bytes digest = 2;
}

message ProtoRobotAccountData {
    required string url             = 1;
    required string consumer_secret = 2;
    optional ProtoRobotCapabilities robot_capabilities = 3;
    required bool   is_verified     = 4;
}
```

**Invariant**: the `account_id` field and the `account_type` discriminant must be
consistent with whichever sub-message is present.

---

## Algorithms & behavior

### 8.1 Password-based login (JAAS flow)

```
POST /auth/signin
Content-Type: application/x-www-form-urlencoded

address=user%40example.com&password=secret
```

Step-by-step:

1. **Parse body** — read the first line of the POST body as a URL-encoded string.
   Extract `address` and `password` fields.

2. **Normalize address** — if `address` contains no `@`, append `@<server-domain>`.
   Lowercase the result.

3. **Look up account** — fetch `AccountData` by ParticipantId from the
   AccountStore.  If not found → fail.

4. **Type check** — if the account is a RobotAccountData → fail (robots do not
   log in via password).

5. **Password check** — if the submitted password is null/empty, or if
   `SHA-512(salt || UTF-8(candidate))` does not match the stored digest → fail.

   > **Known Java bug (do not replicate).** The Java reference
   > (`AccountStoreLoginModule.login()`) does **not** guard against a *null stored
   > `passwordDigest`*.  It calls `account.asHuman().getPasswordDigest().verify(password)`
   > with no null check.  For a `HumanAccountData` created without a password (e.g.
   > auto-created via X.509 cert login, where `RegistrationUtil.createAccountIfMissing`
   > passes a null `PasswordDigest`), `getPasswordDigest()` returns null and the call
   > throws an uncaught `NullPointerException`.  Because the login endpoint only catches
   > `LoginException`, this surfaces as **HTTP 500**, not the clean HTTP 403 a faithful
   > reading of "fail" implies.  The Go rewrite **MUST** treat a null/absent stored
   > password digest as "password auth disabled for this account" and return a clean
   > authentication failure (HTTP 403), never a crash.  Note this is distinct from a
   > null/empty *submitted* password, which the Java already rejects cleanly.

6. **Create/get session** — call `request.getSession(true)` to create a new
   session (or reuse existing).

7. **Bind participant** — store the ParticipantId under key `"user"` in the
   session attributes.

8. **Redirect** — if query string has `r=<path>` and `<path>` has no host
   component, redirect to that path; otherwise redirect to `/`.

On failure (steps 3–5): respond HTTP 403 with the login HTML page and a
`"FAILED"` status annotation.

**JAAS configuration** (`wave/config/jaas.config`):

```
Wave {
    org.waveprotocol.box.server.authentication.AccountStoreLoginModule required debug=true;
};
```

The login context name is `"Wave"`. `AccountStoreLoginModule` is a standard JAAS
`LoginModule`. It retrieves the AccountStore through a static singleton
(`AccountStoreHolder`) because JAAS instantiates login modules without dependency
injection.

### 8.2 X.509 client-certificate login

Enabled by config `security.enable_clientauth = true` and
`security.clientauth_cert_domain = <domain>`.

When client auth is enabled, `AuthenticationServlet.doPost` (and `doGet` if certs
are already present) attempts certificate login before falling back to password:

1. Read `javax.servlet.request.X509Certificate[]` from request attributes.
2. If no certs and `disable_loginpage = true` → HTTP 403 (cert required).
3. If no certs and `disable_loginpage = false` → fall through to password login.
4. For each cert, extract `SubjectX500Principal` from the certificate chain.
5. Parse the distinguished name as an LDAP name.  Look for an RDN with OID
   `1.2.840.113549.1.9.1` (PKCS#9 emailAddress).
6. Decode the email: the raw `byte[]` from the RDN has a 2-byte header (tag +
   length); the rest is ASCII.  Assert `length < 128`.
7. If the email domain matches `clientauth_cert_domain` and the local part is a
   valid wave identifier:
   - If the account exists → authenticate as that ParticipantId.
   - If the account does not exist and `disable_registration = false` → create the
     account (no password) and send a welcome wave; then authenticate.
   - If the account does not exist and `disable_registration = true` → fail.
8. Bind participant to session and redirect (same as §8.1 steps 6–8).

### 8.3 Session representation

An authenticated session is an HTTP server-side session (Jetty `HashSessionManager`,
persisted to disk under `sessions_store_directory`).  The session is identified
by the `JSESSIONID` cookie.

Session attributes:

| Key | Type | Meaning |
|-----|------|---------|
| `"user"` | `ParticipantId` | The authenticated participant. Absent if unauthenticated. |

`SessionManager` interface:

```
getLoggedInUser(session)     → ParticipantId | null
getLoggedInAccount(session)  → AccountData | null   // fetches from AccountStore
setLoggedInUser(session, id) → void                 // binds participant
logout(session)              → void                 // removes "user" attr
getSessionFromToken(token)   → HttpSession | null   // looks up session by JSESSIONID value
getLoginUrl(redirect)        → string               // "/auth/signin?r=<redirect>"
```

**Session persistence**: Jetty `HashSessionManager` saves sessions to disk every
60 seconds and restores them on server restart.  The `session_cookie_max_age`
config key controls the `Max-Age` of the `JSESSIONID` cookie; `-1` means
session-scoped (no `Max-Age`).

**No anonymous/guest access**: `checkAccessPermission` requires a non-null
ParticipantId, so unauthenticated users have no read access to any wavelet.

### 8.4 WebSocket connection authentication

WebSocket connections are established at `/socket`.  At upgrade time:

1. The HTTP upgrade request carries the session cookie.  The server reads the
   session from the cookie and calls `sessionManager.getLoggedInUser(session)`.
2. The resulting `ParticipantId` (possibly null if not logged in) is stored in
   the `WebSocketConnection.loggedInUser` field for the lifetime of the connection.

**ProtocolAuthenticate fallback** (workaround for environments where cookies don't
flow through the WebSocket upgrade):

After opening the WebSocket, the client may send a `ProtocolAuthenticate` message:

```proto
message ProtocolAuthenticate {
    required string token = 1;  // value of the JSESSIONID cookie
}
```

The server:
1. Calls `sessionManager.getSessionFromToken(token)`.
2. Calls `sessionManager.getLoggedInUser(session)` to get the ParticipantId.
3. Asserts the result is non-null.
4. Asserts that `loggedInUser` is either null (connection not yet bound) or equals
   the authenticated participant (no rebinding to a different user is allowed).
5. Sets `loggedInUser` to the authenticated ParticipantId.
6. Responds with `ProtocolAuthenticationResult {}` (empty message).

All subsequent RPC calls on the connection run as `loggedInUser`.

### 8.5 Account registration

**Endpoint**: `POST /register`

Flow:

1. If `administration.disable_registration = true` → HTTP 403, show page with
   error.
2. Parse `address` and `password` from POST body.
3. Validate address:
   - Trim and lowercase.
   - If no `@`, append `@<server-domain>`.
   - The local part must match `[\\w.]+` (letters, digits, underscores, periods).
   - The domain must equal the server domain.
4. If an account with that ParticipantId already exists → error "Account already
   exists".
5. Create a `HumanAccountData` with `PasswordDigest(password)`.  If `password` is
   null/absent, use empty string (`""`).
6. Write to AccountStore via `putAccount`.
7. Call `WelcomeRobot.greet(id)` — this creates a copy of the configured welcome
   wave for the new user (if `welcome_wave_id` is set).  Failure is logged but
   does not abort registration.
8. Return HTTP 200 with success message.

**Registration does not log the new user in.**  Unlike `/auth/signin` (§8.1) and
cert login (§8.2), the `/register` endpoint does not create or bind an `HttpSession`
and issues no redirect — it only renders the registration result page (HTTP 200,
"Registration complete.").  `UserRegistrationServlet` injects no `SessionManager` and
never calls `setLoggedInUser`.  The user must separately `POST` to `/auth/signin` to
obtain an authenticated session.  A Go port must not assume registration implicitly
authenticates the new account.

**GET /register** renders the registration form.

**Admin user** (`administration.admin_user`): a single ParticipantId that has
elevated privilege.  Currently used by agent robots (PasswordAdmin, Registration)
to perform actions on behalf of others.  The default value `"@"` is deliberately
invalid so no account can inadvertently become admin.

### 8.6 Sign-out

**Endpoint**: `GET /auth/signout`

1. Call `sessionManager.logout(session)` — removes the `"user"` attribute from
   the session.
2. If query parameter `r` is present and starts with `/`, redirect to it.
   Otherwise respond HTTP 200 with a simple "Logged out." page.

### 8.7 Wavelet access authorization

There is a **single access predicate** (`WaveletDataUtil.checkAccessPermission`)
that gates both reads and writes. A participant satisfies it — and thus has
**read access** to a wavelet — if and only if:

```
participantId != null
AND (
    snapshot == null   // empty wavelet (no snapshot yet) — see below
    OR wavelet.participants contains participantId
    OR (sharedDomainParticipantId != null
        AND wavelet.participants contains sharedDomainParticipantId)
)
```

Where `sharedDomainParticipantId` is the synthetic address `@<domain>` (no local
part).  Adding `@example.com` to a wavelet's participant list makes it readable by
all authenticated users on `example.com` — and, per the write-access rule below,
also delta-submittable by them.

If the wavelet is empty (no snapshot yet), **everyone** has access — this
allows the first delta to be submitted.

**Write access** (delta submission) is gate-kept at the server submit path, **not**
in the OT / concurrency-control layer.  `WaveServerImpl.submitDelta` calls the
wavelet container's `checkAccessPermission(author)` — the **same** predicate used
for reads (it delegates to `WaveletDataUtil.checkAccessPermission(snapshot, author,
sharedDomainParticipantId)`).  If the author is neither in the participant set nor
covered by the `@domain` shared-domain grant, the submit is rejected (the Java
reference returns a `badRequest` federation error: "`<author>` is not a participant
of `<wavelet>`").  The OT layer performs version/hash validation, duplicate
elimination, and transformation, but performs **no** author authorization.

> A Go port must **not** add a stricter "author must be an explicit participant"
> check — that would contradict the shared-domain grant. There is a single access
> predicate; it gates both reads and writes, and it treats the `@domain` shared
> participant as sufficient for both.

**No roles or ACLs beyond the participant set**: there is no reader/writer/admin
distinction within a wavelet.  Being in `wavelet.participants` (or being covered by
the `@domain` shared participant) is sufficient for both read and write.

---

## Wire / storage formats

### HTTP endpoints summary

| Method | Path | Purpose |
|--------|------|---------|
| `GET`  | `/auth/signin` | Show login form |
| `POST` | `/auth/signin` | Authenticate (password or cert) |
| `GET`  | `/auth/signout` | Sign out |
| `GET`  | `/register` | Show registration form |
| `POST` | `/register` | Create account |
| `WS`   | `/socket` | WebSocket RPC (carries `ProtocolAuthenticate`) |

### Login POST body

```
application/x-www-form-urlencoded
address=<participant-address>&password=<cleartext-password>
```

Fields:

| Field | Description |
|-------|-------------|
| `address` | Full address (`user@domain`) or just local name (domain appended server-side). Case-insensitive; lowercased before lookup. |
| `password` | Cleartext password. The server zeroes the char array after use. |

### Session cookie

Name: `JSESSIONID`
Value: opaque session token managed by Jetty.
`Max-Age`: from `network.session_cookie_max_age` (default `-1` = session-scoped).
The cookie is HTTP-only (Jetty default); sent over HTTPS when SSL is enabled.

### AccountStore persistence

AccountStore implementations (file-based, MongoDB) serialize `AccountData` records
as `ProtoAccountData` protocol buffers.  The file-based store uses one file per
account keyed by participant address.  See spec 05 for store-level details.

---

## Interfaces / APIs

### AccountStore

```
interface AccountStore {
    initializeAccountStore() throws PersistenceException
    getAccount(id ParticipantId) → AccountData | null
    putAccount(account AccountData)
    removeAccount(id ParticipantId)
}
```

`putAccount` is an upsert (create or overwrite).

### SessionManager

```
interface SessionManager {
    getLoggedInUser(session) → ParticipantId | null
    getLoggedInAccount(session) → AccountData | null
    setLoggedInUser(session, id ParticipantId)
    logout(session)
    getLoginUrl(redirect string) → string
    getSessionFromToken(token string) → HttpSession | null
}
```

### ServerRpcController (per-connection)

```
interface ServerRpcController {
    getLoggedInUser() → ParticipantId | null
    // ... (RPC dispatch methods — see spec 04/06)
}
```

Each RPC controller carries the `loggedInUser` for the duration of the connection.
The waveserver frontend uses this to authorize wavelet open/submit requests.

---

## Edge cases & failure modes

| Situation | Behavior |
|-----------|----------|
| Account not found during login | HTTP 403, generic "username or password incorrect" message (no indication of which). |
| Robot account presented for password login | HTTP 403 (robot accounts have no PasswordDigest). |
| Null or empty *submitted* password | HTTP 403 (rejected cleanly: a password, even empty, is required). |
| Null *stored* `passwordDigest` on a human account (e.g. cert-created account) | **Java bug**: uncaught `NullPointerException` → HTTP 500 (`getPasswordDigest()` is dereferenced without a null check). The Go rewrite must treat this as a clean HTTP 403 (password auth disabled). |
| `disable_loginpage = true` and no client cert | HTTP 403 with an explanatory message. |
| X.509 email domain does not match `clientauth_cert_domain` | Login fails; returns null participant. |
| X.509 email longer than 127 chars | `IllegalStateException` (hard assertion). |
| Registration with existing address | HTTP 403, "Account already exists". |
| Registration with address on wrong domain | HTTP 403, validation error. |
| Welcome wave creation fails | Logged as a warning; registration still succeeds. |
| Successful registration | HTTP 200, "Registration complete." — but **no authenticated session is created** and no redirect is issued. The user must `POST /auth/signin` separately to log in. |
| `ProtocolAuthenticate` token is invalid | `IllegalArgumentException` thrown, connection terminated. |
| `ProtocolAuthenticate` sent after connection already authenticated as different user | `IllegalStateException`, connection terminated. |
| Session not found (expired or invalid JSESSIONID) | `getLoggedInUser` returns null; subsequent RPCs fail access checks. |
| Access check with null ParticipantId | Always returns false (no anonymous read). |
| Empty wavelet (no snapshot) | Access check returns true for any non-null participant. |

---

## Open questions / ambiguities

### What the current scheme assumes

1. **Single-domain server**: the authentication model assumes one authoritative
   domain.  Usernames not containing `@` are assumed to belong to that domain.
   Federation is separate (spec 07).

2. **Cookie-based sessions for browsers; token fallback for WebSocket**: the
   `ProtocolAuthenticate` token mechanism is an acknowledged workaround for a
   known bug.  The session token is just the raw `JSESSIONID` value — there is no
   CSRF protection or signed token.

3. **SHA-512 without iteration**: the password hashing is one-pass SHA-512 with a
   16-byte random salt.  This is weak by modern standards (no bcrypt, scrypt, or
   Argon2; no iteration count).

4. **No role model within wavelets**: participant membership is binary
   (in/out).  There is no read-only participant concept.

5. **Admin is a single configurable address**: there is no admin group or
   role system; just one configured ParticipantId.

6. **Robot authentication via OAuth 1.0a** (covered in spec 09): robots do not go
   through the password/cert path at all; they authenticate via signed OAuth
   requests against the Data API.  The `RobotAccountData.consumerSecret` is the
   shared secret; the consumer key is the robot's participant address.

### What a modern replacement must preserve

For the Go rewrite to be compatible, it must maintain the following identities and
contracts:

- **ParticipantId is the identity**: every authenticated session maps to exactly
  one `name@domain` address.  All downstream subsystems (wavelet participants,
  delta authorship, search, robots) use this address, not an opaque user ID.

- **Session → ParticipantId binding at connection time**: when a WebSocket
  connection is established, the server must know the authenticated ParticipantId
  before processing any RPCs.  The `ProtocolAuthenticate` message provides a
  late-binding path if cookies are unavailable.

- **AccountStore interface**: the Go replacement needs a `getAccount / putAccount /
  removeAccount` interface keyed by ParticipantId, storing human vs. robot
  variants.

- **Robot accounts are not loggable-in by humans**: the type discrimination
  (human vs. robot) must be preserved; a robot's credentials are OAuth secrets,
  not passwords.

- **Shared-domain participant (`@domain`)**: any wavelet that lists `@domain` as a
  participant must be both readable and writable (delta-authorable) by all
  authenticated local users; the single access check gates both.  This is a
  data-model invariant the auth check depends on.

### Decisions for the rewrite

1. **Replace SHA-512 with bcrypt/Argon2**: a modern password hash should be used.
   The `PasswordDigest` on-disk format (salt + digest bytes) needs a migration path
   or a version tag.

2. **Replace JAAS with direct auth logic**: JAAS is a Java-specific framework.  Go
   should perform credential checking directly (look up account, verify hash).

3. **Replace JSESSIONID cookies with signed session tokens (JWT or similar)**:
   eliminates the need for the `ProtocolAuthenticate` workaround, since a signed
   token can be sent in both the HTTP upgrade and WebSocket headers.

4. **CSRF protection**: the current implementation has no CSRF tokens on the login
   or registration forms.

5. **Locale storage**: the Java implementation stores locale in-memory only (not
   in the proto).  The Go rewrite should decide whether locale belongs in the
   account record or in a separate user-preference store.

6. **Admin privilege model**: the single `admin_user` config key is fragile.  The
   rewrite might introduce a proper admin role in the account record.

7. **Guard null stored password digest (fix the Java NPE bug)**: the Java reference
   dereferences `getPasswordDigest()` without a null check during login
   (`AccountStoreLoginModule.login()`), so a human account with no password (created
   via cert login) crashes the login attempt with an uncaught `NullPointerException`
   (surfacing as HTTP 500) instead of failing cleanly.  The Go rewrite **must** treat
   a null/absent stored digest as "password authentication disabled for this account"
   and return a clean authentication failure.

---

## Source references

| Path | Role |
|------|------|
| `wave/src/main/java/org/waveprotocol/box/server/account/AccountData.java` | AccountData interface |
| `wave/src/main/java/org/waveprotocol/box/server/account/HumanAccountData.java` | HumanAccountData interface |
| `wave/src/main/java/org/waveprotocol/box/server/account/HumanAccountDataImpl.java` | HumanAccountData implementation |
| `wave/src/main/java/org/waveprotocol/box/server/account/RobotAccountData.java` | RobotAccountData interface |
| `wave/src/main/java/org/waveprotocol/box/server/account/RobotAccountDataImpl.java` | RobotAccountData implementation |
| `wave/src/main/java/org/waveprotocol/box/server/authentication/PasswordDigest.java` | Salted SHA-512 password hash |
| `wave/src/main/java/org/waveprotocol/box/server/authentication/AccountStoreLoginModule.java` | JAAS LoginModule doing account lookup + hash verification |
| `wave/src/main/java/org/waveprotocol/box/server/authentication/AccountStoreHolder.java` | Static singleton bridging Guice DI to JAAS |
| `wave/src/main/java/org/waveprotocol/box/server/authentication/HttpRequestBasedCallbackHandler.java` | Feeds HTTP POST fields into JAAS callbacks |
| `wave/src/main/java/org/waveprotocol/box/server/authentication/ParticipantPrincipal.java` | JAAS Principal wrapping a ParticipantId |
| `wave/src/main/java/org/waveprotocol/box/server/authentication/SessionManager.java` | SessionManager interface |
| `wave/src/main/java/org/waveprotocol/box/server/authentication/SessionManagerImpl.java` | SessionManager backed by Jetty HashSessionManager |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/AuthenticationServlet.java` | Login endpoint (password + X.509); sets session; handles redirect |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/UserRegistrationServlet.java` | Account registration endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/SignOutServlet.java` | Sign-out endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/ServerRpcProvider.java` | WebSocket upgrade (reads session cookie); `ProtocolAuthenticate` handling |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/ServerRpcControllerImpl.java` | Per-RPC controller carrying loggedInUser |
| `wave/src/main/java/org/waveprotocol/box/server/persistence/AccountStore.java` | AccountStore interface |
| `wave/src/main/java/org/waveprotocol/box/server/util/WaveletDataUtil.java` | `checkAccessPermission` (participant + shared-domain check) |
| `wave/src/main/java/org/waveprotocol/box/server/util/RegistrationUtil.java` | Username validation, account creation, welcome-bot invocation |
| `wave/src/proto/proto/org/waveprotocol/box/server/persistence/protos/account-store.proto` | Protobuf schema for persisted account records |
| `wave/config/jaas.config` | JAAS configuration naming the `"Wave"` login context |
| `wave/config/reference.conf` | Auth-related config keys (enable_clientauth, disable_registration, disable_loginpage, admin_user, session_cookie_max_age, sessions_store_directory) |
