package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sgrankin/wave/internal/clock"
)

// stateTTL bounds how long an OAuth/OIDC redirect may sit before its state cookie
// expires. The IdP round-trip is interactive (seconds); a few minutes covers a
// slow user without leaving a usable CSRF/replay window open.
const stateTTL = 10 * time.Minute

// stateData is the payload carried across an OAuth/OIDC redirect: a random nonce
// (bound into the upstream `state`/`nonce` parameters to defeat CSRF/replay), the
// sanitized post-login redirect target, an expiry, and, for OIDC, the PKCE code
// verifier. It is signed (not encrypted) and stored in a short-TTL cookie; it
// holds no secret beyond the verifier, whose secrecy the cookie's HttpOnly+Secure
// attributes protect.
type stateData struct {
	Nonce        string `json:"n"`
	Redirect     string `json:"r"`
	Expiry       int64  `json:"e"` // unix seconds
	CodeVerifier string `json:"v,omitempty"`
}

// stateCodec signs and verifies state payloads with HMAC-SHA256 and issues/reads
// the short-TTL cookie that carries them. The key is the session signing key
// (already restart-stable and secret); a distinct cookie name per method keeps
// concurrent flows from clobbering each other.
type stateCodec struct {
	key        []byte
	cookieName string
	secure     bool
	clk        clock.Clock
}

// newStateCodec builds a state codec for an interactive method, keyed by the
// Service's session signing key (already restart-stable and secret) and matching
// the Service's cookie-Secure policy. cookieName must be unique per method so
// concurrent flows don't clobber each other's state.
func (s *Service) newStateCodec(cookieName string) *stateCodec {
	return &stateCodec{
		key:        s.sessions.key,
		cookieName: cookieName,
		secure:     s.SecureCookies,
		clk:        s.sessions.clk,
	}
}

// newNonce returns a fresh URL-safe random nonce.
func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// issue signs data and writes it to the state cookie on w, returning the nonce the
// caller binds into the upstream `state` parameter. data.Expiry is set here.
func (c *stateCodec) issue(w http.ResponseWriter, data stateData) error {
	data.Expiry = c.clk.Now().Add(stateTTL).Unix()
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("auth: encode state: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	token := payload + "." + c.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     c.cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(stateTTL / time.Second),
	})
	return nil
}

// verify reads and validates the state cookie from r: constant-time signature
// check, then expiry. It returns the decoded payload. The caller still must
// compare the returned Nonce against the upstream-echoed `state` (constant-time)
// to complete the CSRF defence.
func (c *stateCodec) verify(r *http.Request) (stateData, error) {
	cookie, err := r.Cookie(c.cookieName)
	if err != nil {
		return stateData{}, fmt.Errorf("auth: missing state cookie")
	}
	payload, sig, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return stateData{}, fmt.Errorf("auth: malformed state cookie")
	}
	if !hmac.Equal([]byte(sig), []byte(c.sign(payload))) {
		return stateData{}, fmt.Errorf("auth: bad state signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return stateData{}, fmt.Errorf("auth: bad state payload: %w", err)
	}
	var data stateData
	if err := json.Unmarshal(raw, &data); err != nil {
		return stateData{}, fmt.Errorf("auth: decode state: %w", err)
	}
	if c.clk.Now().Unix() >= data.Expiry {
		return stateData{}, fmt.Errorf("auth: state expired")
	}
	return data, nil
}

// clear expires the state cookie (called after a successful callback, so a stale
// cookie can't be replayed).
func (c *stateCodec) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (c *stateCodec) sign(payload string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// matchNonce reports whether the upstream-echoed state value matches the nonce the
// flow issued, in constant time (a CSRF/forgery check).
func matchNonce(issued, echoed string) bool {
	return issued != "" && hmac.Equal([]byte(issued), []byte(echoed))
}
