package id_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
)

func TestWaveletNameToURI(t *testing.T) {
	wave, _ := id.NewWaveID("example.com", "w+abc")

	t.Run("same domain omits wave-domain prefix", func(t *testing.T) {
		wlt, _ := id.NewWaveletID("example.com", "conv+root")
		n := id.NewWaveletName(wave, wlt)
		if got, want := id.WaveletNameToURI(n), "wave://example.com/w+abc/conv+root"; got != want {
			t.Errorf("WaveletNameToURI = %q, want %q", got, want)
		}
	})

	t.Run("different domain includes wave-domain prefix", func(t *testing.T) {
		wlt, _ := id.NewWaveletID("privatereply.com", "conv+3sG7")
		n := id.NewWaveletName(wave, wlt)
		if got, want := id.WaveletNameToURI(n), "wave://privatereply.com/example.com!w+abc/conv+3sG7"; got != want {
			t.Errorf("WaveletNameToURI = %q, want %q", got, want)
		}
	})

	t.Run("valid local ids are not percent-encoded", func(t *testing.T) {
		// '@', '+', '*', '-', '.', '_', '~' all pass through unescaped.
		wlt, _ := id.NewWaveletID("example.com", "user+alice@example.com")
		n := id.NewWaveletName(wave, wlt)
		if got, want := id.WaveletNameToURI(n), "wave://example.com/w+abc/user+alice@example.com"; got != want {
			t.Errorf("WaveletNameToURI = %q, want %q", got, want)
		}
	})

	t.Run("non-ASCII UCS char is percent-encoded as UTF-8 bytes", func(t *testing.T) {
		// 'é' (U+00E9) is a valid identifier char (UCS > 0x7F) and encodes to its
		// UTF-8 bytes %C3%A9 in the URI path that seeds the version-zero hash.
		uwave, err := id.NewWaveID("example.com", "w+café")
		if err != nil {
			t.Fatalf("NewWaveID with UCS char: %v", err)
		}
		wlt, _ := id.NewWaveletID("example.com", "conv+root")
		n := id.NewWaveletName(uwave, wlt)
		if got, want := id.WaveletNameToURI(n), "wave://example.com/w+caf%C3%A9/conv+root"; got != want {
			t.Errorf("WaveletNameToURI = %q, want %q", got, want)
		}
	})
}
