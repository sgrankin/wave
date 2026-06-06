package cc

// Ported (convergence subset) from Java ClientAndServerTest:
//   wave/src/test/java/org/waveprotocol/wave/concurrencycontrol/client/ClientAndServerTest.java
//
// Java drives a full client-CC state machine (ConcurrencyControl, ServerMock,
// ClientMock, ServerConnectionMock) against XML documents parsed by a SuperSink,
// with exact insertion-point arithmetic that depends on the XML structure. We
// have neither the client-side CC state machine (deferred to work item #11) nor
// an XML/SuperSink parser, so we port the BEHAVIORAL INTENT of the pure
// multi-client convergence scenarios (testSimple2Client, testConcurrent3Client)
// against our real server pieces:
//
//   - cc.TransformToHead + cc.MemoryHistory perform the server-side transform.
//   - wavelet.Data applies committed deltas and computes the real hashed-version
//     chain (HasSignature validation in TransformToHead depends on it).
//   - Convergence is checked by comparing every client's reconstructed document
//     against the server document via op.Compose / DocOp.Equal.
//
// Adaptation: plain character documents (Retain/Characters) replace the XML
// blips, so insertion points are unambiguous in our op model. The convergence
// property (all participants reach an identical document) and the OT tie-break
// (a client's edit at a shared point lands to the LEFT of a concurrent server
// edit) are preserved exactly.
//
// The recovery / ghost / reconnection / server-crash / flaky-client scenarios
// are NOT ported: they exercise the client CC state machine (work item #11). See
// skipped[] in the report.

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// csServer is a minimal in-package server: a wavelet plus its delta history,
// driving the real cc.TransformToHead on submit. It re-implements the essential
// behavior of Java ServerMock + ConcurrencyControlCore for convergence testing.
type csServer struct {
	t   *testing.T
	w   *wavelet.Data
	h   *MemoryHistory
	seq int // monotonic counter for unique serialized-delta bytes
}

func newCSServer(t *testing.T) *csServer {
	t.Helper()
	w := newWavelet(t)
	return &csServer{t: t, w: w, h: NewMemoryHistory(w.HashedVersion())}
}

// submit transforms a client delta to head, applies it, and records it in the
// history — the server's receive+process path. It returns the resulting head
// version the delta committed at. Mirrors ServerMock receiving a delta and
// ConcurrencyControlCore.onClientDelta transforming + applying it.
func (s *csServer) submit(author id.ParticipantID, base version.HashedVersion, ops []waveop.Operation) version.HashedVersion {
	s.t.Helper()
	delta := waveop.NewWaveletDelta(author, base, ops)
	transformed, err := TransformToHead(s.h, delta)
	if err != nil {
		s.t.Fatalf("TransformToHead(author=%s, base=v%d): %v", author.Address(), base.Version(), err)
	}
	s.seq++
	bytesID := []byte{byte(s.seq)}
	if err := s.w.ApplyDelta(transformed, bytesID); err != nil {
		s.t.Fatalf("ApplyDelta: %v", err)
	}
	resulting := s.w.HashedVersion()
	s.h.Append(TransformedWaveletDelta{Author: author, ResultingVersion: resulting, Ops: transformed.Ops()})
	return resulting
}

// content returns the server's view of a blip's document.
func (s *csServer) content(blipID string) op.DocOp {
	b, ok := s.w.Blip(blipID)
	if !ok {
		return op.EmptyDoc()
	}
	return b.Content()
}

// A participant's document view is reconstructed by composing the committed
// server log (in commit order) onto the genesis document — see assertAllConverge.
// Because every client observes the same ordered, server-transformed log, all
// clients converge, the invariant Java's checkClientDoc asserts after
// clientsReceiveServerOperations.

