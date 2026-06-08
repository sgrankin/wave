package auth

import "net/http"

// DevMethod is the DEVELOPMENT trust-any login re-expressed as a Method: it
// trusts whatever address the caller submits (no proof), provisions it under the
// dev MintPolicy (the chosen address, restricted to the configured domain), and
// sets the session cookie. It is the same flow as Service.DevLoginHandler.
//
// SECURITY: it asserts identity with no proof and sets a cookie on a GET (login-
// CSRF-capable), so it is LoopbackOnly — the server refuses to mount it on a
// public bind (requireSafeAuthBind). Production uses a real interactive method
// (GitHub, OIDC) or proxy behind an authenticating proxy.
type DevMethod struct {
	Service *Service
	// Domain is the address namespace dev logins may mint: a bare username gets
	// it appended, and (under MintPolicy, commit 3) a full address is rejected
	// unless its domain matches.
	Domain string
}

// Name identifies the method.
func (DevMethod) Name() string { return "dev" }

// Mount registers the dev login at the shared /login (its entry point is the
// address-entry form, not a per-method start route). prefix is unused: the dev
// form lives at /login so the existing landing page and redirect flow are
// unchanged.
func (m DevMethod) Mount(mux *http.ServeMux, _ string) {
	mux.Handle("/login", m.Service.DevLoginHandler(m.Domain))
}

// RequireLoopback: the dev login is never safe on a public bind.
func (DevMethod) RequireLoopback() bool { return true }

// ProxyMethod is the trusted-header login re-expressed as a Method: it runs
// the provider chain (a TrustedHeader provider) and, on success, sets the session
// cookie. The header is set by a fronting authenticating proxy.
//
// SECURITY: the header is attacker-forgeable on any bind not exclusively reachable
// through that proxy. The server cannot verify the proxy is in front, so this is
// LoopbackOnly unless the operator explicitly asserts the bind is proxy-exclusive
// (ProxyExclusive), per docs/architecture/04-auth-model.md §4.
type ProxyMethod struct {
	Service *Service
	// ProxyExclusive asserts the -ws bind is reachable only through the trusted
	// proxy; only then may trusted-header bind to a public address.
	ProxyExclusive bool
}

// Name identifies the method.
func (ProxyMethod) Name() string { return "proxy" }

// Mount registers the proxy login at the shared /login (it reads the identity
// from the request header set by the proxy). prefix is unused.
func (m ProxyMethod) Mount(mux *http.ServeMux, _ string) {
	mux.Handle("/login", m.Service.LoginHandler())
}

// RequireLoopback: trusted-header is loopback-only unless the operator asserts the
// bind is proxy-exclusive.
func (m ProxyMethod) RequireLoopback() bool { return !m.ProxyExclusive }
