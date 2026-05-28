# 07 — Federation

## Purpose & scope

This spec covers server-to-server Wave federation: how two Wave service providers (WSPs) on
different internet domains share wavelets, exchange deltas, and cryptographically authenticate
those deltas. It also covers the crypto subsystem (signing, certificate chains, verification) that
secures federation messages.

In scope: the federation concept, provider/listener interfaces, the XMPP transport mapping (as
specified — with a frank assessment of what actually exists in this codebase), delta signing, the
`ProtocolSignedDelta` / `ProtocolSignerInfo` message shapes, error model, and the noop
(single-domain) path that the current codebase actually uses in production.

Out of scope: wave/wavelet data model (see [01-data-model](01-data-model.md)), OT and delta
application (see [02-operational-transform](02-operational-transform.md) and
[03-concurrency-control](03-concurrency-control.md)), wire protocol for the client-server path
(see [04-wire-protocol](04-wire-protocol.md)).

---

## Concepts & glossary

| Term | Definition |
|------|-----------|
| **WSP (Wave Service Provider)** | A server installation serving users at a specific domain, e.g. `example.com`. |
| **Hosting provider** | The WSP whose domain matches the wavelet's domain component. It is the single authoritative server for that wavelet: it applies deltas, maintains the canonical state, and serves history. |
| **Remote provider** | Any WSP that is not the hosting provider for a given wavelet but whose users are participants. It receives pushed delta updates and can request history. |
| **Local wavelet** | A wavelet whose domain matches the local WSP's domain. Applied and stored locally; the local server is the hosting provider. |
| **Remote wavelet** | A wavelet hosted by a foreign WSP. The local server maintains a copy that is updated by pushed deltas or history fetches. |
| **Federation host** | The logical role on the hosting provider's side that serves incoming requests from remote servers (history, signer info, submit). |
| **Federation remote** | The logical role on the remote provider's side that sends requests and receives pushed updates from the hosting provider. |
| **FederationHostBridge** | Dependency-injection qualifier marking the `WaveletFederationListener.Factory` used to push updates *from* the local waveserver out to remote domains (toward the transport layer). |
| **FederationRemoteBridge** | DI qualifier marking the `WaveletFederationProvider` used to reach remote/hosting servers (toward the transport layer). |
| **ProtocolSignedDelta** | A canonically serialised `ProtocolWaveletDelta` (as raw bytes) plus one or more RSA signatures from the submitting domain(s). |
| **ProtocolAppliedWaveletDelta** | The hosting provider's record of a delta after application: wraps the signed original, the version it was applied at, the number of ops applied, and the application timestamp. |
| **SignerInfo / ProtocolSignerInfo** | A certificate chain (signer cert first, CA last) used to verify signatures. The chain is identified by the SHA-256 (or SHA-512) hash of its PkiPath encoding. |
| **Signer ID** | The binary hash of a signer's PkiPath-encoded certificate chain. Used as a lookup key in every `ProtocolSignature`. |
| **XMPP** | The Extensible Messaging and Presence Protocol (RFC 6120). The Wave federation spec defines XMPP as the transport for inter-server messages. In this codebase, **no XMPP implementation is present**; the only deployed transport is the noop. |
| **HashedVersion** | A (version-number, history-hash) pair. The history-hash is a running chain of SHA-256 digests over applied deltas. See [01-data-model](01-data-model.md) for the full definition. |

---

## Data structures

### ProtocolWaveletDelta

The core unit of change. Defined in `federation.proto`.

```
ProtocolWaveletDelta {
  hashed_version:  ProtocolHashedVersion   // target version (required)
  author:          string                  // wave address of contributor (required)
  operation:       []ProtocolWaveletOperation
  address_path:    []string                // federated group delegation path (usually empty)
}
```

