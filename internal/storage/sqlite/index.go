package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// migrateWaveletMeta adds the digest-projection columns (last_modified_time, title,
// snippet) to wavelet_meta when a database predates them. The index is a rebuildable
// cache, so existing rows take the empty/zero column defaults until the next commit
// (or a Rebuild) backfills them. Idempotent: a fresh database already has the columns
// from indexSchema, so nothing is altered.
func migrateWaveletMeta(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(wavelet_meta)`)
	if err != nil {
		return fmt.Errorf("sqlite: inspect wavelet_meta: %w", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlite: scan table_info: %w", err)
		}
		have[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, c := range []struct{ name, decl string }{
		{"last_modified_time", "INTEGER NOT NULL DEFAULT 0"},
		{"title", "TEXT NOT NULL DEFAULT ''"},
		{"snippet", "TEXT NOT NULL DEFAULT ''"},
	} {
		if have[c.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE wavelet_meta ADD COLUMN " + c.name + " " + c.decl); err != nil {
			return fmt.Errorf("sqlite: add wavelet_meta.%s: %w", c.name, err)
		}
	}
	return nil
}

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

CREATE TABLE IF NOT EXISTS wavelet_meta (
  wave_id               TEXT    NOT NULL,
  wavelet_id            TEXT    NOT NULL,
  creator               TEXT    NOT NULL,
  last_modified_version INTEGER NOT NULL,
  last_modified_time    INTEGER NOT NULL DEFAULT 0,
  title                 TEXT    NOT NULL DEFAULT '',
  snippet               TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (wave_id, wavelet_id)
);

CREATE VIRTUAL TABLE IF NOT EXISTS blip_text USING fts5 (
  wave_id    UNINDEXED,
  wavelet_id UNINDEXED,
  blip_id    UNINDEXED,
  text
);
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

// SetWaveletMeta records a wavelet's digest projection (creator, last-modified
// version + time, precomputed title and snippet).
func (s *Store) SetWaveletMeta(name id.WaveletName, meta storage.WaveletMeta) error {
	if _, err := s.db.Exec(
		`INSERT OR REPLACE INTO wavelet_meta
		   (wave_id, wavelet_id, creator, last_modified_version, last_modified_time, title, snippet)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name.Wave().Serialize(), name.Wavelet().Serialize(), meta.Creator.Address(),
		meta.LastModifiedVersion, meta.LastModifiedTime, meta.Title, meta.Snippet); err != nil {
		return fmt.Errorf("sqlite: set wavelet meta %s: %w", name, err)
	}
	return nil
}

