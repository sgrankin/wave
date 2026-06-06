package clientcc_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clientcc"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// --- fixtures ---

func mkName(t *testing.T) id.WaveletName {
	t.Helper()
	w, err := id.NewWaveID("example.com", "w+conv")
	if err != nil {
		t.Fatal(err)
	}
	wl, err := id.NewWaveletID("example.com", "conv+root")
	if err != nil {
		t.Fatal(err)
	}
	return id.NewWaveletName(w, wl)
}

func mkPID(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func opCtx(a id.ParticipantID) waveop.Context {
	return waveop.Context{Creator: a, Timestamp: 1000, VersionIncrement: 1}
}

func blipContentOp(a id.ParticipantID, blipID string, content op.DocOp) waveop.Operation {
	return waveop.WaveletBlipOperation{
		BlipID: blipID,
		BlipOp: waveop.BlipContentOperation{Ctx: opCtx(a), ContentOp: content},
	}
}

func docInsert(s string) op.DocOp {
	return op.NewDocOp([]op.Component{op.Characters{Text: s}})
}

// insertCharOp builds a blip "b" content op inserting one char at pos against a
// document of the given length.
func insertCharOp(a id.ParticipantID, length, pos int, ch byte) []waveop.Operation {
	var comps []op.Component
	if pos > 0 {
		comps = append(comps, op.Retain{Count: pos})
	}
	comps = append(comps, op.Characters{Text: string(ch)})
	if length-pos > 0 {
		comps = append(comps, op.Retain{Count: length - pos})
	}
	return []waveop.Operation{blipContentOp(a, "b", op.NewDocOp(comps))}
}

func blipText(t *testing.T, c *clientcc.CC, blipID string) string {
	t.Helper()
	d, ok := c.Blip(blipID)
	if !ok {
		t.Fatalf("no blip %q", blipID)
	}
	s, err := doc.PlainText(d)
	if err != nil {
		t.Fatalf("plain text: %v", err)
	}
	return s
}

// --- simulated server ---

type simServer struct {
	hist  *cc.MemoryHistory
	blips map[string]op.DocOp
}

func newSimServer(name id.WaveletName) *simServer {
	return &simServer{hist: cc.NewMemoryHistory(version.Zero(name)), blips: map[string]op.DocOp{}}
}

// submit transforms a client delta to head, applies it to the server document and
// history (recording the submitter's nonce so a resync tail can carry it), and
// returns the resulting version plus the applied (transformed) ops.
func (s *simServer) submit(t *testing.T, d waveop.WaveletDelta, nonce string) (version.HashedVersion, []waveop.Operation) {
	t.Helper()
	tr, err := cc.TransformToHead(s.hist, d)
	if err != nil {
		t.Fatalf("server transform-to-head: %v", err)
	}
	ops := tr.Ops()
	if len(ops) == 0 {
		t.Fatal("unexpected no-op submit (these tests use inserts only)")
	}
	head := s.hist.CurrentHashedVersion()
	const ts = 1000
	hashBytes := codec.HashBytes(tr.Author(), head.Version(), ts, ops)
	resulting := version.Apply(head, hashBytes, uint64(len(ops)))
	applyServerDoc(t, s.blips, ops)
	s.hist.Append(cc.TransformedWaveletDelta{
		Author: tr.Author(), ResultingVersion: resulting, Timestamp: ts, Ops: ops, Nonce: nonce,
	})
	return resulting, ops
}

func (s *simServer) text(t *testing.T, blipID string) string {
	t.Helper()
	d, ok := s.blips[blipID]
	if !ok {
		t.Fatalf("server has no blip %q", blipID)
	}
	out, err := doc.PlainText(d)
	if err != nil {
		t.Fatalf("server plain text: %v", err)
	}
	return out
}

func applyServerDoc(t *testing.T, blips map[string]op.DocOp, ops []waveop.Operation) {
	t.Helper()
	for _, o := range ops {
		wbo, ok := o.(waveop.WaveletBlipOperation)
		if !ok {
			continue
		}
		bco, ok := wbo.BlipOp.(waveop.BlipContentOperation)
		if !ok {
			continue
		}
		cur, ok := blips[wbo.BlipID]
		if !ok {
			cur = op.EmptyDoc()
		}
		next, err := op.Compose(cur, bco.ContentOp)
		if err != nil {
			t.Fatalf("server apply blip %q: %v", wbo.BlipID, err)
		}
		blips[wbo.BlipID] = next
	}
}

// seed creates blip "b" with the given content and returns the seeding ops and
// resulting version so clients can initialize from it via OnServerDelta.
func (s *simServer) seed(t *testing.T, author id.ParticipantID, content string) (version.HashedVersion, []waveop.Operation) {
	t.Helper()
	d := waveop.NewWaveletDelta(author, s.hist.CurrentHashedVersion(), []waveop.Operation{
		waveop.AddParticipant{Ctx: opCtx(author), Participant: author},
		blipContentOp(author, "b", docInsert(content)),
	})
	return s.submit(t, d, "")
}

// TestAckRaceHoldsThenSettles drives the option-1 case: a submit ack is delivered
// before the concurrent server delta that preceded the in-flight delta. The client
// must hold the ack, apply the delta, then settle — and converge.
func TestAckRaceHoldsThenSettles(t *testing.T) {
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	bob := mkPID(t, "bob@example.com")

	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	a := clientcc.New(name, alice, version.Zero(name), "sessA")
	b := clientcc.New(name, bob, version.Zero(name), "sessB")
	for _, c := range []*clientcc.CC{a, b} {
		if _, err := c.OnServerDelta(seedOps, seedVer, ""); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Both edit concurrently against the seed: alice inserts "A" at 0, bob "B" at end.
	aOut, err := a.Edit(insertCharOp(alice, 1, 0, 'A'))
	if err != nil || aOut == nil {
		t.Fatalf("alice edit: out=%v err=%v", aOut, err)
	}
	bOut, err := b.Edit(insertCharOp(bob, 1, 1, 'B'))
	if err != nil || bOut == nil {
		t.Fatalf("bob edit: out=%v err=%v", bOut, err)
	}

	// Server applies bob first, then alice (transformed past bob).
	bVer, bOps := srv.submit(t, bOut.Delta, bOut.Nonce)
	aVer, aOps := srv.submit(t, aOut.Delta, aOut.Nonce)

	// Alice: deliver her ack BEFORE bob's preceding delta (the race). She must hold.
	if out := a.OnAck(aVer, uint64(len(aOps))); out != nil {
		t.Fatalf("alice sent a delta before settling: %v", out)
	}
	if out, err := a.OnServerDelta(bOps, bVer, bOut.Nonce); err != nil || out != nil {
		t.Fatalf("alice OnServerDelta(bob): out=%v err=%v", out, err)
	}

	// Bob: ack then alice's delta, normal order.
	if out := b.OnAck(bVer, uint64(len(bOps))); out != nil {
		t.Fatalf("bob unexpectedly sent: %v", out)
	}
	if out, err := b.OnServerDelta(aOps, aVer, aOut.Nonce); err != nil || out != nil {
		t.Fatalf("bob OnServerDelta(alice): out=%v err=%v", out, err)
	}

	want := srv.text(t, "b")
	if want != "AXB" {
		t.Fatalf("server text = %q, want AXB", want)
	}
	if got := blipText(t, a, "b"); got != want {
		t.Errorf("alice = %q, want %q", got, want)
	}
	if got := blipText(t, b, "b"); got != want {
		t.Errorf("bob = %q, want %q", got, want)
	}
	if a.ServerVersion().Compare(aVer) != 0 || b.ServerVersion().Compare(aVer) != 0 {
		t.Errorf("clients not at head v%d (alice v%d, bob v%d)",
			aVer.Version(), a.ServerVersion().Version(), b.ServerVersion().Version())
	}
}

// TestVersionIncrementIgnored locks the op-count version basis: an op carrying a
// non-unit VersionIncrement must advance the version by ONE (op count).
func TestVersionIncrementIgnored(t *testing.T) {
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	c := clientcc.New(name, alice, version.Zero(name), "sessA")
	if _, err := c.OnServerDelta(seedOps, seedVer, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := c.ServerVersion().Version()

	ctx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 5}
	ops := []waveop.Operation{waveop.WaveletBlipOperation{
		BlipID: "b",
		BlipOp: waveop.BlipContentOperation{
			Ctx:       ctx,
			ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "Y"}, op.Retain{Count: 1}}),
		},
	}}
	out, err := c.Edit(ops)
	if err != nil || out == nil {
		t.Fatalf("edit: out=%v err=%v", out, err)
	}
	resulting, applied := srv.submit(t, out.Delta, out.Nonce)
	if resulting.Version() != before+1 {
		t.Fatalf("server advanced to v%d, want v%d (op-count basis)", resulting.Version(), before+1)
	}
	if got := c.OnAck(resulting, uint64(len(applied))); got != nil {
		t.Fatalf("unexpected resend: %v", got)
	}
	if got := c.ServerVersion().Version(); got != before+1 {
		t.Errorf("client at v%d, want v%d (must count ops, not sum VersionIncrement)", got, before+1)
	}
}