// csInsert builds a blip-content op inserting text at pos in a doc of total
// length docLen (so the trailing retain is docLen-pos). Mirrors
// DeltaTestUtil.insert at a known document length.
func csInsert(author id.ParticipantID, blipID string, pos, docLen int, text string) waveop.Operation {
	// Mirror DocOpBuilder, which drops a zero-width retain: only emit the leading
	// retain when pos > 0, and the trailing retain when there is remaining doc.
	var comps []op.Component
	if pos > 0 {
		comps = append(comps, op.Retain{Count: pos})
	}
	comps = append(comps, op.Characters{Text: text})
	if rem := docLen - pos; rem > 0 {
		comps = append(comps, op.Retain{Count: rem})
	}
	ctx := waveop.Context{Creator: author, Timestamp: 1, VersionIncrement: 1}
	return waveop.WaveletBlipOperation{
		BlipID: blipID,
		BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: op.NewDocOp(comps)},
	}
}

// csText renders a single-blip document's leading character runs as a string
// (the document we built is character-only, so this is the readable content).
func csText(t *testing.T, d op.DocOp) string {
	t.Helper()
	out := ""
	for _, c := range d.Components() {
		switch v := c.(type) {
		case op.Characters:
			out += v.Text
		case op.Retain:
			// retains over inserted text; nothing to render
		default:
			t.Fatalf("unexpected component %T in character document", c)
		}
	}
	return out
}

// TestClientServerSimple2Client ports the intent of testSimple2Client: two
// clients take turns inserting; with no concurrency each delta targets the
// current head, so every edit lands deterministically and both clients converge.
func TestClientServerSimple2Client(t *testing.T) {
	s := newCSServer(t)
	c0 := pid(t, "0@example.com")
	c1 := pid(t, "1@example.com")
	const blip = "b"

	// Inject a v0 delta so clients always submit AFTER version 0 (Java
	// injectV0Delta): seed the blip with "abc" from a system author.
	nobody := pid(t, "nobody@example.com")
	seed := s.submit(nobody, s.w.HashedVersion(), []waveop.Operation{
		csInsert(nobody, blip, 0, 0, "abc"),
	})
	// doc is now "abc" (length 3).

	// Client 0 inserts "X" at front: "Xabc".
	s.submit(c0, seed, []waveop.Operation{csInsert(c0, blip, 0, 3, "X")})
	if got := csText(t, s.content(blip)); got != "Xabc" {
		t.Fatalf("after c0 insert: %q, want %q", got, "Xabc")
	}

	// Client 1, now at head, inserts "Y" after "X": "XYabc".
	s.submit(c1, s.w.HashedVersion(), []waveop.Operation{csInsert(c1, blip, 1, 4, "Y")})
	if got := csText(t, s.content(blip)); got != "XYabc" {
		t.Fatalf("after c1 insert: %q, want %q", got, "XYabc")
	}

	// Client 0, at head, inserts "Z" after "XY": "XYZabc".
	s.submit(c0, s.w.HashedVersion(), []waveop.Operation{csInsert(c0, blip, 2, 5, "Z")})
	if got := csText(t, s.content(blip)); got != "XYZabc" {
		t.Fatalf("after c0 insert: %q, want %q", got, "XYZabc")
	}

	// Both clients, having received the same committed log, see the server doc.
	assertAllConverge(t, s, blip, "XYZabc")
}

// TestClientServerConcurrent3Client ports the intent of testConcurrent3Client:
// three clients concurrently insert at the same base version. They are
// serialized server-side via TransformToHead in submission order; the OT
// tie-break places each later submitter's edit to the LEFT of the earlier ones
// at the shared insertion point. All three converge.
func TestClientServerConcurrent3Client(t *testing.T) {
	s := newCSServer(t)
	c0 := pid(t, "0@example.com")
	c1 := pid(t, "1@example.com")
	c2 := pid(t, "2@example.com")
	const blip = "b"

	nobody := pid(t, "nobody@example.com")
	base := s.submit(nobody, s.w.HashedVersion(), []waveop.Operation{
		csInsert(nobody, blip, 0, 0, "abc"),
	})
	// doc is "abc"; base is the shared version all three clients author against.

	// All three insert at position 0 (front) of the length-3 doc, concurrently.
	// Submitted in order c0, c1, c2. TransformToHead transforms c1 past c0's
	// committed delta and c2 past both. A client's insert at a shared point goes
	// to the LEFT of the already-committed (server) insert, so the LAST submitter
	// ends up leftmost: c2, then c1, then c0, then "abc".
	s.submit(c0, base, []waveop.Operation{csInsert(c0, blip, 0, 3, "0X")})
	s.submit(c1, base, []waveop.Operation{csInsert(c1, blip, 0, 3, "1X")})
	s.submit(c2, base, []waveop.Operation{csInsert(c2, blip, 0, 3, "2X")})

	if got := csText(t, s.content(blip)); got != "2X1X0Xabc" {
		t.Fatalf("after concurrent inserts: %q, want %q", got, "2X1X0Xabc")
	}
	assertAllConverge(t, s, blip, "2X1X0Xabc")
}

