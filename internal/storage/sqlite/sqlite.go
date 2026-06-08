// Package sqlite is the SQLite-backed DeltaStore (the default backend). It uses
// the pure-Go modernc.org/sqlite driver (CGO-free), WAL journaling, and the full
// WaveletDeltaRecord per delta so snapshot+tail replay is bit-identical (stored
// hashes/timestamps are read back, never recomputed).
//
// Spec: docs/specs/05-storage-persistence.md; schema in docs/architecture/01-target-architecture.md.
package sqlite

import (
	"context"
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
	// This is also load-bearing for an in-memory database (path ":memory:"): the
	// in-memory DB lives only as long as its owning connection, so pinning to a
	// single connection keeps it alive for the process. Do not add
	// SetConnMaxLifetime or raise the max without revisiting that.
	db.SetMaxOpenConns(1)
	// Each store contributes its own DDL (delta log, accounts, …). All are
	// CREATE TABLE IF NOT EXISTS, so running them on every open is idempotent.
	for _, ddl := range []string{schema, accountsSchema, snapshotsSchema, indexSchema, settingsSchema, readStateSchema, credentialsSchema} {
		if _, err := db.Exec(ddl); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite: init schema: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// Compile-time assertions that Store implements the storage contracts.
var (
	_ storage.DeltaStore      = (*Store)(nil)
	_ storage.AccountStore    = (*Store)(nil)
	_ storage.SnapshotStore   = (*Store)(nil)
	_ storage.IndexStore      = (*Store)(nil)
	_ storage.SettingsStore   = (*Store)(nil)
	_ storage.ReadStateStore  = (*Store)(nil)
	_ storage.CredentialStore = (*Store)(nil)
)

// Checkpoint forces a WAL checkpoint (TRUNCATE), folding the write-ahead log
// back into the main database file and truncating it. Closing the database also
// checkpoints, but an explicit checkpoint at a clean shutdown point keeps the
// on-disk file self-contained (no large -wal sidecar) for backup/inspection.
func (s *Store) Checkpoint() error {
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("sqlite: wal checkpoint: %w", err)
	}
	return nil
}

// Backup writes a consistent snapshot of the whole database to destPath using
// SQLite's `VACUUM INTO`. It is safe to run on a live database (WAL active): the
// snapshot is transactionally consistent, fully checkpointed, and defragmented —
// unlike a raw file copy, which can be torn while the WAL has uncommitted frames.
// destPath must not already exist (SQLite refuses to overwrite an existing file).
func (s *Store) Backup(ctx context.Context, destPath string) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", destPath); err != nil {
		return fmt.Errorf("sqlite: backup to %q: %w", destPath, err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database is reachable — the readiness check backing /readyz.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Open returns delta access for the named wavelet.
func (s *Store) Open(name id.WaveletName) (storage.DeltasAccess, error) {
	return &deltasAccess{
		db:        s.db,
		waveID:    name.Wave().Serialize(),
		waveletID: name.Wavelet().Serialize(),
	}, nil
}

// Delete permanently removes a wavelet's delta log, returning whether anything
// was deleted.
func (s *Store) Delete(name id.WaveletName) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM deltas WHERE wave_id = ? AND wavelet_id = ?`,
		name.Wave().Serialize(), name.Wavelet().Serialize())
	if err != nil {
		return false, fmt.Errorf("sqlite: delete wavelet %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlite: delete wavelet %s: rows affected: %w", name, err)
	}
	return n > 0, nil
}

// Lookup returns the wavelet IDs of the given wave that have at least one delta.
// Every row in the table is a delta, so any wavelet present is non-empty.
func (s *Store) Lookup(wave id.WaveID) ([]id.WaveletID, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT wavelet_id FROM deltas WHERE wave_id = ? ORDER BY wavelet_id`,
		wave.Serialize())
	if err != nil {
		return nil, fmt.Errorf("sqlite: lookup %s: %w", wave, err)
	}
	defer func() { _ = rows.Close() }()
	var out []id.WaveletID
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("sqlite: lookup scan: %w", err)
		}
		wl, err := id.ParseWaveletID(s)
		if err != nil {
			return nil, fmt.Errorf("sqlite: lookup: bad stored wavelet id %q: %w", s, err)
		}
		out = append(out, wl)
	}
	return out, rows.Err()
}

// WaveIDs returns all wave IDs with at least one non-empty wavelet (a snapshot).
func (s *Store) WaveIDs() ([]id.WaveID, error) {
	rows, err := s.db.Query(`SELECT DISTINCT wave_id FROM deltas ORDER BY wave_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: wave ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []id.WaveID
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("sqlite: wave ids scan: %w", err)
		}
		w, err := id.ParseWaveID(s)
		if err != nil {
			return nil, fmt.Errorf("sqlite: wave ids: bad stored wave id %q: %w", s, err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
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
			Nonce:            r.Nonce,
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
		Nonce:            sd.Nonce,
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

// ReadFrom returns records with applied_at_version >= from, in application order.
func (a *deltasAccess) ReadFrom(from uint64) ([]storage.DeltaRecord, error) {
	rows, err := a.db.Query(
		`SELECT applied_at_version, transformed_blob FROM deltas
		 WHERE wave_id = ? AND wavelet_id = ? AND applied_at_version >= ?
		 ORDER BY applied_at_version`,
		a.waveID, a.waveletID, from)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read from %d: %w", from, err)
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

// GetDeltaByEndVersion returns the record whose resulting version equals the
// given version (uses idx_deltas_end).
func (a *deltasAccess) GetDeltaByEndVersion(resultingVersion uint64) (storage.DeltaRecord, bool, error) {
	var appliedAt int64
	var blob []byte
	err := a.db.QueryRow(
		`SELECT applied_at_version, transformed_blob FROM deltas
		 WHERE wave_id = ? AND wavelet_id = ? AND resulting_version = ?`,
		a.waveID, a.waveletID, resultingVersion).Scan(&appliedAt, &blob)
	if err == sql.ErrNoRows {
		return storage.DeltaRecord{}, false, nil
	}
	if err != nil {
		return storage.DeltaRecord{}, false, fmt.Errorf("sqlite: get delta by end version %d: %w", resultingVersion, err)
	}
	rec, err := recordFrom(uint64(appliedAt), blob)
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
