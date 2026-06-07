package sqlite_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/sgrankin/wave/internal/storage/sqlite"
)

// TestBackup verifies VACUUM INTO produces a consistent, reopenable copy that
// carries the source data, and that it refuses to overwrite an existing file.
func TestBackup(t *testing.T) {
	dir := t.TempDir()
	src, err := sqlite.Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	want := []byte("backup-me")
	if err := src.PutSetting("k", want); err != nil {
		t.Fatalf("put: %v", err)
	}

	dest := filepath.Join(dir, "backup.db")
	if err := src.Backup(context.Background(), dest); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// The backup opens as a valid store and carries the source data.
	bk, err := sqlite.Open(dest)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer bk.Close()
	got, ok, err := bk.GetSetting("k")
	if err != nil || !ok {
		t.Fatalf("get from backup = ok %v err %v", ok, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("backup value = %q, want %q", got, want)
	}

	// VACUUM INTO refuses to overwrite an existing file (a safety property).
	if err := src.Backup(context.Background(), dest); err == nil {
		t.Error("backup over an existing file should have failed")
	}
}
