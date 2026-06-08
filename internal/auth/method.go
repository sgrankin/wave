package auth

import "net/http"

// Method is one authentication method mounted on the browser server. Both
// stateless (dev, trusted-header) and interactive (GitHub, OIDC) methods
// implement it, so the server wires them uniformly: enable a set of methods, mount
// each, and every one converges on Service.SetCookie (one session model). See
// docs/architecture/04-auth-model.md §2.
type Method interface {
	// Name is the method's stable identifier ("dev", "proxy", "github", "oidc").
	// It namespaces the method's routes and keys its credentials.
	Name() string
	// Mount registers the method's HTTP routes on mux under prefix (e.g.
	// "/auth/github"). A stateless method whose entry point is the shared /login
	// (dev, proxy) may register nothing here and instead be surfaced via Service's
	// /login handler; an interactive method registers its start + callback.
	Mount(mux *http.ServeMux, prefix string)
}

// InteractiveMethod is a Method a user starts from a button on the landing
// page: it has a human label and a start URL. GET /auth/methods lists these so the
// client renders the right sign-in buttons. Stateless methods (proxy, dev's
// trust-any form) are not interactive in this sense and are omitted.
type InteractiveMethod interface {
	Method
	// Label is the human-readable button text ("Sign in with GitHub").
	Label() string
	// StartPath is the URL the sign-in button navigates to (the method's start
	// route under its mount prefix, e.g. "/auth/github/start"). The client appends
	// the post-login redirect as a query parameter.
	StartPath() string
}

// LoopbackOnly is implemented by a method that is only safe on a loopback bind:
// it asserts identity with no cryptographic proof against this server (dev's
// trust-any login) or trusts a request header (trusted-header on a bind that is
// not proxy-exclusive). The server refuses to expose such a method on a public
// -ws address (requireSafeAuthBind). Methods backed by a real IdP handshake
// (GitHub, OIDC) do NOT implement this and may bind publicly.
type LoopbackOnly interface {
	Method
	// RequireLoopback reports whether this method must bind to loopback. It is a
	// method (not just the marker interface) so a method can be conditionally
	// unsafe — e.g. trusted-header is safe behind a proxy but the server cannot
	// verify that, so it returns true unless the operator asserts the proxy bind.
	RequireLoopback() bool
}
