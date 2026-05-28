// Package id defines Wave's identifier types and their serialization.
//
// Wave addresses everything by structured identifiers rather than opaque keys:
// a WaveID names a wave, a WaveletID names a wavelet within a provider domain,
// a WaveletName globally locates a wavelet, and a ParticipantID is an
// email-like identity. These types are immutable value types constructed
// through validating constructors.
//
// Spec: docs/specs/01-data-model.md (§2.6–2.9, §7). Naming maps Java's `Id`
// suffix to Go's `ID` initialism: WaveId→WaveID, WaveletId→WaveletID,
// ParticipantId→ParticipantID.
package id

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Well-known identifier constants (spec §"Well-known identifiers").
const (
	// TokenSeparator joins the '+'-separated tokens of a local id.
	TokenSeparator = '+'
	// EscapePrefix escapes a literal TokenSeparator, '!', or '~' within a token.
	EscapePrefix = '~'

	// WaveURIScheme is the scheme of a wavelet URI (used to seed version-zero hashes).
	WaveURIScheme = "wave"

	// WavePrefix is the local-id prefix for conversational waves ("w+<seed>").
	WavePrefix = "w"
	// ProfileWavePrefix is the local-id prefix for profile waves.
	ProfileWavePrefix = "prof"

	// ConvWaveletPrefix is the local-id prefix for conversation wavelets.
	ConvWaveletPrefix = "conv"
	// ConvRootWavelet is the conventional conversation root wavelet local id.
	ConvRootWavelet = "conv+root"
	// UserDataWaveletPrefix is the local-id prefix for user-data wavelets ("user+<address>").
	UserDataWaveletPrefix = "user"

	// BlipPrefix is the document-id prefix for blip documents.
	BlipPrefix = "b"
	// GhostBlipPrefix is the document-id prefix for ghost (unrendered) blips.
	GhostBlipPrefix = "g"
	// ManifestDocID is the document id of the conversation manifest.
	ManifestDocID = "conversation"
)

// WaveID identifies a wave: a provider domain plus a domain-unique local id.
type WaveID struct {
	domain string
	id     string
}

// NewWaveID returns a validated WaveID, or an error if domain or id are invalid.
func NewWaveID(domain, id string) (WaveID, error) {
	if !IsValidDomain(domain) {
		return WaveID{}, fmt.Errorf("id: invalid wave domain %q", domain)
	}
	if !IsValidIdentifier(id) {
		return WaveID{}, fmt.Errorf("id: invalid wave id %q", id)
	}
	return WaveID{domain: domain, id: id}, nil
}

// Domain returns the provider domain.
func (w WaveID) Domain() string { return w.domain }

// ID returns the domain-local identifier.
func (w WaveID) ID() string { return w.id }

// Serialize returns the modern serialization "<domain>/<id>" (spec §7.1).
func (w WaveID) Serialize() string { return w.domain + "/" + w.id }

// String returns the modern serialization.
func (w WaveID) String() string { return w.Serialize() }

// ParseWaveID parses a serialized wave id in either the modern ("<domain>/<id>")
// or legacy ("<domain>!<id>") form. Both are accepted because neither separator
// can appear in a valid domain or local id.
func ParseWaveID(s string) (WaveID, error) {
	domain, localID, err := splitDomainID(s, "wave id")
	if err != nil {
		return WaveID{}, err
	}
	return NewWaveID(domain, localID)
}

// WaveletID identifies a wavelet: a provider domain plus a domain-unique local
// id. The wavelet domain may differ from its wave's domain (e.g. private
// replies, user-data wavelets hosted on a federated domain).
type WaveletID struct {
	domain string
	id     string
}

// NewWaveletID returns a validated WaveletID, or an error if domain or id are invalid.
func NewWaveletID(domain, id string) (WaveletID, error) {
	if !IsValidDomain(domain) {
		return WaveletID{}, fmt.Errorf("id: invalid wavelet domain %q", domain)
	}
	if !IsValidIdentifier(id) {
		return WaveletID{}, fmt.Errorf("id: invalid wavelet id %q", id)
	}
	return WaveletID{domain: domain, id: id}, nil
}

// Domain returns the provider domain hosting the wavelet.
func (w WaveletID) Domain() string { return w.domain }

// ID returns the domain-local identifier.
func (w WaveletID) ID() string { return w.id }

// Serialize returns the legacy serialization "<domain>!<id>".
//
// This differs from WaveID.Serialize (which uses '/'): the inconsistency is a
// historical artifact preserved for compatibility (spec §2.7).
func (w WaveletID) Serialize() string { return w.domain + "!" + w.id }

// String returns the legacy serialization.
func (w WaveletID) String() string { return w.Serialize() }

// ParseWaveletID parses a serialized wavelet id in either the legacy
// ("<domain>!<id>") or modern ("<domain>/<id>") form; both are accepted.
func ParseWaveletID(s string) (WaveletID, error) {
	domain, localID, err := splitDomainID(s, "wavelet id")
	if err != nil {
		return WaveletID{}, err
	}
	return NewWaveletID(domain, localID)
}

// splitDomainID splits a "<domain><sep><id>" string on the first '/' or '!'.
// Neither separator can occur in a valid domain or identifier, so the split is
// unambiguous regardless of which serialization produced it.
func splitDomainID(s, what string) (domain, localID string, err error) {
	i := strings.IndexAny(s, "/!")
	if i < 0 {
		return "", "", fmt.Errorf("id: malformed %s %q: missing domain separator", what, s)
	}
	domain, localID = s[:i], s[i+1:]
	if strings.ContainsAny(localID, "/!") {
		return "", "", fmt.Errorf("id: malformed %s %q: multiple separators", what, s)
	}
	return domain, localID, nil
}

