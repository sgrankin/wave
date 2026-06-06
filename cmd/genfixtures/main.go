// Command genfixtures emits cross-language OT/codec conformance vectors for the
// TypeScript client (web/). Each vector carries inputs and the Go reference
// outputs as hex-encoded canonical CBOR (the real codec encoding), so loading a
// vector exercises the TS CBOR decode AND the TS algebra against the Go oracle in
// one shot. The TS side decodes the hex into DocOps/ops, runs its own
// compose/transform, and checks the result equals the decoded expected output.
//
// Usage: go run ./cmd/genfixtures > web/src/wave/testdata/fixtures.json
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

type fixtures struct {
	Compose        []composeCase   `json:"compose"`
	Transform      []transformCase `json:"transform"`
	DeltaTransform []deltaCase     `json:"deltaTransform"`
	Codec          []codecCase     `json:"codec"`
}

type composeCase struct {
	Note string `json:"note"`
	A    string `json:"a"`
	B    string `json:"b"`
	Out  string `json:"out"`
}

type transformCase struct {
	Note        string `json:"note"`
	Client      string `json:"client"`
	Server      string `json:"server"`
	ClientPrime string `json:"clientPrime"`
	ServerPrime string `json:"serverPrime"`
}

// deltaCase carries each ops list as a StoredDelta hex; the TS decodes it and
// takes .ops. (There is no bare ops-list codec entry point; StoredDelta wraps one.)
type deltaCase struct {
	Note        string `json:"note"`
	Client      string `json:"client"`
	Server      string `json:"server"`
	ClientPrime string `json:"clientPrime"`
	ServerPrime string `json:"serverPrime"`
}

