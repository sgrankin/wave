package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/auth"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeAccounts is an in-memory AccountStore for provisioner tests.
type fakeAccounts struct{ m map[string]*storage.Account }

func newFakeAccounts() *fakeAccounts { return &fakeAccounts{m: map[string]*storage.Account{}} }
func (f *fakeAccounts) GetAccount(p id.ParticipantID) (*storage.Account, bool, error) {
	a, ok := f.m[p.Address()]
	return a, ok, nil
}
func (f *fakeAccounts) PutAccount(a *storage.Account) error { f.m[a.ID.Address()] = a; return nil }
func (f *fakeAccounts) RemoveAccount(p id.ParticipantID) error {
	delete(f.m, p.Address())
	return nil
}

func TestSessionRoundTrip(t *testing.T) {
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	s := auth.NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk)
	alice := pid(t, "alice@example.com")

	tok := s.Issue(alice)
	got, err := s.Verify(tok)
	if err != nil || got != alice {
		t.Fatalf("verify = %v, %v; want %v", got, err, alice)
	}

	// Expired after the TTL.
	clk.Advance(2 * time.Hour)
	if _, err := s.Verify(tok); err == nil {
		t.Error("expected expired token to be rejected")
	}
	clk.Set(time.UnixMilli(1_000_000))

	// Wrong key rejects.
	other := auth.NewSessions([]byte("ffffffffffffffffffffffffffffffff"), time.Hour, clk)
	if _, err := other.Verify(tok); err == nil {
		t.Error("expected token signed with a different key to be rejected")
	}
	// Tampered signature/payload/format reject.
	for _, bad := range []string{tok + "x", "garbage", "a.b", tok[:len(tok)-2]} {
		if _, err := s.Verify(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestShortKeyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected NewSessions to panic on a short key")
		}
	}()
	auth.NewSessions([]byte("too-short"), time.Hour, nil)
}

func TestLocalProvider(t *testing.T) {
	alice := pid(t, "alice@example.com")
	p, ok, err := auth.Local{User: alice}.Authenticate(httptest.NewRequest("GET", "/", nil))
	if err != nil || !ok || p != alice {
		t.Errorf("local = %v, %v, %v; want %v, true, nil", p, ok, err, alice)
	}
}

func TestTrustedHeader(t *testing.T) {
	th := auth.TrustedHeader{Header: "X-Auth-User", Domain: "example.com"}

	// Full address passes through.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Auth-User", "alice@example.com")
	if p, ok, err := th.Authenticate(r); !ok || err != nil || p != pid(t, "alice@example.com") {
		t.Errorf("full address = %v, %v, %v", p, ok, err)
	}
	// Bare username gets the default domain.
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Auth-User", "bob")
	if p, ok, err := th.Authenticate(r); !ok || err != nil || p != pid(t, "bob@example.com") {
		t.Errorf("bare username = %v, %v, %v", p, ok, err)
	}
	// Absent header → not found (ok=false), no error.
	if _, ok, err := th.Authenticate(httptest.NewRequest("GET", "/", nil)); ok || err != nil {
		t.Errorf("absent header = ok %v err %v, want false/nil", ok, err)
	}
	// Bare username with no default domain → error.
	noDomain := auth.TrustedHeader{Header: "X-Auth-User"}
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Auth-User", "carol")
	if _, _, err := noDomain.Authenticate(r); err == nil {
		t.Error("bare username with no default domain should error")
	}
}

func TestProvisioner(t *testing.T) {
	accounts := newFakeAccounts()
	alice := pid(t, "alice@example.com")

	// Without register-on-first-use, an unknown identity is rejected.
	strict := auth.Provisioner{Accounts: accounts, RegisterOnFirstUse: false}
	if err := strict.Ensure(alice); err == nil {
		t.Error("strict provisioner should reject an unknown identity")
	}
	// With it, the account is created.
	open := auth.Provisioner{Accounts: accounts, RegisterOnFirstUse: true}
	if err := open.Ensure(alice); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, ok, _ := accounts.GetAccount(alice); !ok {
		t.Error("expected account to be provisioned")
	}
	// Idempotent for an existing account, even under the strict policy.
	if err := strict.Ensure(alice); err != nil {
		t.Errorf("ensure existing under strict policy: %v", err)
	}
}

func TestServiceCookieAndChain(t *testing.T) {
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	sessions := auth.NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk)
	accounts := newFakeAccounts()
	svc := auth.NewService(sessions,
		auth.Provisioner{Accounts: accounts, RegisterOnFirstUse: true},
		auth.TrustedHeader{Header: "X-Auth-User", Domain: "example.com"})
	alice := pid(t, "alice@example.com")

	// No cookie + trusted header → authenticated and provisioned.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Auth-User", "alice")
	got, ok, err := svc.Authenticate(r)
	if err != nil || !ok || got != alice {
		t.Fatalf("chain auth = %v, %v, %v", got, ok, err)
	}
	if _, ok, _ := accounts.GetAccount(alice); !ok {
		t.Error("expected provisioning on first chain auth")
	}

	// A valid session cookie authenticates without any provider header.
	rec := httptest.NewRecorder()
	svc.SetCookie(rec, alice)
	r2 := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		r2.AddCookie(c)
	}
	if got, ok, err := svc.Authenticate(r2); err != nil || !ok || got != alice {
		t.Errorf("cookie auth = %v, %v, %v", got, ok, err)
	}

	// No identity at all → ok=false.
	if _, ok, err := svc.Authenticate(httptest.NewRequest("GET", "/", nil)); ok || err != nil {
		t.Errorf("no identity = ok %v err %v, want false/nil", ok, err)
	}
}

func TestMiddleware(t *testing.T) {
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	svc := auth.NewService(auth.NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk),
		auth.Provisioner{Accounts: newFakeAccounts(), RegisterOnFirstUse: true},
		auth.TrustedHeader{Header: "X-Auth-User", Domain: "example.com"})
	alice := pid(t, "alice@example.com")

	var seen id.ParticipantID
	var sawParticipant bool
	h := svc.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, sawParticipant = auth.ParticipantFrom(r.Context())
	}))

	// Unauthenticated → 401, handler not reached.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth status = %d, want 401", rec.Code)
	}
	if sawParticipant {
		t.Error("handler should not run when unauthenticated")
	}

	// Authenticated → handler runs with the participant in context.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Auth-User", "alice")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !sawParticipant || seen != alice {
		t.Errorf("context participant = %v (seen=%v), want %v", seen, sawParticipant, alice)
	}
}
