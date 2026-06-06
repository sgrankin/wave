package cc

// Ported from Java DeltaPairTest:
//   wave/src/test/java/org/waveprotocol/wave/concurrencycontrol/client/DeltaPairTest.java
//
// The Java DeltaPair.transform() reconciles a client op-list against a server
// op-list: each client op is transformed past every server op and vice versa
// (pairwise OT). Our cc.transformOpLists is the same algebra (it iterates
// client-outer/server-inner where Java iterates server-outer/client-inner, but
// the resulting transformed pair is identical for distinct concurrent deltas —
// verified by testMultipleClientServerOps below).
//
// Adaptation notes:
//   - Java builds ops over an implicit XML/document via DocOpBuilder.retain/
//     characters; we build the same DocOps directly (Retain/Characters), since
//     our op model is the same insertion algebra.
//   - testIsSame's VersionUpdateOp half is a deliberate architecture drop in our
//     port (no VersionUpdateOp type; transformOpLists does not special-case the
//     identical-delta nullification — see transform.go). The areSame predicate
//     itself is ported against waveop.EqualOps + creator equality.

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

// dpInsert builds a WaveletBlipOperation that inserts text at pos with the given
// trailing retain, authored by author. Mirrors DeltaTestUtil.insert: retain(pos)
// characters(text) and, when remaining > 0, retain(remaining).
func dpInsert(author id.ParticipantID, pos int, text string, remaining int) waveop.Operation {
	// DocOpBuilder.retain drops a zero-width retain; mirror that for pos == 0.
	var comps []op.Component
	if pos > 0 {
		comps = append(comps, op.Retain{Count: pos})
	}
	comps = append(comps, op.Characters{Text: text})
	if remaining > 0 {
		comps = append(comps, op.Retain{Count: remaining})
	}
	ctx := waveop.Context{Creator: author, Timestamp: 0, VersionIncrement: 1}
	return waveop.WaveletBlipOperation{
		BlipID: "blip id",
		BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: op.NewDocOp(comps)},
	}
}

// dpContent extracts the DocOp from a blip-content WaveletOperation (test helper).
func dpContent(t *testing.T, o waveop.Operation) op.DocOp {
	t.Helper()
	wbo, ok := o.(waveop.WaveletBlipOperation)
	if !ok {
		t.Fatalf("expected WaveletBlipOperation, got %T", o)
	}
	bc, ok := wbo.BlipOp.(waveop.BlipContentOperation)
	if !ok {
		t.Fatalf("expected BlipContentOperation, got %T", wbo.BlipOp)
	}
	return bc.ContentOp
}

// dpCheckInsert asserts o is an insertion of content at location with the given
// trailing retain (ports DeltaPairTest.checkInsert).
func dpCheckInsert(t *testing.T, o waveop.Operation, location int, content string, remaining int) {
	t.Helper()
	comps := []op.Component{op.Retain{Count: location}, op.Characters{Text: content}}
	if remaining > 0 {
		comps = append(comps, op.Retain{Count: remaining})
	}
	want := op.NewDocOp(comps)
	got := dpContent(t, o)
	if !got.Equal(want) {
		t.Errorf("insert op = %v, want %v", got.Components(), want.Components())
	}
}

// TestDeltaPairMultipleClientServerOps ports DeltaPairTest.testMultipleClientServerOps.
func TestDeltaPairMultipleClientServerOps(t *testing.T) {
	client := pid(t, "client@example.com")
	server := pid(t, "server@example.com")

	// Client insert ".A.B"
	clientOps := []waveop.Operation{
		dpInsert(client, 1, "A", 1),
		dpInsert(client, 3, "B", 0),
	}
	// Server insert ".2.1"
	serverOps := []waveop.Operation{
		dpInsert(server, 2, "1", 0),
		dpInsert(server, 1, "2", 2),
	}

	clientPrime, serverPrime, err := transformOpLists(clientOps, serverOps)
	if err != nil {
		t.Fatalf("transformOpLists: %v", err)
	}

	// Where client and server insert at the same point, the client op is
	// transformed to the LEFT of the server op.
	if len(clientPrime) != 2 {
		t.Fatalf("client' size = %d, want 2", len(clientPrime))
	}
	dpCheckInsert(t, clientPrime[0], 1, "A", 3)
	dpCheckInsert(t, clientPrime[1], 4, "B", 1)

	if len(serverPrime) != 2 {
		t.Fatalf("server' size = %d, want 2", len(serverPrime))
	}
	dpCheckInsert(t, serverPrime[0], 4, "1", 0)
	dpCheckInsert(t, serverPrime[1], 2, "2", 3)
}

// dpAreSame ports DeltaPair.areSame: two op-lists are "the same" iff they have
// the same length and each pair of ops matches by author (creator) AND payload.
// waveop.EqualOps compares payload only (ignoring Context), so the creator check
// is layered on top to match Java's matchOperations.
func dpAreSame(a, b []waveop.Operation) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Context().Creator != b[i].Context().Creator {
			return false
		}
	}
	return waveop.EqualOps(a, b)
}

// TestDeltaPairIsSame ports the areSame predicate half of DeltaPairTest.testIsSame.
// The VersionUpdateOp half (transform() yielding empty client + version-update
// server ops) is skipped: see TestDeltaPairIsSameNullification.
func TestDeltaPairIsSame(t *testing.T) {
	client := pid(t, "client@example.com")

	clientOps := []waveop.Operation{
		dpInsert(client, 1, "A", 1),
		dpInsert(client, 3, "B", 0),
	}
	// Same ops, same author (Java reuses CLIENT_UTIL for both sides).
	serverOps := []waveop.Operation{
		dpInsert(client, 1, "A", 1),
		dpInsert(client, 3, "B", 0),
	}

	if !dpAreSame(clientOps, serverOps) {
		t.Errorf("areSame(client, server) = false, want true")
	}

	// Differing author must break sameness (matchOperations checks the creator).
	server := pid(t, "server@example.com")
	otherAuthor := []waveop.Operation{
		dpInsert(server, 1, "A", 1),
		dpInsert(server, 3, "B", 0),
	}
	if dpAreSame(clientOps, otherAuthor) {
		t.Errorf("areSame with differing authors = true, want false")
	}

	// Differing payload must break sameness.
	differentPayload := []waveop.Operation{
		dpInsert(client, 1, "A", 1),
		dpInsert(client, 3, "C", 0),
	}
	if dpAreSame(clientOps, differentPayload) {
		t.Errorf("areSame with differing payload = true, want false")
	}
}

// TestDeltaPairIsSameNullification documents the dropped VersionUpdateOp behavior.
func TestDeltaPairIsSameNullification(t *testing.T) {
	t.Skip("ADAPT: no VersionUpdateOp type in our port; transformOpLists does not " +
		"special-case the identical-delta nullification (server transform-to-head " +
		"consumes only the client side). See transform.go and skipped[] in the report.")
}
