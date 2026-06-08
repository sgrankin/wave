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
	// Dev's MintPolicy is the chosen address restricted to defaultDomain (§4): a
	// bare username gets it appended; a full address in another domain is rejected.
	// (An empty defaultDomain means "any", preserving the old trust-any behavior.)
	policy := AnyAddress()
	if defaultDomain != "" {
		policy = DomainOnly(defaultDomain)
	}
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
		// MintSession enforces the dev policy, provisions (idempotent re-login), and
		// sets the cookie — the same convergence point the IdP methods use.
		if err := s.MintSession(w, p, "", policy); err != nil {
			http.Error(w, "login denied: "+err.Error(), http.StatusForbidden)
			return
		}
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
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>Wave — dev login</title>
<meta name="theme-color" content="#4060c0">
<style>
  :root { color-scheme: light; }
  html, body { height: 100%; }
  body {
    margin: 0; font: 16px/1.5 system-ui, sans-serif; color: #222; background: #fff;
    display: flex; align-items: center; justify-content: center;
    padding: 24px; box-sizing: border-box;
  }
  .card { width: 100%; max-width: 22rem; }
  h1 { font-size: 1.7rem; color: #4060c0; margin: 0 0 0.15rem; }
  .sub { color: #555; margin: 0 0 1.25rem; font-size: 0.95rem; }
  label { display: block; font-size: 0.85rem; color: #555; margin-bottom: 5px; }
  input[type=text] {
    /* font-size >= 16px so iOS Safari does not zoom in on focus. */
    font: inherit; font-size: 16px; width: 100%; box-sizing: border-box;
    padding: 12px; border: 1px solid #ccc; border-radius: 8px; -webkit-appearance: none;
  }
  input[type=text]:focus {
    outline: none; border-color: #4060c0; box-shadow: 0 0 0 2px rgba(64, 96, 192, 0.18);
  }
  button {
    font: inherit; font-size: 16px; font-weight: 600; width: 100%; margin-top: 14px;
    padding: 12px 16px; border: none; border-radius: 8px; background: #4060c0; color: #fff; cursor: pointer;
  }
  button:hover { background: #36509c; }
  button:active { background: #2f4789; }
  .hint { color: #888; font-size: 0.8rem; margin-top: 1.1rem; }
</style>
<div class="card">
  <h1>Wave</h1>
  <p class="sub">Sign in (dev)</p>
  <form method="get" action="/login">
    <input type="hidden" name="redirect" value="{{.Redirect}}">
    <label for="user">Address</label>
    <input id="user" type="text" name="user" placeholder="you@example.com"
      autofocus autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" inputmode="email">
    <button type="submit">Continue</button>
  </form>
  <p class="hint">Development sign-in: any address is accepted, no password.</p>
</div>
`))

func renderDevLoginForm(w http.ResponseWriter, redirect string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = devLoginTmpl.Execute(w, struct{ Redirect string }{redirect})
}
