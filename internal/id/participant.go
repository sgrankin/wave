package id

import (
	"fmt"
	"strings"
)

// ParticipantID is an email-like identity "name@domain", normalized to
// lowercase. The name may be empty: "@domain" is the shared-domain participant,
// which grants domain-wide access (spec §2.9 and the access-control model).
type ParticipantID struct {
	address string
}

// NewParticipantID validates and normalizes address into a ParticipantID. The
// address must contain exactly one '@', which must not be the last character
// (an empty name before the '@' is allowed). The stored address is lowercased.
//
// Validation matches the original ParticipantId: only the '@' structure is
// checked; the name and domain parts are not otherwise validated.
func NewParticipantID(address string) (ParticipantID, error) {
	i := strings.IndexByte(address, '@')
	switch {
	case i < 0:
		return ParticipantID{}, fmt.Errorf("id: participant address %q missing '@'", address)
	case i >= len(address)-1:
		return ParticipantID{}, fmt.Errorf("id: participant address %q missing domain", address)
	case strings.IndexByte(address[i+1:], '@') >= 0:
		return ParticipantID{}, fmt.Errorf("id: participant address %q has multiple '@'", address)
	}
	return ParticipantID{address: strings.ToLower(address)}, nil
}

// Address returns the normalized "name@domain" address.
func (p ParticipantID) Address() string { return p.address }

// String returns the address.
func (p ParticipantID) String() string { return p.address }

// Name returns the part before the '@' (empty for the shared-domain participant).
func (p ParticipantID) Name() string {
	if i := strings.IndexByte(p.address, '@'); i >= 0 {
		return p.address[:i]
	}
	return p.address
}

// Domain returns the part after the last '@'.
func (p ParticipantID) Domain() string {
	if i := strings.LastIndexByte(p.address, '@'); i >= 0 {
		return p.address[i+1:]
	}
	return p.address
}
