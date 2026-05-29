package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// Provider extracts a verified ParticipantId from a request. ok is false when
// the provider finds no identity (the chain tries the next provider); err is for
// a malformed or forged identity (a hard failure, not "try the next").
type Provider interface {
	Name() string
	Authenticate(r *http.Request) (participant id.ParticipantID, ok bool, err error)
}

// Local pins every request to a single participant — dev / single-user mode.
type Local struct{ User id.ParticipantID }

// Name identifies the provider.
func (Local) Name() string { return "local" }

// Authenticate always returns the pinned user.
func (l Local) Authenticate(*http.Request) (id.ParticipantID, bool, error) {
	return l.User, true, nil
}

// TrustedHeader reads a verified identity from a header set by a fronting proxy
// (tailscale serve, oauth2-proxy, Cloudflare Access, nginx forward-auth, …).
//
// SECURITY: only enable this on a listener *exclusively* reachable through that
// proxy. On a publicly reachable bind the header is attacker-forgeable and this
// is a complete authentication bypass.
type TrustedHeader struct {
	Header string // header carrying the identity, e.g. "X-Authenticated-User"
	Domain string // appended to a bare username (one with no '@'); "" requires a full address
}

// Name identifies the provider.
func (TrustedHeader) Name() string { return "trusted-header" }

// Authenticate resolves the identity from the configured header.
func (t TrustedHeader) Authenticate(r *http.Request) (id.ParticipantID, bool, error) {
	v := strings.TrimSpace(r.Header.Get(t.Header))
	if v == "" {
		return id.ParticipantID{}, false, nil
	}
	addr := v
	if !strings.Contains(addr, "@") {
		if t.Domain == "" {
			return id.ParticipantID{}, false, fmt.Errorf("auth: trusted-header identity %q has no domain and no default is configured", v)
		}
		addr = v + "@" + t.Domain
	}
	p, err := id.NewParticipantID(addr)
	if err != nil {
		return id.ParticipantID{}, false, fmt.Errorf("auth: trusted-header bad identity %q: %w", v, err)
	}
	return p, true, nil
}

// Provisioner enforces the account policy for an authenticated identity. With
// RegisterOnFirstUse, an unknown identity auto-provisions a minimal human
// account (no password — auth is external) plus its ParticipantId, and nothing
// else (no UDW seed, no welcome wave). Otherwise an unknown identity is rejected.
type Provisioner struct {
	Accounts           storage.AccountStore
	RegisterOnFirstUse bool
}

// Ensure makes sure participant has an account, per policy.
func (p Provisioner) Ensure(participant id.ParticipantID) error {
	_, ok, err := p.Accounts.GetAccount(participant)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if !p.RegisterOnFirstUse {
		return fmt.Errorf("auth: no account for %s and register-on-first-use is off", participant)
	}
	return p.Accounts.PutAccount(&storage.Account{
		ID:    participant,
		Kind:  storage.AccountHuman,
		Human: &storage.HumanAccount{},
	})
}

// Service ties the pieces together: it resolves a request to a participant
// (honoring a valid session cookie first, then the provider chain with
// provisioning), mints session cookies, and offers HTTP middleware that binds
// the participant to the request context.
type Service struct {
	providers   []Provider
	provisioner Provisioner
	sessions    *Sessions
	cookieName  string
	// SecureCookies sets the Secure flag on session cookies. Default true; set
	// false only for local HTTP (non-TLS) development.
	SecureCookies bool
}

// NewService builds a Service from a session signer, a provisioner, and an
// ordered provider chain (first provider to return ok wins).
func NewService(sessions *Sessions, provisioner Provisioner, providers ...Provider) *Service {
	return &Service{
		providers:     providers,
		provisioner:   provisioner,
		sessions:      sessions,
		cookieName:    "wave_session",
		SecureCookies: true,
	}
}

// Authenticate resolves the participant for a request: a valid session cookie
// first, then the provider chain (provisioning on success). ok is false when no
// provider supplies an identity.
func (s *Service) Authenticate(r *http.Request) (id.ParticipantID, bool, error) {
	if c, err := r.Cookie(s.cookieName); err == nil {
		if p, err := s.sessions.Verify(c.Value); err == nil {
			return p, true, nil
		}
		// An invalid/expired cookie is not fatal: fall through to the providers.
	}
	for _, prov := range s.providers {
		p, ok, err := prov.Authenticate(r)
		if err != nil {
			return id.ParticipantID{}, false, fmt.Errorf("auth: provider %s: %w", prov.Name(), err)
		}
		if !ok {
			continue
		}
		if err := s.provisioner.Ensure(p); err != nil {
			return id.ParticipantID{}, false, err
		}
		return p, true, nil
	}
	return id.ParticipantID{}, false, nil
}

// SetCookie writes a session cookie for participant. HttpOnly + SameSite=Lax
// blocks the cross-site form-POST CSRF vector; state-changing wave RPCs run over
// the (non-form) session protocol, not browser forms.
func (s *Service) SetCookie(w http.ResponseWriter, participant id.ParticipantID) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    s.sessions.Issue(participant),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// Login authenticates a request via the chain and, on success, sets a session
// cookie so subsequent requests skip the providers.
func (s *Service) Login(w http.ResponseWriter, r *http.Request) (id.ParticipantID, bool, error) {
	p, ok, err := s.Authenticate(r)
	if err != nil || !ok {
		return p, ok, err
	}
	s.SetCookie(w, p)
	return p, true, nil
}

type ctxKey struct{}

// Middleware authenticates each request, binds the participant to the context
// (read it with ParticipantFrom), and rejects unauthenticated requests.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := s.Authenticate(r)
		if err != nil {
			http.Error(w, "authentication error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

// ParticipantFrom returns the authenticated participant bound to ctx by
// Middleware, and whether one is present.
func ParticipantFrom(ctx context.Context) (id.ParticipantID, bool) {
	p, ok := ctx.Value(ctxKey{}).(id.ParticipantID)
	return p, ok
}