// TestClientServerConcurrentDifferentPoints checks convergence when concurrent
// clients edit DIFFERENT, non-conflicting positions of the same base document —
// the order-independent core of OT. Three clients (at the same base) insert at
// the front, middle, and end; the result is well-formed and all converge.
func TestClientServerConcurrentDifferentPoints(t *testing.T) {
	s := newCSServer(t)
	c0 := pid(t, "0@example.com")
	c1 := pid(t, "1@example.com")
	c2 := pid(t, "2@example.com")
	const blip = "b"

	nobody := pid(t, "nobody@example.com")
	base := s.submit(nobody, s.w.HashedVersion(), []waveop.Operation{
		csInsert(nobody, blip, 0, 0, "abc"),
	})
	// doc "abc": c0 inserts "P" at front (pos 0), c1 "Q" in the middle (pos 2,
	// between b and c), c2 "R" at the end (pos 3). Distinct points, so commit
	// order does not change the relative result: "Pab Q c R" => "PabQcR".
	s.submit(c0, base, []waveop.Operation{csInsert(c0, blip, 0, 3, "P")})
	s.submit(c1, base, []waveop.Operation{csInsert(c1, blip, 2, 3, "Q")})
	s.submit(c2, base, []waveop.Operation{csInsert(c2, blip, 3, 3, "R")})

	if got := csText(t, s.content(blip)); got != "PabQcR" {
		t.Fatalf("distinct-point concurrent inserts: %q, want %q", got, "PabQcR")
	}
	assertAllConverge(t, s, blip, "PabQcR")
}

// assertAllConverge checks the server's committed document against the expected
// text and confirms it equals a from-genesis replay of the committed log. The
// load-bearing conformance assertions live in the callers (the exact post-
// transform text after concurrent submits, e.g. "2X1X0Xabc", which exercises the
// real TransformToHead tie-break); this helper guards the weaker property that the
// committed log replays to that same state — there is one server lineage here, not
// independent client lineages, so it is a replay check, not true multi-party
// convergence (that is the client-CC story in #11).
func assertAllConverge(t *testing.T, s *csServer, blipID, wantText string) {
	t.Helper()
	server := s.content(blipID)
	if got := csText(t, server); got != wantText {
		t.Fatalf("server content = %q, want %q", got, wantText)
	}

	// Replay the committed log from genesis: compose each committed delta's blip
	// op for blipID, in version order. A client that has received the whole log
	// reconstructs exactly this.
	doc := op.EmptyDoc()
	for v := uint64(0); v < s.h.CurrentVersion(); {
		d, ok := s.h.DeltaStartingAt(v)
		if !ok {
			t.Fatalf("missing committed delta at v%d", v)
		}
		for _, o := range d.Ops {
			wbo, isBlip := o.(waveop.WaveletBlipOperation)
			if !isBlip || wbo.BlipID != blipID {
				continue
			}
			bc, isContent := wbo.BlipOp.(waveop.BlipContentOperation)
			if !isContent {
				continue
			}
			composed, err := op.Compose(doc, bc.ContentOp)
			if err != nil {
				t.Fatalf("compose committed delta at v%d: %v", v, err)
			}
			doc = composed
		}
		v = d.ResultingVersion.Version()
	}

	if !doc.Equal(server) {
		t.Errorf("reconstructed client view %v != server %v", doc.Components(), server.Components())
	}
	if got := csText(t, doc); got != wantText {
		t.Errorf("reconstructed client text = %q, want %q", got, wantText)
	}
}
