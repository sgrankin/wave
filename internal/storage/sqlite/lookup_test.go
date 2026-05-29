package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
)

// appendOne appends a single first delta to the named wavelet.
func appendOne(t *testing.T, store *sqlite.Store, name id.WaveletName, author id.ParticipantID) {
	t.Helper()
	access, err := store.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	rec := chainRecord(version.Zero(name), author, op.NewDocOp([]op.Component{op.Characters{Text: "hi"}}))
	if err := access.Append([]storage.DeltaRecord{rec}); err != nil {
		t.Fatalf("append to %s: %v", name, err)
	}
}

func TestDeltaStoreLookupAndWaveIDs(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	alice := pid(t, "alice@example.com")

	waA1 := waveletName(t, "w+a", "conv+root")
	waA2 := waveletName(t, "w+a", "user+bob")
	waB1 := waveletName(t, "w+b", "conv+root")
	appendOne(t, store, waA1, alice)
	appendOne(t, store, waA2, alice)
	appendOne(t, store, waB1, alice)

	// Lookup wave w+a → both its wavelets.
	wls, err := store.Lookup(waA1.Wave())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, wl := range wls {
		got[wl.Serialize()] = true
	}
	if len(got) != 2 || !got[waA1.Wavelet().Serialize()] || !got[waA2.Wavelet().Serialize()] {
		t.Errorf("Lookup(w+a) = %v, want the two w+a wavelets", got)
	}

	// A wave with no deltas → empty.
	noWave, _ := id.NewWaveID("example.com", "w+none")
	if wls, err := store.Lookup(noWave); err != nil || len(wls) != 0 {
		t.Errorf("Lookup(absent) = %v (err %v), want empty", wls, err)
	}

	// WaveIDs → both waves.
	waves, err := store.WaveIDs()
	if err != nil {
		t.Fatal(err)
	}
	gotW := map[string]bool{}
	for _, w := range waves {
		gotW[w.Serialize()] = true
	}
	if len(gotW) != 2 || !gotW[waA1.Wave().Serialize()] || !gotW[waB1.Wave().Serialize()] {
		t.Errorf("WaveIDs = %v, want w+a and w+b", gotW)
	}
}

func TestDeltaStoreDelete(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	name := waveletName(t, "w+a", "conv+root")
	appendOne(t, store, name, pid(t, "alice@example.com"))

	deleted, err := store.Delete(name)
	if err != nil || !deleted {
		t.Fatalf("Delete = %v (err %v), want true", deleted, err)
	}
	access, _ := store.Open(name)
	if empty, _ := access.IsEmpty(); !empty {
		t.Error("wavelet should be empty after delete")
	}
	// Deleting an absent wavelet reports false.
	if deleted, err := store.Delete(name); err != nil || deleted {
		t.Errorf("Delete(absent) = %v (err %v), want false", deleted, err)
	}
}

func TestGetDeltaByEndVersion(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	name := waveletName(t, "w+a", "conv+root")
	access, _ := store.Open(name)
	alice := pid(t, "alice@example.com")

	r0 := chainRecord(version.Zero(name), alice, op.NewDocOp([]op.Component{op.Characters{Text: "hi"}}))
	r1 := chainRecord(r0.ResultingVersion, alice, op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "!"}}))
	if err := access.Append([]storage.DeltaRecord{r0, r1}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := access.GetDeltaByEndVersion(r0.ResultingVersion.Version())
	if err != nil || !ok {
		t.Fatalf("GetDeltaByEndVersion(%d): ok=%v err=%v", r0.ResultingVersion.Version(), ok, err)
	}
	recordEqual(t, got, r0)

	got, ok, _ = access.GetDeltaByEndVersion(r1.ResultingVersion.Version())
	if !ok {
		t.Fatal("GetDeltaByEndVersion for r1 not found")
	}
	recordEqual(t, got, r1)

	if _, ok, err := access.GetDeltaByEndVersion(999); ok || err != nil {
		t.Errorf("GetDeltaByEndVersion(999) = ok %v err %v, want false/nil", ok, err)
	}
}
