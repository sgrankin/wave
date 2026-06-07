package sqlite

import (
	"database/sql"
	"fmt"
)

// settingsSchema stores singleton server settings as opaque key→blob rows (the
// session signing key, etc.). Not hash-chained; values are opaque, so this
// deliberately does NOT use the frozen delta codec.
const settingsSchema = `
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value BLOB NOT NULL
);
`

// GetSetting returns the value for key, and whether it exists.
func (s *Store) GetSetting(key string) ([]byte, bool, error) {
	var value []byte
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite: get setting %q: %w", key, err)
	}
	return value, true, nil
}

// PutSetting creates or replaces the value for key.
func (s *Store) PutSetting(key string, value []byte) error {
	if _, err := s.db.Exec(
		`INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, key, value); err != nil {
		return fmt.Errorf("sqlite: put setting %q: %w", key, err)
	}
	return nil
}
