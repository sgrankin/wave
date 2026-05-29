package sqlite

import (
	"database/sql"
	"fmt"

	"github.com/sgrankin/wave/internal/id"
)

// snapshotsSchema stores materialized wavelet snapshots keyed by wavelet + the
// version they capture. The state blob is opaque to SQLite (encoded by package
// snapshot). Snapshots are a derivable cache, so this is plain storage with no
// hash-chain semantics.
const snapshotsSchema = `
CREATE TABLE IF NOT EXISTS snapshots (
  wave_id    TEXT    NOT NULL,
  wavelet_id TEXT    NOT NULL,
  version    INTEGER NOT NULL,
  state      BLOB    NOT NULL,
  PRIMARY KEY (wave_id, wavelet_id, version)
);
`

// GetLatestSnapshot returns the highest-versioned snapshot for the wavelet.
func (s *Store) GetLatestSnapshot(name id.WaveletName) (uint64, []byte, bool, error) {
	var v int64
	var blob []byte
	err := s.db.QueryRow(
		`SELECT version, state FROM snapshots
		 WHERE wave_id = ? AND wavelet_id = ?
		 ORDER BY version DESC LIMIT 1`,
		name.Wave().Serialize(), name.Wavelet().Serialize()).Scan(&v, &blob)
	if err == sql.ErrNoRows {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("sqlite: get latest snapshot %s: %w", name, err)
	}
	return uint64(v), blob, true, nil
}

// PutSnapshot stores (or replaces) the snapshot for the wavelet at version.
func (s *Store) PutSnapshot(name id.WaveletName, snapshotVersion uint64, blob []byte) error {
	if _, err := s.db.Exec(
		`INSERT OR REPLACE INTO snapshots (wave_id, wavelet_id, version, state) VALUES (?, ?, ?, ?)`,
		name.Wave().Serialize(), name.Wavelet().Serialize(), snapshotVersion, blob); err != nil {
		return fmt.Errorf("sqlite: put snapshot %s@%d: %w", name, snapshotVersion, err)
	}
	return nil
}
