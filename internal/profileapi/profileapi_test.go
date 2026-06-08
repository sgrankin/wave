package profileapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/profileapi"
	"github.com/sgrankin/wave/internal/storage"
)

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

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

// fakeAccounts is an in-memory storage.Account map keyed by canonical address.
type fakeAccounts struct {
	m           map[string]*storage.Account
	getErrAddrs map[string]bool // addresses whose GetAccount returns an error
	putErr      error           // returned by PutAccount when non-nil
	getCount    int             // total GetAccount calls
	putSeen     int             // total successful PutAccount calls
}

func newAccounts() *fakeAccounts {
	return &fakeAccounts{m: map[string]*storage.Account{}, getErrAddrs: map[string]bool{}}
}

func (f *fakeAccounts) GetAccount(p id.ParticipantID) (*storage.Account, bool, error) {
	f.getCount++
	if f.getErrAddrs[p.Address()] {
		return nil, false, errors.New("simulated store read error")
	}
	a, ok := f.m[p.Address()]
	return a, ok, nil
}

func (f *fakeAccounts) PutAccount(a *storage.Account) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.putSeen++
	f.m[a.ID.Address()] = a
	return nil
}

// CreateAccount satisfies storage.AccountStore (insert-only). profileapi does not
// use it, but the fake must implement the full interface.
func (f *fakeAccounts) CreateAccount(a *storage.Account) (bool, error) {
	if _, ok := f.m[a.ID.Address()]; ok {
		return false, nil
	}
	f.m[a.ID.Address()] = a
	return true, nil
}

func (f *fakeAccounts) putHuman(t *testing.T, addr, name string) {
	t.Helper()
	p := pid(t, addr)
	f.m[p.Address()] = &storage.Account{ID: p, Kind: storage.AccountHuman, Human: &storage.HumanAccount{DisplayName: name}}
}

func handler(a profileapi.Accounts) http.Handler {
	return profileapi.New(a, identify, nil).Routes()
}

// decodeProfiles unmarshals the {"profiles":[...]} body into an address→name map.
func decodeProfiles(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var resp struct {
		Profiles []profileapi.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode profiles: %v (body=%s)", err, body)
	}
	out := map[string]string{}
	for _, p := range resp.Profiles {
		out[p.Address] = p.DisplayName
	}
	return out
}

