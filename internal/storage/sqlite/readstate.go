package sqlite

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
)

// readStateSchema records per-participant read progress per wavelet (the wavelet
// version the participant last marked read), keyed by the serialized wavelet
// name. Private per-user state, not hash-chained.
const readStateSchema = `
CREATE TABLE IF NOT EXISTS read_state (
  participant_id TEXT    NOT NULL,
  wavelet        TEXT    NOT NULL,
  read_version   INTEGER NOT NULL,
  PRIMARY KEY (participant_id, wavelet)
);
`

// SetReadVersion records that participant has read wavelet through version,
// monotonically (a lower version than the stored one is ignored via MAX).
func (s *Store) SetReadVersion(participant id.ParticipantID, wavelet id.WaveletName, version uint64) error {
	if _, err := s.db.Exec(
		`INSERT INTO read_state (participant_id, wavelet, read_version) VALUES (?, ?, ?)
		 ON CONFLICT(participant_id, wavelet)
		 DO UPDATE SET read_version = MAX(read_version, excluded.read_version)`,
		participant.Address(), wavelet.Serialize(), version); err != nil {
		return fmt.Errorf("sqlite: set read version %s/%s: %w", participant, wavelet, err)
	}
	return nil
}

// ReadVersions returns all of participant's read versions, keyed by serialized
// wavelet name.
func (s *Store) ReadVersions(participant id.ParticipantID) (map[string]uint64, error) {
	rows, err := s.db.Query(
		`SELECT wavelet, read_version FROM read_state WHERE participant_id = ?`,
		participant.Address())
	if err != nil {
		return nil, fmt.Errorf("sqlite: read versions %s: %w", participant, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]uint64{}
	for rows.Next() {
		var name string
		var v int64
		if err := rows.Scan(&name, &v); err != nil {
			return nil, fmt.Errorf("sqlite: read versions scan: %w", err)
		}
		out[name] = uint64(v)
	}
	return out, rows.Err()
}
