package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// memCreds is an in-memory CredentialStore for method tests.
type memCreds struct{ m map[string]storage.Credential }

func newMemCreds() *memCreds { return &memCreds{m: map[string]storage.Credential{}} }
func (c *memCreds) key(method, subject string) string {
	return method + "\x00" + subject
}
func (c *memCreds) GetCredential(method, subject string) (storage.Credential, bool, error) {
	cr, ok := c.m[c.key(method, subject)]
	return cr, ok, nil
}
func (c *memCreds) PutCredential(cr storage.Credential) error {
	if existing, ok := c.m[c.key(cr.Method, cr.Subject)]; ok {
		cr.CreatedAt = existing.CreatedAt // preserve, like the sqlite store
	}
	c.m[c.key(cr.Method, cr.Subject)] = cr
	return nil
}
func (c *memCreds) ListByAccount(account id.ParticipantID) ([]storage.Credential, error) {
	var out []storage.Credential
	for _, cr := range c.m {
		if cr.Account == account {
			out = append(out, cr)
		}
	}
	return out, nil
}

// memAccounts is an in-package in-memory AccountStore (the *_test.go fakeAccounts
// lives in the external test package and isn't visible here).
type memAccounts struct{ m map[string]*storage.Account }

func newMemAccounts() *memAccounts { return &memAccounts{m: map[string]*storage.Account{}} }
func (a *memAccounts) GetAccount(p id.ParticipantID) (*storage.Account, bool, error) {
	acct, ok := a.m[p.Address()]
	return acct, ok, nil
}
func (a *memAccounts) PutAccount(acct *storage.Account) error {
	a.m[acct.ID.Address()] = acct
	return nil
}
func (a *memAccounts) RemoveAccount(p id.ParticipantID) error {
	delete(a.m, p.Address())
	return nil
}

func mustPID(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func devSvc(t *testing.T, accounts storage.AccountStore) *Service {
	t.Helper()
	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	svc := NewService(
		NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk),
		Provisioner{Accounts: accounts, RegisterOnFirstUse: true},
	)
	svc.SecureCookies = false
	return svc
}

// githubStub stands up a fake GitHub: the OAuth token endpoint and the user API.
func githubStub(t *testing.T, userJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("access_token=stub-token&token_type=bearer"))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(userJSON))
	})
	return httptest.NewServer(mux)
}

// newGitHubMethodForTest wires a GitHubMethod pointed at the stub server.
func newGitHubMethodForTest(t *testing.T, svc *Service, creds storage.CredentialStore, accounts storage.AccountStore, srv *httptest.Server) *GitHubMethod {
	t.Helper()
	m := NewGitHubMethod("cid", "csecret", "http://wave.local/auth/github/callback", svc, creds,
		Provisioner{Accounts: accounts, RegisterOnFirstUse: true})
	// Point the oauth endpoints and the user API at the stub.
	m.oauth.Endpoint = oauth2.Endpoint{
		AuthURL:  srv.URL + "/login/oauth/authorize",
		TokenURL: srv.URL + "/login/oauth/access_token",
	}
	githubAPIUser = srv.URL + "/user"
	t.Cleanup(func() { githubAPIUser = "https://api.github.com/user" })
	// Pin a fixed clock so credential created_at is deterministic.
	m.clk = clock.NewFixed(time.UnixMilli(2_000_000))
	return m
}

// driveCallback simulates the GitHub redirect back: issue a state cookie via start,
// then call callback with the echoed state + a code, carrying the cookie.
func driveCallback(t *testing.T, m *GitHubMethod) *httptest.ResponseRecorder {
	t.Helper()
	// start: capture the issued state cookie and the nonce (the `state` query param
	// on the redirect Location).
	startRec := httptest.NewRecorder()
	m.start(startRec, httptest.NewRequest("GET", "/auth/github/start?redirect=/app", nil))
	loc, err := url.Parse(startRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("bad redirect location: %v", err)
	}
	nonce := loc.Query().Get("state")
	if nonce == "" {
		t.Fatal("start did not set a state nonce")
	}

	cbReq := httptest.NewRequest("GET", "/auth/github/callback?code=abc&state="+nonce, nil)
	for _, c := range startRec.Result().Cookies() {
		cbReq.AddCookie(c)
	}
	cbRec := httptest.NewRecorder()
	m.callback(cbRec, cbReq)
	return cbRec
}

// TestGitHubCallbackMintsAddress: a first GitHub login mints <login>@github, binds
// the credential keyed by the numeric id, stores the login as DisplayName, and sets
// a session cookie.
func TestGitHubCallbackMintsAddress(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	srv := githubStub(t, `{"id":424242,"login":"octocat"}`)
	defer srv.Close()
	svc := devSvc(t, accounts)
	m := newGitHubMethodForTest(t, svc, creds, accounts, srv)

	rec := driveCallback(t, m)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d (body %q), want 303", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/app" {
		t.Errorf("redirect = %q, want /app", loc)
	}
	// Account minted as octocat@github with the login as DisplayName.
	acct, ok, _ := accounts.GetAccount(mustPID(t, "octocat@github"))
	if !ok {
		t.Fatal("expected octocat@github to be provisioned")
	}
	if acct.Human == nil || acct.Human.DisplayName != "octocat" {
		t.Errorf("display name = %+v, want octocat", acct.Human)
	}
	// Credential bound under the numeric id.
	cr, ok, _ := creds.GetCredential("github", "424242")
	if !ok || cr.Account != mustPID(t, "octocat@github") {
		t.Errorf("credential = %+v ok=%v, want octocat@github", cr, ok)
	}
	// A session cookie was set.
	hasSession := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wave_session" && c.Value != "" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Error("expected a session cookie")
	}
}

