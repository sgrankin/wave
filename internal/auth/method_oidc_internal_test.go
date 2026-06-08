package auth

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// mintOIDC drives the real callback convergence (loginFor → MintIdP) for claims,
// returning the resolved participant. It mirrors exactly what (*OIDCMethod).callback
// does after token verification, so these tests exercise the production path without
// standing up a JWKS/token endpoint.
func mintOIDC(t *testing.T, m *OIDCMethod, claims oidcClaims) (id.ParticipantID, error) {
	t.Helper()
	return m.Service.MintIdP(httptest.NewRecorder(), m.Credentials, m.loginFor(claims))
}

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

			got, err := mintOIDC(t, m, tc.claims)
			if err != nil {
				t.Fatalf("MintIdP: %v", err)
			}
			if got.Address() != tc.wantAddr {
				t.Errorf("address = %q, want %q", got.Address(), tc.wantAddr)
			}
			// The account is provisioned at the derived address.
			if _, ok, _ := accounts.GetAccount(got); !ok {
				t.Errorf("expected account %q to be provisioned", got)
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
	got1, err := mintOIDC(t, m, first)
	if err != nil {
		t.Fatal(err)
	}
	// Same sub, different email → still the original account (sub is the key).
	second := oidcClaims{Subject: "sub-1", Email: "alice.new@corp.com", EmailVerified: true}
	got2, err := mintOIDC(t, m, second)
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
	got1, err := mintOIDC(t, m, oidcClaims{Subject: "sub-9"})
	if err != nil {
		t.Fatal(err)
	}
	if got1.Address() != "sub-9@idp.internal" {
		t.Fatalf("first address = %q, want sub-9@idp.internal", got1.Address())
	}
	// Second login: IdP now asserts a verified email in another domain. The returning
	// user must still resolve to the original bound address with NO lock-out — MintIdP
	// never re-derives or re-checks policy for a bound credential.
	got2, err := mintOIDC(t, m, oidcClaims{Subject: "sub-9", Email: "x@elsewhere.com", EmailVerified: true})
	if err != nil {
		t.Fatalf("returning user must not error: %v", err)
	}
	if got2 != got1 {
		t.Errorf("returning user resolved to %q, want the original %q", got2, got1)
	}
}

// TestOIDCRejectsClaimedAddress is the account-takeover regression for OIDC: a FIRST
// login (no credential bound to issuer+sub) whose derived address — here a reassigned
// verified email — already belongs to another account must be REJECTED, not adopted.
func TestOIDCRejectsClaimedAddress(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	m := oidcTestMethod(t, accounts, creds, "login.corp.com")

	// alice@corp.com already exists, owned by a different (unbound) identity.
	_ = accounts.PutAccount(&storage.Account{
		ID: mustPID(t, "alice@corp.com"), Kind: storage.AccountHuman,
		Human: &storage.HumanAccount{DisplayName: "the original alice"},
	})

	// A new sub presents a verified email that derives the already-owned address.
	_, err := mintOIDC(t, m, oidcClaims{Subject: "attacker-sub", Email: "alice@corp.com", EmailVerified: true})
	if err == nil {
		t.Fatal("first login deriving a claimed address must be rejected")
	}
	// No credential bound for the attacker's sub.
	if _, ok, _ := creds.GetCredential("oidc", "login.corp.com|attacker-sub"); ok {
		t.Error("attacker's credential must not be bound to a claimed address")
	}
	// Victim's display name untouched.
	acct, _, _ := accounts.GetAccount(mustPID(t, "alice@corp.com"))
	if acct.Human == nil || acct.Human.DisplayName != "the original alice" {
		t.Errorf("victim account was mutated: %+v", acct.Human)
	}
}

// TestOIDCRejectsSharedDomainParticipant is the shared-domain regression: an OIDC
// verified email with an EMPTY local part ("@corp.com") derives the shared-domain
// participant, which grants domain-wide access (spec §2.9). MintIdP must refuse to mint
// it from an IdP claim — and bind nothing, provision nothing.
func TestOIDCRejectsSharedDomainParticipant(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	m := oidcTestMethod(t, accounts, creds, "login.corp.com")

	// A verified email whose address is "@corp.com" (empty local part).
	_, err := mintOIDC(t, m, oidcClaims{Subject: "shared-sub", Email: "@corp.com", EmailVerified: true})
	if err == nil {
		t.Fatal("minting the shared-domain participant from an IdP claim must be rejected")
	}
	if !strings.Contains(err.Error(), "shared-domain") {
		t.Errorf("error = %v, want a shared-domain refusal", err)
	}
	// Nothing was provisioned at the shared-domain address.
	if _, ok, _ := accounts.GetAccount(mustPID(t, "@corp.com")); ok {
		t.Error("the shared-domain participant must not be provisioned")
	}
	// No credential was bound for the subject.
	if _, ok, _ := creds.GetCredential("oidc", "login.corp.com|shared-sub"); ok {
		t.Error("no credential must be bound when the derived address is refused")
	}
}

// TestEmailVerifiedFlexDecode: email_verified arriving as a JSON STRING ("true")
// rather than a bool must still decode (no fail-closed outage) and grant the email's
// domain. The OIDC spec types it as bool, but real-world IdPs emit the string form.
func TestEmailVerifiedFlexDecode(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`true`, true},
		{`false`, false},
		{`"true"`, true},
		{`"false"`, false},
		{`"1"`, true},
		{`"0"`, false},
		{`null`, false},
		{`"TRUE"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			var claims oidcClaims
			if err := json.Unmarshal([]byte(`{"sub":"s","email":"e@x.com","email_verified":`+tc.raw+`}`), &claims); err != nil {
				t.Fatalf("decode %s: %v", tc.raw, err)
			}
			if bool(claims.EmailVerified) != tc.want {
				t.Errorf("email_verified %s decoded to %v, want %v", tc.raw, bool(claims.EmailVerified), tc.want)
			}
		})
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
