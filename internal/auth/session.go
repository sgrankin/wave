// Package auth is the authentication seam: every method resolves a request to a
// verified ParticipantId, provisions an account if policy allows, and mints a
// signed session. It replaces the Java JAAS password + X.509 stack with a
// pluggable provider chain (local / trusted-header now; tsnet / OIDC / passkey
// addable behind the same interface) over stateless signed-token sessions.
//
// Spec: docs/specs/08-authentication-accounts.md; architecture §Authentication.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
)

// Sessions issues and verifies stateless, HMAC-signed session tokens. A token
// carries the participant and an expiry; there is no server-side session table,
// so the signing key must be secret and stable across restarts. Statelessness
// trades away instant revocation — rely on a modest TTL (and key rotation to
// revoke en masse) until a revocation store is warranted.
type Sessions struct {
	key []byte
	ttl time.Duration
	clk clock.Clock
}

// minKeyLen is the minimum signing-key length (a full SHA-256 block of entropy).
const minKeyLen = 32

// NewSessions returns a Sessions signer. A nil clock uses the system clock. It
// panics if key is shorter than 32 bytes: a weak or empty key silently disables
// token security (HMAC accepts any key length), so this misconfiguration must
// fail loudly at boot rather than appear to work.
func NewSessions(key []byte, ttl time.Duration, clk clock.Clock) *Sessions {
	if len(key) < minKeyLen {
		panic(fmt.Sprintf("auth: session key must be at least %d bytes, got %d", minKeyLen, len(key)))
	}
	if clk == nil {
		clk = clock.System{}
	}
	return &Sessions{key: key, ttl: ttl, clk: clk}
}

// Issue mints a signed token for participant, valid for the configured TTL.
// Format: base64url(payload) "." base64url(HMAC-SHA256(payload)), where payload
// is "address\nexpiryUnix".
func (s *Sessions) Issue(participant id.ParticipantID) string {
	expiry := s.clk.Now().Add(s.ttl).Unix()
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(participant.Address() + "\n" + strconv.FormatInt(expiry, 10)))
	return payload + "." + s.sign(payload)
}

// Verify checks a token's signature and expiry and returns its participant.
func (s *Sessions) Verify(token string) (id.ParticipantID, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return id.ParticipantID{}, fmt.Errorf("auth: malformed token")
	}
	// Constant-time signature check before trusting any payload bytes.
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return id.ParticipantID{}, fmt.Errorf("auth: bad token signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return id.ParticipantID{}, fmt.Errorf("auth: bad token payload: %w", err)
	}
	// expiry is the trailing field; split on the LAST separator so the parse is
	// unambiguous even if the address itself contained a newline (the signature
	// already guarantees the bytes are server-issued, but don't rely on that).
	i := strings.LastIndexByte(string(raw), '\n')
	if i < 0 {
		return id.ParticipantID{}, fmt.Errorf("auth: malformed token payload")
	}
	addr, expiryStr := string(raw)[:i], string(raw)[i+1:]
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return id.ParticipantID{}, fmt.Errorf("auth: bad token expiry: %w", err)
	}
	if s.clk.Now().Unix() >= expiry {
		return id.ParticipantID{}, fmt.Errorf("auth: token expired")
	}
	p, err := id.NewParticipantID(addr)
	if err != nil {
		return id.ParticipantID{}, fmt.Errorf("auth: bad token participant: %w", err)
	}
	return p, nil
}

func (s *Sessions) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
