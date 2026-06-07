package playbackapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/playbackapi"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/wavelet"
)

const waveName = "example.com/w+abc/~/conv+root"

// identify reads the test user from a header (empty ⇒ unauthenticated).
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

type fakeAccess struct{ allow bool }

func (f fakeAccess) CanAccess(id.ParticipantID, id.WaveletName) (bool, error) { return f.allow, nil }

type fakeWaves struct {
	headers   []server.DeltaHeader
	states    map[uint64]*wavelet.Data
	stateErr  error // if set, StateAt returns it (a storage/replay failure)
}

func (f fakeWaves) DeltaHeaders(id.WaveletName) ([]server.DeltaHeader, error) { return f.headers, nil }

func (f fakeWaves) StateAt(_ id.WaveletName, version uint64) (*wavelet.Data, error) {
	if f.stateErr != nil {
		return nil, f.stateErr
	}
	if version == 0 {
		return nil, nil
	}
	w, ok := f.states[version]
	if !ok {
		// A bad version is the sentinel error (→ 404); other errors are storage faults (→ 500).
		return nil, fmt.Errorf("%w %d", server.ErrNoVersion, version)
	}
	return w, nil
}

// makeState builds a wavelet.Data at the given version with one content blip
// "b+root" authored by author, via the snapshot decoder.
func makeState(t *testing.T, version uint64, author, text string) *wavelet.Data {
	t.Helper()
	w, err := wavelet.FromState(wavelet.SnapshotState{
		WaveID:       "example.com/w+abc",
		WaveletID:    "example.com/conv+root",
		Creator:      author,
		Version:      version,
		Participants: []string{author},
		Blips: []wavelet.BlipSnapshot{{
			ID:      "b+root",
			Author:  author,
			Content: op.NewDocOp([]op.Component{op.Characters{Text: text}}),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func handler(waves playbackapi.Waves, allow bool) http.Handler {
	return playbackapi.New(waves, fakeAccess{allow: allow}, identify, nil).Routes()
}

func do(t *testing.T, h http.Handler, method, target, user string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeltasTimeline(t *testing.T) {
	alice := func() id.ParticipantID { p, _ := id.NewParticipantID("alice@example.com"); return p }()
	waves := fakeWaves{headers: []server.DeltaHeader{
		{Author: alice, Version: 2, Timestamp: 1000, OpCount: 2},
		{Author: alice, Version: 3, Timestamp: 1001, OpCount: 1},
	}}
	rec := do(t, handler(waves, true), "GET", "/api/playback/deltas?wave="+url.QueryEscape(waveName), "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Deltas []playbackapi.DeltaDigest `json:"deltas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Deltas) != 2 || body.Deltas[0].Version != 2 || body.Deltas[0].OpCount != 2 ||
		body.Deltas[0].Author != "alice@example.com" || body.Deltas[1].Version != 3 {
		t.Fatalf("deltas = %+v", body.Deltas)
	}
}

func TestStateAtVersion(t *testing.T) {
	waves := fakeWaves{states: map[uint64]*wavelet.Data{
		3: makeState(t, 3, "alice@example.com", "hello world"),
	}}
	rec := do(t, handler(waves, true), "GET", "/api/playback/state?wave="+url.QueryEscape(waveName)+"&version=3", "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	var view playbackapi.ConversationView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Version != 3 || len(view.Participants) != 1 || view.Participants[0] != "alice@example.com" {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Blips) != 1 || view.Blips[0].ID != "b+root" || view.Blips[0].Text != "hello world" ||
		view.Blips[0].Author != "alice@example.com" {
		t.Fatalf("blips = %+v", view.Blips)
	}
}

func TestStateVersionZeroIsEmpty(t *testing.T) {
	rec := do(t, handler(fakeWaves{}, true), "GET", "/api/playback/state?wave="+url.QueryEscape(waveName)+"&version=0", "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var view playbackapi.ConversationView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Version != 0 || len(view.Participants) != 0 || len(view.Blips) != 0 {
		t.Fatalf("empty view expected, got %+v", view)
	}
}

func TestStateUnknownVersionIs404(t *testing.T) {
	rec := do(t, handler(fakeWaves{states: map[uint64]*wavelet.Data{}}, true),
		"GET", "/api/playback/state?wave="+url.QueryEscape(waveName)+"&version=7", "alice@example.com")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestStateStorageErrorIs500(t *testing.T) {
	// A non-sentinel error from StateAt is a server fault, not a bad-version 404.
	waves := fakeWaves{stateErr: errors.New("disk on fire")}
	rec := do(t, handler(waves, true), "GET", "/api/playback/state?wave="+url.QueryEscape(waveName)+"&version=3", "alice@example.com")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (storage error must not be a 404)", rec.Code)
	}
}

func TestUnauthenticatedIs401(t *testing.T) {
	rec := do(t, handler(fakeWaves{}, true), "GET", "/api/playback/deltas?wave="+url.QueryEscape(waveName), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNonMemberIs403(t *testing.T) {
	rec := do(t, handler(fakeWaves{}, false /* deny */), "GET", "/api/playback/deltas?wave="+url.QueryEscape(waveName), "bob@example.com")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestBadWaveIs400(t *testing.T) {
	rec := do(t, handler(fakeWaves{}, true), "GET", "/api/playback/deltas?wave=not-a-wave", "alice@example.com")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBadVersionIs400(t *testing.T) {
	rec := do(t, handler(fakeWaves{}, true), "GET", "/api/playback/state?wave="+url.QueryEscape(waveName)+"&version=abc", "alice@example.com")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
