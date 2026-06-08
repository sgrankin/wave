package auth

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
)

// MintPolicy is the address namespace a method is allowed to assert — the auth
// layer's hard security boundary (docs/architecture/04-auth-model.md §4). A method
// may only mint addresses its policy permits, so a GitHub login can never claim
// alice@example.com and a trusted-header on a public bind can't impersonate.
//
// The zero value permits nothing (fail-closed); use the constructors. Enforcement
// lives in Service.provision, which every method's provisioning path routes
// through.
type MintPolicy struct {
	// kind selects how Permits decides; see the constructors.
	kind policyKind
	// domain is the single domain a DomainPolicy permits.
	domain string
}

type policyKind int

const (
	policyNone   policyKind = iota // fail-closed zero value: permits nothing
	policyAny                      // permits any address (dev/trusted-header only)
	policyDomain                   // permits exactly one domain
)

// AnyAddress permits any address. It is for methods that are trusted to assert
// arbitrary identities — dev (loopback only) and trusted-header (proxy only). Do
// not use it for an externally-driven method (GitHub, OIDC): those must be pinned
// to a namespace they control.
func AnyAddress() MintPolicy { return MintPolicy{kind: policyAny} }

// DomainOnly permits exactly the addresses in domain (e.g. "github" for GitHub
// logins → "<login>@github"; the local domain for passkey/dev chosen addresses;
// an issuer host for OIDC by sub). An address in any other domain is rejected.
func DomainOnly(domain string) MintPolicy { return MintPolicy{kind: policyDomain, domain: domain} }

// Permits reports whether p is within the policy's namespace, with an error
// explaining a rejection (so the rejection is legible in logs/responses).
func (mp MintPolicy) Permits(p id.ParticipantID) error {
	switch mp.kind {
	case policyAny:
		return nil
	case policyDomain:
		if p.Domain() == mp.domain {
			return nil
		}
		return fmt.Errorf("address %s is outside this method's namespace @%s", p, mp.domain)
	default:
		return fmt.Errorf("address %s rejected: method has no minting authority", p)
	}
}
