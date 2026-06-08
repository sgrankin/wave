package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// oidcStateCookie is the per-method state cookie name (distinct from GitHub's).
const oidcStateCookie = "wave_oidc_state"

// oidcVerifier is the subset of oidc.IDTokenVerifier this method needs; an
// interface so tests can substitute a stub that returns synthesized claims without
// a real JWKS.
type oidcVerifier interface {
	Verify(ctx context.Context, rawIDToken string) (*oidc.IDToken, error)
}

// OIDCMethod is interactive generic OIDC (OpenID Connect) login — Google, Okta,
// any compliant provider. Discovery runs once at startup (NewOIDCMethod), caching
// the verifier and endpoints. It mints the verified email when present, else
// `sub@<issuer-host>` (the fake-domain namespacing of §4): the IdP can never
// assert an address outside that namespace. The issuer+sub is the stable
// credential subject; the `name` claim is stored as the account DisplayName.
//
// Security: the redirect carries a PKCE (S256) challenge and a nonce, both bound
// into the signed state cookie. /callback verifies the raw ID token (signature +
// issuer + audience + expiry via go-oidc), the nonce claim, and the PKCE verifier
// before trusting any claim.
type OIDCMethod struct {
	Service     *Service
	Credentials storage.CredentialStore
	Provisioner Provisioner

	issuerHost string
	oauth      *oauth2.Config
	verifier   oidcVerifier
	state      *stateCodec
	clk        clock.Clock
}

// NewOIDCMethod performs OIDC discovery against issuer and returns a configured
// method. clientID/clientSecret/redirectURL are the registered OAuth client;
// redirectURL must equal the provider's registered callback (…/auth/oidc/callback).
// It fails fast if discovery fails (a misconfigured issuer must not boot silently).
func NewOIDCMethod(ctx context.Context, issuer, clientID, clientSecret, redirectURL string, svc *Service, creds storage.CredentialStore, prov Provisioner) (*OIDCMethod, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc discovery for %q: %w", issuer, err)
	}
	host, err := issuerHostOf(issuer)
	if err != nil {
		return nil, err
	}
	m := &OIDCMethod{
		Service:     svc,
		Credentials: creds,
		Provisioner: prov,
		issuerHost:  host,
		verifier:    provider.Verifier(&oidc.Config{ClientID: clientID}),
		state:       svc.newStateCodec(oidcStateCookie),
		clk:         clock.System{},
	}
	m.oauth = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
	return m, nil
}

// issuerHostOf extracts the host of an issuer URL, used as the fallback address
// domain (sub@<issuer-host>) when no verified email is present. A valid RFC-1035
// host is required so the resulting address parses.
func issuerHostOf(issuer string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("auth: oidc issuer %q has no host", issuer)
	}
	host := u.Hostname() // strips any port
	if !id.IsValidDomain(host) {
		return "", fmt.Errorf("auth: oidc issuer host %q is not a usable address domain", host)
	}
	return host, nil
}

// Name identifies the method.
func (*OIDCMethod) Name() string { return "oidc" }

// Label is the sign-in button text.
func (*OIDCMethod) Label() string { return "Sign in with SSO" }

// StartPath is the sign-in entry URL (under the mount prefix).
func (*OIDCMethod) StartPath() string { return "/auth/oidc/start" }

// Mount registers the start and callback routes.
func (m *OIDCMethod) Mount(mux *http.ServeMux, prefix string) {
	mux.Handle(prefix+"/start", http.HandlerFunc(m.start))
	mux.Handle(prefix+"/callback", http.HandlerFunc(m.callback))
}

// start issues the state cookie (nonce + PKCE verifier + redirect) and redirects
// to the provider with the S256 challenge and nonce.
func (m *OIDCMethod) start(w http.ResponseWriter, r *http.Request) {
	nonce, err := newNonce()
	if err != nil {
		http.Error(w, "auth start error", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier() // PKCE code verifier
	redirect := sanitizeRedirect(r.FormValue("redirect"))
	if err := m.state.issue(w, stateData{Nonce: nonce, Redirect: redirect, CodeVerifier: verifier}); err != nil {
		http.Error(w, "auth start error", http.StatusInternalServerError)
		return
	}
	authURL := m.oauth.AuthCodeURL(nonce,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// oidcClaims is the subset of ID-token claims we read.
type oidcClaims struct {
	Subject       string   `json:"sub"`
	Email         string   `json:"email"`
	EmailVerified flexBool `json:"email_verified"`
	Name          string   `json:"name"`
	Nonce         string   `json:"nonce"`
}

// flexBool decodes a JSON boolean OR its string encodings ("true"/"false", "1"/"0").
// The OIDC spec types email_verified as a boolean, but real providers (some Azure AD
// tenants, SAML-bridged IdPs) emit it as a JSON string. A strict bool field would
// make the whole claim decode fail and lock those users out — a fail-closed OUTAGE,
// not a bypass, but a real one. Anything not recognizably true decodes to false, so
// the security posture (only a genuine true grants the email's domain) is preserved.
type flexBool bool

// UnmarshalJSON accepts true/false, "true"/"false", "1"/"0" (case-insensitive),
// treating anything else (including null) as false.
func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(data)), `"`))
	*b = flexBool(s == "true" || s == "1")
	return nil
}

