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
	return p.newHuman(participant, "")
}

// RegisterChosen provisions a chosen address the user picked under their method's
// MintPolicy (passkey / dev), distinct from Ensure's derived-address path
// (GitHub → foo@github, verified email → that email, which are known at first
// contact). It is uniqueness-checked: an address already taken by an account is
// rejected, so two users cannot register the same chosen address. displayName is
// stored if non-empty. See docs/architecture/04-auth-model.md §5.
//
// The check is ATOMIC: it does an insert-only CreateAccount, so two concurrent
// first-logins deriving the same address cannot both pass an absent-check and both
// provision (the loser sees the address as taken). A separate GetAccount-then-Put
// would be a TOCTOU race that clobbers the winner.
func (p Provisioner) RegisterChosen(participant id.ParticipantID, displayName string) error {
	created, err := p.Accounts.CreateAccount(&storage.Account{
		ID:    participant,
		Kind:  storage.AccountHuman,
		Human: &storage.HumanAccount{DisplayName: displayName},
	})
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("auth: address %s is already taken", participant)
	}
	return nil
}

// EnsureDisplayName provisions participant if absent (a derived address) and, when
// displayName is non-empty, stores it as the account's DisplayName — used by the
// IdP methods (GitHub login, OIDC name) so the roster shows a human name. An
// existing account's DisplayName is updated to the latest from the IdP.
func (p Provisioner) EnsureDisplayName(participant id.ParticipantID, displayName string) error {
	acct, ok, err := p.Accounts.GetAccount(participant)
	if err != nil {
		return err
	}
	if !ok {
		if !p.RegisterOnFirstUse {
			return fmt.Errorf("auth: no account for %s and register-on-first-use is off", participant)
		}
		return p.newHuman(participant, displayName)
	}
	// Refresh the display name from the IdP if it changed (and we have one).
	if displayName != "" && acct.Kind == storage.AccountHuman && acct.Human != nil && acct.Human.DisplayName != displayName {
		acct.Human.DisplayName = displayName
		return p.Accounts.PutAccount(acct)
	}
	return nil
}

