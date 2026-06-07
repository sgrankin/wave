package auth

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/sgrankin/wave/internal/id"
)

// WhoAmIHandler reports the authenticated participant as JSON ({"address": ...}).
// It must be mounted behind Middleware (it reads the participant from the request
// context); an unauthenticated request gets 401. The browser client fetches this
// on load to learn its own identity (which now rides the session cookie rather
// than a URL parameter), and redirects to the login endpoint on a 401.
func (s *Service) WhoAmIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := ParticipantFrom(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"address": p.Address()})
	})
}

// LogoutHandler clears the session cookie. Mount it WITHOUT Middleware (clearing a
// cookie needs no valid identity); responds 204. The client re-boots afterward —
// which, with no cookie, shows the login modal (dev) or re-auths via the proxy.
func (s *Service) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.ClearCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})
}

// LoginHandler authenticates via the provider chain (e.g. a trusted proxy header)
// and, on success, sets the session cookie and redirects to the (local) redirect
// parameter, default "/". Use it for proxy / non-interactive deployments where
// the identity is already asserted on the request.
func (s *Service) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirect := sanitizeRedirect(r.FormValue("redirect"))
		_, ok, err := s.Login(w, r)
		if err != nil {
			http.Error(w, "authentication error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	})
}

// DevLoginHandler is a DEVELOPMENT login: it trusts whatever address the caller
// provides (the "user" form/query value), provisions it per policy, sets the
// session cookie, and redirects. A bare username (no '@') gets defaultDomain
// appended. With no "user" value it serves a minimal address-entry form.
//
// SECURITY: this trusts the client's claimed identity with no proof — it is the
// dev replacement for the old ?user= query param and must never be mounted in a
// real deployment. It also sets the session cookie on a GET, so it is login-CSRF-
// capable (an attacker page can force a victim into a chosen session); both are
// why the dev bind is restricted to loopback (see cmd/waved requireSafeAuthBind).
// Production uses LoginHandler behind a trusted proxy (or a real interactive
// flow), not this.
func (s *Service) DevLoginHandler(defaultDomain string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirect := sanitizeRedirect(r.FormValue("redirect"))
		user := strings.TrimSpace(r.FormValue("user"))
		if user == "" {
			renderDevLoginForm(w, redirect)
			return
		}
		addr := user
		if !strings.Contains(addr, "@") && defaultDomain != "" {
			addr = addr + "@" + defaultDomain
		}
		p, err := id.NewParticipantID(addr)
		if err != nil {
			http.Error(w, "invalid address: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.provisioner.Ensure(p); err != nil {
			http.Error(w, "provision failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.SetCookie(w, p)
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	})
}

// sanitizeRedirect restricts a post-login redirect to a local (same-origin) path,
// defending against open-redirect. Only a value beginning with a single "/" is
// allowed; rejected are: a "//host" prefix (a protocol-relative URL) and any
// value containing a backslash (browsers fold "\" to "/" in the authority
// position, so "/\evil.com" would navigate to http://evil.com). Anything else
// becomes "/".
func sanitizeRedirect(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsRune(s, '\\') {
		return "/"
	}
	return s
}

var devLoginTmpl = template.Must(template.New("devlogin").Parse(`<!doctype html>
<meta charset="utf-8">
<title>Wave — dev login</title>
<style>
  body { font: 15px system-ui, sans-serif; max-width: 24rem; margin: 4rem auto; padding: 0 1rem; }
  h1 { font-size: 1.1rem; }
  .hint { color: #777; font-size: 0.85rem; }
  input { font: inherit; padding: 0.4rem 0.5rem; width: 100%; box-sizing: border-box; margin: 0.5rem 0; }
  button { font: inherit; padding: 0.4rem 1rem; cursor: pointer; }
</style>
<h1>Sign in (dev)</h1>
<form method="get" action="/login">
  <input type="hidden" name="redirect" value="{{.Redirect}}">
  <input type="text" name="user" placeholder="you@example.com" autofocus autocomplete="off">
  <button type="submit">Continue</button>
</form>
<p class="hint">Development sign-in: any address is accepted, no password.</p>
`))

func renderDevLoginForm(w http.ResponseWriter, redirect string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = devLoginTmpl.Execute(w, struct{ Redirect string }{redirect})
}
