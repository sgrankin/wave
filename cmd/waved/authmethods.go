package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sgrankin/wave/internal/auth"
	"github.com/sgrankin/wave/internal/storage/sqlite"
)

// buildInteractiveMethods constructs the enabled INTERACTIVE auth methods (GitHub
// OAuth, generic OIDC). Stateless methods (dev, proxy) are built inline in
// buildAuth. Each converges on Service.SetCookie via the policy-checked
// MintSession path.
func buildInteractiveMethods(ctx context.Context, cfg config, svc *auth.Service, store *sqlite.Store) ([]auth.Method, error) {
	var methods []auth.Method
	if cfg.authGitHub {
		gh, err := buildGitHubMethod(cfg, svc, store)
		if err != nil {
			return nil, err
		}
		methods = append(methods, gh)
	}
	if cfg.authOIDC {
		om, err := buildOIDCMethod(ctx, cfg, svc, store)
		if err != nil {
			return nil, err
		}
		methods = append(methods, om)
	}
	return methods, nil
}

// firstNonEmpty returns the first non-empty argument (flag value, then env), so a
// secret may come from either the flag or its WAVED_ env var. (The generic env
// fallback in applyEnvDefaults covers the flags; this also reads the secret-only
// env names that have no dedicated flag.)
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// buildGitHubMethod constructs the GitHub OAuth method. The client id/secret come
// from the flags or WAVED_GITHUB_CLIENT_ID / WAVED_GITHUB_CLIENT_SECRET (secrets
// are read here, never logged). The callback URL is derived from -auth-public-url.
func buildGitHubMethod(cfg config, svc *auth.Service, store *sqlite.Store) (auth.Method, error) {
	clientID := firstNonEmpty(cfg.githubClientID, os.Getenv("WAVED_GITHUB_CLIENT_ID"))
	clientSecret := firstNonEmpty(cfg.githubClientSecret, os.Getenv("WAVED_GITHUB_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("-auth-github requires WAVED_GITHUB_CLIENT_ID and WAVED_GITHUB_CLIENT_SECRET")
	}
	redirectURL, err := callbackURL(cfg, "/auth/github/callback")
	if err != nil {
		return nil, fmt.Errorf("-auth-github: %w", err)
	}
	prov := auth.Provisioner{Accounts: store, RegisterOnFirstUse: true}
	return auth.NewGitHubMethod(clientID, clientSecret, redirectURL, svc, store, prov), nil
}

// buildOIDCMethod constructs the generic OIDC method, performing discovery against
// the issuer at startup. Secrets come from the flags or WAVED_OIDC_* env vars.
func buildOIDCMethod(ctx context.Context, cfg config, svc *auth.Service, store *sqlite.Store) (auth.Method, error) {
	issuer := firstNonEmpty(cfg.oidcIssuer, os.Getenv("WAVED_OIDC_ISSUER"))
	clientID := firstNonEmpty(cfg.oidcClientID, os.Getenv("WAVED_OIDC_CLIENT_ID"))
	clientSecret := firstNonEmpty(cfg.oidcClientSecret, os.Getenv("WAVED_OIDC_CLIENT_SECRET"))
	redirectURL := firstNonEmpty(cfg.oidcRedirectURL, os.Getenv("WAVED_OIDC_REDIRECT_URL"))
	if issuer == "" || clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("-auth-oidc requires WAVED_OIDC_ISSUER, WAVED_OIDC_CLIENT_ID, and WAVED_OIDC_CLIENT_SECRET")
	}
	if redirectURL == "" {
		var err error
		if redirectURL, err = callbackURL(cfg, "/auth/oidc/callback"); err != nil {
			return nil, fmt.Errorf("-auth-oidc: set WAVED_OIDC_REDIRECT_URL (%w)", err)
		}
	}
	prov := auth.Provisioner{Accounts: store, RegisterOnFirstUse: true}
	m, err := auth.NewOIDCMethod(ctx, issuer, clientID, clientSecret, redirectURL, svc, store, prov)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// callbackURL builds the externally reachable OAuth callback URL from
// -auth-public-url (which the operator sets to the public origin, e.g.
// https://wave.example.com). It must be set for OAuth methods because the IdP
// redirects the browser there; the server cannot infer it from the bind address
// behind a proxy/TLS terminator.
func callbackURL(cfg config, path string) (string, error) {
	if cfg.authPublicURL == "" {
		return "", fmt.Errorf("set -auth-public-url to the server's public origin (e.g. https://wave.example.com) for the OAuth callback")
	}
	return cfg.authPublicURL + path, nil
}