// newHuman writes a fresh minimal human account (no password — auth is external)
// with an optional display name.
func (p Provisioner) newHuman(participant id.ParticipantID, displayName string) error {
	return p.Accounts.PutAccount(&storage.Account{
		ID:    participant,
		Kind:  storage.AccountHuman,
		Human: &storage.HumanAccount{DisplayName: displayName},
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

// MintSession is the policy-enforcing convergence point for the DEV / trusted
// derived-address path (DevLoginHandler): the caller has asserted an address it is
// trusted to claim. It rejects an address outside policy (the §4 security boundary),
// provisions-or-adopts the account (storing displayName), and sets the session
// cookie. It deliberately ADOPTS a pre-existing account at the address — correct for
// dev (re-login as an existing user) and trusted-header, where the request itself is
// the proof of the address.
//
// Credential-backed IdP methods (GitHub, OIDC) must NOT use this: the address is
// DERIVED from a mutable claim (a recyclable login / reassignable email), so adopting
// a pre-existing account would be takeover. They route through MintIdP, which binds
// to the stable (method, subject) and uniqueness-checks first logins.
func (s *Service) MintSession(w http.ResponseWriter, participant id.ParticipantID, displayName string, policy MintPolicy) error {
	if err := policy.Permits(participant); err != nil {
		return fmt.Errorf("auth: mint denied: %w", err)
	}
	if err := s.provisioner.EnsureDisplayName(participant, displayName); err != nil {
		return err
	}
	s.SetCookie(w, participant)
	return nil
}

// IdPLogin describes a verified interactive-IdP login (GitHub, OIDC) for MintIdP.
// The trust anchor is the stable (Method, Subject) credential key — a GitHub numeric
// id, an OIDC issuer+sub — NOT the address, which is derived from a mutable claim.
type IdPLogin struct {
	// Method and Subject are the stable credential key (e.g. "github" + numeric id).
	Method, Subject string
	// DisplayName is the human name from the IdP (refreshed on every login).
	DisplayName string
	// CreatedAt is the unix-seconds timestamp stamped on a first-login binding.
	CreatedAt int64
	// Derive computes the FIRST-LOGIN address, the MintPolicy bounding its namespace,
	// and the credential data blob. It is called ONLY when no binding exists, so a
	// returning user is never blocked by a derive error from drifted claims (a login
	// that no longer parses, an email_verified that flipped).
	Derive func() (participant id.ParticipantID, policy MintPolicy, data string, err error)
}

// MintIdP is the single security-critical convergence point for credential-backed
// IdP methods. It resolves the (Method, Subject) credential and splits two cases:
//
//   - RETURNING user (binding exists): the bound account is authoritative — ownership
//     was proven when the binding was created. It refreshes the display name and
//     issues the session WITHOUT re-deriving or re-checking policy, so drift in a
//     mutable claim (a GitHub rename, an email_verified flip) can neither move the
//     address nor lock the user out.
//   - FIRST login (no binding): the derived address must be IN-POLICY and UNCLAIMED.
//     Adopting a pre-existing account here would be account takeover — a recycled
//     GitHub login or reassigned email could derive an address another credential
//     already owns. It provisions the derived address with a uniqueness check
//     (RegisterChosen), records the binding, and issues the session. Binding happens
//     only after provisioning succeeds, so a rejected address leaves no orphan.
//
// It returns the resolved participant (for the caller to log).
func (s *Service) MintIdP(w http.ResponseWriter, store storage.CredentialStore, in IdPLogin) (id.ParticipantID, error) {
	if p, ok, err := resolveCredential(store, in.Method, in.Subject); err != nil {
		return id.ParticipantID{}, err
	} else if ok {
		// Returning user: bound account is authoritative; refresh the name only.
		if err := s.provisioner.EnsureDisplayName(p, in.DisplayName); err != nil {
			return id.ParticipantID{}, err
		}
		s.SetCookie(w, p)
		return p, nil
	}
	// First login: derive, then require the address be in-policy and unclaimed.
	participant, policy, data, err := in.Derive()
	if err != nil {
		return id.ParticipantID{}, err
	}
	// Refuse the shared-domain participant ("@domain", empty local part), which grants
	// domain-wide access (spec §2.9). An IdP claim — e.g. an OIDC email_verified for a
	// bare "@corp.com" — must never mint the most-privileged identity in a domain.
	if participant.Name() == "" {
		return id.ParticipantID{}, fmt.Errorf("auth: refusing to mint the shared-domain participant %q from an IdP claim", participant)
	}
	if err := policy.Permits(participant); err != nil {
		return id.ParticipantID{}, fmt.Errorf("auth: mint denied: %w", err)
	}
	if err := s.provisioner.RegisterChosen(participant, in.DisplayName); err != nil {
		return id.ParticipantID{}, err // includes "address is already taken" → takeover blocked
	}
	if err := bindCredential(store, in.Method, in.Subject, participant, data, in.CreatedAt); err != nil {
		return id.ParticipantID{}, err
	}
	s.SetCookie(w, participant)
	return participant, nil
}

// MintChosen is the policy-enforcing convergence point for a chosen-address method
// (passkey / dev): the user picked participant under the method's MintPolicy. It
// rejects an out-of-policy address, registers the chosen address with a uniqueness
// check (rejecting one already taken), then sets the session cookie. Use this when
// the address is user-selected; use MintSession when it is derived from the proof.
func (s *Service) MintChosen(w http.ResponseWriter, participant id.ParticipantID, displayName string, policy MintPolicy) error {
	if err := policy.Permits(participant); err != nil {
		return fmt.Errorf("auth: mint denied: %w", err)
	}
	if err := s.provisioner.RegisterChosen(participant, displayName); err != nil {
		return err
	}
	s.SetCookie(w, participant)
	return nil
}

// ClearCookie expires the session cookie (logout). It mirrors SetCookie's
// attributes (Path/Secure/etc.) so the browser matches and overwrites the cookie,
// then MaxAge=-1 deletes it.
func (s *Service) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
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
