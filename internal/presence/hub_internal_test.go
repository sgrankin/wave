package presence

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
)

func ppid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func recv(t *testing.T, c *conn) Update {
	t.Helper()
	select {
	case u := <-c.send:
		return u
	default:
		t.Fatal("expected a broadcast, got none")
		return Update{}
	}
}

func TestHubJoinUpdateLeaveFanout(t *testing.T) {
	h := NewHub()
	alice := &conn{participant: ppid(t, "alice@example.com"), send: make(chan Update, 8)}
	bob := &conn{participant: ppid(t, "bob@example.com"), send: make(chan Update, 8)}

	// Alice joins an empty room: no others to snapshot, nothing broadcast to her.
	if snap := h.join("room", alice); len(snap) != 0 {
		t.Fatalf("alice join snapshot = %v, want empty", snap)
	}

	// Bob joins: his snapshot includes alice (online), and alice is told bob is online.
	snap := h.join("room", bob)
	if len(snap) != 1 || snap[0].Participant != "alice@example.com" || !snap[0].Online {
		t.Fatalf("bob join snapshot = %+v, want [alice online]", snap)
	}
	if u := recv(t, alice); u.Participant != "bob@example.com" || !u.Online {
		t.Fatalf("alice should see bob online, got %+v", u)
	}

	// Bob updates: alice receives the typing/focused-blip state.
	h.update("room", bob, State{Typing: true, BlipID: "b+1"})
	if u := recv(t, alice); u.Participant != "bob@example.com" || !u.Typing || u.BlipID != "b+1" || !u.Online {
		t.Fatalf("alice should see bob typing in b+1, got %+v", u)
	}

	// Bob leaves: alice receives a departure (online:false). The sender never gets
	// its own events (bob's channel stays empty).
	h.leave("room", bob)
	if u := recv(t, alice); u.Participant != "bob@example.com" || u.Online {
		t.Fatalf("alice should see bob offline, got %+v", u)
	}
	select {
	case u := <-bob.send:
		t.Fatalf("bob (the sender) should not receive its own events, got %+v", u)
	default:
	}
}

func TestHubDropsSlowConsumerUpdateWithoutBlocking(t *testing.T) {
	h := NewHub()
	// A receiver with a full (zero-capacity-ish) buffer: fill it, then ensure an
	// update to a busy peer does not block the hub.
	slow := &conn{participant: ppid(t, "slow@example.com"), send: make(chan Update, 1)}
	fast := &conn{participant: ppid(t, "fast@example.com"), send: make(chan Update, 8)}
	h.join("room", slow)
	h.join("room", fast) // slow gets "fast online" (fills slow.send to 1)

	// slow.send is now full; another broadcast to slow must be dropped, not block.
	done := make(chan struct{})
	go func() {
		h.update("room", fast, State{Typing: true})
		close(done)
	}()
	<-done // if broadcastLocked blocked on the full channel, this would hang
}
