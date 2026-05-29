package attachapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/attachapi"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage/attachments"
)

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func waveletName(t *testing.T) id.WaveletName {
	t.Helper()
	w, _ := id.NewWaveID("example.com", "w+a")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	return id.NewWaveletName(w, wl)
}

// identify reads the test user from a header.
func identify(r *http.Request) (id.ParticipantID, bool) {
	v := r.Header.Get("X-Test-User")
	if v == "" {
		return id.ParticipantID{}, false
	}
	p, err := id.NewParticipantID(v)
	if err != nil {
		return id.ParticipantID{}, false
	}
	return p, true
}

// fakeAccess grants access to (participant, wavelet) pairs in its set.
type fakeAccess struct{ allowed map[string]bool }

func (f fakeAccess) CanAccess(p id.ParticipantID, w id.WaveletName) (bool, error) {
	return f.allowed[p.Address()+"|"+w.String()], nil
}

func setup(t *testing.T) (http.Handler, fakeAccess) {
	t.Helper()
	store, err := attachments.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	access := fakeAccess{allowed: map[string]bool{}}
	return attachapi.New(store, access, identify).Routes(), access
}

func uploadURL(name id.WaveletName, filename, mime string) string {
	q := url.Values{
		"wave":     {name.Wave().Serialize()},
		"wavelet":  {name.Wavelet().Serialize()},
		"filename": {filename},
		"mime":     {mime},
	}
	return "/attachments?" + q.Encode()
}

func TestUploadDownloadRoundTrip(t *testing.T) {
	h, access := setup(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	access.allowed[alice.Address()+"|"+name.String()] = true

	// Upload.
	req := httptest.NewRequest("POST", uploadURL(name, "notes.txt", "text/plain"), strings.NewReader("file-contents"))
	req.Header.Set("X-Test-User", alice.Address())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body %q", rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.ID == "" {
		t.Fatalf("upload response = %q (err %v)", rec.Body.String(), err)
	}

	// Download as alice.
	dreq := httptest.NewRequest("GET", "/attachments/"+resp.ID, nil)
	dreq.Header.Set("X-Test-User", alice.Address())
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, dreq)
	if drec.Code != http.StatusOK {
		t.Fatalf("download status = %d", drec.Code)
	}
	if got := drec.Body.String(); got != "file-contents" {
		t.Errorf("downloaded %q, want file-contents", got)
	}
	if ct := drec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
}

func TestDownloadAccessControl(t *testing.T) {
	h, access := setup(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	access.allowed[alice.Address()+"|"+name.String()] = true // bob is NOT granted

	// Alice uploads.
	req := httptest.NewRequest("POST", uploadURL(name, "f", "text/plain"), strings.NewReader("secret"))
	req.Header.Set("X-Test-User", alice.Address())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status %d", rec.Code)
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	// Bob (non-participant) is forbidden.
	dreq := httptest.NewRequest("GET", "/attachments/"+resp.ID, nil)
	dreq.Header.Set("X-Test-User", bob.Address())
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, dreq)
	if drec.Code != http.StatusForbidden {
		t.Errorf("bob download status = %d, want 403", drec.Code)
	}

	// Anonymous is unauthorized.
	areq := httptest.NewRequest("GET", "/attachments/"+resp.ID, nil)
	arec := httptest.NewRecorder()
	h.ServeHTTP(arec, areq)
	if arec.Code != http.StatusUnauthorized {
		t.Errorf("anon download status = %d, want 401", arec.Code)
	}
}

func TestUploadToWaveletNotAMember(t *testing.T) {
	h, _ := setup(t) // no access granted
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	req := httptest.NewRequest("POST", uploadURL(name, "f", "text/plain"), strings.NewReader("x"))
	req.Header.Set("X-Test-User", alice.Address())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("upload by non-member status = %d, want 403", rec.Code)
	}
}

func TestDownloadMissing(t *testing.T) {
	h, access := setup(t)
	alice := pid(t, "alice@example.com")
	access.allowed[alice.Address()+"|"+waveletName(t).String()] = true
	req := httptest.NewRequest("GET", "/attachments/deadbeef", nil)
	req.Header.Set("X-Test-User", alice.Address())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing attachment status = %d, want 404", rec.Code)
	}
}

func TestThumbnailSetAndGet(t *testing.T) {
	h, access := setup(t)
	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	access.allowed[alice.Address()+"|"+name.String()] = true

	// Upload data first.
	up := httptest.NewRequest("POST", uploadURL(name, "img", "image/png"), strings.NewReader("pngbytes"))
	up.Header.Set("X-Test-User", alice.Address())
	urec := httptest.NewRecorder()
	h.ServeHTTP(urec, up)
	var resp struct{ ID string }
	_ = json.Unmarshal(urec.Body.Bytes(), &resp)

	// No thumbnail yet → 404.
	g := httptest.NewRequest("GET", "/attachments/"+resp.ID+"/thumbnail", nil)
	g.Header.Set("X-Test-User", alice.Address())
	grec := httptest.NewRecorder()
	h.ServeHTTP(grec, g)
	if grec.Code != http.StatusNotFound {
		t.Errorf("thumbnail-before-set status = %d, want 404", grec.Code)
	}

	// Set the thumbnail.
	s := httptest.NewRequest("PUT", "/attachments/"+resp.ID+"/thumbnail", strings.NewReader("thumbbytes"))
	s.Header.Set("X-Test-User", alice.Address())
	srec := httptest.NewRecorder()
	h.ServeHTTP(srec, s)
	if srec.Code != http.StatusNoContent {
		t.Fatalf("set thumbnail status = %d", srec.Code)
	}

	// Now get it.
	g2 := httptest.NewRequest("GET", "/attachments/"+resp.ID+"/thumbnail", nil)
	g2.Header.Set("X-Test-User", alice.Address())
	g2rec := httptest.NewRecorder()
	h.ServeHTTP(g2rec, g2)
	if g2rec.Code != http.StatusOK || readBody(t, g2rec.Result().Body) != "thumbbytes" {
		t.Errorf("get thumbnail = %d / %q", g2rec.Code, g2rec.Body.String())
	}
}

func readBody(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b)
}
