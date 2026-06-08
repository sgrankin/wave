package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// githubAPIUser is the GitHub user endpoint. A field, so tests can point it at an
// httptest server.
var githubAPIUser = "https://api.github.com/user"

// githubStateCookie is the per-method state cookie name (distinct from OIDC's).
const githubStateCookie = "wave_github_state"

// GitHubMethod is interactive GitHub OAuth login. It mints `<login>@github`
// addresses (the fake-domain namespacing of §4) — a GitHub login can never claim
// an address in another domain. The stable numeric GitHub id is the credential
// subject (the login can change; the id can't), so a renamed user keeps their
// account; the login is stored as the account DisplayName.
//
// Flow: /start issues a signed, short-TTL state cookie (nonce + redirect) and
// redirects to GitHub; /callback verifies the state (constant-time nonce match),
// exchanges the code, reads {id,login}, resolves the credential → account (or
// mints <login>@github on first login under the GitHub MintPolicy), and sets the
// session cookie via Service.MintSession.
type GitHubMethod struct {
	ClientID     string
	ClientSecret string
	Service      *Service
	Credentials  storage.CredentialStore
	Provisioner  Provisioner
	// Policy is the address namespace GitHub may mint — DomainOnly("github").
	Policy MintPolicy

	oauth *oauth2.Config
	state *stateCodec
	clk   clock.Clock
}

// NewGitHubMethod wires a GitHubMethod with its oauth2 config and state codec.
// redirectBaseURL is the externally reachable base (scheme+host), used to build the
// OAuth redirect URL GitHub calls back; it must match the GitHub app's registered
// callback. A nil clock uses the system clock.
func NewGitHubMethod(clientID, clientSecret, redirectURL string, svc *Service, creds storage.CredentialStore, prov Provisioner) *GitHubMethod {
	m := &GitHubMethod{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Service:      svc,
		Credentials:  creds,
		Provisioner:  prov,
		Policy:       DomainOnly("github"),
		clk:          clock.System{},
	}
	m.oauth = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     github.Endpoint,
		RedirectURL:  redirectURL,
		Scopes:       []string{"read:user"},
	}
	m.state = svc.newStateCodec(githubStateCookie)
	return m
}

// Name identifies the method.
func (*GitHubMethod) Name() string { return "github" }

// Label is the sign-in button text.
func (*GitHubMethod) Label() string { return "Sign in with GitHub" }

// StartPath is the sign-in entry URL (under the mount prefix).
func (*GitHubMethod) StartPath() string { return "/auth/github/start" }

// Mount registers the start and callback routes.
func (m *GitHubMethod) Mount(mux *http.ServeMux, prefix string) {
	mux.Handle(prefix+"/start", http.HandlerFunc(m.start))
	mux.Handle(prefix+"/callback", http.HandlerFunc(m.callback))
}

// start issues the state cookie and redirects to GitHub's authorize endpoint.
func (m *GitHubMethod) start(w http.ResponseWriter, r *http.Request) {
	nonce, err := newNonce()
	if err != nil {
		http.Error(w, "auth start error", http.StatusInternalServerError)
		return
	}
	redirect := sanitizeRedirect(r.FormValue("redirect"))
	if err := m.state.issue(w, stateData{Nonce: nonce, Redirect: redirect}); err != nil {
		http.Error(w, "auth start error", http.StatusInternalServerError)
		return
	}
	// The nonce rides the upstream `state` parameter; GitHub echoes it to /callback,
	// where it is compared against the signed cookie (CSRF defence).
	http.Redirect(w, r, m.oauth.AuthCodeURL(nonce), http.StatusSeeOther)
}

// callback verifies state, exchanges the code, reads the GitHub identity, and
// mints a session.
func (m *GitHubMethod) callback(w http.ResponseWriter, r *http.Request) {
	state, err := m.state.verify(r)
	if err != nil {
		http.Error(w, "auth callback error", http.StatusBadRequest)
		return
	}
	m.state.clear(w)
	if !matchNonce(state.Nonce, r.FormValue("state")) {
		http.Error(w, "auth state mismatch", http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")
	if code == "" {
		http.Error(w, "auth missing code", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, err := m.oauth.Exchange(ctx, code)
	if err != nil {
		http.Error(w, "auth exchange failed", http.StatusBadGateway)
		return
	}
	gh, err := m.fetchUser(ctx, tok)
	if err != nil {
		http.Error(w, "auth user fetch failed", http.StatusBadGateway)
		return
	}

	participant, err := m.resolveOrMint(gh)
	if err != nil {
		http.Error(w, "login denied: "+err.Error(), http.StatusForbidden)
		return
	}
	// Convergence: policy-checked provision + session cookie. GitHub's policy is the
	// constant @github namespace (resolved and minted addresses are both *@github),
	// so it permits the participant in either case. The display name is the login.
	if err := m.Service.MintSession(w, participant, gh.Login, m.Policy); err != nil {
		http.Error(w, "login denied: "+err.Error(), http.StatusForbidden)
		return
	}
	http.Redirect(w, r, state.Redirect, http.StatusSeeOther)
}

// githubUser is the subset of the GitHub user endpoint we read.
type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// fetchUser reads {id,login} from the GitHub user API using the access token.
func (m *GitHubMethod) fetchUser(ctx context.Context, tok *oauth2.Token) (githubUser, error) {
	client := m.oauth.Client(ctx, tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIUser, nil)
	if err != nil {
		return githubUser{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return githubUser{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return githubUser{}, fmt.Errorf("github user endpoint: status %d", resp.StatusCode)
	}
	var u githubUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return githubUser{}, fmt.Errorf("decode github user: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return githubUser{}, fmt.Errorf("github user missing id/login")
	}
	return u, nil
}

// resolveOrMint maps the GitHub identity to a Wave address: an existing credential
// (keyed by the stable numeric id) → its account; otherwise the derived
// <login>@github, recorded as a new credential. The login is NOT the key (it can
// change); the numeric id is.
func (m *GitHubMethod) resolveOrMint(gh githubUser) (id.ParticipantID, error) {
	subject := strconv.FormatInt(gh.ID, 10)
	if p, ok, err := resolveCredential(m.Credentials, "github", subject); err != nil {
		return id.ParticipantID{}, err
	} else if ok {
		return p, nil
	}
	// First login: derive <login>@github and bind the credential. MintSession (the
	// caller) re-checks the policy before issuing the cookie; we check here too so a
	// bad login can't even be bound.
	participant, err := id.NewParticipantID(gh.Login + "@github")
	if err != nil {
		return id.ParticipantID{}, fmt.Errorf("github login %q is not a valid address: %w", gh.Login, err)
	}
	if err := m.Policy.Permits(participant); err != nil {
		return id.ParticipantID{}, err
	}
	data, _ := json.Marshal(map[string]string{"login": gh.Login})
	if err := bindCredential(m.Credentials, "github", subject, participant, string(data), m.clk.Now().Unix()); err != nil {
		return id.ParticipantID{}, err
	}
	return participant, nil
}
