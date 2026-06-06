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

// span is the op count — the basis the real server uses to advance the version
// (one per op; VersionIncrement is ignored). Mirrors wavelet.ApplyDelta so the
// simulated server models production, not the client's internal accounting.
func span(ops []waveop.Operation) uint64 {
	return uint64(len(ops))
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
// history, and returns the resulting version plus the applied (transformed) ops.
func (s *simServer) submit(t *testing.T, d waveop.WaveletDelta) (version.HashedVersion, []waveop.Operation) {
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
	resulting := version.Apply(head, hashBytes, span(ops))
	applyServerDoc(t, s.blips, ops)
	s.hist.Append(cc.TransformedWaveletDelta{Author: tr.Author(), ResultingVersion: resulting, Timestamp: ts, Ops: ops})
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
	return s.submit(t, d)
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

	a := clientcc.New(name, alice, version.Zero(name))
	b := clientcc.New(name, bob, version.Zero(name))
	for _, c := range []*clientcc.CC{a, b} {
		if _, err := c.OnServerDelta(seedOps, seedVer); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Both edit concurrently against the seed (v_seed): alice inserts "A" at 0,
	// bob inserts "B" at the end.
	aDelta, err := a.Edit(insertCharOp(alice, 1, 0, 'A'))
	if err != nil || aDelta == nil {
		t.Fatalf("alice edit: delta=%v err=%v", aDelta, err)
	}
	bDelta, err := b.Edit(insertCharOp(bob, 1, 1, 'B'))
	if err != nil || bDelta == nil {
		t.Fatalf("bob edit: delta=%v err=%v", bDelta, err)
	}

	// Server applies bob first, then alice (transformed past bob).
	bVer, bOps := srv.submit(t, *bDelta)
	aVer, aOps := srv.submit(t, *aDelta)

	// Alice: deliver her ack BEFORE bob's preceding delta (the race). She must hold.
	if out := a.OnAck(aVer, uint64(len(aOps))); out != nil {
		t.Fatalf("alice sent a delta before settling: %v", out)
	}
	if out, err := a.OnServerDelta(bOps, bVer); err != nil || out != nil {
		t.Fatalf("alice OnServerDelta(bob): out=%v err=%v", out, err)
	}

	// Bob: ack then alice's delta, normal order.
	if out := b.OnAck(bVer, uint64(len(bOps))); out != nil {
		t.Fatalf("bob unexpectedly sent: %v", out)
	}
	if out, err := b.OnServerDelta(aOps, aVer); err != nil || out != nil {
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
// non-unit VersionIncrement must advance the version by ONE (op count), matching
// the server. Before the fix, the client summed VersionIncrement and never settled.
func TestVersionIncrementIgnored(t *testing.T) {
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	c := clientcc.New(name, alice, version.Zero(name))
	if _, err := c.OnServerDelta(seedOps, seedVer); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := c.ServerVersion().Version()

	// One op with a deliberately non-unit VersionIncrement; insert "Y" before "X".
	ctx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 5}
	ops := []waveop.Operation{waveop.WaveletBlipOperation{
		BlipID: "b",
		BlipOp: waveop.BlipContentOperation{
			Ctx:       ctx,
			ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "Y"}, op.Retain{Count: 1}}),
		},
	}}
	d, err := c.Edit(ops)
	if err != nil || d == nil {
		t.Fatalf("edit: d=%v err=%v", d, err)
	}
	resulting, applied := srv.submit(t, *d)
	if resulting.Version() != before+1 {
		t.Fatalf("server advanced to v%d, want v%d (op-count basis)", resulting.Version(), before+1)
	}
	if out := c.OnAck(resulting, uint64(len(applied))); out != nil {
		t.Fatalf("unexpected resend: %v", out)
	}
	if got := c.ServerVersion().Version(); got != before+1 {
		t.Errorf("client at v%d, want v%d (must count ops, not sum VersionIncrement)", got, before+1)
	}
}

// TestOpsAppliedZeroSettlesInPlace locks CRITICAL-2: an ack reporting zero applied
// ops (a deduped resend or a fully transformed-away submit) must settle the
// in-flight delta in place — clearing the slot and advancing nothing — rather than
// underflowing and wedging. Document-level reconciliation of such cases is part of
// the deferred resync work; this pins the settle mechanics.
func TestOpsAppliedZeroSettlesInPlace(t *testing.T) {
	name := mkName(t)
	alice := mkPID(t, "alice@example.com")
	srv := newSimServer(name)
	seedVer, seedOps := srv.seed(t, alice, "X")

	c := clientcc.New(name, alice, version.Zero(name))
	if _, err := c.OnServerDelta(seedOps, seedVer); err != nil {
		t.Fatalf("seed: %v", err)
	}
	v := c.ServerVersion()

	if d, err := c.Edit(insertCharOp(alice, 1, 0, 'Z')); err != nil || d == nil {
		t.Fatalf("edit: d=%v err=%v", d, err)
	}
	// Server reports the submit applied nothing at the current version.
	if out := c.OnAck(v, 0); out != nil {
		t.Fatalf("unexpected resend after no-op ack: %v", out)
	}
	if c.ServerVersion().Compare(v) != 0 {
		t.Errorf("version advanced on a zero-op ack: v%d, want v%d", c.ServerVersion().Version(), v.Version())
	}
	// The slot must be free: a fresh edit sends immediately.
	d2, err := c.Edit(insertCharOp(alice, 2, 2, 'W'))
	if err != nil {
		t.Fatalf("second edit: %v", err)
	}
	if d2 == nil {
		t.Fatal("in-flight slot still wedged after a zero-op ack")
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
	ops []waveop.Operation
	ver version.HashedVersion
}

// pendingAck is a submit ack awaiting (out-of-order) delivery: the resulting
// version and the server's authoritative applied op count.
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
		c := clientcc.New(name, au, version.Zero(name))
		if _, err := c.OnServerDelta(seedOps, seedVer); err != nil {
			t.Fatalf("seed client %d: %v", i, err)
		}
		nodes[i] = &node{cc: c, author: au}
	}

	send := func(from *node, d waveop.WaveletDelta) {
		ver, ops := srv.submit(t, d)
		from.ackPend = &pendingAck{ver: ver, opsApplied: uint64(len(ops))}
		for _, n := range nodes {
			if n != from {
				n.inbox = append(n.inbox, serverDelta{ops: ops, ver: ver})
			}
		}
	}
	deliverInbox := func(n *node) {
		sd := n.inbox[0]
		n.inbox = n.inbox[1:]
		out, err := n.cc.OnServerDelta(sd.ops, sd.ver)
		if err != nil {
			t.Fatalf("OnServerDelta: %v", err)
		}
		if out != nil {
			send(n, *out)
		}
	}
	deliverAck := func(n *node) {
		a := *n.ackPend
		n.ackPend = nil
		if out := n.cc.OnAck(a.ver, a.opsApplied); out != nil {
			send(n, *out)
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
			d, err := a.n.cc.Edit(ops)
			if err != nil {
				t.Fatalf("edit: %v", err)
			}
			if d != nil {
				send(a.n, *d)
			}
		case kInbox:
			deliverInbox(a.n)
		case kAck:
			deliverAck(a.n)
		}
	}

	// Drain to quiescence: deliver all remaining deltas and acks (settles may queue
	// further sends, which add more to deliver).
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