`address_path` encodes the delegation chain for federated group access. In the common case it is
empty and the delta has exactly one signature (from the author's domain). If non-empty, each
unique domain in `[address_path..., author]` requires one corresponding signature.

### ProtocolSignedDelta

```
ProtocolSignedDelta {
  delta:      bytes                // serialised ProtocolWaveletDelta (required)
  signature:  []ProtocolSignature
}
```

`delta` is the canonical binary protobuf encoding of the `ProtocolWaveletDelta`. The signature is
computed over these exact bytes. Receivers must not re-encode the delta before verifying.

### ProtocolSignature

```
ProtocolSignature {
  signature_bytes:     bytes
  signer_id:           bytes               // hash of cert chain (PkiPath SHA-256 by default)
  signature_algorithm: SignatureAlgorithm  // enum: SHA1_RSA = 1
}
```

The only defined algorithm is `SHA1_RSA` (SHA-1 with RSA PKCS#1 v1.5, JCA name `SHA1withRSA`).

### ProtocolSignerInfo

```
ProtocolSignerInfo {
  hash_algorithm: HashAlgorithm    // SHA256 = 1, SHA512 = 2
  domain:         string           // domain the certificates were issued to
  certificate:    []bytes          // X.509 DER-encoded certs, signer cert first, CA cert last
}
```

The `hash_algorithm` field specifies how the signer ID is computed from the PkiPath encoding of
the chain. Receivers should verify that the target certificate (index 0) is issued to `domain`.

### ProtocolAppliedWaveletDelta

```
ProtocolAppliedWaveletDelta {
  signed_original_delta:      ProtocolSignedDelta    // required
  hashed_version_applied_at:  ProtocolHashedVersion  // optional; omitted if no OT was needed
  operations_applied:         int32                  // required
  application_timestamp:      int64                  // ms since epoch; required
}
```

`hashed_version_applied_at` is omitted when the delta was applied without transformation (i.e., it
arrived at exactly the version it targeted). This is an optimisation; the applying version can
still be recovered from the delta itself.

### ProtocolHashedVersion

```
ProtocolHashedVersion {
  version:      int64   // wavelet version number
  history_hash: bytes   // exactly 20 bytes for version > 0; variable-length URI bytes at version 0
}
```

The `history_hash` length is **not** uniformly 20 bytes: it is exactly 20 bytes (the first 160
bits of the SHA-256 chain output) only for versions > 0. At version 0 the field holds the raw,
variable-length UTF-8 bytes of the wavelet-name URI (typically much longer than 20 bytes — see
below).

The hash chain (versions > 0): `history_hash[N+1] = SHA-256(history_hash[N] || appliedDeltaBytes)[0:20]`.
The first 20 bytes of the 32-byte SHA-256 output are retained (`HASH_SIZE_BITS = 160`). See
[03-concurrency-control](03-concurrency-control.md) for how HashedVersion is used in the
client-server protocol; the same value appears in federation.

Version-zero hash is **NOT** a SHA-256 digest and is **NOT** truncated. For version 0,
`history_hash = UTF-8(IdURIEncoderDecoder.waveletNameToURI(waveletName))` — the raw UTF-8 bytes of
the canonical wavelet URI `wave://<waveletDomain>/<waveDomainPart><waveLocalId>/<waveletLocalId>`
(variable length, typically longer than 20 bytes). No cryptographic digest and no 20-byte
truncation are applied at version zero (see `HashedVersionZeroFactoryImpl.createVersionZero`, which
delegates from `ProtocolHashedVersionFactory.createVersionZero`). The
`SHA-256(prevHash ‖ appliedDeltaBytes)[0:20]` chaining rule applies **only** to versions > 0. A Go
implementation that applies SHA-256 (or any truncation) at version zero would be incompatible with
the Java at the hash-chain level. See [01-data-model](01-data-model.md) §2.5 for the canonical
version-zero definition.

### FederationError

```
FederationError {
  error_code:    Code     // required
  error_message: string   // optional description
}
```

Error codes (mapped directly to XMPP error stanza names per RFC 3920 §9.3.3):

| Code | Value | Meaning |
|------|-------|---------|
| OK | 0 | Internal success only |
| BAD_REQUEST | 1 | Malformed request |
| ITEM_NOT_FOUND | 2 | Wavelet doesn't exist or requester not authorised (ambiguous by design) |
| NOT_ACCEPTABLE | 3 | Invalid delta, invalid signer info, etc. |
| NOT_AUTHORIZED | 4 | Signer info not available — **defined but unused by the reference server**; the unknown-signer-on-submit case actually returns INTERNAL_SERVER_ERROR (see below) |
| RESOURCE_CONSTRAINT | 5 | Back-off |
| UNDEFINED_CONDITION | 6 | Unrecognised wire error |
| REMOTE_SERVER_TIMEOUT | 7 | Timeout (may be generated internally) |
| UNEXPECTED_REQUEST | 8 | In-flight ID reuse |
| INTERNAL_SERVER_ERROR | 9 | Server fault |

---

## Algorithms & behavior

### Wavelet ownership and routing

A wavelet `wave://example.com/w+abc/conv+root` is hosted by `example.com`. Any WSP whose users
are participants in that wavelet is a remote provider for it.

When the hosting server applies a delta:
1. It notifies every domain that has participants on the wavelet by calling
   `WaveletFederationListener.waveletDeltaUpdate` for that domain.
2. After persisting the delta, it issues a commit notice via
   `WaveletFederationListener.waveletCommitUpdate`.

When a remote server's user edits a remote wavelet:
1. The remote server signs the delta using its own private key.
2. It calls `WaveletFederationProvider.submitRequest` toward the hosting server.
3. The hosting server verifies the signature, runs OT, applies the delta, and calls back via
   `SubmitResultListener.onSuccess` with the resulting version.

### History gap recovery

Remote servers may receive an out-of-order push (version N+5 arrives before N+1..N+4). When a
`RemoteWaveletContainer` detects a gap (incoming delta's `appliedAt > currentVersion`), it calls
`WaveletFederationProvider.requestHistory` to fill the gap, then retries applying the buffered
deltas once the history arrives.

### Delta signing (outbound)

1. Serialise the `ProtocolWaveletDelta` to canonical protobuf bytes.
2. Compute `SHA1withRSA(privateKey, deltaBytes)`.
3. Build `ProtocolSignature{signature_bytes, signer_id, SHA1_RSA}`.
4. Wrap in `ProtocolSignedDelta{delta: deltaBytes, signature: [sig]}`.

The `signer_id` is the SHA-256 hash of the PkiPath encoding of the cert chain (first 32 bytes;
no truncation for the signer ID — unlike the history hash, the full digest is used).

Before submitting, the remote server must post its `ProtocolSignerInfo` to the hosting server so
the host can verify the signature. This is done via `WaveletFederationProvider.postSignerInfo`.

### Delta verification (inbound)

When the hosting server receives a `ProtocolSignedDelta`:

1. Parse the inner `delta` bytes as `ProtocolWaveletDelta`. Reject if malformed.
2. Reject if `delta.hashed_version.version == 0` (remote servers may not create wavelets).
3. Build the extended address path: `[delta.address_path..., delta.author]`.
4. Deduplicate by domain. The resulting domain list must match the signature count exactly.
5. For each domain/signature pair:
   a. Look up `SignerInfo` by `signature.signer_id` in the local `CertPathStore`.
   b. If not found, the verification step raises an internal `UnknownSigner` condition (the caller
      should have called `postSignerInfo` first). On the submit path the hosting server maps this to
      a `FederationError` with code `INTERNAL_SERVER_ERROR` (message `"Unknown signer"`, via
      `FederationErrors.internalServerError`). (Note: `UNKNOWN_SIGNER` is **not** a wire error code —
      it is only an internal exception name, `UnknownSignerException`.)
   c. Validate the cert chain with PKIX validation against the trust store.
   d. Match the leaf certificate's authority to the domain. First extract the Common Name (CN) from
      the leaf cert's Subject DN using the regex `CN=([^,]+)`. If no CN is present, reject
      immediately with a verification failure (Java throws `SignatureException` `"no common name
      found in signer certificate"`) — the CN must be present even when a SubjectAlternativeName
      would otherwise match, because the CN is extracted first and a null CN throws before SAN is
      examined. Then accept the cert as matching the domain if **any** of the following holds,
      checked **in order**, succeeding on the first match:
        i. **Exact CN match:** the CN equals the domain.
        ii. **SubjectAlternativeName match:** any DNS-type (type 2) SubjectAlternativeName equals the
            domain.
        iii. **Wildcard CN fallback:** the CN starts with `"*."`; strip the leading `"*."` to get the
             cert's parent domain, strip the first label from the domain (everything after the first
             `"."`), and accept if these are equal. E.g. CN `*.example.com` matches domain
             `sub.example.com` (both reduce to `example.com`). This wildcard match is **CN-only**
             (never applied to SubjectAlternativeNames), wildcards only a single label, and is
             case-sensitive.
      If none match, reject with a signature error (`"expected <domain> as CN or alternative name in
      cert"`).
   e. Verify `SHA1withRSA(leafPublicKey, deltaBytes) == signature.signature_bytes`.
6. If all signatures verify, proceed to OT and application.

Verification can be disabled via configuration flag `federation.waveserver_disable_verification`
(default: false). When disabled, step 5 is skipped entirely. **This flag must not be set in any
security-sensitive deployment.**

### Certificate chain validation

The `CachedCertPathValidator` wraps Java's PKIX path validator with a short-lived cache keyed on
the cert chain identity. It:
- Uses PKIX algorithm with revocation checking **disabled** (no CRL/OCSP).
- Accepts the Wave-specific OID `1.3.6.1.4.1.11129.2.1.1` without failing on unknown critical extensions.
- Validates against the JVM's default trust store, plus two hardcoded StartCom CA certificates
  (legacy: StartCom was the recommended free-cert CA for Wave federation during the Google Wave era).

Trust roots are configurable: the default is `DefaultTrustRootsProvider` which loads from the JVM
keystore plus the two StartCom additions.

### Signer info lifecycle

1. Before a remote server can submit deltas, it must call `postSignerInfo` to register its cert chain.
2. The hosting server validates the chain and stores it in `CertPathStore` (keyed by signer ID).
3. When the hosting server serves history, receivers that encounter an unknown `signer_id` must call
   `getDeltaSignerInfo` (on the hosting server) to fetch the chain. The hosting server is required
   to retain the `SignerInfo` for every delta in its history.
4. `prefetchDeltaSignerInfo` coalesces concurrent requests for the same signer: exactly one
   outbound fetch per domain per signer ID is in flight at a time.

### Noop (single-domain) mode

The currently shipped code always loads `NoOpFederationModule`:

- `NoOpFederationRemote` implements `WaveletFederationProvider` by calling `onFailure` with
  `"Federation is not enabled!"` on every method.
- `NoOpFederationHost` implements `WaveletFederationListener.Factory` the same way.
- `NoOpFederationTransport.startFederation()` is a no-op.

In this mode, no remote wavelets can be received or updated, and no local wavelets can be pushed.
Cross-domain waves simply don't work. All participant addresses are expected to belong to the
single configured domain.

---

## Wire / storage formats

### XMPP transport mapping (spec-level, not implemented)

The Wave Federation Protocol specifies XMPP as the transport. The intended mapping is:

- Each WSP runs an XMPP component (or server-to-server connection) listening on its domain.
- Federation messages are encoded as protobuf-in-XML payloads inside XMPP `<iq>` stanzas.
- Push updates (`waveletDeltaUpdate`, `waveletCommitUpdate`) use XMPP pubsub or direct `<message>` stanzas.
- Request/response operations (`submitRequest`, `requestHistory`, `getDeltaSignerInfo`,
  `postSignerInfo`) use XMPP `<iq type="get">` / `<iq type="result">` / `<iq type="error">`.
- XMPP error stanzas map to `FederationError` codes (the error code enum is explicitly designed
  to correspond to RFC 3920 §9.3.3 error conditions).

**No XMPP implementation exists in this repository.** The `wave/src/main/java` tree contains only
the noop implementation. All search terms — "xmpp", "jabber", "smack", "component", "Stanza" —
return zero hits in the Java source (other than a comment in `DefaultTrustRootsProvider` noting that
one of the hardcoded CA certs was used "for the XMPP community"). The XMPP transport was present
in the original Google Wave codebase and in earlier open-source forks, but is absent here.

### Protobuf schemas

Defined in `wave/src/proto/proto/org/waveprotocol/wave/federation/`:
- `federation.protodevel` — `ProtocolWaveletDelta`, `ProtocolHashedVersion`,
  `ProtocolWaveletOperation`, `ProtocolDocumentOperation`, `ProtocolAppliedWaveletDelta`,
  `ProtocolSignedDelta`, `ProtocolSignature`, `ProtocolSignerInfo`
- `federation_error.protodevel` — `FederationError`

Note: files use `.protodevel` extension rather than `.proto` but are valid proto2 syntax.

### SignerInfo persistent store

`SignerInfoStore` (extends `CertPathStore`) provides `getSignerInfo(byte[] signerId)` and
`putSignerInfo(ProtocolSignerInfo)`. The default in-memory implementation (`DefaultCertPathStore`)
uses a `ConcurrentMap<ByteBuffer, SignerInfo>` keyed by the raw signer-ID bytes. The persistence
layer (`MemoryStore` or a database-backed implementation) wraps `SignerInfoStore` for durability.
The hosting server MUST durably store all signer infos used in its delta history.

### Server configuration for signing

When signing is enabled (non-noop), the server reads from config:
- `federation.certificate_private_key` — path to PKCS#8 PEM private key file
- `federation.certificate_files` — list of paths to X.509 certificate files (signer first, CA last)
- `federation.certificate_domain` — domain the certificate was issued to

The signer uses `SHA1_RSA` for signing and `SHA256` for signer ID computation.

---

## Interfaces / APIs

### WaveletFederationProvider

Implemented by: the waveserver (federation host side) and the federation transport (remote side).

```
WaveletFederationProvider {
  submitRequest(waveletName, signedDelta, SubmitResultListener)
  requestHistory(waveletName, domain, startVersion, endVersion, lengthLimit, HistoryResponseListener)
  getDeltaSignerInfo(signerId, waveletName, deltaEndVersion, DeltaSignerInfoResponseListener)
  postSignerInfo(destinationDomain, signerInfo, PostSignerInfoResponseListener)
}
```

All methods are asynchronous; results are delivered via callback interfaces. Every callback
interface extends `FederationListener` which provides `onFailure(FederationError)`.

`requestHistory`:
- `startVersion` and `endVersion` are inclusive and exclusive respectively.
- `lengthLimit` is a byte-size hint; the server may return fewer deltas when the limit is hit,
  setting `versionTruncatedAt` accordingly.
- `domain` is the requester's domain, used to authorise the request (caller must have a participant
  on the wavelet).

`getDeltaSignerInfo`:
- Called by a remote when it receives a delta with an unknown signer ID.
- The host validates that `signerId` was actually used to sign a delta ending at `deltaEndVersion`
  (via `LocalWaveletContainer.isDeltaSigner`), preventing arbitrary cert exfiltration.

### WaveletFederationListener

Implemented by: the waveserver (remote side receives updates) and the federation transport (host
side pushes updates).

```
WaveletFederationListener {
  waveletDeltaUpdate(waveletName, []ByteString appliedDeltaBytes, WaveletUpdateCallback)
  waveletCommitUpdate(waveletName, committedVersion, WaveletUpdateCallback)
}
WaveletFederationListener.Factory {
  listenerForDomain(domain) WaveletFederationListener
}
```

`waveletDeltaUpdate`: `deltas` is a list of serialised `ProtocolAppliedWaveletDelta` bytes. They
are NOT transformed — the remote must apply OT itself if it has diverged.

`waveletCommitUpdate`: notifies the remote that the hosting server has durably stored up to
`committedVersion`. If the callback `onSuccess` is called, the remote need not retry; `onFailure`
obligates the host to retry eventually.

### WaveletFederationListener.WaveletUpdateCallback

```
WaveletUpdateCallback {
  onSuccess()
  onFailure(FederationError)
}
```

### FederationTransport

```
FederationTransport {
  startFederation()
}
```

Called once at server startup after all Guice bindings are resolved. Implementors use this to
open network connections, register XMPP components, etc.

### CertificateManager

Internal interface mediating between the waveserver and the crypto/federation layers:

```
CertificateManager {
  getLocalDomains() Set<String>
  getLocalSigner() SignatureHandler
  signDelta(delta ByteStringMessage<ProtocolWaveletDelta>) ProtocolSignedDelta
  verifyDelta(signedDelta) ByteStringMessage<ProtocolWaveletDelta>  // throws SignatureException|UnknownSignerException
  storeSignerInfo(signerInfo ProtocolSignerInfo)                    // throws SignatureException
  retrieveSignerInfo(signerId) ProtocolSignerInfo                   // null if not found
  prefetchDeltaSignerInfo(provider, signerId, waveletName, endVersion, callback)
}
```

### SignatureHandler

Abstraction over signing; allows non-signing (noop) and signing modes:

```
SignatureHandler {
  getDomain() string
  getSignerInfo() SignerInfo   // null if no cert configured
  sign(delta ByteStringMessage<ProtocolWaveletDelta>) []ProtocolSignature
}
```

`NonSigningSignatureHandler` returns an empty signature list and null signer info — used in
deployments where no X.509 cert is configured.

### DI wiring summary

```
@FederationRemoteBridge WaveletFederationProvider
    → NoOpFederationRemote (noop)
    → Transport-specific impl (XMPP, if built)

@FederationHostBridge WaveletFederationListener.Factory
    → NoOpFederationHost (noop)
    → Transport-specific impl (XMPP, if built)

WaveServerImpl implements:
    WaveletFederationProvider    (serves as the host, responds to incoming from remote)
    WaveletFederationListener.Factory  (serves as the remote, receives pushes from host)

WaveletNotificationDispatcher
    → holds @FederationHostBridge WaveletFederationListener.Factory
    → calls listenerForDomain(domain).waveletDeltaUpdate() for each participant domain
       when a local wavelet is updated
```

---

## Edge cases & failure modes

**Unknown signer on submit**: The hosting server returns `INTERNAL_SERVER_ERROR` (code 9) with
message `"Unknown signer"` (`FederationErrors.internalServerError`; see
`WaveServerImpl.submitRequest`). Although the proto reserves `NOT_AUTHORIZED` (code 4, "Signer info
not available") for exactly this case, the reference implementation never uses `NOT_AUTHORIZED` and
instead returns `INTERNAL_SERVER_ERROR`. A faithful port must return `INTERNAL_SERVER_ERROR` here.
The remote should call `postSignerInfo` first, then retry `submitRequest`.

**Unknown signer on history**: The remote calls `getDeltaSignerInfo` before applying received
deltas. If that fetch fails, the delta may still be applied without verification (depending on
retry logic), or the update is failed. The host is required to have the signer info; a failure
here is a host-side defect.

**Discontiguous history**: A remote receives version N+5 but is at version N. It buffers the
incoming deltas in `pendingDeltas` and fires a `requestHistory(N, N+5)`. History is applied
recursively until the gap is filled or the fetch fails. Failed history requests log a severe error
but currently have no retry logic — the state is left pending.

**Signature count mismatch**: `verifyDelta` throws `SignatureException` if the number of
signatures does not equal the number of unique domains in the extended address path. Remote submit
is rejected with `BAD_REQUEST`.

**Hash chain mismatch**: If `appliedAt.hash != currentVersion.hash` the remote container marks
its state corrupted. Corrupted state is not recovered automatically.

**Cert expiry / revocation**: `CachedCertPathValidator` checks expiry at validation time but
revocation checking is disabled. Expired certs cause verification failures at the next re-validation
(cache miss). There is no active cert-rotation mechanism.

**Commit without having the deltas**: If a remote receives a commit notice for a version it
hasn't seen, it creates the wavelet container and marks the commit as pending, expecting history
to arrive later.

---

## Open questions / ambiguities

### Is federation load-bearing for a single-machine Go rewrite?

**Short answer: No. Drop it or stub it with a noop.**

Longer assessment:

1. **The production codebase already runs in noop mode.** `ServerMain.buildFederationModule`
   unconditionally returns `NoOpFederationModule`. There is no configuration switch to enable an
   XMPP transport because the XMPP transport was never ported to this Apache Wave fork. Federation
   has been effectively disabled for years.

2. **No XMPP code exists.** Despite the spec defining XMPP as the transport, there is zero XMPP
   implementation in this repository. A rewrite that omits federation is not losing any
   functionality that the current codebase provides.

3. **Single-machine deployment has no use for cross-domain federation.** If all users share one
   domain, federation is never invoked. The only federation-adjacent code that runs is:
   - `CertificateManager.signDelta` (always called) when local deltas are submitted —
     but the signature is only verified if the delta comes from a remote server, which never
     happens in noop mode.
   - `WaveletNotificationDispatcher` calling `waveletDeltaUpdate` on the
     `NoOpFederationHost` for every applied delta — immediately failing, which is fine.

4. **What actually needs to be ported for correctness:**
   - The `FederationError`/`FederationException` types are used as error containers in the
     waveserver internals (`RemoteWaveletContainerImpl`), so the error type itself must exist.
   - `ProtocolSignedDelta`, `ProtocolAppliedWaveletDelta`, `ProtocolHashedVersion`, and
     `ProtocolWaveletDelta` are the core persistence and concurrency-control types — they appear in
     storage and must be preserved regardless of federation (see spec 05).
   - `CertificateManager.signDelta` is called on every local delta submission. In a Go rewrite,
     this can be a no-op that returns an empty signature list, matching `NonSigningSignatureHandler`
     behaviour.
   - `CertificateManager.verifyDelta` is only called for deltas from remote servers. In noop mode
     (no remote servers), it is never reached. A Go rewrite can implement it as a pass-through.

5. **Recommended approach for the Go rewrite:**
   - Implement the noop equivalents of `WaveletFederationProvider` and
     `WaveletFederationListener.Factory` that return errors immediately.
   - Do not implement XMPP, certificate signing, or cert chain validation.
   - Preserve the `ProtocolSignedDelta` / `ProtocolAppliedWaveletDelta` protobuf wrappers because
     they appear in the delta store (spec 05).
   - If cross-domain support is ever needed in the future, the interface layer is clean enough to
     plug in an implementation behind `WaveletFederationProvider`/`WaveletFederationListener`.

### XMPP stanza shapes

Not documented here because no implementation exists to reverse-engineer. The Google Wave
Federation Protocol specification (originally at code.google.com/p/wave-protocol/) describes the
stanza shapes. If an XMPP transport is ever implemented, that external spec is the reference.

### Extended address path / federated groups

The `address_path` field in `ProtocolWaveletDelta` enables delegation through federated group
addresses. `CertificateManagerImpl.signDelta` asserts `getAddressPathCount() == 0` and does not
support non-empty paths. The verification path handles them correctly in theory, but the signing
path does not generate them. A rewrite can safely ignore this field for now.

### Certificate revocation

Revocation checking is disabled. For a production deployment that re-enables federation, CRL or
OCSP checking should be added. The `WaveCertPathValidator` interface is the right extension point.

### Trust roots

The hardcoded StartCom CA certificates in `DefaultTrustRootsProvider` are for a CA that no
longer operates (StartCom shut down in 2018). A real federation deployment would need
a configurable trust store or rely solely on the JVM's default CAs.

---

## Source references

| Path | Role |
|------|------|
| `wave/src/proto/proto/org/waveprotocol/wave/federation/federation.protodevel` | Core message definitions: `ProtocolWaveletDelta`, `ProtocolSignedDelta`, `ProtocolSignature`, `ProtocolSignerInfo`, `ProtocolAppliedWaveletDelta`, `ProtocolHashedVersion` |
| `wave/src/proto/proto/org/waveprotocol/wave/federation/federation_error.protodevel` | `FederationError` enum and message |
| `wave/src/main/java/org/waveprotocol/wave/federation/WaveletFederationProvider.java` | Provider interface (host-side and remote-facing RPC surface) |
| `wave/src/main/java/org/waveprotocol/wave/federation/WaveletFederationListener.java` | Listener interface (push updates from host to remote) |
| `wave/src/main/java/org/waveprotocol/wave/federation/FederationTransport.java` | Transport lifecycle interface |
| `wave/src/main/java/org/waveprotocol/wave/federation/FederationErrors.java` | Factory helpers for `FederationError` values |
| `wave/src/main/java/org/waveprotocol/wave/federation/FederationException.java` | Exception wrapper around `FederationError` |
| `wave/src/main/java/org/waveprotocol/wave/federation/ProtocolHashedVersionFactory.java` | Hash chain computation: `nextHash = SHA-256(prevHash || appliedDeltaBytes)[0:20]` |
| `wave/src/main/java/org/waveprotocol/wave/federation/noop/` | Noop implementations (actually deployed) |
| `wave/src/main/java/org/waveprotocol/wave/crypto/WaveSigner.java` | Signs byte arrays with SHA1withRSA |
| `wave/src/main/java/org/waveprotocol/wave/crypto/WaveSignatureVerifier.java` | Verifies signatures; checks CN/SAN match for domain |
| `wave/src/main/java/org/waveprotocol/wave/crypto/SignerInfo.java` | In-memory cert chain with signer ID computation (PkiPath + hash) |
| `wave/src/main/java/org/waveprotocol/wave/crypto/CachedCertPathValidator.java` | PKIX cert chain validation with result cache; handles Wave OID |
| `wave/src/main/java/org/waveprotocol/wave/crypto/CertPathStore.java` | Interface: signer ID → SignerInfo lookup and store |
| `wave/src/main/java/org/waveprotocol/wave/crypto/DefaultCertPathStore.java` | In-memory `CertPathStore` implementation |
| `wave/src/main/java/org/waveprotocol/wave/crypto/WaveSignerFactory.java` | Reads PKCS#8 private key + X.509 certs from files, constructs `WaveSigner` |
| `wave/src/main/java/org/waveprotocol/wave/crypto/DefaultTrustRootsProvider.java` | JVM default trust store + two hardcoded StartCom CAs |
| `wave/src/main/java/org/waveprotocol/wave/crypto/AlgorithmUtil.java` | Maps proto enums to JCA algorithm names |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/CertificateManager.java` | Internal interface coordinating signing/verification/prefetch |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/CertificateManagerImpl.java` | Implementation: multi-domain signature verification, coalesced signer-info fetching |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/SignatureHandler.java` | Signing abstraction (signing vs noop) |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/SigningSignatureHandler.java` | Signing implementation; reads cert/key from config |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/RemoteWaveletContainerImpl.java` | Remote wavelet state machine: signature prefetch, gap detection, history backfill |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/WaveServerImpl.java` | Implements both provider (host-facing) and listener factory (remote-facing); glues waveserver to federation layer |
| `wave/src/main/java/org/waveprotocol/box/server/waveserver/WaveletNotificationDispatcher.java` | Fans out applied deltas to per-domain federation listeners |
| `wave/src/main/java/org/waveprotocol/box/server/persistence/SignerInfoStore.java` | Persistent `CertPathStore` interface |
| `wave/src/main/java/org/waveprotocol/box/server/ServerMain.java` | Entry point; always loads `NoOpFederationModule` |
