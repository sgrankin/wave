package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// TestWaveletMetaMigration: a database created before wavelet_meta gained its digest
// columns (last_modified_time, title, snippet) is migrated in place on Open — the new
// columns are added (so digest writes/reads work), and the legacy row survives with
// its preserved version and the empty/zero defaults.
func TestWaveletMetaMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	legacy := waveletName(t, "w+legacy", "conv+root")
	alice := pid(t, "alice@example.com")

	// Stand up the OLD schema (no digest columns) and seed a meta row + inbox
	// membership, so the legacy wave appears in a digest query after migration.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE wavelet_meta (
		   wave_id TEXT NOT NULL, wavelet_id TEXT NOT NULL, creator TEXT NOT NULL,
		   last_modified_version INTEGER NOT NULL, PRIMARY KEY (wave_id, wavelet_id))`,
		`CREATE TABLE wave_participants (
		   participant_id TEXT NOT NULL, wave_id TEXT NOT NULL, wavelet_id TEXT NOT NULL,
		   PRIMARY KEY (participant_id, wave_id, wavelet_id))`,
	} {
		if _, err := raw.Exec(ddl); err != nil {
			t.Fatalf("create old schema: %v", err)
		}
	}
	waveStr, waveletStr := legacy.Wave().Serialize(), legacy.Wavelet().Serialize()
	if _, err := raw.Exec(
		`INSERT INTO wavelet_meta (wave_id, wavelet_id, creator, last_modified_version) VALUES (?, ?, ?, ?)`,
		waveStr, waveletStr, alice.Address(), 7); err != nil {
		t.Fatalf("seed legacy meta: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO wave_participants (participant_id, wave_id, wavelet_id) VALUES (?, ?, ?)`,
		alice.Address(), waveStr, waveletStr); err != nil {
		t.Fatalf("seed legacy membership: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Open through the store → migrateWaveletMeta adds the columns in place.
	store := openStore(t, path)
	defer store.Close()

	// The legacy wave still resolves, with its preserved version and default digest.
	ds, err := store.InboxDigests(alice, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("inbox digests = %d, want 1 (legacy wave)", len(ds))
	}
	if ds[0].Version != 7 {
		t.Errorf("legacy version = %d, want 7 (preserved across migration)", ds[0].Version)
	}
	if ds[0].Title != "" || ds[0].Snippet != "" || ds[0].LastModifiedTime != 0 {
		t.Errorf("legacy digest = %+v, want empty title/snippet/time defaults", ds[0])
	}

	// A fresh digest write exercises the new columns end to end.
	fresh := waveletName(t, "w+fresh", "conv+root")
	if err := store.SetWaveletParticipants(fresh, []id.ParticipantID{alice}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWaveletMeta(fresh, storage.WaveletMeta{
		Creator: alice, LastModifiedVersion: 3, LastModifiedTime: 4242, Title: "Hello", Snippet: "Hello world",
	}); err != nil {
		t.Fatalf("set meta after migrate: %v", err)
	}
	ds, err = store.InboxDigests(alice, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got *storage.WaveDigest
	for i := range ds {
		if ds[i].Title == "Hello" {
			got = &ds[i]
		}
	}
	if got == nil {
		t.Fatalf("fresh digest not found in %+v", ds)
	}
	if got.Snippet != "Hello world" || got.LastModifiedTime != 4242 || got.Version != 3 {
		t.Errorf("fresh digest = %+v, want Hello/Hello world/4242/v3", *got)
	}
}

// TestOpenIsIdempotentForDigestColumns: opening an already-migrated (current-schema)
// database does not error — the migration sees the columns present and adds nothing.
func TestOpenIsIdempotentForDigestColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wave.db")
	openStore(t, path).Close()
	openStore(t, path).Close() // second open must be a no-op, not a duplicate-column error
}
