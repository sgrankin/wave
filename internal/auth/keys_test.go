package auth_test

import (
	"bytes"
	"testing"

	"github.com/sgrankin/wave/internal/auth"
)

// fakeSettings is an in-memory SettingsStore for signing-key tests.
type fakeSettings struct{ m map[string][]byte }

func newFakeSettings() *fakeSettings { return &fakeSettings{m: map[string][]byte{}} }
func (f *fakeSettings) GetSetting(k string) ([]byte, bool, error) {
	v, ok := f.m[k]
	return v, ok, nil
}
func (f *fakeSettings) PutSetting(k string, v []byte) error { f.m[k] = v; return nil }

// TestSigningKeyGeneratesAndPersists: the first call mints a >=32-byte key and
// stores it; later calls return the same bytes (a changing key would invalidate
// every outstanding session — log everyone out on restart).
func TestSigningKeyGeneratesAndPersists(t *testing.T) {
	s := newFakeSettings()
	k1, err := auth.SigningKey(s)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(k1) < 32 {
		t.Errorf("key length %d, want >= 32", len(k1))
	}
	if len(s.m) == 0 {
		t.Error("key was not persisted")
	}
	k2, err := auth.SigningKey(s)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !bytes.Equal(k1, k2) {
		t.Error("signing key changed across calls (would invalidate all sessions)")
	}
}

// TestSigningKeyReusesStored: a valid stored key is returned as-is.
func TestSigningKeyReusesStored(t *testing.T) {
	s := newFakeSettings()
	stored := bytes.Repeat([]byte{0xab}, 32)
	s.m["auth.session.signing-key"] = stored
	got, err := auth.SigningKey(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, stored) {
		t.Error("expected the stored key to be reused verbatim")
	}
}

// TestSigningKeyRegeneratesTooShort: a stored key that is too short (corrupt /
// legacy) is replaced with a fresh valid one rather than handed to NewSessions
// (which would panic).
func TestSigningKeyRegeneratesTooShort(t *testing.T) {
	s := newFakeSettings()
	s.m["auth.session.signing-key"] = []byte("short")
	got, err := auth.SigningKey(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 32 {
		t.Errorf("regenerated key length %d, want >= 32", len(got))
	}
}
