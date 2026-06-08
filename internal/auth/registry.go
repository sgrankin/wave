package auth

import (
	"encoding/json"
	"net/http"
)

// Registry is the set of enabled authentication methods for the browser server.
// It mounts each method's routes, exposes GET /auth/methods for the client landing
// page, and reports whether any enabled method requires a loopback-only bind (the
// per-method safety check the server enforces, replacing the old single -auth mode
// string). See docs/architecture/04-auth-model.md §2, §6.
type Registry struct {
	methods []Method
}

// NewRegistry builds a Registry from the enabled methods, in order. The order is
// the order methods appear in /auth/methods (the client renders buttons in that
// order). A nil or empty set means no login is possible (only an existing session
// cookie authenticates).
func NewRegistry(methods ...Method) *Registry {
	return &Registry{methods: methods}
}

// Mount registers every method's routes plus GET /auth/methods on mux. Each method
// is mounted under "/auth/<name>" (its own start/callback routes); the shared
// /login (dev, proxy) is registered by those methods at the fixed path. /logout
// and /whoami are mounted by the caller (one session model, method-independent).
func (reg *Registry) Mount(mux *http.ServeMux) {
	for _, m := range reg.methods {
		m.Mount(mux, "/auth/"+m.Name())
	}
	mux.Handle("/auth/methods", reg.MethodsHandler())
}

// methodInfo is the JSON shape of one interactive method in /auth/methods.
type methodInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Start string `json:"start"`
}

// MethodsHandler serves GET /auth/methods: a JSON array describing the enabled
// INTERACTIVE methods (name + label + start URL) so the landing page renders the
// right sign-in buttons. Stateless methods (proxy, dev's address form) are
// omitted — they are not buttons a user clicks to begin an IdP handshake. The
// array may be empty (e.g. proxy-only or session-cookie-only deployments); the
// client then falls back to its plain /login entry point.
func (reg *Registry) MethodsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Build a non-nil slice so the JSON is [] not null when empty (the client
		// can iterate it unconditionally).
		out := []methodInfo{}
		for _, m := range reg.methods {
			im, ok := m.(InteractiveMethod)
			if !ok {
				continue
			}
			out = append(out, methodInfo{Name: im.Name(), Label: im.Label(), Start: im.StartPath()})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// RequiresLoopback reports whether any enabled method must bind to loopback (a
// LoopbackOnly method whose RequireLoopback is true). The server consults this to
// refuse a public -ws bind when an unsafe method is enabled — per-method safety,
// not one global mode string.
func (reg *Registry) RequiresLoopback() bool {
	for _, m := range reg.methods {
		if lo, ok := m.(LoopbackOnly); ok && lo.RequireLoopback() {
			return true
		}
	}
	return false
}
