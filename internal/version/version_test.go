package version_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/version"
)

func waveletName(t *testing.T) id.WaveletName {
	t.Helper()
	wave, err := id.NewWaveID("example.com", "w+abc")
	if err != nil {
		t.Fatal(err)
	}
	wlt, err := id.NewWaveletID("example.com", "conv+root")
	if err != nil {
		t.Fatal(err)
	}
	return id.NewWaveletName(wave, wlt)
}

func TestVersionZeroIsRawURIBytes(t *testing.T) {
	name := waveletName(t)
	v := version.Zero(name)

	if v.Version() != 0 {
		t.Errorf("Version() = %d, want 0", v.Version())
	}
	// Version-zero history hash is the raw UTF-8 bytes of the URI — NOT a digest.
	want := "wave://example.com/w+abc/conv+root"
	if got := string(v.HistoryHash()); got != want {
		t.Errorf("VersionZero history hash = %q, want %q (raw URI bytes)", got, want)
	}
	if !v.Signed() {
		t.Error("VersionZero should be signed (non-empty hash)")
	}
}

func TestApplyHashChain(t *testing.T) {
	v0 := version.Zero(waveletName(t))
	delta := []byte("serialized-applied-delta")
	const ops = 3

	v1 := version.Apply(v0, delta, ops)

	if v1.Version() != ops {
		t.Errorf("Version() = %d, want %d", v1.Version(), ops)
	}
	// Independently recompute SHA-256(prevHash || deltaBytes)[:20].
	sum := sha256.Sum256(append(append([]byte{}, v0.HistoryHash()...), delta...))
	want := sum[:20]
	if got := v1.HistoryHash(); string(got) != string(want) {
		t.Errorf("Apply hash = %x, want %x", got, want)
	}
	if len(v1.HistoryHash()) != 20 {
		t.Errorf("history hash len = %d, want 20", len(v1.HistoryHash()))
	}
}

func TestApplyGoldenVector(t *testing.T) {
	// Locks the hash-chain algorithm AND the concatenation order
	// (historyHash || deltaBytes). The expected value was computed independently
	// as SHA-256("wave://example.com/w+abc/conv+root" + "delta")[:20]; a
	// transposed concatenation would not match.
	v1 := version.Apply(version.Zero(waveletName(t)), []byte("delta"), 1)
	const wantHex = "03edcb1dfcb03a78d65c62a8be7e994d29b5c039"
	if got := hex.EncodeToString(v1.HistoryHash()); got != wantHex {
		t.Errorf("Apply hash = %s, want %s (concatenation order regression?)", got, wantHex)
	}
	if v1.Version() != 1 {
		t.Errorf("version = %d, want 1", v1.Version())
	}
}

func TestCompareSignedByteOrdering(t *testing.T) {
	// The original compares history-hash bytes as signed: 0x80 (= -128) sorts
	// BEFORE 0x01. Unsigned bytes.Compare would give the opposite; this guards
	// the signed semantics.
	hi := version.NewHashedVersion(5, []byte{0x80})
	lo := version.NewHashedVersion(5, []byte{0x01})
	if hi.Compare(lo) >= 0 {
		t.Errorf("Compare({0x80},{0x01}) = %d, want < 0 (signed byte ordering)", hi.Compare(lo))
	}
	if lo.Compare(hi) <= 0 {
		t.Error("Compare({0x01},{0x80}) should be > 0")
	}
}

func TestNewHashedVersionCopiesInput(t *testing.T) {
	// Mutating the caller's slice after construction must not affect the version.
	src := []byte{1, 2, 3}
	v := version.NewHashedVersion(1, src)
	src[0] = 0xFF
	if v.HistoryHash()[0] != 1 {
		t.Error("NewHashedVersion did not copy its input; external mutation leaked in")
	}
}

func TestApplyChainsCumulatively(t *testing.T) {
	// Version arithmetic accumulates across deltas.
	v := version.Zero(waveletName(t))
	v = version.Apply(v, []byte("d1"), 2)
	v = version.Apply(v, []byte("d2"), 5)
	if v.Version() != 7 {
		t.Errorf("cumulative version = %d, want 7", v.Version())
	}
}

func TestUnsigned(t *testing.T) {
	v := version.Unsigned(42)
	if v.Version() != 42 {
		t.Errorf("Version() = %d, want 42", v.Version())
	}
	if v.Signed() {
		t.Error("Unsigned version should not be signed")
	}
	if len(v.HistoryHash()) != 0 {
		t.Errorf("unsigned hash len = %d, want 0", len(v.HistoryHash()))
	}
}

func TestStringFormat(t *testing.T) {
	hash := []byte{0x01, 0x02, 0x03}
	v := version.NewHashedVersion(42, hash)
	want := "42:" + base64.StdEncoding.EncodeToString(hash)
	if got := v.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	// Unsigned renders with an empty hash component.
	if got := version.Unsigned(7).String(); got != "7:" {
		t.Errorf("Unsigned String() = %q, want %q", got, "7:")
	}
}

func TestEqualAndCompare(t *testing.T) {
	a := version.NewHashedVersion(5, []byte{1, 2, 3})
	b := version.NewHashedVersion(5, []byte{1, 2, 3})
	c := version.NewHashedVersion(6, []byte{1, 2, 3})

	if !a.Equal(b) {
		t.Error("a.Equal(b) = false, want true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) = true, want false")
	}
	if a.Compare(c) >= 0 {
		t.Error("a.Compare(c) should be < 0 (lower version)")
	}
	if c.Compare(a) <= 0 {
		t.Error("c.Compare(a) should be > 0")
	}
	// Unsigned sorts before signed at the same version.
	if version.Unsigned(5).Compare(a) >= 0 {
		t.Error("unsigned(5) should sort before signed version 5")
	}
}
