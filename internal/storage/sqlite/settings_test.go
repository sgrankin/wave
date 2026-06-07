package sqlite_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/sgrankin/wave/internal/storage/sqlite"
)

func TestSettingsRoundTrip(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Absent key.
	if _, ok, err := store.GetSetting("missing"); err != nil || ok {
		t.Errorf("missing setting = ok %v err %v, want false/nil", ok, err)
	}

	// Put then get.
	want := []byte{0x00, 0x01, 0xff, 0xfe, 0x10}
	if err := store.PutSetting("k", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := store.GetSetting("k")
	if err != nil || !ok {
		t.Fatalf("get = ok %v err %v", ok, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("value = %x, want %x", got, want)
	}

	// Replace.
	want2 := []byte("replaced")
	if err := store.PutSetting("k", want2); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, _, _ := store.GetSetting("k"); !bytes.Equal(got, want2) {
		t.Errorf("after replace = %q, want %q", got, want2)
	}
}

// TestSettingsPersistAcrossReopen: a setting survives closing and reopening the
// same on-disk database (the signing-key-survives-restart property).
func TestSettingsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wave.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("durable")
	if err := store.PutSetting("key", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	got, ok, err := store2.GetSetting("key")
	if err != nil || !ok || !bytes.Equal(got, want) {
		t.Errorf("after reopen = %q (ok %v err %v), want %q", got, ok, err, want)
	}
}
