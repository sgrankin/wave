package id_test

import (
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/id"
)

func mustWaveID(t *testing.T, domain, local string) id.WaveID {
	t.Helper()
	w, err := id.NewWaveID(domain, local)
	if err != nil {
		t.Fatalf("NewWaveID(%q,%q): %v", domain, local, err)
	}
	return w
}

func mustWaveletID(t *testing.T, domain, local string) id.WaveletID {
	t.Helper()
	w, err := id.NewWaveletID(domain, local)
	if err != nil {
		t.Fatalf("NewWaveletID(%q,%q): %v", domain, local, err)
	}
	return w
}

func TestWaveIDSerialize(t *testing.T) {
	// WaveID serializes with '/' (modern).
	if got, want := mustWaveID(t, "example.com", "w+abc123").Serialize(), "example.com/w+abc123"; got != want {
		t.Errorf("WaveID.Serialize() = %q, want %q", got, want)
	}
}

func TestWaveletIDSerialize(t *testing.T) {
	// WaveletID serializes with '!' (legacy) — deliberately different from WaveID.
	if got, want := mustWaveletID(t, "example.com", "conv+root").Serialize(), "example.com!conv+root"; got != want {
		t.Errorf("WaveletID.Serialize() = %q, want %q", got, want)
	}
}

func TestParseWaveIDBothForms(t *testing.T) {
	for _, s := range []string{"example.com/w+abc123", "example.com!w+abc123"} {
		w, err := id.ParseWaveID(s)
		if err != nil {
			t.Fatalf("ParseWaveID(%q): %v", s, err)
		}
		if w.Domain() != "example.com" || w.ID() != "w+abc123" {
			t.Errorf("ParseWaveID(%q) = {%q,%q}", s, w.Domain(), w.ID())
		}
	}
}

func TestParseWaveletIDBothForms(t *testing.T) {
	for _, s := range []string{"example.com!conv+root", "example.com/conv+root"} {
		w, err := id.ParseWaveletID(s)
		if err != nil {
			t.Fatalf("ParseWaveletID(%q): %v", s, err)
		}
		if w.Domain() != "example.com" || w.ID() != "conv+root" {
			t.Errorf("ParseWaveletID(%q) = {%q,%q}", s, w.Domain(), w.ID())
		}
	}
}

func TestWaveletNameSerializeElision(t *testing.T) {
	tests := []struct {
		name string
		wave id.WaveID
		wlt  id.WaveletID
		want string
	}{
		{
			name: "same domain elides to ~",
			wave: mustWaveID(t, "example.com", "w+abc"),
			wlt:  mustWaveletID(t, "example.com", "conv+root"),
			want: "example.com/w+abc/~/conv+root",
		},
		{
			name: "different domain written out",
			wave: mustWaveID(t, "example.com", "w+abc"),
			wlt:  mustWaveletID(t, "other.com", "conv+xyz"),
			want: "example.com/w+abc/other.com/conv+xyz",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := id.NewWaveletName(tc.wave, tc.wlt)
			if got := n.Serialize(); got != tc.want {
				t.Errorf("Serialize() = %q, want %q", got, tc.want)
			}
			// Round-trip.
			got, err := id.ParseWaveletName(tc.want)
			if err != nil {
				t.Fatalf("ParseWaveletName(%q): %v", tc.want, err)
			}
			if got.Wave() != tc.wave || got.Wavelet() != tc.wlt {
				t.Errorf("round-trip = {%v,%v}, want {%v,%v}", got.Wave(), got.Wavelet(), tc.wave, tc.wlt)
			}
		})
	}
}

func TestParseWaveletNameRejectsUnnormalisedDomain(t *testing.T) {
	// The matching wavelet domain must be elided to "~", not written out.
	if _, err := id.ParseWaveletName("example.com/w+abc/example.com/conv+root"); err == nil {
		t.Error("ParseWaveletName accepted un-normalised domains, want error")
	}
}

func TestParseWaveletNameTokenCount(t *testing.T) {
	for _, s := range []string{
		"example.com/w+abc",               // 2 tokens
		"example.com/w+abc/~",             // 3 tokens
		"example.com/w+abc/~/conv+root/x", // 5 tokens
		"example.com/w+abc/~/conv+root/",  // trailing slash
	} {
		if _, err := id.ParseWaveletName(s); err == nil {
			t.Errorf("ParseWaveletName(%q) = nil error, want error", s)
		}
	}
}

func TestIsValidDomain(t *testing.T) {
	valid := []string{"example.com", "a.b.c", "76.com", "my.arbitrary.domain", "localhost"}
	invalid := []string{"", ".", "example.", ".example.com", "-bad.com", "bad-.com", "ex ample.com", "EXAMPLE.com"}
	for _, d := range valid {
		if !id.IsValidDomain(d) {
			t.Errorf("IsValidDomain(%q) = false, want true", d)
		}
	}
	for _, d := range invalid {
		if id.IsValidDomain(d) {
			t.Errorf("IsValidDomain(%q) = true, want false", d)
		}
	}
}

func TestIsValidIdentifier(t *testing.T) {
	valid := []string{"w+abc", "conv+root", "user+alice@example.com", "b+x*y", "a-b_c.d~e"}
	invalid := []string{"", "a/b", "a!b", "a b", "a\x7fb"}
	for _, s := range valid {
		if !id.IsValidIdentifier(s) {
			t.Errorf("IsValidIdentifier(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if id.IsValidIdentifier(s) {
			t.Errorf("IsValidIdentifier(%q) = true, want false", s)
		}
	}
}

func TestIsValidDomainLengthBoundary(t *testing.T) {
	if d := strings.Repeat("a", 253); !id.IsValidDomain(d) {
		t.Errorf("IsValidDomain(253 chars) = false, want true")
	}
	if d := strings.Repeat("a", 254); id.IsValidDomain(d) {
		t.Errorf("IsValidDomain(254 chars) = true, want false")
	}
}

func TestNewWaveIDValidation(t *testing.T) {
	if _, err := id.NewWaveID("bad domain", "w+abc"); err == nil {
		t.Error("NewWaveID accepted invalid domain")
	}
	if _, err := id.NewWaveID("example.com", "a/b"); err == nil {
		t.Error("NewWaveID accepted invalid id")
	}
}
