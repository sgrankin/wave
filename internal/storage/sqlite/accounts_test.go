package sqlite_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/sqlite"
)

// openAccounts opens a fresh store for account tests (pid and openStore helpers
// are shared with sqlite_test.go).
func openAccounts(t *testing.T) *sqlite.Store {
	t.Helper()
	return openStore(t, filepath.Join(t.TempDir(), "accounts.db"))
}

func TestAccountHumanRoundTrip(t *testing.T) {
	s := openAccounts(t)
	alice := pid(t, "alice@example.com")

	want := &storage.Account{
		ID:   alice,
		Kind: storage.AccountHuman,
		Human: &storage.HumanAccount{
			Password: &storage.PasswordDigest{Salt: []byte{1, 2, 3}, Digest: []byte{9, 8, 7, 6}},
			Locale:   "en",
		},
	}
	if err := s.PutAccount(want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetAccount(alice)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v / %+v\nwant %+v / %+v",
			got, got.Human, want, want.Human)
	}
}

func TestAccountPasswordlessHuman(t *testing.T) {
	s := openAccounts(t)
	bob := pid(t, "bob@example.com")
	// A human account with password auth disabled (nil digest) must round-trip.
	want := &storage.Account{ID: bob, Kind: storage.AccountHuman, Human: &storage.HumanAccount{}}
	if err := s.PutAccount(want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetAccount(bob)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Human == nil || got.Human.Password != nil {
		t.Errorf("expected non-nil Human with nil Password, got %+v", got.Human)
	}
}

func TestAccountRobotRoundTrip(t *testing.T) {
	s := openAccounts(t)
	robot := pid(t, "search@example.com")
	want := &storage.Account{
		ID:   robot,
		Kind: storage.AccountRobot,
		Robot: &storage.RobotAccount{
			URL:            "https://example.com/robot",
			ConsumerSecret: "s3cr3t",
			Capabilities:   []byte("caps-blob"),
			Verified:       true,
		},
	}
	if err := s.PutAccount(want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetAccount(robot)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got.Robot, want.Robot)
	}
}

func TestAccountReplaceAndRemove(t *testing.T) {
	s := openAccounts(t)
	alice := pid(t, "alice@example.com")

	if err := s.PutAccount(&storage.Account{ID: alice, Kind: storage.AccountHuman,
		Human: &storage.HumanAccount{Locale: "en"}}); err != nil {
		t.Fatal(err)
	}
	// Replace with a new locale.
	if err := s.PutAccount(&storage.Account{ID: alice, Kind: storage.AccountHuman,
		Human: &storage.HumanAccount{Locale: "fr"}}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetAccount(alice)
	if got.Human.Locale != "fr" {
		t.Errorf("locale after replace = %q, want fr", got.Human.Locale)
	}

	if err := s.RemoveAccount(alice); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, err := s.GetAccount(alice); ok || err != nil {
		t.Errorf("after remove: ok=%v err=%v, want false/nil", ok, err)
	}
	// Removing an absent account is a no-op.
	if err := s.RemoveAccount(alice); err != nil {
		t.Errorf("remove absent: %v", err)
	}
}

func TestGetMissingAccount(t *testing.T) {
	s := openAccounts(t)
	if _, ok, err := s.GetAccount(pid(t, "nobody@example.com")); ok || err != nil {
		t.Errorf("missing account: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestPutAccountKindMismatch(t *testing.T) {
	s := openAccounts(t)
	alice := pid(t, "alice@example.com")
	// Kind says human but no Human data → error, not a silent bad row.
	if err := s.PutAccount(&storage.Account{ID: alice, Kind: storage.AccountHuman}); err == nil {
		t.Error("expected error for human account with nil Human data")
	}
}

func TestCreateAccountInsertOnly(t *testing.T) {
	s := openAccounts(t)
	alice := pid(t, "alice@example.com")

	// First create inserts the row and reports created=true.
	first := &storage.Account{ID: alice, Kind: storage.AccountHuman,
		Human: &storage.HumanAccount{DisplayName: "the first alice", Locale: "en"}}
	created, err := s.CreateAccount(first)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if !created {
		t.Fatal("first create reported created=false, want true")
	}

	// A second create at the same address is a no-op: created=false, original preserved.
	second := &storage.Account{ID: alice, Kind: storage.AccountHuman,
		Human: &storage.HumanAccount{DisplayName: "the clobbering alice", Locale: "fr"}}
	created, err = s.CreateAccount(second)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if created {
		t.Error("second create reported created=true, want false (address already taken)")
	}
	got, ok, err := s.GetAccount(alice)
	if err != nil || !ok {
		t.Fatalf("get after second create: ok=%v err=%v", ok, err)
	}
	if got.Human.DisplayName != "the first alice" || got.Human.Locale != "en" {
		t.Errorf("original data was clobbered: %+v, want the first alice / en", got.Human)
	}
}

func TestCreateAccountKindMismatch(t *testing.T) {
	s := openAccounts(t)
	alice := pid(t, "alice@example.com")
	// Same kind-mismatch guard as PutAccount: human kind with nil Human data errors.
	if _, err := s.CreateAccount(&storage.Account{ID: alice, Kind: storage.AccountHuman}); err == nil {
		t.Error("expected error for human account with nil Human data")
	}
	// And nothing was inserted.
	if _, ok, _ := s.GetAccount(alice); ok {
		t.Error("a failed CreateAccount must not insert a row")
	}
}
