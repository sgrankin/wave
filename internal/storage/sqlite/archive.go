package sqlite

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
)

// archiveSchema records which waves a participant has archived out of their inbox.
// Private per-user state (like read_state): an archived=1 row hides the wave from the
// default inbox view. Archiving is a personal inbox preference, NOT a membership
// change — the wave is still openable and the participant still receives its deltas.
const archiveSchema = `
CREATE TABLE IF NOT EXISTS inbox_archive (
  participant_id TEXT    NOT NULL,
  wavelet        TEXT    NOT NULL,
  archived       INTEGER NOT NULL,
  PRIMARY KEY (participant_id, wavelet)
);
`

// SetArchived sets whether participant has archived wavelet out of their inbox.
func (s *Store) SetArchived(participant id.ParticipantID, wavelet id.WaveletName, archived bool) error {
	a := 0
	if archived {
		a = 1
	}
	if _, err := s.db.Exec(
		`INSERT INTO inbox_archive (participant_id, wavelet, archived) VALUES (?, ?, ?)
		 ON CONFLICT(participant_id, wavelet) DO UPDATE SET archived = excluded.archived`,
		participant.Address(), wavelet.Serialize(), a); err != nil {
		return fmt.Errorf("sqlite: set archived %s/%s: %w", participant, wavelet, err)
	}
	return nil
}

// ArchivedWaves returns the set of wavelets participant has archived (archived=1),
// keyed by serialized wavelet name.
func (s *Store) ArchivedWaves(participant id.ParticipantID) (map[string]bool, error) {
	rows, err := s.db.Query(
		`SELECT wavelet FROM inbox_archive WHERE participant_id = ? AND archived = 1`,
		participant.Address())
	if err != nil {
		return nil, fmt.Errorf("sqlite: archived waves %s: %w", participant, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sqlite: archived waves scan: %w", err)
		}
		out[name] = true
	}
	return out, rows.Err()
}
