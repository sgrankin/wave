// Package attachapi serves attachment blobs over HTTP: upload, download, and
// thumbnail, with access scoped by wavelet participation. It sits behind an
// authentication layer (the participant is resolved from the request) and an
// access checker (participation), over the AttachmentStore.
//
// Spec: docs/specs/12-attachments.md.
package attachapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/blobfs"
)

// AccessChecker decides whether a participant may access a wavelet (the
// participation predicate).
type AccessChecker interface {
	CanAccess(participant id.ParticipantID, wavelet id.WaveletName) (bool, error)
}

// Handler serves the attachment HTTP routes. Identify resolves the
// authenticated participant from a request (typically auth.ParticipantFrom of
// the request context); a request with no participant is unauthorized.
type Handler struct {
	store    storage.AttachmentStore
	access   AccessChecker
	identify func(*http.Request) (id.ParticipantID, bool)
	maxBytes int64 // per-upload cap (data + thumbnail); <=0 disables the cap
}

// New builds a Handler. maxBytes caps a single upload's body (data or thumbnail) to
// protect the single-machine disk from an unbounded blob; <=0 disables the cap.
func New(store storage.AttachmentStore, access AccessChecker, identify func(*http.Request) (id.ParticipantID, bool), maxBytes int64) *Handler {
	return &Handler{store: store, access: access, identify: identify, maxBytes: maxBytes}
}

// limitBody wraps the request body with the per-upload cap (if configured) so a read
// past the limit fails instead of streaming an unbounded blob to disk.
func (h *Handler) limitBody(w http.ResponseWriter, r *http.Request) io.Reader {
	if h.maxBytes <= 0 {
		return r.Body
	}
	return http.MaxBytesReader(w, r.Body, h.maxBytes)
}

// tooLarge reports whether a store error (or the bytes read) indicates the upload
// exceeded the cap, so the handler can answer 413 rather than 500.
func (h *Handler) tooLarge(err error, read int64) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe) || (h.maxBytes > 0 && read >= h.maxBytes)
}

// Routes returns the attachment routes as an http.Handler. Mount it (typically
// behind an auth middleware that populates the participant the Identify func
// reads).
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /attachments", h.upload)
	mux.HandleFunc("GET /attachments/{id}", h.download)
	mux.HandleFunc("GET /attachments/{id}/thumbnail", h.thumbnail)
	mux.HandleFunc("PUT /attachments/{id}/thumbnail", h.setThumbnail)
	return mux
}

// upload stores a new attachment's data + metadata for a target wavelet (query
// params wave, wavelet; optional filename, mime). The uploader must be a
// participant of the target wavelet. Responds with {"id": "..."}.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	participant, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, err := waveletFromQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.authorize(w, participant, name) {
		return
	}

	attachmentID, err := newID()
	if err != nil {
		http.Error(w, "id generation failed", http.StatusInternalServerError)
		return
	}
	// Count bytes as they stream to the store so the metadata size is exact; the
	// body is capped so an oversized upload fails fast (a bounded partial blob may be
	// written, but it is orphaned — no metadata references it).
	counter := &countingReader{r: h.limitBody(w, r)}
	if err := h.store.PutData(attachmentID, counter); err != nil {
		if h.tooLarge(err, counter.n) {
			http.Error(w, "attachment too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "store data: "+err.Error(), http.StatusInternalServerError)
		return
	}
	meta := &storage.AttachmentMetadata{
		AttachmentID: attachmentID,
		Wave:         name.Wave().Serialize(),
		Wavelet:      name.Wavelet().Serialize(),
		Uploader:     participant.Address(),
		Filename:     r.URL.Query().Get("filename"),
		MimeType:     mimeOrDefault(r.URL.Query().Get("mime")),
		Size:         counter.n,
	}
	if err := h.store.PutMetadata(attachmentID, meta); err != nil {
		http.Error(w, "store metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": attachmentID})
}

// download streams an attachment's data to a participant of its wavelet.
func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	h.serveBlob(w, r, false)
}

// thumbnail streams an attachment's thumbnail (404 if it has none).
func (h *Handler) thumbnail(w http.ResponseWriter, r *http.Request) {
	h.serveBlob(w, r, true)
}

func (h *Handler) serveBlob(w http.ResponseWriter, r *http.Request, thumb bool) {
	participant, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	attachmentID := r.PathValue("id")
	meta, ok, err := h.store.GetMetadata(attachmentID)
	if err != nil {
		http.Error(w, "metadata lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, err := waveletFromMeta(meta)
	if err != nil {
		http.Error(w, "corrupt metadata", http.StatusInternalServerError)
		return
	}
	if !h.authorize(w, participant, name) {
		return
	}

	open := h.store.OpenData
	if thumb {
		open = h.store.OpenThumbnail
	}
	rc, size, ok, err := open(attachmentID)
	if err != nil {
		http.Error(w, "open blob failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = rc.Close() }()
	if !thumb {
		w.Header().Set("Content-Type", meta.MimeType)
		if meta.Filename != "" {
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", meta.Filename))
		}
	}
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, rc)
}

// setThumbnail stores a client-provided thumbnail for an existing attachment.
// (Server-side thumbnail generation from images is out of scope here.)
func (h *Handler) setThumbnail(w http.ResponseWriter, r *http.Request) {
	participant, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	attachmentID := r.PathValue("id")
	meta, ok, err := h.store.GetMetadata(attachmentID)
	if err != nil {
		http.Error(w, "metadata lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, err := waveletFromMeta(meta)
	if err != nil {
		http.Error(w, "corrupt metadata", http.StatusInternalServerError)
		return
	}
	if !h.authorize(w, participant, name) {
		return
	}
	tr := &countingReader{r: h.limitBody(w, r)}
	if err := h.store.PutThumbnail(attachmentID, tr); err != nil {
		if errors.Is(err, blobfs.ErrExists) {
			http.Error(w, "thumbnail already set", http.StatusConflict)
			return
		}
		if h.tooLarge(err, tr.n) {
			http.Error(w, "thumbnail too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "store thumbnail failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authorize checks participation, writing the appropriate error and returning
// false if access is denied.
func (h *Handler) authorize(w http.ResponseWriter, participant id.ParticipantID, name id.WaveletName) bool {
	allowed, err := h.access.CanAccess(participant, name)
	if err != nil {
		http.Error(w, "access check failed", http.StatusInternalServerError)
		return false
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func waveletFromQuery(r *http.Request) (id.WaveletName, error) {
	wave, err := id.ParseWaveID(r.URL.Query().Get("wave"))
	if err != nil {
		return id.WaveletName{}, fmt.Errorf("bad or missing wave: %w", err)
	}
	wavelet, err := id.ParseWaveletID(r.URL.Query().Get("wavelet"))
	if err != nil {
		return id.WaveletName{}, fmt.Errorf("bad or missing wavelet: %w", err)
	}
	return id.NewWaveletName(wave, wavelet), nil
}

func waveletFromMeta(m *storage.AttachmentMetadata) (id.WaveletName, error) {
	wave, err := id.ParseWaveID(m.Wave)
	if err != nil {
		return id.WaveletName{}, err
	}
	wavelet, err := id.ParseWaveletID(m.Wavelet)
	if err != nil {
		return id.WaveletName{}, err
	}
	return id.NewWaveletName(wave, wavelet), nil
}

func mimeOrDefault(m string) string {
	if m == "" {
		return "application/octet-stream"
	}
	return m
}

// newID returns a random 128-bit attachment id as hex.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
