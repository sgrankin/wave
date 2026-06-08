package sqlite

import (
	"database/sql"
	"fmt"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// credentialsSchema binds external credentials to accounts. Keyed by
// (method, subject) — the method that minted the proof and that method's stable
// per-user identifier; account is the resolved address; data is method-specific
// JSON (opaque to the store, so it deliberately does NOT use the frozen delta
// codec). The account index backs ListByAccount (account-linking / merge).
const credentialsSchema = `
CREATE TABLE IF NOT EXISTS credentials (
  method     TEXT    NOT NULL,
  subject    TEXT    NOT NULL,
  account    TEXT    NOT NULL,
  data       TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (method, subject)
);
CREATE INDEX IF NOT EXISTS idx_credentials_account ON credentials (account);
`

// GetCredential returns the credential for (method, subject), and whether one
// exists.
func (s *Store) GetCredential(method, subject string) (storage.Credential, bool, error) {
	var account, data string
	var createdAt int64
	err := s.db.QueryRow(
		`SELECT account, data, created_at FROM credentials WHERE method = ? AND subject = ?`,
		method, subject).Scan(&account, &data, &createdAt)
	if err == sql.ErrNoRows {
		return storage.Credential{}, false, nil
	}
	if err != nil {
		return storage.Credential{}, false, fmt.Errorf("sqlite: get credential %s/%s: %w", method, subject, err)
	}
	pid, err := id.NewParticipantID(account)
	if err != nil {
		return storage.Credential{}, false, fmt.Errorf("sqlite: credential %s/%s has bad account %q: %w", method, subject, account, err)
	}
	return storage.Credential{
		Method:    method,
		Subject:   subject,
		Account:   pid,
		Data:      data,
		CreatedAt: createdAt,
	}, true, nil
}

// PutCredential creates or replaces the credential keyed by (method, subject). On
// replace it preserves the original created_at (the binding's age is stable):
// COALESCE keeps the row's existing value, falling back to the supplied one for a
// fresh insert.
func (s *Store) PutCredential(c storage.Credential) error {
	if _, err := s.db.Exec(
		`INSERT INTO credentials (method, subject, account, data, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (method, subject) DO UPDATE SET account = excluded.account, data = excluded.data`,
		c.Method, c.Subject, c.Account.Address(), c.Data, c.CreatedAt); err != nil {
		return fmt.Errorf("sqlite: put credential %s/%s: %w", c.Method, c.Subject, err)
	}
	return nil
}

// ListByAccount returns every credential bound to account (uses
// idx_credentials_account).
func (s *Store) ListByAccount(account id.ParticipantID) ([]storage.Credential, error) {
	rows, err := s.db.Query(
		`SELECT method, subject, data, created_at FROM credentials WHERE account = ?`,
		account.Address())
	if err != nil {
		return nil, fmt.Errorf("sqlite: list credentials for %s: %w", account, err)
	}
	defer func() { _ = rows.Close() }()
	var out []storage.Credential
	for rows.Next() {
		var method, subject, data string
		var createdAt int64
		if err := rows.Scan(&method, &subject, &data, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: list credentials scan: %w", err)
		}
		out = append(out, storage.Credential{
			Method:    method,
			Subject:   subject,
			Account:   account,
			Data:      data,
			CreatedAt: createdAt,
		})
	}
	return out, rows.Err()
}
