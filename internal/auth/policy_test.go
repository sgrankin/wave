package auth_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/auth"
	"github.com/sgrankin/wave/internal/clock"
)

// newService builds a Service over an in-memory account store for mint tests.
func newService(t *testing.T, accounts *fakeAccounts, register bool) *auth.Service {
	t.Helper()
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	svc := auth.NewService(
		auth.NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk),
		auth.Provisioner{Accounts: accounts, RegisterOnFirstUse: register},
	)
	svc.SecureCookies = false
	return svc
}

func TestMintPolicyPermits(t *testing.T) {
	any := auth.AnyAddress()
	gh := auth.DomainOnly("github")

	// AnyAddress permits anything.
	if err := any.Permits(pid(t, "alice@example.com")); err != nil {
		t.Errorf("AnyAddress should permit any address: %v", err)
	}
	// DomainOnly permits its domain, rejects others.
	if err := gh.Permits(pid(t, "alice@github")); err != nil {
		t.Errorf("DomainOnly(github) should permit alice@github: %v", err)
	}
	if err := gh.Permits(pid(t, "alice@example.com")); err == nil {
		t.Error("DomainOnly(github) must reject alice@example.com")
	}
	// The zero value is fail-closed.
	var zero auth.MintPolicy
	if err := zero.Permits(pid(t, "alice@example.com")); err == nil {
		t.Error("zero MintPolicy must permit nothing")
	}
}

// TestMintSessionRejectsOutOfPolicy is the security-boundary test: a method minting
// outside its namespace fails, no account is created, and no cookie is set.
func TestMintSessionRejectsOutOfPolicy(t *testing.T) {
	accounts := newFakeAccounts()
	svc := newService(t, accounts, true)

	// A "GitHub" method (policy DomainOnly("github")) tries to claim an example.com
	// address — the cross-namespace attack the boundary blocks.
	rec := httptest.NewRecorder()
	err := svc.MintSession(rec, pid(t, "alice@example.com"), "Alice", auth.DomainOnly("github"))
	if err == nil {
		t.Fatal("MintSession must reject an address outside the method's namespace")
	}
	if _, ok, _ := accounts.GetAccount(pid(t, "alice@example.com")); ok {
		t.Error("no account should be provisioned for a rejected mint")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("no session cookie should be set for a rejected mint")
	}

	// The in-policy address succeeds, provisions with the display name, and sets a cookie.
	rec2 := httptest.NewRecorder()
	if err := svc.MintSession(rec2, pid(t, "alice@github"), "Alice", auth.DomainOnly("github")); err != nil {
		t.Fatalf("in-policy mint failed: %v", err)
	}
	acct, ok, _ := accounts.GetAccount(pid(t, "alice@github"))
	if !ok {
		t.Fatal("in-policy mint should provision the account")
	}
	if acct.Human == nil || acct.Human.DisplayName != "Alice" {
		t.Errorf("display name = %+v, want Alice", acct.Human)
	}
	sessionCookie(t, rec2.Result())
}

// TestMintChosenUniqueness: a chosen address is registered once; a second account
// claiming the same chosen address is rejected (passkey/dev chosen-registration).
func TestMintChosenUniqueness(t *testing.T) {
	accounts := newFakeAccounts()
	svc := newService(t, accounts, true)
	policy := auth.DomainOnly("example.com")

	rec := httptest.NewRecorder()
	if err := svc.MintChosen(rec, pid(t, "alice@example.com"), "", policy); err != nil {
		t.Fatalf("first chosen registration failed: %v", err)
	}
	// A different user cannot claim the same address.
	rec2 := httptest.NewRecorder()
	if err := svc.MintChosen(rec2, pid(t, "alice@example.com"), "", policy); err == nil {
		t.Error("MintChosen must reject an already-taken address")
	}
	// Out of policy is rejected too.
	rec3 := httptest.NewRecorder()
	if err := svc.MintChosen(rec3, pid(t, "bob@other.com"), "", policy); err == nil {
		t.Error("MintChosen must reject an out-of-policy address")
	}
}
