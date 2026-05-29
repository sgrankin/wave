// Package blobfs is a content-addressed filesystem blob store: a flat directory
// of immutable, write-once blobs keyed by arbitrary strings (base64url-encoded
// into safe filenames). Writes are atomic (temp file + fsync + rename). It backs
// attachment data/thumbnail/metadata blobs (spec §"File-based attachment store"),
// and is the reusable filesystem half of the storage layer (bytes live here;
// queryable metadata lives in SQLite).
//
// Spec: docs/specs/05-storage-persistence.md §5.3, §5.8.
package blobfs

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrExists is returned by Put when a blob with the given name already exists;
// blobs are write-once (delete first to replace).
var ErrExists = errors.New("blobfs: blob already exists")

// Store is a directory of content-addressed blobs.
type Store struct {
	dir string
}

// New creates (if needed) the backing directory and returns a Store.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("blobfs: mkdir %q: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// path maps a blob name to its on-disk path. base64url has no '.', so blob files
// never collide with the ".tmp-*" files Put creates.
func (s *Store) path(name string) string {
	return filepath.Join(s.dir, base64.RawURLEncoding.EncodeToString([]byte(name)))
}

// Put atomically writes a new blob from r (temp file + fsync + rename, so a
// reader never sees a partial blob). It returns ErrExists if name is already
// present.
//
// The existence check is best-effort against concurrent writers: two Puts of the
// SAME name racing can both pass the check and the second rename wins (matching
// the Java file backend's check-then-write). This is not a real scenario —
// blob names are content-addressed/unique per write — so callers need not
// serialize Puts of distinct names; same-name concurrency is the caller's
// responsibility.
func (s *Store) Put(name string, r io.Reader) (err error) {
	p := s.path(name)
	if _, statErr := os.Stat(p); statErr == nil {
		return ErrExists
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("blobfs: stat %q: %w", name, statErr)
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("blobfs: temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Remove the temp file unless the rename below consumes it.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blobfs: write %q: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blobfs: sync %q: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blobfs: close %q: %w", name, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		return fmt.Errorf("blobfs: rename %q: %w", name, err)
	}
	return nil
}

// Open opens a blob for reading, returning its size and whether it exists. The
// caller must Close the returned reader.
func (s *Store) Open(name string) (io.ReadCloser, int64, bool, error) {
	f, err := os.Open(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("blobfs: open %q: %w", name, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, false, fmt.Errorf("blobfs: stat %q: %w", name, err)
	}
	return f, fi.Size(), true, nil
}

// ReadAll reads a whole blob into memory (convenience for small blobs such as
// metadata), returning whether it exists.
func (s *Store) ReadAll(name string) ([]byte, bool, error) {
	rc, _, ok, err := s.Open(name)
	if err != nil || !ok {
		return nil, ok, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, true, fmt.Errorf("blobfs: read %q: %w", name, err)
	}
	return b, true, nil
}

// Delete removes a blob (no-op if absent).
func (s *Store) Delete(name string) error {
	if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blobfs: delete %q: %w", name, err)
	}
	return nil
}
