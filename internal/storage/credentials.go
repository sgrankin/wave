package storage

import "github.com/sgrankin/wave/internal/id"

// Credential binds an external proof of identity — a GitHub numeric id, an OIDC
// `sub`, a passkey credential id — to a Wave account (a ParticipantID). The proof
// is NOT itself an address, so interactive login flows resolve
// (method, subject) → Credential → account, rather than reading an address off
// the request (the address-asserting providers in auth.go do that). See
// docs/architecture/04-auth-model.md §3.
//
// It is keyed by (Method, Subject): Method is the auth method that minted it
// ("github", "oidc", …), Subject is that method's stable per-user identifier
// (the GitHub numeric id, the OIDC issuer+sub, …). Account is the resolved
// ParticipantID. Data is an opaque method-specific JSON blob (e.g. the OIDC
// issuer, a passkey public key + sign count); the store does not interpret it.
type Credential struct {
	Method    string
	Subject   string
	Account   id.ParticipantID
	Data      string // method-specific JSON; "" when the method needs none
	CreatedAt int64  // unix seconds, set at first PutCredential
}

// CredentialStore persists external-credential → account bindings. It is global
// (not per-wavelet), like AccountStore. Unique on (Method, Subject): one external
// credential maps to exactly one account; ListByAccount enables the reverse
// (account linking / showing a user their bound credentials).
type CredentialStore interface {
	// GetCredential returns the credential for (method, subject), and whether one
	// exists.
	GetCredential(method, subject string) (Credential, bool, error)
	// PutCredential creates or replaces the credential keyed by (Method, Subject).
	// It preserves the original CreatedAt on replace (the binding's age is stable).
	PutCredential(c Credential) error
	// ListByAccount returns every credential bound to account, for account-linking
	// UIs and merge. Order is unspecified.
	ListByAccount(account id.ParticipantID) ([]Credential, error)
}