// TestGitHubCallbackResolvesExistingCredential: a returning user (same numeric id)
// resolves to the bound account even if the login changed — the id is the key, the
// login is not.
func TestGitHubCallbackResolvesExistingCredential(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	// Pre-bind id 424242 → an existing chosen account (a renamed user).
	_ = creds.PutCredential(storage.Credential{
		Method: "github", Subject: "424242", Account: mustPID(t, "original@github"), CreatedAt: 1,
	})
	_ = accounts.PutAccount(&storage.Account{ID: mustPID(t, "original@github"), Kind: storage.AccountHuman, Human: &storage.HumanAccount{}})

	srv := githubStub(t, `{"id":424242,"login":"renamed-login"}`)
	defer srv.Close()
	svc := devSvc(t, accounts)
	m := newGitHubMethodForTest(t, svc, creds, accounts, srv)

	rec := driveCallback(t, m)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", rec.Code)
	}
	// No new renamed-login@github account; the original is reused.
	if _, ok, _ := accounts.GetAccount(mustPID(t, "renamed-login@github")); ok {
		t.Error("a renamed login must not create a second account")
	}
	if _, ok, _ := accounts.GetAccount(mustPID(t, "original@github")); !ok {
		t.Error("the original account should still resolve")
	}
}

// TestGitHubCallbackRejectsClaimedAddress is the account-takeover regression: a
// FIRST GitHub login (no credential bound to its numeric id) whose derived address
// <login>@github already belongs to another account must be REJECTED, not adopted.
// Scenario: octocat@github exists (e.g. the original octocat, since renamed/deleted,
// freeing the login); an attacker now holding the "octocat" login under a DIFFERENT
// numeric id logs in. Without the uniqueness check this would hand them the existing
// account; MintIdP's RegisterChosen rejects it instead.
func TestGitHubCallbackRejectsClaimedAddress(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	// octocat@github already exists, owned by someone else, with NO github credential
	// bound to the attacker's numeric id.
	_ = accounts.PutAccount(&storage.Account{
		ID: mustPID(t, "octocat@github"), Kind: storage.AccountHuman,
		Human: &storage.HumanAccount{DisplayName: "the original octocat"},
	})

	srv := githubStub(t, `{"id":999999,"login":"octocat"}`)
	defer srv.Close()
	svc := devSvc(t, accounts)
	m := newGitHubMethodForTest(t, svc, creds, accounts, srv)

	rec := driveCallback(t, m)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("takeover attempt status = %d (body %q), want 403", rec.Code, rec.Body.String())
	}
	// No credential was bound for the attacker's id (binding happens only after a
	// successful uniqueness-checked provision).
	if _, ok, _ := creds.GetCredential("github", "999999"); ok {
		t.Error("attacker's credential must not be bound to a claimed address")
	}
	// The victim's account is untouched (display name not overwritten).
	acct, ok, _ := accounts.GetAccount(mustPID(t, "octocat@github"))
	if !ok || acct.Human == nil || acct.Human.DisplayName != "the original octocat" {
		t.Errorf("victim account was mutated: %+v", acct.Human)
	}
	// No session cookie was issued.
	for _, c := range rec.Result().Cookies() {
		if c.Name == "wave_session" && c.Value != "" {
			t.Error("a session cookie must not be issued on a rejected takeover")
		}
	}
}

// TestGitHubCallbackRejectsStateMismatch: a forged/absent state nonce is rejected
// (CSRF defence), no account, no cookie.
func TestGitHubCallbackRejectsStateMismatch(t *testing.T) {
	accounts := newMemAccounts()
	creds := newMemCreds()
	srv := githubStub(t, `{"id":1,"login":"x"}`)
	defer srv.Close()
	m := newGitHubMethodForTest(t, devSvc(t, accounts), creds, accounts, srv)

	// Issue a valid state cookie, but echo a DIFFERENT state value in the callback.
	startRec := httptest.NewRecorder()
	m.start(startRec, httptest.NewRequest("GET", "/auth/github/start", nil))
	cbReq := httptest.NewRequest("GET", "/auth/github/callback?code=abc&state=WRONG", nil)
	for _, c := range startRec.Result().Cookies() {
		cbReq.AddCookie(c)
	}
	cbRec := httptest.NewRecorder()
	m.callback(cbRec, cbReq)
	if cbRec.Code != http.StatusBadRequest {
		t.Errorf("state mismatch status = %d, want 400", cbRec.Code)
	}
	if strings.Contains(cbRec.Body.String(), "csecret") {
		t.Error("error body leaked a secret")
	}
}
