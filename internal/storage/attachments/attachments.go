// Package attachments is the filesystem-backed AttachmentStore: data and
// thumbnail bytes plus JSON metadata, each in its own content-addressed blobfs
// directory under a single root. This mirrors the Java file-based attachment
// store (spec §"File-based attachment store"), but Delete removes all three
// blobs (the Java file backend left metadata/thumbnail behind — a quirk not
// worth reproducing).
//
// Spec: docs/specs/05-storage-persistence.md §5.3, §5.8.
package attachments

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/blobfs"
)

// Store is a filesystem AttachmentStore rooted at a directory, with data/,
// thumb/, and meta/ subdirectories.
type Store struct {
	data  *blobfs.Store
	thumb *blobfs.Store
	meta  *blobfs.Store
}

// New creates (if needed) the attachment directories under root.
func New(root string) (*Store, error) {
	data, err := blobfs.New(filepath.Join(root, "data"))
	if err != nil {
		return nil, err
	}
	thumb, err := blobfs.New(filepath.Join(root, "thumb"))
	if err != nil {
		return nil, err
	}
	meta, err := blobfs.New(filepath.Join(root, "meta"))
	if err != nil {
		return nil, err
	}
	return &Store{data: data, thumb: thumb, meta: meta}, nil
}

// GetMetadata returns an attachment's metadata, and whether it exists.
func (s *Store) GetMetadata(attachmentID string) (*storage.AttachmentMetadata, bool, error) {
	b, ok, err := s.meta.ReadAll(attachmentID)
	if err != nil || !ok {
		return nil, ok, err
	}
	var m storage.AttachmentMetadata
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false, fmt.Errorf("attachments: decode metadata %q: %w", attachmentID, err)
	}
	return &m, true, nil
}

// PutMetadata stores an attachment's metadata (write-once).
func (s *Store) PutMetadata(attachmentID string, m *storage.AttachmentMetadata) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("attachments: encode metadata %q: %w", attachmentID, err)
	}
	return s.meta.Put(attachmentID, bytes.NewReader(b))
}

// OpenData opens the data blob for reading.
func (s *Store) OpenData(attachmentID string) (io.ReadCloser, int64, bool, error) {
	return s.data.Open(attachmentID)
}

// PutData stores the data blob (write-once).
func (s *Store) PutData(attachmentID string, r io.Reader) error {
	return s.data.Put(attachmentID, r)
}

// OpenThumbnail opens the thumbnail blob (may be absent).
func (s *Store) OpenThumbnail(attachmentID string) (io.ReadCloser, int64, bool, error) {
	return s.thumb.Open(attachmentID)
}

// PutThumbnail stores the thumbnail blob (write-once).
func (s *Store) PutThumbnail(attachmentID string, r io.Reader) error {
	return s.thumb.Put(attachmentID, r)
}

// Delete removes an attachment's data, thumbnail, and metadata. It attempts all
// three even if one fails, returning the joined error.
func (s *Store) Delete(attachmentID string) error {
	return errors.Join(
		s.data.Delete(attachmentID),
		s.thumb.Delete(attachmentID),
		s.meta.Delete(attachmentID),
	)
}

// Compile-time assertion that Store implements the contract.
var _ storage.AttachmentStore = (*Store)(nil)
