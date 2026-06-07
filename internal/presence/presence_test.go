package presence_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/presence"
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

type fakeAccess struct{ allow bool }

func (f fakeAccess) CanAccess(id.ParticipantID, id.WaveletName) (bool, error) { return f.allow, nil }

const waveName = "example.com/w+pres/~/conv+root"

func dial(t *testing.T, ctx context.Context, base, user string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/presence?wave=" + url.QueryEscape(waveName)
	opts := &websocket.DialOptions{}
	if user != "" {
		opts.HTTPHeader = http.Header{"X-Test-User": {user}}
	}
	return websocket.Dial(ctx, wsURL, opts)
}

func TestPresenceBroadcastsTypingAndPresence(t *testing.T) {
	h := presence.New(context.Background(), presence.NewHub(), fakeAccess{allow: true}, identify, nil)
	hs := httptest.NewServer(h)
	defer hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Alice connects first.
	alice, _, err := dial(t, ctx, hs.URL, "alice@example.com")
	if err != nil {
		t.Fatalf("alice dial: %v", err)
	}
	defer alice.Close(websocket.StatusNormalClosure, "")

	// Bob connects: alice should be told bob is online.
	bob, _, err := dial(t, ctx, hs.URL, "bob@example.com")
	if err != nil {
		t.Fatalf("bob dial: %v", err)
	}
	defer bob.Close(websocket.StatusNormalClosure, "")

	var u presence.Update
	if err := wsjson.Read(ctx, alice, &u); err != nil {
		t.Fatalf("alice read (bob online): %v", err)
	}
	if u.Participant != "bob@example.com" || !u.Online {
		t.Fatalf("alice should see bob online, got %+v", u)
	}

	// Bob sends typing state → alice receives it.
	if err := wsjson.Write(ctx, bob, presence.State{Typing: true, BlipID: "b+root"}); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(ctx, alice, &u); err != nil {
		t.Fatalf("alice read (bob typing): %v", err)
	}
	if u.Participant != "bob@example.com" || !u.Typing || u.BlipID != "b+root" {
		t.Fatalf("alice should see bob typing in b+root, got %+v", u)
	}

	// Bob disconnects → alice receives a departure.
	bob.Close(websocket.StatusNormalClosure, "")
	if err := wsjson.Read(ctx, alice, &u); err != nil {
		t.Fatalf("alice read (bob offline): %v", err)
	}
	if u.Participant != "bob@example.com" || u.Online {
		t.Fatalf("alice should see bob offline, got %+v", u)
	}
}

func TestPresenceJoinSnapshot(t *testing.T) {
	h := presence.New(context.Background(), presence.NewHub(), fakeAccess{allow: true}, identify, nil)
	hs := httptest.NewServer(h)
	defer hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, _, err := dial(t, ctx, hs.URL, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close(websocket.StatusNormalClosure, "")
	// Alice announces a focused blip.
	if err := wsjson.Write(ctx, alice, presence.State{BlipID: "b+root"}); err != nil {
		t.Fatal(err)
	}
	// Bob joins later and must receive alice in his join snapshot.
	bob, _, err := dial(t, ctx, hs.URL, "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close(websocket.StatusNormalClosure, "")
	var u presence.Update
	if err := wsjson.Read(ctx, bob, &u); err != nil {
		t.Fatalf("bob read (alice snapshot): %v", err)
	}
	if u.Participant != "alice@example.com" || !u.Online {
		t.Fatalf("bob's join snapshot should include alice online, got %+v", u)
	}
}

func TestPresenceUnauthenticatedRejected(t *testing.T) {
	h := presence.New(context.Background(), presence.NewHub(), fakeAccess{allow: true}, identify, nil)
	hs := httptest.NewServer(h)
	defer hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := dial(t, ctx, hs.URL, "") // no user
	if err == nil {
		t.Fatal("expected dial to fail (401 before upgrade)")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

func TestPresenceNonMemberForbidden(t *testing.T) {
	h := presence.New(context.Background(), presence.NewHub(), fakeAccess{allow: false}, identify, nil)
	hs := httptest.NewServer(h)
	defer hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := dial(t, ctx, hs.URL, "bob@example.com")
	if err == nil {
		t.Fatal("expected dial to fail (403 before upgrade)")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}