func TestGetProfilesBatch(t *testing.T) {
	a := newAccounts()
	a.putHuman(t, "alice@example.com", "Alice Smith")
	a.putHuman(t, "bob@example.com", "") // account exists, no name

	req := httptest.NewRequest("GET", "/api/profiles?addr=alice@example.com&addr=bob@example.com&addr=carol@example.com", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeProfiles(t, rec.Body.Bytes())
	// One entry per valid requested address, including the unknown carol (empty
	// name) so the client can cache it and not refetch.
	if len(got) != 3 {
		t.Fatalf("got %d profiles, want 3: %v", len(got), got)
	}
	if got["alice@example.com"] != "Alice Smith" {
		t.Errorf("alice name = %q, want %q", got["alice@example.com"], "Alice Smith")
	}
	if got["bob@example.com"] != "" {
		t.Errorf("bob name = %q, want empty", got["bob@example.com"])
	}
	if _, ok := got["carol@example.com"]; !ok {
		t.Errorf("carol missing; unknown addresses should still return an (empty) entry")
	}
}

func TestGetProfilesSkipsMalformed(t *testing.T) {
	a := newAccounts()
	a.putHuman(t, "alice@example.com", "Alice")

	req := httptest.NewRequest("GET", "/api/profiles?addr=not-an-address&addr=alice@example.com", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	got := decodeProfiles(t, rec.Body.Bytes())
	if len(got) != 1 || got["alice@example.com"] != "Alice" {
		t.Fatalf("got %v, want only alice", got)
	}
}

func TestGetProfilesEmpty(t *testing.T) {
	a := newAccounts()
	req := httptest.NewRequest("GET", "/api/profiles", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeProfiles(t, rec.Body.Bytes())
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestGetProfilesDedupes(t *testing.T) {
	a := newAccounts()
	a.putHuman(t, "alice@example.com", "Alice")

	// The same address (in mixed case) repeated should produce one entry and one
	// store read, not one per copy.
	req := httptest.NewRequest("GET", "/api/profiles?addr=alice@example.com&addr=Alice@Example.com&addr=alice@example.com", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	got := decodeProfiles(t, rec.Body.Bytes())
	if len(got) != 1 {
		t.Fatalf("got %d profiles, want 1 (deduped): %v", len(got), got)
	}
	if a.getCount != 1 {
		t.Errorf("GetAccount called %d times, want 1 (deduped)", a.getCount)
	}
}

func TestGetProfilesCanonicalizesAddress(t *testing.T) {
	a := newAccounts()
	a.putHuman(t, "alice@example.com", "Alice")

	// Mixed-case input must come back canonicalized (lowercased) — the client keys
	// its cache on the canonical address.
	req := httptest.NewRequest("GET", "/api/profiles?addr=Alice@Example.COM", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	got := decodeProfiles(t, rec.Body.Bytes())
	if got["alice@example.com"] != "Alice" {
		t.Fatalf("got %v, want canonicalized alice@example.com → Alice", got)
	}
}

func TestGetProfilesSkipsLoadErrorButReturnsRest(t *testing.T) {
	a := newAccounts()
	a.putHuman(t, "alice@example.com", "Alice")
	a.putHuman(t, "carol@example.com", "Carol")
	a.getErrAddrs["bob@example.com"] = true // bob's read fails

	req := httptest.NewRequest("GET", "/api/profiles?addr=alice@example.com&addr=bob@example.com&addr=carol@example.com", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a load error must not fail the batch)", rec.Code)
	}
	got := decodeProfiles(t, rec.Body.Bytes())
	if len(got) != 2 || got["alice@example.com"] != "Alice" || got["carol@example.com"] != "Carol" {
		t.Fatalf("got %v, want alice+carol with bob skipped", got)
	}
	if _, ok := got["bob@example.com"]; ok {
		t.Errorf("bob should be skipped (his read errored)")
	}
}

func TestGetProfilesTruncatesOverCap(t *testing.T) {
	a := newAccounts()
	// Request well over the 256 cap of distinct addresses.
	q := "/api/profiles?"
	for i := 0; i < 300; i++ {
		if i > 0 {
			q += "&"
		}
		q += "addr=u" + strconv.Itoa(i) + "@example.com"
	}
	req := httptest.NewRequest("GET", q, nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	got := decodeProfiles(t, rec.Body.Bytes())
	if len(got) != 256 {
		t.Fatalf("got %d profiles, want exactly 256 (capped)", len(got))
	}
}

func TestSetProfileCreatesAndUpdates(t *testing.T) {
	a := newAccounts()

	// First set auto-provisions a human account.
	body := strings.NewReader(`{"displayName":"  Alice Smith  "}`)
	req := httptest.NewRequest("POST", "/api/profile", body)
	req.Header.Set("X-Test-User", "alice@example.com")
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", rec.Code, rec.Body)
	}
	acct, ok, _ := a.GetAccount(pid(t, "alice@example.com"))
	if !ok || acct.Human == nil {
		t.Fatalf("account not created")
	}
	if acct.Human.DisplayName != "Alice Smith" { // trimmed
		t.Errorf("name = %q, want %q (trimmed)", acct.Human.DisplayName, "Alice Smith")
	}

	// Second set updates the existing account, preserving other fields.
	acct.Human.Locale = "en"
	a.m[acct.ID.Address()] = acct
	body = strings.NewReader(`{"displayName":"Alice S."}`)
	req = httptest.NewRequest("POST", "/api/profile", body)
	req.Header.Set("X-Test-User", "alice@example.com")
	rec = httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update status = %d, want 204", rec.Code)
	}
	acct, _, _ = a.GetAccount(pid(t, "alice@example.com"))
	if acct.Human.DisplayName != "Alice S." {
		t.Errorf("name = %q, want %q", acct.Human.DisplayName, "Alice S.")
	}
	if acct.Human.Locale != "en" {
		t.Errorf("locale = %q, want preserved %q", acct.Human.Locale, "en")
	}
}

func TestSetProfileRequiresAuth(t *testing.T) {
	a := newAccounts()
	req := httptest.NewRequest("POST", "/api/profile", strings.NewReader(`{"displayName":"x"}`))
	// no X-Test-User header ⇒ unauthenticated
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if a.putSeen != 0 {
		t.Errorf("PutAccount called %d times for unauthenticated request, want 0", a.putSeen)
	}
}

func TestSetProfileRejectsLongName(t *testing.T) {
	a := newAccounts()
	long := strings.Repeat("x", 200)
	req := httptest.NewRequest("POST", "/api/profile", strings.NewReader(`{"displayName":"`+long+`"}`))
	req.Header.Set("X-Test-User", "alice@example.com")
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if a.putSeen != 0 {
		t.Errorf("PutAccount called for over-long name, want 0")
	}
}

func TestSetProfileRejectsRobotAccount(t *testing.T) {
	a := newAccounts()
	p := pid(t, "robot@example.com")
	a.m[p.Address()] = &storage.Account{ID: p, Kind: storage.AccountRobot, Robot: &storage.RobotAccount{URL: "http://x"}}

	req := httptest.NewRequest("POST", "/api/profile", strings.NewReader(`{"displayName":"Botty"}`))
	req.Header.Set("X-Test-User", "robot@example.com")
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for robot account", rec.Code)
	}
}

func TestSetProfileGetErrorIs500(t *testing.T) {
	a := newAccounts()
	a.getErrAddrs["alice@example.com"] = true

	req := httptest.NewRequest("POST", "/api/profile", strings.NewReader(`{"displayName":"Alice"}`))
	req.Header.Set("X-Test-User", "alice@example.com")
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on lookup error", rec.Code)
	}
	// The raw storage error text must not leak to the client.
	if strings.Contains(rec.Body.String(), "simulated store read error") {
		t.Errorf("response leaked the raw storage error: %q", rec.Body.String())
	}
	if a.putSeen != 0 {
		t.Errorf("PutAccount called despite a failed lookup, want 0")
	}
}

func TestSetProfilePutErrorIs500(t *testing.T) {
	a := newAccounts()
	a.putErr = errors.New("simulated store write error")

	req := httptest.NewRequest("POST", "/api/profile", strings.NewReader(`{"displayName":"Alice"}`))
	req.Header.Set("X-Test-User", "alice@example.com")
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on save error", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "simulated store write error") {
		t.Errorf("response leaked the raw storage error: %q", rec.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	a := newAccounts()
	// POST to /api/profiles (only GET defined) ⇒ 405.
	req := httptest.NewRequest("POST", "/api/profiles", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
