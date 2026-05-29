package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// accountsSchema stores accounts with the discriminator and ParticipantID as
// columns and the kind-specific fields in a JSON column (the "lean on JSON"
// principle — accounts are not hash-chained, so JSON is fine and inspectable;
// it deliberately does NOT use the frozen delta codec).
const accountsSchema = `
CREATE TABLE IF NOT EXISTS accounts (
  participant_id TEXT PRIMARY KEY,
  kind           TEXT NOT NULL,
  data           TEXT NOT NULL
);
`

// GetAccount returns the account for pid, and whether it exists.
func (s *Store) GetAccount(pid id.ParticipantID) (*storage.Account, bool, error) {
	var kind, data string
	err := s.db.QueryRow(
		`SELECT kind, data FROM accounts WHERE participant_id = ?`, pid.Address()).Scan(&kind, &data)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite: get account %s: %w", pid, err)
	}
	acct := &storage.Account{ID: pid, Kind: storage.AccountKind(kind)}
	switch acct.Kind {
	case storage.AccountHuman:
		var h storage.HumanAccount
		if err := json.Unmarshal([]byte(data), &h); err != nil {
			return nil, false, fmt.Errorf("sqlite: decode human account %s: %w", pid, err)
		}
		acct.Human = &h
	case storage.AccountRobot:
		var r storage.RobotAccount
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, false, fmt.Errorf("sqlite: decode robot account %s: %w", pid, err)
		}
		acct.Robot = &r
	default:
		return nil, false, fmt.Errorf("sqlite: account %s has unknown kind %q", pid, kind)
	}
	return acct, true, nil
}

// PutAccount creates or replaces an account.
func (s *Store) PutAccount(a *storage.Account) error {
	var payload any
	switch a.Kind {
	case storage.AccountHuman:
		if a.Human == nil {
			return fmt.Errorf("sqlite: human account %s missing Human data", a.ID)
		}
		payload = a.Human
	case storage.AccountRobot:
		if a.Robot == nil {
			return fmt.Errorf("sqlite: robot account %s missing Robot data", a.ID)
		}
		payload = a.Robot
	default:
		return fmt.Errorf("sqlite: account %s has unknown kind %q", a.ID, a.Kind)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sqlite: encode account %s: %w", a.ID, err)
	}
	if _, err := s.db.Exec(
		`INSERT OR REPLACE INTO accounts (participant_id, kind, data) VALUES (?, ?, ?)`,
		a.ID.Address(), string(a.Kind), string(data)); err != nil {
		return fmt.Errorf("sqlite: put account %s: %w", a.ID, err)
	}
	return nil
}

// RemoveAccount deletes an account (no-op if absent).
func (s *Store) RemoveAccount(pid id.ParticipantID) error {
	if _, err := s.db.Exec(`DELETE FROM accounts WHERE participant_id = ?`, pid.Address()); err != nil {
		return fmt.Errorf("sqlite: remove account %s: %w", pid, err)
	}
	return nil
}
