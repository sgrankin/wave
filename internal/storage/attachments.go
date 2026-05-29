package storage

import "io"

// AttachmentMetadata describes one attachment (spec §5.3 AttachmentMetadata).
// The byte blobs (data, thumbnail) are stored separately, content-addressed by
// the same attachment id. Wave/Wavelet are serialized id strings recording where
// the attachment lives, so the serving layer can scope access by participation;
// the store itself does not interpret them.
type AttachmentMetadata struct {
	AttachmentID    string
	Wave            string // serialized WaveID
	Wavelet         string // serialized WaveletID
	Uploader        string // participant address
	Filename        string
	MimeType        string
	Size            int64 // data blob size in bytes
	ThumbnailWidth  int
	ThumbnailHeight int
	Malware         bool
}

// AttachmentStore persists attachment metadata and the data/thumbnail byte blobs
// (spec §5.3, §5.8). Blobs are write-once: a Put for an existing id returns an
// already-exists error (replace by deleting first). It is global (not per-wavelet).
type AttachmentStore interface {
	// GetMetadata returns an attachment's metadata, and whether it exists.
	GetMetadata(attachmentID string) (*AttachmentMetadata, bool, error)
	// PutMetadata stores an attachment's metadata (write-once).
	PutMetadata(attachmentID string, m *AttachmentMetadata) error
	// OpenData opens the data blob for reading, with its size and existence. The
	// caller must close the reader.
	OpenData(attachmentID string) (io.ReadCloser, int64, bool, error)
	// PutData stores the data blob (write-once).
	PutData(attachmentID string, r io.Reader) error
	// OpenThumbnail opens the thumbnail blob (may be absent).
	OpenThumbnail(attachmentID string) (io.ReadCloser, int64, bool, error)
	// PutThumbnail stores the thumbnail blob (write-once).
	PutThumbnail(attachmentID string, r io.Reader) error
	// Delete removes an attachment's data, thumbnail, and metadata.
	Delete(attachmentID string) error
}
