// Package sqlite is the SQLite-backed DeltaStore (the default backend). It uses
// the pure-Go modernc.org/sqlite driver (CGO-free), WAL journaling, and the full
// WaveletDeltaRecord per delta so snapshot+tail replay is bit-identical (stored
// hashes/timestamps are read back, never recomputed).
//
// Spec: docs/specs/05-storage-persistence.md; schema in docs/architecture/01-target-architecture.md.
package sqlite

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/version"
)

const schema = `
CREATE TABLE IF NOT EXISTS deltas (
  wave_id            TEXT    NOT NULL,
  wavelet_id         TEXT    NOT NULL,
  applied_at_version INTEGER NOT NULL,
  resulting_version  INTEGER NOT NULL,
  resulting_hash     BLOB    NOT NULL,
  transformed_blob   BLOB    NOT NULL,
  PRIMARY KEY (wave_id, wavelet_id, applied_at_version)
);
-- Supports EndVersion's resulting_version lookup now, and getDeltaByEndVersion
-- (deferred) later.
CREATE INDEX IF NOT EXISTS idx_deltas_end
  ON deltas (wave_id, wavelet_id, resulting_version);
`

// Store is a SQLite-backed DeltaStore. One *sql.DB backs all wavelets; access is
// serialized to a single connection (the one-writer-per-DB model; appends are
// sub-millisecond, so the global lock is a non-issue at single-machine scale).
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and ensures the
// schema. WAL journaling, a busy timeout, and synchronous=FULL are set via the
// DSN so every connection inherits them. synchronous=FULL with WAL fsyncs the
// WAL on each commit, so an appended delta is crash-durable on return (spec
// §5.6); it is set explicitly rather than relying on the compile-time default.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %q: %w", path, err)
	}
	// Serialize to one connection: one writer, and reads are cheap at our scale.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Open returns delta access for the named wavelet.
func (s *Store) Open(name id.WaveletName) (storage.DeltasAccess, error) {
	return &deltasAccess{
		db:        s.db,
		waveID:    name.Wave().Serialize(),
		waveletID: name.Wavelet().Serialize(),
	}, nil
}

type deltasAccess struct {
	db        *sql.DB
	waveID    string
	waveletID string
}

// Close is a no-op: the database is owned by the Store and shared across wavelets.
func (a *deltasAccess) Close() error { return nil }

// endVersionNumber returns the current end version number (0 if empty).
func (a *deltasAccess) endVersionNumber() (uint64, error) {
	var v sql.NullInt64
	err := a.db.QueryRow(
		`SELECT MAX(resulting_version) FROM deltas WHERE wave_id = ? AND wavelet_id = ?`,
		a.waveID, a.waveletID).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("sqlite: end version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return uint64(v.Int64), nil
}

// Append inserts a contiguous batch in one transaction.
func (a *deltasAccess) Append(records []storage.DeltaRecord) error {
	if len(records) == 0 {
		return nil
	}
	end, err := a.endVersionNumber()
	if err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	stmt, err := tx.Prepare(`INSERT INTO deltas
		(wave_id, wavelet_id, applied_at_version, resulting_version, resulting_hash, transformed_blob)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	expected := end
	for i, r := range records {
		if r.AppliedAtVersion != expected {
			return fmt.Errorf("sqlite: non-contiguous append at record %d: applied-at %d, expected %d",
				i, r.AppliedAtVersion, expected)
		}
		blob := codec.EncodeStoredDelta(codec.StoredDelta{
			Author:           r.Author,
			ResultingVersion: r.ResultingVersion,
			Timestamp:        r.Timestamp,
			Ops:              r.Ops,
		})
		if _, err := stmt.Exec(a.waveID, a.waveletID, r.AppliedAtVersion,
			r.ResultingVersion.Version(), r.ResultingVersion.HistoryHash(), blob); err != nil {
			return fmt.Errorf("sqlite: insert delta %d: %w", r.AppliedAtVersion, err)
		}
		expected = r.ResultingVersion.Version()
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	return nil
}

// recordFrom reconstructs a DeltaRecord from a stored row.
func recordFrom(appliedAt uint64, blob []byte) (storage.DeltaRecord, error) {
	sd, err := codec.DecodeStoredDelta(blob)
	if err != nil {
		return storage.DeltaRecord{}, fmt.Errorf("sqlite: decode delta at %d: %w", appliedAt, err)
	}
	return storage.DeltaRecord{
		Author:           sd.Author,
		AppliedAtVersion: appliedAt,
		ResultingVersion: sd.ResultingVersion,
		Timestamp:        sd.Timestamp,
		Ops:              sd.Ops,
	}, nil
}

// ReadAll returns every record in application order.
func (a *deltasAccess) ReadAll() ([]storage.DeltaRecord, error) {
	rows, err := a.db.Query(
		`SELECT applied_at_version, transformed_blob FROM deltas
		 WHERE wave_id = ? AND wavelet_id = ? ORDER BY applied_at_version`,
		a.waveID, a.waveletID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read all: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []storage.DeltaRecord
	for rows.Next() {
		var appliedAt int64
		var blob []byte
		if err := rows.Scan(&appliedAt, &blob); err != nil {
			return nil, fmt.Errorf("sqlite: scan: %w", err)
		}
		rec, err := recordFrom(uint64(appliedAt), blob)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetDelta returns the record applied at the given version.
func (a *deltasAccess) GetDelta(appliedAtVersion uint64) (storage.DeltaRecord, bool, error) {
	var blob []byte
	err := a.db.QueryRow(
		`SELECT transformed_blob FROM deltas
		 WHERE wave_id = ? AND wavelet_id = ? AND applied_at_version = ?`,
		a.waveID, a.waveletID, appliedAtVersion).Scan(&blob)
	if err == sql.ErrNoRows {
		return storage.DeltaRecord{}, false, nil
	}
	if err != nil {
		return storage.DeltaRecord{}, false, fmt.Errorf("sqlite: get delta %d: %w", appliedAtVersion, err)
	}
	rec, err := recordFrom(appliedAtVersion, blob)
	return rec, err == nil, err
}

// EndVersion returns the resulting version of the last record. resulting_version
// is strictly increasing in lockstep with applied_at_version (contiguous,
// append-only log), so the max resulting_version is the last-applied record.
func (a *deltasAccess) EndVersion() (version.HashedVersion, bool, error) {
	var v int64
	var hash []byte
	err := a.db.QueryRow(
		`SELECT resulting_version, resulting_hash FROM deltas
		 WHERE wave_id = ? AND wavelet_id = ?
		 ORDER BY resulting_version DESC LIMIT 1`,
		a.waveID, a.waveletID).Scan(&v, &hash)
	if err == sql.ErrNoRows {
		return version.HashedVersion{}, false, nil
	}
	if err != nil {
		return version.HashedVersion{}, false, fmt.Errorf("sqlite: end version: %w", err)
	}
	return version.NewHashedVersion(uint64(v), hash), true, nil
}

// IsEmpty reports whether the wavelet has any deltas.
func (a *deltasAccess) IsEmpty() (bool, error) {
	var exists int
	err := a.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM deltas WHERE wave_id = ? AND wavelet_id = ?)`,
		a.waveID, a.waveletID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("sqlite: is empty: %w", err)
	}
	return exists == 0, nil
}
