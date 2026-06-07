package attachapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/attachapi"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/attachments"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/transport"
)

// TestMountWithRealMembership exercises the cmd/waved mounting: the attachment
// routes mounted at BOTH "/attachments" and "/attachments/" (the bare upload path
// has no trailing slash), with transport.MembershipChecker over a real seeded
// wavelet as the access gate. A member can upload + download; a non-member is
// forbidden — proving the wiring end to end, not just attachapi's own fakes.
func TestMountWithRealMembership(t *testing.T) {
	dir := t.TempDir()
	store, err := attachments.New(filepath.Join(dir, "att"))
	if err != nil {
		t.Fatal(err)
	}
	dbstore, err := sqlite.Open(filepath.Join(dir, "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbstore.Close() })
	wm := server.NewWaveMap(dbstore, clock.NewFixed(time.UnixMilli(1000)))

	name := waveletName(t)
	alice := pid(t, "alice@example.com")
	bob := pid(t, "bob@example.com")
	// Seed the wavelet so alice is a participant (member); bob is not.
	c, err := wm.Container(name)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := conv.SeedConversation(alice, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeedIfEmpty(alice, ops); err != nil {
		t.Fatal(err)
	}

	ah := attachapi.New(store, transport.MembershipChecker{WaveMap: wm}, identify)
	mux := http.NewServeMux()
	mux.Handle("/attachments", ah.Routes())
	mux.Handle("/attachments/", ah.Routes())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Alice (a member) uploads — exercises the no-trailing-slash "/attachments" mount.
	const payload = "attachment-bytes-123"
	upReq, _ := http.NewRequest("POST", srv.URL+uploadURL(name, "note.txt", "text/plain"), strings.NewReader(payload))
	upReq.Header.Set("X-Test-User", alice.Address())
	upResp, err := http.DefaultClient.Do(upReq)
	if err != nil {
		t.Fatal(err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", upResp.StatusCode)
	}
	var up struct{ ID string }
	if err := json.NewDecoder(upResp.Body).Decode(&up); err != nil {
		t.Fatal(err)
	}
	if up.ID == "" {
		t.Fatal("upload returned no id")
	}

	// Alice downloads — exercises the "/attachments/{id}" (trailing-slash) mount.
	dlReq, _ := http.NewRequest("GET", srv.URL+"/attachments/"+up.ID, nil)
	dlReq.Header.Set("X-Test-User", alice.Address())
	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		t.Fatal(err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d, want 200", dlResp.StatusCode)
	}
	got, _ := io.ReadAll(dlResp.Body)
	if string(got) != payload {
		t.Errorf("downloaded %q, want %q", got, payload)
	}

	// Bob (not a participant) is forbidden from downloading.
	bobReq, _ := http.NewRequest("GET", srv.URL+"/attachments/"+up.ID, nil)
	bobReq.Header.Set("X-Test-User", bob.Address())
	bobResp, err := http.DefaultClient.Do(bobReq)
	if err != nil {
		t.Fatal(err)
	}
	defer bobResp.Body.Close()
	if bobResp.StatusCode != http.StatusForbidden {
		t.Errorf("non-member download status = %d, want 403", bobResp.StatusCode)
	}
}