// callback verifies state, exchanges the code with the PKCE verifier, verifies the
// ID token + nonce, maps claims → address, and mints a session.
func (m *OIDCMethod) callback(w http.ResponseWriter, r *http.Request) {
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
	tok, err := m.oauth.Exchange(ctx, code, oauth2.VerifierOption(state.CodeVerifier))
	if err != nil {
		http.Error(w, "auth exchange failed", http.StatusBadGateway)
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		http.Error(w, "auth no id_token", http.StatusBadGateway)
		return
	}
	claims, err := m.verifyIDToken(ctx, rawID, state.Nonce)
	if err != nil {
		http.Error(w, "auth id_token invalid", http.StatusBadRequest)
		return
	}

	// Convergence: MintIdP resolves issuer+sub → bound account (returning), or
	// uniqueness-checks the derived address (verified email, else sub@issuer-host)
	// before binding — so a reassigned email cannot adopt another user's account.
	// The 403 body is GENERIC: appending err.Error() would leak "address X is already
	// taken", letting an attacker enumerate which derived addresses are registered.
	if _, err := m.Service.MintIdP(w, m.Credentials, m.loginFor(claims)); err != nil {
		http.Error(w, "login denied", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, state.Redirect, http.StatusSeeOther)
}

// verifyIDToken verifies the raw ID token (signature, issuer, audience, expiry via
// go-oidc), extracts the claims, and checks the bound nonce.
func (m *OIDCMethod) verifyIDToken(ctx context.Context, rawID, wantNonce string) (oidcClaims, error) {
	idToken, err := m.verifier.Verify(ctx, rawID)
	if err != nil {
		return oidcClaims{}, fmt.Errorf("verify id_token: %w", err)
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return oidcClaims{}, fmt.Errorf("decode claims: %w", err)
	}
	if claims.Subject == "" {
		return oidcClaims{}, fmt.Errorf("id_token has no sub")
	}
	if !matchNonce(wantNonce, claims.Nonce) {
		return oidcClaims{}, fmt.Errorf("id_token nonce mismatch")
	}
	return claims, nil
}

// mintPolicy is the address namespace this login may mint: the verified email's
// domain when email_verified is set, else the issuer host (sub@<issuer-host>).
func (m *OIDCMethod) mintPolicy(claims oidcClaims) MintPolicy {
	if bool(claims.EmailVerified) && claims.Email != "" {
		if p, err := id.NewParticipantID(claims.Email); err == nil {
			return DomainOnly(p.Domain())
		}
	}
	return DomainOnly(m.issuerHost)
}

// loginFor builds the MintIdP descriptor for an OIDC identity. The credential
// subject is issuer-host|sub (stable across email changes). The first-login address
// is the verified email, else sub@<issuer-host>, bounded by the matching namespace
// policy — the IdP can never assert an address outside it. The `name` claim is the
// display name; Derive runs only on a first login (no binding yet), so a returning
// user is unaffected by a later email_verified flip.
func (m *OIDCMethod) loginFor(claims oidcClaims) IdPLogin {
	return IdPLogin{
		Method:      "oidc",
		Subject:     m.issuerHost + "|" + claims.Subject,
		DisplayName: claims.Name,
		CreatedAt:   m.clk.Now().Unix(),
		Derive: func() (id.ParticipantID, MintPolicy, string, error) {
			participant, err := m.deriveAddress(claims)
			if err != nil {
				return id.ParticipantID{}, MintPolicy{}, "", err
			}
			data, _ := json.Marshal(map[string]string{"issuer_host": m.issuerHost, "sub": claims.Subject})
			return participant, m.mintPolicy(claims), string(data), nil
		},
	}
}

// deriveAddress is the address for a first OIDC login: the verified email if
// present, else sub@<issuer-host>.
func (m *OIDCMethod) deriveAddress(claims oidcClaims) (id.ParticipantID, error) {
	if bool(claims.EmailVerified) && claims.Email != "" {
		p, err := id.NewParticipantID(claims.Email)
		if err != nil {
			return id.ParticipantID{}, fmt.Errorf("verified email %q is not a valid address: %w", claims.Email, err)
		}
		return p, nil
	}
	p, err := id.NewParticipantID(claims.Subject + "@" + m.issuerHost)
	if err != nil {
		return id.ParticipantID{}, fmt.Errorf("sub@issuer %q is not a valid address: %w", claims.Subject+"@"+m.issuerHost, err)
	}
	return p, nil
}