// SetBlipText replaces a blip's searchable text in the FTS index.
func (s *Store) SetBlipText(name id.WaveletName, blipID, text string) error {
	waveID, waveletID := name.Wave().Serialize(), name.Wavelet().Serialize()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: blip text begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`DELETE FROM blip_text WHERE wave_id = ? AND wavelet_id = ? AND blip_id = ?`,
		waveID, waveletID, blipID); err != nil {
		return fmt.Errorf("sqlite: clear blip text %s/%s: %w", name, blipID, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO blip_text (wave_id, wavelet_id, blip_id, text) VALUES (?, ?, ?, ?)`,
		waveID, waveletID, blipID, text); err != nil {
		return fmt.Errorf("sqlite: insert blip text %s/%s: %w", name, blipID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: blip text commit: %w", err)
	}
	return nil
}

// DeleteWaveletIndex removes a wavelet's index rows from all index tables.
func (s *Store) DeleteWaveletIndex(name id.WaveletName) error {
	waveID, waveletID := name.Wave().Serialize(), name.Wavelet().Serialize()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("sqlite: delete index begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"wave_participants", "wavelet_meta", "blip_text"} {
		if _, err := tx.Exec(
			"DELETE FROM "+table+" WHERE wave_id = ? AND wavelet_id = ?", waveID, waveletID); err != nil {
			return fmt.Errorf("sqlite: delete index %s from %s: %w", name, table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: delete index commit: %w", err)
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

// digestColumns is the SELECT list that projects an inbox-membership row (aliased
// wp) + its LEFT-JOINed wavelet_meta (wm) into a storage.WaveDigest, including the
// wavelet's full participant set (newline-joined; addresses never contain newlines)
// via a correlated subquery. COALESCE covers a wavelet present in the inbox but
// lacking a meta row yet (rebuildable index drift). Pair with scanDigests.
const digestColumns = `wp.wave_id, wp.wavelet_id,
	COALESCE(wm.creator, ''), COALESCE(wm.last_modified_version, 0), COALESCE(wm.last_modified_time, 0),
	COALESCE(wm.title, ''), COALESCE(wm.snippet, ''),
	(SELECT GROUP_CONCAT(p2.participant_id, char(10)) FROM wave_participants p2
	 WHERE p2.wave_id = wp.wave_id AND p2.wavelet_id = wp.wavelet_id)`

// scanDigests reads rows shaped by digestColumns into WaveDigests.
func scanDigests(rows *sql.Rows) ([]storage.WaveDigest, error) {
	var out []storage.WaveDigest
	for rows.Next() {
		var waveStr, waveletStr, creator, title, snippet string
		var lmv, lmt int64
		var parts sql.NullString
		if err := rows.Scan(&waveStr, &waveletStr, &creator, &lmv, &lmt, &title, &snippet, &parts); err != nil {
			return nil, fmt.Errorf("sqlite: digest scan: %w", err)
		}
		name, err := parseWaveletName(waveStr, waveletStr)
		if err != nil {
			return nil, err
		}
		d := storage.WaveDigest{
			Wavelet: name, Creator: creator, Title: title, Snippet: snippet,
			Version: uint64(lmv), LastModifiedTime: lmt, Participants: []string{},
		}
		if parts.Valid && parts.String != "" {
			d.Participants = strings.Split(parts.String, "\n")
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// InboxDigests returns the participant's inbox as digest projections, most-recently-
// modified first, capped at limit — served entirely from the index (no wavelet load).
func (s *Store) InboxDigests(participant id.ParticipantID, limit int) ([]storage.WaveDigest, error) {
	q := `SELECT ` + digestColumns + `
		FROM wave_participants wp
		LEFT JOIN wavelet_meta wm ON wm.wave_id = wp.wave_id AND wm.wavelet_id = wp.wavelet_id
		WHERE wp.participant_id = ?
		ORDER BY COALESCE(wm.last_modified_time, 0) DESC, wp.wave_id, wp.wavelet_id`
	args := []any{participant.Address()}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: inbox digests %s: %w", participant, err)
	}
	defer func() { _ = rows.Close() }()
	return scanDigests(rows)
}

// Search returns digest projections matching q, always scoped to q.Participant's
// inbox. Free-text terms are matched against the FTS5 blip index; with:/creator:
// filter the wavelet set; results optionally order by last-modified version.
func (s *Store) Search(q storage.SearchQuery) ([]storage.WaveDigest, error) {
	var sb strings.Builder
	args := []any{q.Participant.Address()}
	sb.WriteString(`SELECT DISTINCT ` + digestColumns + `
		FROM wave_participants wp
		LEFT JOIN wavelet_meta wm ON wm.wave_id = wp.wave_id AND wm.wavelet_id = wp.wavelet_id
		WHERE wp.participant_id = ?`)

	if len(q.Terms) > 0 {
		sb.WriteString(` AND EXISTS (SELECT 1 FROM blip_text bt
			WHERE bt.wave_id = wp.wave_id AND bt.wavelet_id = wp.wavelet_id AND bt.text MATCH ?)`)
		args = append(args, ftsExpr(q.Terms))
	}
	if q.Creator != nil {
		sb.WriteString(` AND wm.creator = ?`)
		args = append(args, q.Creator.Address())
	}
	for _, w := range q.With {
		sb.WriteString(` AND EXISTS (SELECT 1 FROM wave_participants w2
			WHERE w2.wave_id = wp.wave_id AND w2.wavelet_id = wp.wavelet_id AND w2.participant_id = ?)`)
		args = append(args, w.Address())
	}
	if q.OrderByModifiedDesc {
		sb.WriteString(` ORDER BY COALESCE(wm.last_modified_time, 0) DESC, wp.wave_id, wp.wavelet_id`)
	} else {
		sb.WriteString(` ORDER BY wp.wave_id, wp.wavelet_id`)
	}
	if q.Limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, q.Limit)
	}

	rows, err := s.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanDigests(rows)
}

// ftsExpr builds an FTS5 MATCH expression from free-text terms: each term is
// double-quoted (so it is a literal string, not an operator — neutralizing FTS5
// syntax) and the terms are ANDed. Callers guarantee len(terms) > 0.
func ftsExpr(terms []string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " AND ")
}

// IsParticipant reports whether a participant currently belongs to a wavelet.
func (s *Store) IsParticipant(name id.WaveletName, participant id.ParticipantID) (bool, error) {
	var exists int
	err := s.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM wave_participants
		 WHERE wave_id = ? AND wavelet_id = ? AND participant_id = ?)`,
		name.Wave().Serialize(), name.Wavelet().Serialize(), participant.Address()).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("sqlite: is participant %s in %s: %w", participant, name, err)
	}
	return exists == 1, nil
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
