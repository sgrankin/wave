package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
)

func testCodec(clk clock.Clock) *stateCodec {
	return &stateCodec{
		key:        []byte("0123456789abcdef0123456789abcdef"),
		cookieName: "wave_test_state",
		secure:     false,
		clk:        clk,
	}
}

// roundTripCookie copies the Set-Cookie from a recorder onto a fresh request, so
// verify can read what issue wrote.
func roundTripCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "/auth/x/callback", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

func TestStateSignVerifyRoundTrip(t *testing.T) {
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	c := testCodec(clk)

	rec := httptest.NewRecorder()
	in := stateData{Nonce: "nonce-1", Redirect: "/app", CodeVerifier: "pkce-verifier"}
	if err := c.issue(rec, in); err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := c.verify(roundTripCookie(t, rec))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Nonce != "nonce-1" || got.Redirect != "/app" || got.CodeVerifier != "pkce-verifier" {
		t.Errorf("round trip = %+v, want nonce-1/app/pkce-verifier", got)
	}
}

func TestStateRejectsTamperAndExpiry(t *testing.T) {
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	c := testCodec(clk)

	rec := httptest.NewRecorder()
	if err := c.issue(rec, stateData{Nonce: "n", Redirect: "/"}); err != nil {
		t.Fatal(err)
	}
	cookie := rec.Result().Cookies()[0]

	// Tampered value rejects (signature check).
	bad := httptest.NewRequest("GET", "/", nil)
	bad.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value + "x"})
	if _, err := c.verify(bad); err == nil {
		t.Error("tampered state cookie should be rejected")
	}

	// A cookie signed with a different key rejects.
	other := testCodec(clk)
	other.key = []byte("ffffffffffffffffffffffffffffffff")
	if _, err := other.verify(roundTripCookie(t, rec)); err == nil {
		t.Error("state cookie under a different key should be rejected")
	}

	// Missing cookie rejects.
	if _, err := c.verify(httptest.NewRequest("GET", "/", nil)); err == nil {
		t.Error("missing state cookie should be rejected")
	}

	// Expired rejects.
	clk.Advance(stateTTL + time.Second)
	if _, err := c.verify(roundTripCookie(t, rec)); err == nil {
		t.Error("expired state cookie should be rejected")
	}
}

func TestMatchNonceConstantTime(t *testing.T) {
	if !matchNonce("abc", "abc") {
		t.Error("equal nonces should match")
	}
	if matchNonce("abc", "abd") {
		t.Error("different nonces should not match")
	}
	if matchNonce("", "") {
		t.Error("empty issued nonce must never match (avoids accepting a missing state)")
	}
}

// TestIssuerHostOf checks the OIDC issuer→host derivation used for the sub@host
// fallback address namespace.
func TestIssuerHostOf(t *testing.T) {
	cases := []struct {
		issuer  string
		want    string
		wantErr bool
	}{
		{"https://accounts.google.com", "accounts.google.com", false},
		{"https://login.example.com:8443/oidc", "login.example.com", false},
		{"not a url", "", true},
		{"https://", "", true},
	}
	for _, tc := range cases {
		got, err := issuerHostOf(tc.issuer)
		if (err != nil) != tc.wantErr {
			t.Errorf("issuerHostOf(%q) err = %v, wantErr %v", tc.issuer, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("issuerHostOf(%q) = %q, want %q", tc.issuer, got, tc.want)
		}
	}
}
