// Package storage defines the backend-agnostic persistence contracts (spec
// §5.14). The delta log is the source of truth: a contiguous, hash-chained
// sequence of applied deltas per wavelet. Snapshots, accounts, and attachments
// are separate stores (later increments). The default backend is SQLite
// (sub-package sqlite).
//
// Spec: docs/specs/05-storage-persistence.md.
package storage

import (
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// DeltaRecord is a persisted applied delta (spec §5.1, flattened): the operations
// as applied, the version they applied at, the resulting hashed version (stored
// so reload reads it rather than recomputing), the author, and the timestamp.
//
// The spec's WaveletDeltaRecord also carries the federation `applied` blob
// (signed original bytes); it is intentionally dropped this increment (federation
// is gone) and re-addable later as a nullable column with no migration.
type DeltaRecord struct {
	Author           id.ParticipantID
	AppliedAtVersion uint64
	ResultingVersion version.HashedVersion
	Timestamp        int64
	Ops              []waveop.Operation
}

// DeltaStore is the root of delta-log persistence.
type DeltaStore interface {
	// Open returns per-wavelet delta access. The wavelet is created implicitly
	// when its first delta is appended.
	Open(name id.WaveletName) (DeltasAccess, error)
	// Delete permanently removes a wavelet's delta log. It returns whether a
	// wavelet was deleted (false if it had no deltas). Callers must serialize
	// Delete with Open/Append for the same wavelet.
	Delete(name id.WaveletName) (bool, error)
	// Lookup returns the wavelet IDs of the given wave that have at least one
	// delta. Empty if the wave does not exist (spec §5.5).
	Lookup(wave id.WaveID) ([]id.WaveletID, error)
	// WaveIDs returns all wave IDs that have at least one non-empty wavelet. The
	// result is a snapshot taken at call time (spec §5.5 getWaveIdIterator).
	WaveIDs() ([]id.WaveID, error)
	// Close releases the store's resources.
	Close() error
}

// SnapshotStore persists materialized wavelet snapshots — a derivable load-time
// cache (latest snapshot + tail replay), never the authority. Snapshots can be
// dropped and rebuilt from the delta log at any time. It is keyed by wavelet
// name and version.
type SnapshotStore interface {
	// GetLatestSnapshot returns the highest-versioned snapshot for the wavelet:
	// its version, the opaque encoded state, and whether one exists.
	GetLatestSnapshot(name id.WaveletName) (snapshotVersion uint64, blob []byte, ok bool, err error)
	// PutSnapshot stores (or replaces) the snapshot for the wavelet at version.
	PutSnapshot(name id.WaveletName, snapshotVersion uint64, blob []byte) error
}

// DeltasAccess is per-wavelet access to the delta log.
type DeltasAccess interface {
	// Append atomically and durably appends a batch of contiguous records. The
	// batch must start at the current end version and each record must follow the
	// previous (appliedAtVersion == prior resultingVersion).
	Append(records []DeltaRecord) error
	// ReadAll returns every record in application order (for replay).
	ReadAll() ([]DeltaRecord, error)
	// ReadFrom returns records with appliedAtVersion >= from, in application order
	// (the tail replayed on top of a snapshot).
	ReadFrom(from uint64) ([]DeltaRecord, error)
	// GetDelta returns the record applied at the given version, and whether it
	// exists.
	GetDelta(appliedAtVersion uint64) (DeltaRecord, bool, error)
	// GetDeltaByEndVersion returns the record whose resulting version equals the
	// given version, and whether it exists (spec §5.6).
	GetDeltaByEndVersion(resultingVersion uint64) (DeltaRecord, bool, error)
	// EndVersion returns the resulting version of the last record; ok is false
	// when the wavelet has no deltas.
	EndVersion() (hv version.HashedVersion, ok bool, err error)
	// IsEmpty reports whether the wavelet has any deltas.
	IsEmpty() (bool, error)
	// Close releases any resources held by this handle.
	Close() error
}
