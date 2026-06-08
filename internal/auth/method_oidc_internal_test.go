package auth

import (
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/storage"
)

// oidcTestMethod builds an OIDCMethod with the discovery/verifier pieces filled in
// by the test (no network). issuerHost is the sub@host fallback namespace.
func oidcTestMethod(t *testing.T, accounts storage.AccountStore, creds storage.CredentialStore, issuerHost string) *OIDCMethod {
	t.Helper()
	return &OIDCMethod{
		Service:     devSvc(t, accounts),
		Credentials: creds,
		Provisioner: Provisioner{Accounts: accounts, RegisterOnFirstUse: true},
		issuerHost:  issuerHost,
		clk:         clock.NewFixed(time.UnixMilli(2_000_000)),
	}
}

// TestOIDCClaimToAddress table-tests the claim → minted address mapping and the
// MintPolicy that governs it: a verified email maps to that email (policy = its
// domain); an unverified/absent email falls back to sub@<issuer-host> (policy =
// issuer host).
func TestOIDCClaimToAddress(t *testing.T) {
	cases := []struct {
		name       string
		claims     oidcClaims
		issuerHost string
		wantAddr   string
	}{
		{
			name:       "verified email",
			claims:     oidcClaims{Subject: "sub-1", Email: "alice@corp.com", EmailVerified: true, Name: "Alice"},
			issuerHost: "login.corp.com",
			wantAddr:   "alice@corp.com",
		},
		{
			name:       "unverified email falls back to sub@issuer",
			claims:     oidcClaims{Subject: "sub-2", Email: "bob@corp.com", EmailVerified: false},
			issuerHost: "login.corp.com",
			wantAddr:   "sub-2@login.corp.com",
		},
		{
			name:       "no email falls back to sub@issuer",
			claims:     oidcClaims{Subject: "sub-3"},
			issuerHost: "accounts.google.com",
			wantAddr:   "sub-3@accounts.google.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accounts := newMemAccounts()
			creds := newMemCreds()
			m := oidcTestMethod(t, accounts, creds, tc.issuerHost)

			got, policy, err := m.resolveOrMint(tc.claims)
			if err != nil {
				t.Fatalf("resolveOrMint: %v", err)
			}
			if got.Address() != tc.wantAddr {
				t.Errorf("address = %q, want %q", got.Address(), tc.wantAddr)
			}
			// The returned policy must permit the address it produced (it is what
			// MintSession enforces).
			if err := policy.Permits(got); err != nil {
				t.Errorf("returned policy rejects its own address %q: %v", got, err)
			}
			// The credential is bound under issuer-host|sub.
			subject := tc.issuerHost + "|" + tc.claims.Subject
			cr, ok, _ := creds.GetCredential("oidc", subject)
			if !ok || cr.Account != got {
				t.Errorf("credential = %+v ok=%v, want account %q", cr, ok, got)
			}
		})
	}
}

// TestOIDCResolveReturningUser: a second login with the same issuer+sub resolves to
// the already-bound account, even if the email claim changed.
func TestOIDCResolveReturningUser(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	m := oidcTestMethod(t, accounts, creds, "login.corp.com")

	first := oidcClaims{Subject: "sub-1", Email: "alice@corp.com", EmailVerified: true}
	got1, _, err := m.resolveOrMint(first)
	if err != nil {
		t.Fatal(err)
	}
	// Same sub, different email → still the original account (sub is the key).
	second := oidcClaims{Subject: "sub-1", Email: "alice.new@corp.com", EmailVerified: true}
	got2, _, err := m.resolveOrMint(second)
	if err != nil {
		t.Fatal(err)
	}
	if got1 != got2 {
		t.Errorf("returning user resolved to %q, want the original %q", got2, got1)
	}
}

// TestOIDCReturningUserEmailStatusFlip: a user first bound under sub@issuer-host
// (no verified email) logs in again after the IdP starts asserting a verified email
// in a DIFFERENT domain. They must still resolve to the original bound address, and
// the returned policy must permit it (no lock-out from the freshly-derived policy).
func TestOIDCReturningUserEmailStatusFlip(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	m := oidcTestMethod(t, accounts, creds, "idp.internal")

	// First login: no verified email → bound as sub-9@idp.internal.
	got1, _, err := m.resolveOrMint(oidcClaims{Subject: "sub-9"})
	if err != nil {
		t.Fatal(err)
	}
	if got1.Address() != "sub-9@idp.internal" {
		t.Fatalf("first address = %q, want sub-9@idp.internal", got1.Address())
	}
	// Second login: IdP now asserts a verified email in another domain.
	got2, policy, err := m.resolveOrMint(oidcClaims{Subject: "sub-9", Email: "x@elsewhere.com", EmailVerified: true})
	if err != nil {
		t.Fatalf("returning user must not error: %v", err)
	}
	if got2 != got1 {
		t.Errorf("returning user resolved to %q, want the original %q", got2, got1)
	}
	if err := policy.Permits(got2); err != nil {
		t.Errorf("returned policy must permit the bound address (no lock-out): %v", err)
	}
}

// TestOIDCMintPolicyEnforced: provisioning goes through MintSession, which rejects
// an address outside the policy. The mapping derives an in-policy address, so a
// crafted mismatch (a verified email whose domain differs from a DomainOnly applied
// by a caller) cannot leak — exercised here via mintPolicy directly.
func TestOIDCMintPolicy(t *testing.T) {
	m := oidcTestMethod(t, newMemAccounts(), newMemCreds(), "login.corp.com")

	// Verified email → policy permits that email's domain, rejects others.
	pol := m.mintPolicy(oidcClaims{Email: "alice@corp.com", EmailVerified: true})
	if err := pol.Permits(mustPID(t, "alice@corp.com")); err != nil {
		t.Errorf("verified-email policy should permit alice@corp.com: %v", err)
	}
	if err := pol.Permits(mustPID(t, "alice@evil.com")); err == nil {
		t.Error("verified-email policy must reject another domain")
	}

	// No verified email → policy is the issuer host.
	pol2 := m.mintPolicy(oidcClaims{Subject: "s"})
	if err := pol2.Permits(mustPID(t, "s@login.corp.com")); err != nil {
		t.Errorf("issuer-host policy should permit s@login.corp.com: %v", err)
	}
	if err := pol2.Permits(mustPID(t, "s@example.com")); err == nil {
		t.Error("issuer-host policy must reject another domain")
	}
}
