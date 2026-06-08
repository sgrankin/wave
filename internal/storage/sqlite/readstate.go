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
CREATE TABLE IF NOT EXISTS blip_read_state (
  participant_id TEXT    NOT NULL,
  wavelet        TEXT    NOT NULL,
  blip_id        TEXT    NOT NULL,
  read_version   INTEGER NOT NULL,
  PRIMARY KEY (participant_id, wavelet, blip_id)
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

// SetBlipReadVersion records that participant has read blip blipID of wavelet
// through version, monotonically per blip (a lower version is ignored via MAX).
func (s *Store) SetBlipReadVersion(participant id.ParticipantID, wavelet id.WaveletName, blipID string, version uint64) error {
	if _, err := s.db.Exec(
		`INSERT INTO blip_read_state (participant_id, wavelet, blip_id, read_version) VALUES (?, ?, ?, ?)
		 ON CONFLICT(participant_id, wavelet, blip_id)
		 DO UPDATE SET read_version = MAX(read_version, excluded.read_version)`,
		participant.Address(), wavelet.Serialize(), blipID, version); err != nil {
		return fmt.Errorf("sqlite: set blip read version %s/%s/%s: %w", participant, wavelet, blipID, err)
	}
	return nil
}

// BlipReadVersions returns participant's per-blip read versions for one wavelet,
// keyed by blip id.
func (s *Store) BlipReadVersions(participant id.ParticipantID, wavelet id.WaveletName) (map[string]uint64, error) {
	rows, err := s.db.Query(
		`SELECT blip_id, read_version FROM blip_read_state WHERE participant_id = ? AND wavelet = ?`,
		participant.Address(), wavelet.Serialize())
	if err != nil {
		return nil, fmt.Errorf("sqlite: blip read versions %s/%s: %w", participant, wavelet, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]uint64{}
	for rows.Next() {
		var blipID string
		var v int64
		if err := rows.Scan(&blipID, &v); err != nil {
			return nil, fmt.Errorf("sqlite: blip read versions scan: %w", err)
		}
		out[blipID] = uint64(v)
	}
	return out, rows.Err()
}