// WaveletName globally locates a wavelet by its wave and wavelet ids.
type WaveletName struct {
	wave    WaveID
	wavelet WaveletID
}

// NewWaveletName returns a WaveletName for the given wave and wavelet ids.
func NewWaveletName(wave WaveID, wavelet WaveletID) WaveletName {
	return WaveletName{wave: wave, wavelet: wavelet}
}

// Wave returns the wave id.
func (n WaveletName) Wave() WaveID { return n.wave }

// Wavelet returns the wavelet id.
func (n WaveletName) Wavelet() WaveletID { return n.wavelet }

// Serialize returns the modern 4-token form
// "<waveDomain>/<waveLocal>/<waveletDomainOrTilde>/<waveletLocal>". When the
// wavelet domain equals the wave domain it is elided to "~" (spec §2.8); writing
// the matching domain out is invalid and rejected by ParseWaveletName.
func (n WaveletName) Serialize() string {
	waveletDomain := "~"
	if n.wavelet.domain != n.wave.domain {
		waveletDomain = n.wavelet.domain
	}
	return n.wave.domain + "/" + n.wave.id + "/" + waveletDomain + "/" + n.wavelet.id
}

// String returns the modern serialization.
func (n WaveletName) String() string { return n.Serialize() }

// ParseWaveletName parses the modern 4-token serialization produced by
// WaveletName.Serialize.
func ParseWaveletName(s string) (WaveletName, error) {
	if strings.HasSuffix(s, "/") {
		return WaveletName{}, fmt.Errorf("id: wavelet name %q has trailing '/'", s)
	}
	tokens := strings.Split(s, "/")
	if len(tokens) != 4 {
		return WaveletName{}, fmt.Errorf("id: wavelet name %q must have 4 '/'-separated tokens", s)
	}
	waveDomain, waveLocal, waveletDomain, waveletLocal := tokens[0], tokens[1], tokens[2], tokens[3]
	if waveletDomain == waveDomain {
		return WaveletName{}, fmt.Errorf("id: wavelet name %q has un-normalised domains (use '~')", s)
	}
	if waveletDomain == "~" {
		waveletDomain = waveDomain
	}
	wave, err := NewWaveID(waveDomain, waveLocal)
	if err != nil {
		return WaveletName{}, err
	}
	wavelet, err := NewWaveletID(waveletDomain, waveletLocal)
	if err != nil {
		return WaveletName{}, err
	}
	return NewWaveletName(wave, wavelet), nil
}

// IsValidDomain reports whether s is a valid RFC 1035 hostname: dot-separated
// labels matching [a-z0-9]([a-z0-9-]*[a-z0-9])?, total length 1–253, no trailing
// dot, labels not ending in '-'. Labels may start with a digit. (Ported from
// WaveIdentifiers.isValidDomain.)
func IsValidDomain(s string) bool {
	if len(s) < 1 || len(s) > 253 {
		return false
	}
	i := 0
	for i < len(s) {
		c := s[i]
		// A label must begin with a letter or digit.
		if !isLowerAlnum(c) {
			return false
		}
		last := c
		i++
		for i < len(s) {
			c = s[i]
			if !isLowerAlnum(c) && c != '-' {
				break
			}
			last = c
			i++
		}
		if i >= len(s) {
			return last != '-'
		}
		// Labels are separated by dots and may not end with a dash.
		if c != '.' || last == '-' {
			return false
		}
		i++
	}
	// Ended in a dot.
	return false
}

func isLowerAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// IsValidIdentifier reports whether s is a valid wave local identifier:
// non-empty, every code point either a safe ASCII char (A-Za-z0-9-._~+*@) or a
// UCS char above 0x7F per RFC 3987 (ported from WaveIdentifiers.isValidIdentifier).
func IsValidIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == utf8.RuneError {
			return false
		}
		if r < 0x7F {
			if !safeASCII[r] {
				return false
			}
		} else if !isUCSChar(r) {
			return false
		}
	}
	return true
}

// safeASCII marks the ASCII characters (<0x7F) permitted in a local identifier.
var safeASCII = func() [0x7F]bool {
	var t [0x7F]bool
	for c := 'A'; c <= 'Z'; c++ {
		t[c] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		t[c] = true
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	for _, c := range []rune{'-', '.', '_', '~', '+', '*', '@'} {
		t[c] = true
	}
	return t
}()

// isUCSChar reports whether r is a valid UCS code point above 0x7F per RFC 3987
// (ported from WaveIdentifiers.isUcsChar).
func isUCSChar(r rune) bool {
	switch {
	case r >= 0xA0 && r <= 0xD7FF,
		r >= 0xF900 && r <= 0xFDCF,
		r >= 0xFDF0 && r <= 0xFFEF,
		r >= 0x10000 && r <= 0x1FFFD,
		r >= 0x20000 && r <= 0x2FFFD,
		r >= 0x30000 && r <= 0x3FFFD,
		r >= 0x40000 && r <= 0x4FFFD,
		r >= 0x50000 && r <= 0x5FFFD,
		r >= 0x60000 && r <= 0x6FFFD,
		r >= 0x70000 && r <= 0x7FFFD,
		r >= 0x80000 && r <= 0x8FFFD,
		r >= 0x90000 && r <= 0x9FFFD,
		r >= 0xA0000 && r <= 0xAFFFD,
		r >= 0xB0000 && r <= 0xBFFFD,
		r >= 0xC0000 && r <= 0xCFFFD,
		r >= 0xD0000 && r <= 0xDFFFD,
		r >= 0xE1000 && r <= 0xEFFFD:
		return true
	default:
		return false
	}
}