// TestOpsAppliedZeroSettlesInPlace locks the zero-op ack: a deduped or fully
// transformed-away submit (opsApplied==0) settles the in-flight delta in place
// rather than underflowing and wedging the slot.
func TestOpsAppliedZeroSettlesInPlace(t *testing.T) {
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	c := clientcc.New(name, alice, version.Zero(name), "sessA")
	if _, err := c.OnServerDelta(seedOps, seedVer, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	v := c.ServerVersion()

	if out, err := c.Edit(insertCharOp(alice, 1, 0, 'Z')); err != nil || out == nil {
		t.Fatalf("edit: out=%v err=%v", out, err)
	}
	if out := c.OnAck(v, 0); out != nil {
		t.Fatalf("unexpected resend after no-op ack: %v", out)
	}
	if c.ServerVersion().Compare(v) != 0 {
		t.Errorf("version advanced on a zero-op ack: v%d, want v%d", c.ServerVersion().Version(), v.Version())
	}
	out, err := c.Edit(insertCharOp(alice, 2, 2, 'W'))
	if err != nil {
		t.Fatalf("second edit: %v", err)
	}
	if out == nil {
		t.Fatal("in-flight slot still wedged after a zero-op ack")
	}
}

// TestResyncRecognizesOwnDelta: an in-flight delta that committed while the client
// was disconnected appears in the resync tail (no longer suppressed); the client
// recognizes it by nonce and settles it without re-applying.
func TestResyncRecognizesOwnDelta(t *testing.T) {
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	c := clientcc.New(name, alice, version.Zero(name), "sessA")
	if _, err := c.OnServerDelta(seedOps, seedVer, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := c.Edit(insertCharOp(alice, 1, 0, 'Z')) // "ZX" optimistically; in-flight
	if err != nil || out == nil {
		t.Fatalf("edit: out=%v err=%v", out, err)
	}
	// The server applied it (the ack was lost to a disconnect).
	resulting, appliedOps := srv.submit(t, out.Delta, out.Nonce)

	// On resync, the tail carries the client's OWN delta with its nonce. The client
	// recognizes and settles it — no re-apply, no transform-against-self.
	send, err := c.OnServerDelta(appliedOps, resulting, out.Nonce)
	if err != nil {
		t.Fatalf("resync own delta: %v", err)
	}
	if send != nil {
		t.Fatalf("unexpected send (queue empty): %v", send)
	}
	if c.ServerVersion().Compare(resulting) != 0 {
		t.Errorf("client at v%d after recognizing own delta, want v%d", c.ServerVersion().Version(), resulting.Version())
	}
	if got := blipText(t, c, "b"); got != "ZX" {
		t.Errorf("content = %q, want ZX (optimistic edit confirmed, not doubled)", got)
	}
	if rs := c.AfterResync(); rs != nil {
		t.Errorf("AfterResync wanted nothing to resend, got %v", rs)
	}
}

// TestResyncResubmitsUncommitted: an in-flight delta the server never received
// (disconnect before it arrived) is NOT in the resync tail; AfterResync re-submits
// it, re-targeted to the post-resync version, with its original nonce.
func TestResyncResubmitsUncommitted(t *testing.T) {
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	bob := mkPID(t, "bob@example.com")
	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	c := clientcc.New(name, alice, version.Zero(name), "sessA")
	if _, err := c.OnServerDelta(seedOps, seedVer, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := c.Edit(insertCharOp(alice, 1, 0, 'Z')) // in-flight, but server never got it
	if err != nil || out == nil {
		t.Fatalf("edit: out=%v err=%v", out, err)
	}

	// Meanwhile another participant committed a delta; the resync tail carries it
	// (a foreign delta), not the client's own. Bob inserts "Q" at end of "X".
	bobDelta := waveop.NewWaveletDelta(bob, seedVer, []waveop.Operation{
		blipContentOp(bob, "b", op.NewDocOp([]op.Component{op.Retain{Count: 1}, op.Characters{Text: "Q"}})),
	})
	qVer, qOps := srv.submit(t, bobDelta, "sessB.1")
	if _, err := c.OnServerDelta(qOps, qVer, "sessB.1"); err != nil {
		t.Fatalf("resync tail (foreign): %v", err)
	}

	// AfterResync must re-submit the still-unsettled in-flight delta, re-targeted to
	// the post-resync version, keeping its nonce.
	rs := c.AfterResync()
	if rs == nil {
		t.Fatal("AfterResync did not re-submit the uncommitted in-flight delta")
	}
	if rs.Nonce != out.Nonce {
		t.Errorf("resubmit nonce = %q, want original %q", rs.Nonce, out.Nonce)
	}
	if rs.Delta.TargetVersion().Compare(c.ServerVersion()) != 0 {
		t.Errorf("resubmit targets v%d, want current v%d", rs.Delta.TargetVersion().Version(), c.ServerVersion().Version())
	}
	// And it applies cleanly at head, converging with the foreign edit.
	rVer, _ := srv.submit(t, rs.Delta, rs.Nonce)
	if got := c.OnAck(rVer, uint64(len(rs.Delta.Ops()))); got != nil {
		t.Fatalf("unexpected send after resubmit ack: %v", got)
	}
	if got := blipText(t, c, "b"); got != srv.text(t, "b") {
		t.Errorf("after resubmit: client %q vs server %q", got, srv.text(t, "b"))
	}
}

// node is one client plus its in-order inbox of server deltas and a floatable
// pending ack (the simulated network).
type node struct {
	cc      *clientcc.CC
	author  id.ParticipantID
	inbox   []serverDelta
	ackPend *pendingAck
}

type serverDelta struct {
	ops   []waveop.Operation
	ver   version.HashedVersion
	nonce string
}

// pendingAck is a submit ack awaiting (out-of-order) delivery.
type pendingAck struct {
	ver        version.HashedVersion
	opsApplied uint64
}

// TestConvergenceRandom fuzzes random concurrent inserts across three clients with
// random delivery order (acks float relative to each client's in-order delta
// stream, exercising the ack race and version gaps), then asserts every client
// converges to the server document. Repeated over many seeds.
func TestConvergenceRandom(t *testing.T) {
	for seed := int64(1); seed <= 50; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			runConvergence(t, seed, 600)
		})
	}
}

