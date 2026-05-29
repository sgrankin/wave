package attachments_test

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/attachments"
	"github.com/sgrankin/wave/internal/storage/blobfs"
)

func newStore(t *testing.T) *attachments.Store {
	t.Helper()
	s, err := attachments.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAttachmentDataAndThumbnail(t *testing.T) {
	s := newStore(t)
	if err := s.PutData("a1", strings.NewReader("file-bytes")); err != nil {
		t.Fatalf("put data: %v", err)
	}
	if err := s.PutThumbnail("a1", strings.NewReader("thumb")); err != nil {
		t.Fatalf("put thumb: %v", err)
	}

	rc, size, ok, err := s.OpenData("a1")
	if err != nil || !ok {
		t.Fatalf("open data: ok=%v err=%v", ok, err)
	}
	if size != int64(len("file-bytes")) || readAll(t, rc) != "file-bytes" {
		t.Errorf("data mismatch (size %d)", size)
	}
	trc, _, ok, err := s.OpenThumbnail("a1")
	if err != nil || !ok {
		t.Fatalf("open thumb: ok=%v err=%v", ok, err)
	}
	if readAll(t, trc) != "thumb" {
		t.Error("thumbnail mismatch")
	}

	// Write-once: a re-Put of existing data returns blobfs.ErrExists.
	if err := s.PutData("a1", strings.NewReader("x")); !errors.Is(err, blobfs.ErrExists) {
		t.Errorf("second PutData = %v, want ErrExists", err)
	}
}

func TestAttachmentMetadataRoundTrip(t *testing.T) {
	s := newStore(t)
	want := &storage.AttachmentMetadata{
		AttachmentID:    "a1",
		Wave:            "example.com/w+x",
		Wavelet:         "example.com!conv+root",
		Uploader:        "alice@example.com",
		Filename:        "photo.png",
		MimeType:        "image/png",
		Size:            12345,
		ThumbnailWidth:  120,
		ThumbnailHeight: 90,
	}
	if err := s.PutMetadata("a1", want); err != nil {
		t.Fatalf("put metadata: %v", err)
	}
	got, ok, err := s.GetMetadata("a1")
	if err != nil || !ok {
		t.Fatalf("get metadata: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("metadata mismatch:\n got %+v\nwant %+v", got, want)
	}
	if _, ok, _ := s.GetMetadata("missing"); ok {
		t.Error("missing metadata should report not-found")
	}
}

func TestAttachmentDelete(t *testing.T) {
	s := newStore(t)
	if err := s.PutData("a1", strings.NewReader("d")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutThumbnail("a1", strings.NewReader("t")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMetadata("a1", &storage.AttachmentMetadata{AttachmentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, ok, _ := s.OpenData("a1"); ok {
		t.Error("data should be gone")
	}
	if _, _, ok, _ := s.OpenThumbnail("a1"); ok {
		t.Error("thumbnail should be gone")
	}
	if _, ok, _ := s.GetMetadata("a1"); ok {
		t.Error("metadata should be gone")
	}
}
