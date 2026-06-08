package auth

import (
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage/sqlite"
)

// TestRegisterChosenConcurrentSameAddress is the TOCTOU regression for first-login
// provisioning. N concurrent first-logins, each a DISTINCT credential subject (so each
// takes MintIdP's first-login → RegisterChosen path) but all deriving the SAME address,
// race against a REAL sqlite store. Exactly one must win; the rest must be rejected with
// "already taken". Before CreateAccount, the GetAccount-then-PutAccount window let many
// pass the absent-check and all clobber a single row.
func TestRegisterChosenConcurrentSameAddress(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	clk := clock.NewFixed(time.UnixMilli(1_000_000))
	svc := NewService(
		NewSessions([]byte("0123456789abcdef0123456789abcdef"), time.Hour, clk),
		Provisioner{Accounts: store, RegisterOnFirstUse: true},
	)
	svc.SecureCookies = false

	const target = "victim@github"
	want := mustPID(t, target)
	policy := DomainOnly("github")

	const n = 64
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		taken     int
		other     []error
		start     = make(chan struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine is a fresh credential subject (a first login), but all
			// derive the same `target` address — so all converge on RegisterChosen.
			login := IdPLogin{
				Method:      "race",
				Subject:     "subject-" + strconv.Itoa(i),
				DisplayName: "claimant-" + strconv.Itoa(i),
				CreatedAt:   clk.Now().Unix(),
				Derive: func() (id.ParticipantID, MintPolicy, string, error) {
					return want, policy, "", nil
				},
			}
			<-start // release all goroutines at once to maximize the race window
			_, err := svc.MintIdP(httptest.NewRecorder(), store, login)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case strings.Contains(err.Error(), "already taken"):
				taken++
			default:
				other = append(other, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(other) != 0 {
		t.Fatalf("unexpected non-takeover errors: %v", other)
	}
	if successes != 1 {
		t.Fatalf("got %d successful provisions, want exactly 1 (the rest rejected)", successes)
	}
	if taken != n-1 {
		t.Fatalf("got %d 'already taken' rejections, want %d", taken, n-1)
	}

	// The surviving account is the one that won: a human account exists at the address,
	// and the credential bound to it belongs to exactly one of the claimants (the winner
	// was not clobbered by a later writer).
	acct, ok, err := store.GetAccount(want)
	if err != nil || !ok {
		t.Fatalf("survivor account: ok=%v err=%v", ok, err)
	}
	if acct.Human == nil || !strings.HasPrefix(acct.Human.DisplayName, "claimant-") {
		t.Fatalf("survivor display name = %+v, want a single claimant-N", acct.Human)
	}
	winnerName := acct.Human.DisplayName
	winnerSubject := "subject-" + strings.TrimPrefix(winnerName, "claimant-")
	// Exactly one credential should be bound at the address: the winner's. Losers bind
	// nothing (MintIdP binds only after RegisterChosen succeeds).
	bound, err := store.ListByAccount(want)
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}
	if len(bound) != 1 {
		t.Fatalf("got %d credentials bound to %s, want exactly 1 (the winner)", len(bound), target)
	}
	if bound[0].Subject != winnerSubject {
		t.Errorf("bound credential subject = %q, want the winner's %q (account was clobbered)", bound[0].Subject, winnerSubject)
	}
}
