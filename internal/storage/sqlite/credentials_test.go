package sqlite_test

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/sqlite"
)

// openCreds opens a fresh store for credential tests (pid and openStore helpers
// are shared with sqlite_test.go).
func openCreds(t *testing.T) *sqlite.Store {
	t.Helper()
	return openStore(t, filepath.Join(t.TempDir(), "credentials.db"))
}

func TestCredentialRoundTrip(t *testing.T) {
	s := openCreds(t)
	alice := pid(t, "alice@github")

	want := storage.Credential{
		Method:    "github",
		Subject:   "12345",
		Account:   alice,
		Data:      `{"login":"alice"}`,
		CreatedAt: 1000,
	}
	if err := s.PutCredential(want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetCredential("github", "12345")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestCredentialMissing(t *testing.T) {
	s := openCreds(t)
	if _, ok, err := s.GetCredential("github", "nope"); ok || err != nil {
		t.Errorf("missing credential: ok=%v err=%v, want false/nil", ok, err)
	}
}

// TestCredentialReplacePreservesCreatedAt: replacing a credential (e.g. the user
// re-logs and the DisplayName data changes) keeps the original created_at, so the
// binding's age is stable.
func TestCredentialReplacePreservesCreatedAt(t *testing.T) {
	s := openCreds(t)
	alice := pid(t, "alice@github")

	if err := s.PutCredential(storage.Credential{
		Method: "github", Subject: "12345", Account: alice, Data: `{"login":"alice"}`, CreatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	// Re-put with a later created_at and changed data; created_at must not move.
	if err := s.PutCredential(storage.Credential{
		Method: "github", Subject: "12345", Account: alice, Data: `{"login":"alice2"}`, CreatedAt: 9999,
	}); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetCredential("github", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt != 1000 {
		t.Errorf("created_at after replace = %d, want 1000 (preserved)", got.CreatedAt)
	}
	if got.Data != `{"login":"alice2"}` {
		t.Errorf("data after replace = %q, want updated", got.Data)
	}
}

// TestCredentialListByAccount: a single account may own several credentials (a
// GitHub login and an OIDC sub); ListByAccount returns exactly those, and not
// another account's.
func TestCredentialListByAccount(t *testing.T) {
	s := openCreds(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")

	creds := []storage.Credential{
		{Method: "github", Subject: "111", Account: alice, Data: "{}", CreatedAt: 1},
		{Method: "oidc", Subject: "https://issuer/|sub-a", Account: alice, Data: "{}", CreatedAt: 2},
		{Method: "github", Subject: "222", Account: bob, Data: "{}", CreatedAt: 3},
	}
	for _, c := range creds {
		if err := s.PutCredential(c); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListByAccount(alice)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d credentials for alice, want 2: %+v", len(got), got)
	}
	// Order is unspecified; sort by subject for a stable comparison.
	sort.Slice(got, func(i, j int) bool { return got[i].Subject < got[j].Subject })
	if got[0].Method != "github" || got[1].Method != "oidc" {
		t.Errorf("methods = %q/%q, want github/oidc", got[0].Method, got[1].Method)
	}

	// Bob owns exactly one.
	gotBob, err := s.ListByAccount(bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotBob) != 1 || gotBob[0].Subject != "222" {
		t.Errorf("bob's credentials = %+v, want one with subject 222", gotBob)
	}

	// An account with no credentials gets an empty list (not an error).
	gotNobody, err := s.ListByAccount(pid(t, "nobody@example.com"))
	if err != nil || len(gotNobody) != 0 {
		t.Errorf("nobody's credentials = %+v err=%v, want empty/nil", gotNobody, err)
	}
}
