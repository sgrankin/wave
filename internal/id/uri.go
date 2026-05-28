package id

import "strings"

// notEscaped marks the bytes that pass through wavelet-URI percent-encoding
// unescaped: the RFC 3986 path-segment characters (unreserved + sub-delims +
// ":" + "@"), per URIEncoderDecoder.
var notEscaped = func() [256]bool {
	var t [256]bool
	for c := byte('a'); c <= 'z'; c++ {
		t[c] = true
	}
	for c := byte('A'); c <= 'Z'; c++ {
		t[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		t[c] = true
	}
	for _, c := range []byte(":@!$&'()*+,;=-._~") {
		t[c] = true
	}
	return t
}()

const hexDigits = "0123456789ABCDEF"

// percentEncode percent-encodes s per RFC 3986 path-segment rules: bytes in
// notEscaped pass through; every other byte (including all UTF-8 continuation
// bytes of non-ASCII runes) is emitted as %XX.
//
// For any local id that passed IsValidIdentifier, every ASCII character is
// already in notEscaped, so the %XX branch is reached ONLY for non-ASCII UCS
// runes — where this UTF-8 %XX encoding agrees with the original codec. Do not
// "fix" this toward application/x-www-form-urlencoded (URLEncoder) semantics:
// that would change the bytes fed to the version-zero hash and break the chain.
func percentEncode(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if !notEscaped[s[i]] {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if notEscaped[c] {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xF])
	}
	return b.String()
}

// WaveletNameToURI returns the wavelet URI that seeds the version-zero history
// hash:
//
//	wave://<waveletDomain>/[<waveDomain>!]<waveLocal>/<waveletLocal>
//
// The wave-domain prefix is present only when the wave and wavelet domains
// differ; the host is always the wavelet's (hosting) domain. Local ids are
// percent-encoded; domains are not (spec §7.3; ports IdURIEncoderDecoder).
func WaveletNameToURI(n WaveletName) string {
	return WaveURIScheme + "://" + waveletNameToURIPath(n)
}

func waveletNameToURIPath(n WaveletName) string {
	wavePrefix := ""
	if n.wavelet.domain != n.wave.domain {
		wavePrefix = n.wave.domain + "!"
	}
	return n.wavelet.domain + "/" + wavePrefix + percentEncode(n.wave.id) + "/" + percentEncode(n.wavelet.id)
}
