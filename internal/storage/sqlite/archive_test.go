package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage/sqlite"
)

// TestArchiveSetAndList exercises the inbox-archive store: archiving is per
// (participant, wavelet), toggleable, and scoped per participant.
func TestArchiveSetAndList(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	alice, _ := id.NewParticipantID("alice@example.com")
	bob, _ := id.NewParticipantID("bob@example.com")
	w1 := waveletName(t, "w+1", "conv+root")
	w2 := waveletName(t, "w+2", "conv+root")

	// Absent ⇒ nothing archived.
	if a, err := store.ArchivedWaves(alice); err != nil || len(a) != 0 {
		t.Fatalf("empty archived = %v, %v; want {} nil", a, err)
	}

	if err := store.SetArchived(alice, w1, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetArchived(alice, w2, true); err != nil {
		t.Fatal(err)
	}
	got, err := store.ArchivedWaves(alice)
	if err != nil {
		t.Fatal(err)
	}
	if !got[w1.Serialize()] || !got[w2.Serialize()] || len(got) != 2 {
		t.Errorf("archived = %v, want {w1, w2}", got)
	}

	// Un-archiving removes it from the set (archived=0 is not returned).
	if err := store.SetArchived(alice, w1, false); err != nil {
		t.Fatal(err)
	}
	got, _ = store.ArchivedWaves(alice)
	if got[w1.Serialize()] || !got[w2.Serialize()] || len(got) != 1 {
		t.Errorf("after un-archive w1, archived = %v, want {w2}", got)
	}

	// Scoped per participant: bob has none.
	if a, _ := store.ArchivedWaves(bob); len(a) != 0 {
		t.Errorf("bob archived = %v, want {}", a)
	}
}
