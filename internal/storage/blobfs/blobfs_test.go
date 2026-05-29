package blobfs_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/storage/blobfs"
)

func read(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestBlobfsPutOpenDelete(t *testing.T) {
	s, err := blobfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("k1", strings.NewReader("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, size, ok, err := s.Open("k1")
	if err != nil || !ok {
		t.Fatalf("open: ok=%v err=%v", ok, err)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if got := read(t, rc); got != "hello" {
		t.Errorf("content = %q, want hello", got)
	}

	// Write-once: a second Put fails with ErrExists.
	if err := s.Put("k1", strings.NewReader("again")); !errors.Is(err, blobfs.ErrExists) {
		t.Errorf("second Put = %v, want ErrExists", err)
	}

	b, ok, err := s.ReadAll("k1")
	if err != nil || !ok || string(b) != "hello" {
		t.Errorf("ReadAll = %q (ok=%v err=%v), want hello", b, ok, err)
	}

	if err := s.Delete("k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, ok, _ := s.Open("k1"); ok {
		t.Error("blob should be gone after delete")
	}
	// Delete of an absent blob is a no-op.
	if err := s.Delete("k1"); err != nil {
		t.Errorf("delete absent: %v", err)
	}
}

func TestBlobfsMissing(t *testing.T) {
	s, _ := blobfs.New(t.TempDir())
	if _, _, ok, err := s.Open("nope"); ok || err != nil {
		t.Errorf("Open(missing) = ok %v err %v, want false/nil", ok, err)
	}
	if _, ok, err := s.ReadAll("nope"); ok || err != nil {
		t.Errorf("ReadAll(missing) = ok %v err %v, want false/nil", ok, err)
	}
}

// Keys with filesystem-unsafe characters must round-trip (base64url encoding).
func TestBlobfsUnsafeKey(t *testing.T) {
	s, _ := blobfs.New(t.TempDir())
	key := "wave://a/b c+d!e"
	if err := s.Put(key, strings.NewReader("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, _, ok, err := s.Open(key)
	if err != nil || !ok {
		t.Fatalf("open: ok=%v err=%v", ok, err)
	}
	if got := read(t, rc); got != "x" {
		t.Errorf("content = %q, want x", got)
	}
}
