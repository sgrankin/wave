package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage/sqlite"
)

func TestReadStateMonotonicAndScoped(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	alice, _ := id.NewParticipantID("alice@example.com")
	bob, _ := id.NewParticipantID("bob@example.com")
	w1 := waveletName(t, "w+1", "conv+root")
	w2 := waveletName(t, "w+2", "conv+root")

	// Absent ⇒ no entry.
	if v, err := store.ReadVersions(alice); err != nil || len(v) != 0 {
		t.Fatalf("empty read versions = %v, %v; want {} nil", v, err)
	}

	if err := store.SetReadVersion(alice, w1, 5); err != nil {
		t.Fatal(err)
	}
	// Monotonic: a lower version does not regress.
	if err := store.SetReadVersion(alice, w1, 3); err != nil {
		t.Fatal(err)
	}
	// A higher version advances.
	if err := store.SetReadVersion(alice, w2, 9); err != nil {
		t.Fatal(err)
	}

	got, err := store.ReadVersions(alice)
	if err != nil {
		t.Fatal(err)
	}
	if got[w1.Serialize()] != 5 {
		t.Errorf("w1 read version = %d, want 5 (monotonic; lower ignored)", got[w1.Serialize()])
	}
	if got[w2.Serialize()] != 9 {
		t.Errorf("w2 read version = %d, want 9", got[w2.Serialize()])
	}

	// Scoped per participant: bob has none.
	if v, err := store.ReadVersions(bob); err != nil || len(v) != 0 {
		t.Errorf("bob read versions = %v, %v; want {} nil", v, err)
	}
}
