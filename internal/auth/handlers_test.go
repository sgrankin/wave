package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/auth"
	"github.com/sgrankin/wave/internal/clock"
)

func devService(accounts *fakeAccounts) *auth.Service {
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	svc := auth.NewService(
		auth.NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk),
		auth.Provisioner{Accounts: accounts, RegisterOnFirstUse: true},
	)
	svc.SecureCookies = false
	return svc
}

// sessionCookie returns the wave_session cookie from a response, or fails.
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == "wave_session" {
			return c
		}
	}
	t.Fatal("no wave_session cookie set")
	return nil
}

// TestDevLoginForm: GET /login with no user serves the address-entry form.
func TestDevLoginForm(t *testing.T) {
	h := devService(newFakeAccounts()).DevLoginHandler("example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login?redirect=/app", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<form") || !strings.Contains(body, "Sign in") {
		t.Errorf("expected a login form, got: %s", body)
	}
	// The redirect target round-trips into the form (escaped).
	if !strings.Contains(body, "/app") {
		t.Errorf("expected redirect preserved in form, got: %s", body)
	}
}

// TestDevLoginSetsCookieAndProvisions: GET /login?user=… trusts the address,
// provisions it, sets the session cookie, and redirects.
func TestDevLoginSetsCookieAndProvisions(t *testing.T) {
	accounts := newFakeAccounts()
	h := devService(accounts).DevLoginHandler("example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login?user=alice@example.com&redirect=/app", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/app" {
		t.Errorf("Location = %q, want /app", loc)
	}
	sessionCookie(t, rec.Result())
	if _, ok, _ := accounts.GetAccount(pid(t, "alice@example.com")); !ok {
		t.Error("expected the address to be provisioned on dev login")
	}
}

// TestDevLoginBareUsername: a bare username gets the default domain appended.
func TestDevLoginBareUsername(t *testing.T) {
	accounts := newFakeAccounts()
	h := devService(accounts).DevLoginHandler("example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login?user=bob", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if _, ok, _ := accounts.GetAccount(pid(t, "bob@example.com")); !ok {
		t.Error("expected bob@example.com to be provisioned from bare username")
	}
}

// TestDevLoginSanitizesRedirect: an off-site (open-redirect) target is rejected
// and falls back to "/".
func TestDevLoginSanitizesRedirect(t *testing.T) {
	h := devService(newFakeAccounts()).DevLoginHandler("example.com")
	for _, bad := range []string{
		"//evil.com", "https://evil.com", "javascript:alert(1)",
		`/\evil.com`, `/\/evil.com`, `/\\evil.com`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/login?user=alice@example.com&redirect="+bad, nil))
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Errorf("redirect %q → Location %q, want / (open-redirect blocked)", bad, loc)
		}
	}
}

// TestDevLoginInvalidAddress: a malformed address is a 400, no cookie.
func TestDevLoginInvalidAddress(t *testing.T) {
	h := devService(newFakeAccounts()).DevLoginHandler("example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login?user=not@an@address", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestWhoAmI: behind Middleware, /whoami reports the authenticated address as
// JSON; without a session it is 401.
func TestWhoAmI(t *testing.T) {
	accounts := newFakeAccounts()
	svc := devService(accounts)
	h := svc.Middleware(svc.WhoAmIHandler())

	// Unauthenticated → 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/whoami", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth status = %d, want 401", rec.Code)
	}

	// With a valid session cookie → 200 + {"address": ...}.
	mint := httptest.NewRecorder()
	svc.SetCookie(mint, pid(t, "alice@example.com"))
	r := httptest.NewRequest("GET", "/whoami", nil)
	for _, c := range mint.Result().Cookies() {
		r.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r)
	if rec2.Code != http.StatusOK {
		t.Fatalf("auth status = %d, want 200", rec2.Code)
	}
	var got struct{ Address string }
	if err := json.Unmarshal(rec2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Address != "alice@example.com" {
		t.Errorf("address = %q, want alice@example.com", got.Address)
	}
}

// TestProxyLoginHandler: LoginHandler authenticates via the chain (trusted
// header) and sets a session cookie before redirecting.
func TestProxyLoginHandler(t *testing.T) {
	accounts := newFakeAccounts()
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	svc := auth.NewService(
		auth.NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk),
		auth.Provisioner{Accounts: accounts, RegisterOnFirstUse: true},
		auth.TrustedHeader{Header: "X-Auth-User", Domain: "example.com"},
	)
	h := svc.LoginHandler()

	// No identity → 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-identity status = %d, want 401", rec.Code)
	}

	// Trusted header → cookie + redirect.
	r := httptest.NewRequest("GET", "/login?redirect=/app", nil)
	r.Header.Set("X-Auth-User", "alice@example.com")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r)
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec2.Code)
	}
	sessionCookie(t, rec2.Result())
}

func TestLogoutClearsCookie(t *testing.T) {
	svc := devService(newFakeAccounts())
	rec := httptest.NewRecorder()
	svc.LogoutHandler().ServeHTTP(rec, httptest.NewRequest("POST", "/logout", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wave_session" && (c.MaxAge < 0 || c.Value == "") {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}
}