type codecCase struct {
	Note string `json:"note"`
	Kind string `json:"kind"` // "docOp" | "clientDelta" | "storedDelta"
	Hex  string `json:"hex"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genfixtures:", err)
		os.Exit(1)
	}
}

func run() error {
	f := fixtures{}

	// --- helpers ---
	doc := func(comps ...op.Component) op.DocOp { return op.NewDocOp(comps) }
	encDoc := func(d op.DocOp) string { return hex.EncodeToString(codec.EncodeDocOp(d)) }
	alice := mustPID("alice@example.com")
	bob := mustPID("bob@example.com")
	attrs := func(kv ...string) op.Attributes {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		a, err := op.NewAttributes(m)
		if err != nil {
			panic(err)
		}
		return a
	}

	// --- compose: out = Compose(a, b) (b applies over the document a) ---
	composeInputs := []struct {
		note string
		a, b op.DocOp
	}{
		{"append char to text", doc(op.Characters{Text: "hello"}), doc(op.Retain{Count: 5}, op.Characters{Text: "!"})},
		{"insert char at start", doc(op.Characters{Text: "world"}), doc(op.Characters{Text: "_"}, op.Retain{Count: 5})},
		{"delete middle char", doc(op.Characters{Text: "abc"}), doc(op.Retain{Count: 1}, op.DeleteCharacters{Text: "b"}, op.Retain{Count: 1})},
		{"wrap text in element", doc(op.Characters{Text: "x"}),
			doc(op.ElementStart{Type: "line", Attributes: attrs()}, op.Retain{Count: 1}, op.ElementEnd{})},
		{"element with attrs then set attr", doc(op.ElementStart{Type: "p", Attributes: attrs("a", "1")}, op.ElementEnd{}),
			doc(op.ReplaceAttributes{OldAttributes: attrs("a", "1"), NewAttributes: attrs("a", "2")}, op.Retain{Count: 1})},
	}
	for _, c := range composeInputs {
		out, err := op.Compose(c.a, c.b)
		if err != nil {
			return fmt.Errorf("compose %q: %w", c.note, err)
		}
		f.Compose = append(f.Compose, composeCase{c.note, encDoc(c.a), encDoc(c.b), encDoc(out)})
	}

	// --- transform: (client', server') = Transform(client, server) over a common doc length ---
	transformInputs := []struct {
		note           string
		client, server op.DocOp
	}{
		{"concurrent inserts at opposite ends (len 2)",
			doc(op.Characters{Text: "A"}, op.Retain{Count: 2}),
			doc(op.Retain{Count: 2}, op.Characters{Text: "B"})},
		{"both insert at start (len 3)",
			doc(op.Characters{Text: "X"}, op.Retain{Count: 3}),
			doc(op.Characters{Text: "Y"}, op.Retain{Count: 3})},
		{"insert vs delete overlap (len 4)",
			doc(op.Retain{Count: 2}, op.Characters{Text: "Z"}, op.Retain{Count: 2}),
			doc(op.Retain{Count: 1}, op.DeleteCharacters{Text: "bc"}, op.Retain{Count: 1})},
		{"delete vs delete same region (len 3)",
			doc(op.DeleteCharacters{Text: "ab"}, op.Retain{Count: 1}),
			doc(op.Retain{Count: 1}, op.DeleteCharacters{Text: "bc"})},
		{"retain only vs insert (len 2)",
			doc(op.Retain{Count: 2}),
			doc(op.Retain{Count: 1}, op.Characters{Text: "M"}, op.Retain{Count: 1})},
	}
	for _, c := range transformInputs {
		cp, sp, err := op.Transform(c.client, c.server)
		if err != nil {
			return fmt.Errorf("transform %q: %w", c.note, err)
		}
		f.Transform = append(f.Transform, transformCase{c.note, encDoc(c.client), encDoc(c.server), encDoc(cp), encDoc(sp)})
	}

	// --- wavelet-level delta transform via cc.TransformOps ---
	ctx := waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}
	bctx := waveop.Context{Creator: bob, Timestamp: 1000, VersionIncrement: 1}
	blip := func(c waveop.Context, blipID string, content op.DocOp) waveop.Operation {
		return waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: content}}
	}
	deltaInputs := []struct {
		note           string
		client, server []waveop.Operation
	}{
		{"concurrent edits to same blip (content len 2)",
			[]waveop.Operation{blip(ctx, "b", doc(op.Characters{Text: "A"}, op.Retain{Count: 2}))},
			[]waveop.Operation{blip(bctx, "b", doc(op.Retain{Count: 2}, op.Characters{Text: "B"}))}},
		{"edits to different blips (independent)",
			[]waveop.Operation{blip(ctx, "b1", doc(op.Characters{Text: "P"}, op.Retain{Count: 1}))},
			[]waveop.Operation{blip(bctx, "b2", doc(op.Retain{Count: 1}, op.Characters{Text: "Q"}))}},
		{"add-participant vs blip edit",
			[]waveop.Operation{waveop.AddParticipant{Ctx: ctx, Participant: bob}},
			[]waveop.Operation{blip(bctx, "b", doc(op.Retain{Count: 1}, op.Characters{Text: "C"}))}},
		{"concurrent add of same participant",
			[]waveop.Operation{waveop.AddParticipant{Ctx: ctx, Participant: bob}},
			[]waveop.Operation{waveop.AddParticipant{Ctx: bctx, Participant: bob}}},
	}
	for _, c := range deltaInputs {
		cp, sp, err := cc.TransformOps(c.client, c.server)
		if err != nil {
			return fmt.Errorf("delta transform %q: %w", c.note, err)
		}
		f.DeltaTransform = append(f.DeltaTransform, deltaCase{
			c.note, encOps(alice, c.client), encOps(bob, c.server), encOps(alice, cp), encOps(bob, sp),
		})
	}

	// --- codec round-trip vectors (decode → structural check on the TS side) ---
	cdHV := version.NewHashedVersion(3, []byte("targethash----------"))
	clientDelta := codec.EncodeClientDelta(codec.ClientDelta{
		Author: alice, TargetVersion: cdHV, Nonce: "sess.7",
		Ops: []waveop.Operation{
			blip(ctx, "b", doc(op.Retain{Count: 2}, op.Characters{Text: "!"})),
			waveop.AddParticipant{Ctx: ctx, Participant: bob},
		},
	})
	f.Codec = append(f.Codec,
		codecCase{"docop with element + attrs + annotation", "docOp", encDoc(doc(
			op.ElementStart{Type: "line", Attributes: attrs("t", "h1")},
			op.AnnotationBoundary{Boundary: mustBoundary(nil, []op.AnnotationChange{{Key: "style/font", OldValue: nil, NewValue: ptr("bold")}})},
			op.Characters{Text: "Hi"},
			op.AnnotationBoundary{Boundary: mustBoundary([]string{"style/font"}, nil)},
			op.ElementEnd{},
		))},
		codecCase{"client delta with blip + addParticipant", "clientDelta", hex.EncodeToString(clientDelta)},
		codecCase{"stored delta", "storedDelta", encOps(alice, []waveop.Operation{
			blip(ctx, "b", doc(op.Characters{Text: "seed"})),
		})},
	)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(f)
}

// encOps encodes an ops list as a StoredDelta hex (its resulting version/hash are
// placeholders; the TS reads only .ops). The op count drives the version so the
// encoding is self-consistent.
func encOps(author id.ParticipantID, ops []waveop.Operation) string {
	rv := version.NewHashedVersion(uint64(len(ops)), []byte("fixturehash---------"))
	return hex.EncodeToString(codec.EncodeStoredDelta(codec.StoredDelta{
		Author: author, ResultingVersion: rv, Timestamp: 1000, Ops: ops, Nonce: "",
	}))
}

func mustPID(addr string) id.ParticipantID {
	p, err := id.NewParticipantID(addr)
	if err != nil {
		panic(err)
	}
	return p
}

func mustBoundary(ends []string, changes []op.AnnotationChange) op.AnnotationBoundaryMap {
	m, err := op.NewAnnotationBoundaryMap(ends, changes)
	if err != nil {
		panic(err)
	}
	return m
}

func ptr(s string) *string { return &s }
