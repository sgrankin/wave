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

// TestBlipReadStateMonotonicAndScoped exercises the per-blip read-state store: it is
// monotonic per blip, scoped per (participant, wavelet), and independent of the
// wavelet-level read state.
func TestBlipReadStateMonotonicAndScoped(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	alice, _ := id.NewParticipantID("alice@example.com")
	bob, _ := id.NewParticipantID("bob@example.com")
	w := waveletName(t, "w+conv", "conv+root")

	if v, err := store.BlipReadVersions(alice, w); err != nil || len(v) != 0 {
		t.Fatalf("absent blip read versions = %v, %v; want {} nil", v, err)
	}

	for _, set := range []struct {
		blip string
		ver  uint64
	}{{"b+root", 5}, {"b+root", 3}, {"b+reply", 9}} { // 3 < 5 must be ignored (monotonic)
		if err := store.SetBlipReadVersion(alice, w, set.blip, set.ver); err != nil {
			t.Fatalf("set blip read %s@%d: %v", set.blip, set.ver, err)
		}
	}

	got, err := store.BlipReadVersions(alice, w)
	if err != nil {
		t.Fatal(err)
	}
	if got["b+root"] != 5 {
		t.Errorf("b+root read version = %d, want 5 (monotonic; lower ignored)", got["b+root"])
	}
	if got["b+reply"] != 9 {
		t.Errorf("b+reply read version = %d, want 9", got["b+reply"])
	}

	// Scoped per (participant, wavelet): bob and another wavelet are empty.
	if v, _ := store.BlipReadVersions(bob, w); len(v) != 0 {
		t.Errorf("bob blip read versions = %v, want {}", v)
	}
	if v, _ := store.BlipReadVersions(alice, waveletName(t, "w+other", "conv+root")); len(v) != 0 {
		t.Errorf("other-wavelet blip read versions = %v, want {}", v)
	}
	// Per-blip is independent of the wavelet-level read state.
	if v, _ := store.ReadVersions(alice); len(v) != 0 {
		t.Errorf("wavelet-level read versions = %v, want {} (per-blip writes must not touch it)", v)
	}
}
