package sqlite

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
)

// indexSchema is the derived read index. wave_participants is the inbox: which
// participants belong to which wavelet (rebuildable from the delta log). Search
// (FTS5) is layered on later. Serialized ids are stored; they parse back
// unambiguously ('/' and '!' separators appear in neither domains nor local ids).
const indexSchema = `
CREATE TABLE IF NOT EXISTS wave_participants (
  participant_id TEXT NOT NULL,
  wave_id        TEXT NOT NULL,
  wavelet_id     TEXT NOT NULL,
  PRIMARY KEY (participant_id, wave_id, wavelet_id)
);
CREATE INDEX IF NOT EXISTS idx_wp_wavelet ON wave_participants (wave_id, wavelet_id);
`

// SetWaveletParticipants replaces the recorded participant set for a wavelet in
// one transaction (delete old rows, insert current).
func (s *Store) SetWaveletParticipants(name id.WaveletName, participants []id.ParticipantID) error {
	waveID := name.Wave().Serialize()
	waveletID := name.Wavelet().Serialize()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: index begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM wave_participants WHERE wave_id = ? AND wavelet_id = ?`, waveID, waveletID); err != nil {
		return fmt.Errorf("sqlite: clear participants %s: %w", name, err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO wave_participants (participant_id, wave_id, wavelet_id) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare participants: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range participants {
		if _, err := stmt.Exec(p.Address(), waveID, waveletID); err != nil {
			return fmt.Errorf("sqlite: index participant %s: %w", p, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: index commit: %w", err)
	}
	return nil
}

// DeleteWaveletIndex removes a wavelet's index rows.
func (s *Store) DeleteWaveletIndex(name id.WaveletName) error {
	if _, err := s.db.Exec(
		`DELETE FROM wave_participants WHERE wave_id = ? AND wavelet_id = ?`,
		name.Wave().Serialize(), name.Wavelet().Serialize()); err != nil {
		return fmt.Errorf("sqlite: delete index %s: %w", name, err)
	}
	return nil
}

// InboxWavelets returns the wavelets a participant currently belongs to.
func (s *Store) InboxWavelets(participant id.ParticipantID) ([]id.WaveletName, error) {
	rows, err := s.db.Query(
		`SELECT wave_id, wavelet_id FROM wave_participants WHERE participant_id = ?
		 ORDER BY wave_id, wavelet_id`, participant.Address())
	if err != nil {
		return nil, fmt.Errorf("sqlite: inbox %s: %w", participant, err)
	}
	defer func() { _ = rows.Close() }()
	var out []id.WaveletName
	for rows.Next() {
		var waveStr, waveletStr string
		if err := rows.Scan(&waveStr, &waveletStr); err != nil {
			return nil, fmt.Errorf("sqlite: inbox scan: %w", err)
		}
		name, err := parseWaveletName(waveStr, waveletStr)
		if err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// parseWaveletName reconstructs a WaveletName from stored serialized ids.
func parseWaveletName(waveStr, waveletStr string) (id.WaveletName, error) {
	wave, err := id.ParseWaveID(waveStr)
	if err != nil {
		return id.WaveletName{}, fmt.Errorf("sqlite: bad stored wave id %q: %w", waveStr, err)
	}
	wavelet, err := id.ParseWaveletID(waveletStr)
	if err != nil {
		return id.WaveletName{}, fmt.Errorf("sqlite: bad stored wavelet id %q: %w", waveletStr, err)
	}
	return id.NewWaveletName(wave, wavelet), nil
}