func runConvergence(t *testing.T, seed int64, steps int) {
	t.Helper()
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	bob := mkPID(t, "bob@example.com")
	carol := mkPID(t, "carol@example.com")

	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	authors := []id.ParticipantID{alice, bob, carol}
	nodes := make([]*node, len(authors))
	for i, au := range authors {
		c := clientcc.New(name, au, version.Zero(name), fmt.Sprintf("sess%d", i))
		if _, err := c.OnServerDelta(seedOps, seedVer, ""); err != nil {
			t.Fatalf("seed client %d: %v", i, err)
		}
		nodes[i] = &node{cc: c, author: au}
	}

	send := func(from *node, o *clientcc.Outgoing) {
		ver, ops := srv.submit(t, o.Delta, o.Nonce)
		from.ackPend = &pendingAck{ver: ver, opsApplied: uint64(len(ops))}
		for _, n := range nodes {
			if n != from {
				n.inbox = append(n.inbox, serverDelta{ops: ops, ver: ver, nonce: o.Nonce})
			}
		}
	}
	deliverInbox := func(n *node) {
		sd := n.inbox[0]
		n.inbox = n.inbox[1:]
		out, err := n.cc.OnServerDelta(sd.ops, sd.ver, sd.nonce)
		if err != nil {
			t.Fatalf("OnServerDelta: %v", err)
		}
		if out != nil {
			send(n, out)
		}
	}
	deliverAck := func(n *node) {
		a := *n.ackPend
		n.ackPend = nil
		if out := n.cc.OnAck(a.ver, a.opsApplied); out != nil {
			send(n, out)
		}
	}

	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < steps; i++ {
		const (
			kEdit = iota
			kInbox
			kAck
		)
		type action struct {
			kind int
			n    *node
		}
		var acts []action
		for _, n := range nodes {
			acts = append(acts, action{kEdit, n})
			if len(n.inbox) > 0 {
				acts = append(acts, action{kInbox, n})
			}
			if n.ackPend != nil {
				acts = append(acts, action{kAck, n})
			}
		}
		a := acts[rng.Intn(len(acts))]
		switch a.kind {
		case kEdit:
			content, _ := a.n.cc.Blip("b")
			length := content.DocumentLength()
			ops := insertCharOp(a.n.author, length, rng.Intn(length+1), byte('a'+rng.Intn(26)))
			out, err := a.n.cc.Edit(ops)
			if err != nil {
				t.Fatalf("edit: %v", err)
			}
			if out != nil {
				send(a.n, out)
			}
		case kInbox:
			deliverInbox(a.n)
		case kAck:
			deliverAck(a.n)
		}
	}

	// Drain to quiescence.
	for {
		progressed := false
		for _, n := range nodes {
			for len(n.inbox) > 0 {
				deliverInbox(n)
				progressed = true
			}
			if n.ackPend != nil {
				deliverAck(n)
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}

	want := srv.text(t, "b")
	head := srv.hist.CurrentHashedVersion()
	for i, n := range nodes {
		if got := blipText(t, n.cc, "b"); got != want {
			t.Errorf("client %d did not converge: got %q, want %q", i, got, want)
		}
		if n.cc.ServerVersion().Compare(head) != 0 {
			t.Errorf("client %d at v%d, server head v%d", i, n.cc.ServerVersion().Version(), head.Version())
		}
	}
	t.Logf("converged on %d-rune document at v%d", len(want), head.Version())
}
