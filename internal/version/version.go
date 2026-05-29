// Package version defines wavelet versioning. A HashedVersion pairs a version
// number (the count of operations applied to a wavelet) with a history hash
// that chains every applied delta, making history tamper-evident and giving
// concurrent clients an exact agreement point.
//
// Spec: docs/specs/01-data-model.md §2.5 and §5.
package version

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/sgrankin/wave/internal/id"
)

// hashSizeBytes is the number of leading SHA-256 bytes retained in a history
// hash: 160 bits, matching HashedVersionFactoryImpl.
const hashSizeBytes = 160 / 8

// HashedVersion is a wavelet version number and a history hash over the deltas
// that produced it. It is an immutable value: the history hash must not be
// mutated after construction. The zero value is not meaningful — construct via
// NewHashedVersion, Unsigned, Zero, or Apply.
type HashedVersion struct {
	version     uint64
	historyHash []byte
}

// NewHashedVersion returns a HashedVersion with the given version and history
// hash. The hash is copied, so the caller may reuse the input slice.
func NewHashedVersion(version uint64, historyHash []byte) HashedVersion {
	return HashedVersion{version: version, historyHash: append([]byte(nil), historyHash...)}
}

// Unsigned returns a HashedVersion with an empty history hash, used where only
// the version number matters and integrity is not checked (e.g. supplement
// read-state markers).
func Unsigned(version uint64) HashedVersion {
	return HashedVersion{version: version, historyHash: []byte{}}
}

// Version returns the operation count this version represents.
func (v HashedVersion) Version() uint64 { return v.version }

// HistoryHash returns the history hash (empty for unsigned versions). The
// returned slice aliases internal state and must be treated as read-only;
// mutating it would silently corrupt the version (and any hash chain built on
// it). Construct via NewHashedVersion, which copies, if you need ownership.
func (v HashedVersion) HistoryHash() []byte { return v.historyHash }

// Signed reports whether the version carries a non-empty history hash.
func (v HashedVersion) Signed() bool { return len(v.historyHash) > 0 }

// Equal reports whether v and other have the same version and history hash.
func (v HashedVersion) Equal(other HashedVersion) bool {
	return v.version == other.version && bytes.Equal(v.historyHash, other.historyHash)
}

// Compare orders by version, then by history hash. An unsigned version sorts
// before any signed version with the same number.
//
// The byte comparison treats hash bytes as SIGNED, matching the original
// HashedVersion.compareTo (Java bytes are signed). This differs from
// bytes.Compare (unsigned) and matters for bytes >= 0x80; it is reproduced so
// the ordering is identical to the reference implementation.
func (v HashedVersion) Compare(other HashedVersion) int {
	if v.version != other.version {
		if v.version < other.version {
			return -1
		}
		return 1
	}
	return compareHistoryHash(v.historyHash, other.historyHash)
}

// compareHistoryHash compares two history hashes treating bytes as signed
// (int8), matching the Java reference. A shorter hash sorts before a longer one
// when one is a prefix of the other.
func compareHistoryHash(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if int8(a[i]) < int8(b[i]) {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

// String returns the informal "<version>:<base64hash>" form (standard base64,
// matching HashedVersion.toString). An unsigned version renders as "<version>:".
func (v HashedVersion) String() string {
	return fmt.Sprintf("%d:%s", v.version, base64.StdEncoding.EncodeToString(v.historyHash))
}

// Zero returns the version-zero HashedVersion for a wavelet: version 0
// with a history hash equal to the raw UTF-8 bytes of the wavelet URI. NO digest
// is applied at version zero — the URI bytes are the seed of the hash chain
// (spec §2.5, §7.3).
func Zero(name id.WaveletName) HashedVersion {
	return HashedVersion{version: 0, historyHash: []byte(id.WaveletNameToURI(name))}
}

// Apply returns the HashedVersion reached by applying a delta to a wavelet at
// appliedAt, where appliedDeltaBytes is the delta's serialized form and
// operationsApplied is its operation count:
//
//	version = appliedAt.Version + operationsApplied
//	hash    = SHA-256(appliedAt.HistoryHash || appliedDeltaBytes)[:20]
//
// appliedDeltaBytes MUST be the project's frozen canonical encoding of the
// applied delta — codec.HashBytes (canonical CBOR of author, applied-at version,
// timestamp, ops); see architecture invariant #2. It is NOT Java's
// ProtocolAppliedWaveletDelta (federation is dropped, so there is no byte-compat
// requirement). Hashing any other encoding diverges the chain.
func Apply(appliedAt HashedVersion, appliedDeltaBytes []byte, operationsApplied uint64) HashedVersion {
	h := sha256.New()
	h.Write(appliedAt.historyHash)
	h.Write(appliedDeltaBytes)
	sum := h.Sum(nil)
	return HashedVersion{
		version:     appliedAt.version + operationsApplied,
		historyHash: sum[:hashSizeBytes],
	}
}
