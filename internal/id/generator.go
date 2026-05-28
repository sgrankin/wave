package id

import (
	"math/bits"
	"strings"
	"sync"
)

// SeedFunc supplies the session-specific seed mixed into generated ids for
// cross-generator uniqueness. The seed must not contain '*'. It may change over
// the generator's lifetime.
type SeedFunc func() string

// Generator allocates unique wave, wavelet, and document ids within a default
// domain, following the original IdGeneratorImpl scheme: each unique token is
// "<seed><web64(counter)>" with a per-generator monotonic counter starting at 0.
// Safe for concurrent use.
type Generator struct {
	domain string
	seed   SeedFunc

	mu sync.Mutex
	// counter is uint32; the original used a signed int and overflowed (with an
	// assertion) past 2^31-1. Values >= 2^31 have no Java counterpart but are
	// unreachable in practice (2+ billion ids from one generator instance).
	counter uint32
}

// NewGenerator returns a Generator that allocates ids under domain, mixing
// seed() into each unique token.
func NewGenerator(domain string, seed SeedFunc) *Generator {
	return &Generator{domain: domain, seed: seed}
}

// DefaultDomain returns the domain ids are allocated under.
func (g *Generator) DefaultDomain() string { return g.domain }

// NewUniqueToken returns the next "<seed><web64(counter)>" token and advances
// the counter.
func (g *Generator) NewUniqueToken() string {
	g.mu.Lock()
	c := g.counter
	g.counter++
	g.mu.Unlock()
	return g.seed() + web64Encode(c)
}

// NewWaveID allocates a new conversational wave id ("w+<token>").
func (g *Generator) NewWaveID() (WaveID, error) {
	return NewWaveID(g.domain, buildID(WavePrefix, g.NewUniqueToken()))
}

// NewConversationRootWaveletID returns the conventional conv+root wavelet id in
// the generator's default domain.
func (g *Generator) NewConversationRootWaveletID() (WaveletID, error) {
	return NewWaveletID(g.domain, ConvRootWavelet)
}

// NewConversationWaveletID allocates a new non-root conversation wavelet id
// ("conv+<token>").
func (g *Generator) NewConversationWaveletID() (WaveletID, error) {
	return NewWaveletID(g.domain, buildID(ConvWaveletPrefix, g.NewUniqueToken()))
}

// NewUserDataWaveletID returns the user-data wavelet id for p
// ("user+<address>"), hosted on the participant's own domain.
func (g *Generator) NewUserDataWaveletID(p ParticipantID) (WaveletID, error) {
	return NewWaveletID(p.Domain(), buildID(UserDataWaveletPrefix, p.Address()))
}

// NewBlipID allocates a new blip document id ("b+<token>").
func (g *Generator) NewBlipID() string {
	return buildID(BlipPrefix, g.NewUniqueToken())
}

// ConversationRootWaveletID returns the conv+root wavelet id hosted in the
// wave's own domain (ports buildConversationRootWaveletId).
func ConversationRootWaveletID(wave WaveID) (WaveletID, error) {
	return NewWaveletID(wave.Domain(), ConvRootWavelet)
}

// web64Alphabet is the web-safe base-64 alphabet used to encode id counters
// (0–25→A–Z, 26–51→a–z, 52–61→0–9, 62→'-', 63→'_').
const web64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// web64Encode encodes v as a minimum-length, big-endian, unpadded web-safe
// base-64 string (ports IdGeneratorImpl.base64Encode). web64Encode(0) == "A".
func web64Encode(v uint32) string {
	if v == 0 {
		return "A"
	}
	n := (32 - bits.LeadingZeros32(v) + 5) / 6 // ceil((32 - leadingZeros) / 6)
	buf := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		buf[i] = web64Alphabet[v&0x3F]
		v >>= 6
	}
	return string(buf)
}

// buildID joins tokens with the token separator, escaping any literal '~', '+',
// or '!' within a token (ports SimplePrefixEscaper.join).
//
// TODO(phase2): the original asserted each generated id classifies correctly
// (IdUtil.isBlipId) — e.g. a '*' in the seed would misclassify a non-blip id as
// a blip. Re-add that post-generation check once IdUtil lands, and enforce the
// "seed must not contain '*'" contract documented on SeedFunc.
func buildID(tokens ...string) string {
	var b strings.Builder
	for i, t := range tokens {
		if i > 0 {
			b.WriteByte(TokenSeparator)
		}
		b.WriteString(escapeToken(t))
	}
	return b.String()
}

// escapeToken prefixes each '~', '+', and '!' in s with '~'.
func escapeToken(s string) string {
	if !strings.ContainsAny(s, "~+!") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == EscapePrefix || c == TokenSeparator || c == '!' {
			b.WriteByte(EscapePrefix)
		}
		b.WriteByte(c)
	}
	return b.String()
}
