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
	"math/rand"
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

	// --- seeded property-based random vectors ---
	// Fixed seed ensures deterministic, reproducible output across runs.
	rng := rand.New(rand.NewSource(1))

	// compose: ~150 random cases
	const randComposeCount = 150
	for i := 0; i < randComposeCount; i++ {
		d := randomDoc(rng)
		o := randomOp(rng, d)
		out, err := op.Compose(d, o)
		if err != nil {
			// randomOp guarantees validity via construction + retry; this is a safety net.
			i--
			continue
		}
		f.Compose = append(f.Compose, composeCase{
			Note: fmt.Sprintf("random:%d", i),
			A:    encDoc(d),
			B:    encDoc(o),
			Out:  encDoc(out),
		})
	}

	// transform: ~150 random cases — two INDEPENDENT ops over the SAME doc
	const randTransformCount = 150
	for i := 0; i < randTransformCount; {
		d := randomDoc(rng)
		client := randomOp(rng, d)
		server := randomOp(rng, d)
		cp, sp, err := op.Transform(client, server)
		if err != nil {
			// some concurrent op pairs are legitimately incompatible; skip silently
			continue
		}
		f.Transform = append(f.Transform, transformCase{
			Note:        fmt.Sprintf("random:%d", i),
			Client:      encDoc(client),
			Server:      encDoc(server),
			ClientPrime: encDoc(cp),
			ServerPrime: encDoc(sp),
		})
		i++
	}

	// delta transform: ~20 random cases wrapping random DocOps as blip ops
	const randDeltaCount = 20
	blipIDs := []string{"b0", "b1", "b2"}
	for i := 0; i < randDeltaCount; {
		d := randomDoc(rng)
		clientOp := randomOp(rng, d)
		serverOp := randomOp(rng, d)

		bID := blipIDs[rng.Intn(len(blipIDs))]
		cOps := []waveop.Operation{blip(ctx, bID, clientOp)}
		sOps := []waveop.Operation{blip(bctx, bID, serverOp)}

		cp, sp, err := cc.TransformOps(cOps, sOps)
		if err != nil {
			continue
		}
		f.DeltaTransform = append(f.DeltaTransform, deltaCase{
			Note:        fmt.Sprintf("random:%d", i),
			Client:      encOps(alice, cOps),
			Server:      encOps(bob, sOps),
			ClientPrime: encOps(alice, cp),
			ServerPrime: encOps(bob, sp),
		})
		i++
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(f)
}

// fuzzAlphabet is the set of runes used in random text generation. Includes
// ASCII and multi-byte runes to exercise UTF-8 character counting in TS.
var fuzzAlphabet = []rune("abcXY😀é")

// randText returns a random string of 1..maxRunes runes from fuzzAlphabet.
// Returns "" only when maxRunes <= 0.
func randText(rng *rand.Rand, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	n := 1 + rng.Intn(maxRunes)
	r := make([]rune, n)
	for i := range r {
		r[i] = fuzzAlphabet[rng.Intn(len(fuzzAlphabet))]
	}
	return string(r)
}

// randomDoc builds a valid DocInitialization (insertion-only DocOp) of
// moderate length, mixing Characters, ElementStart/End, and occasional
// AnnotationBoundary components. The document is always non-empty.
func randomDoc(rng *rand.Rand) op.DocOp {
	var comps []op.Component
	annKey := "" // tracks any currently-open annotation

	maybeAnnotation := func() {
		if annKey == "" && rng.Intn(4) == 0 {
			key := []string{"style/bold", "style/italic", "link/auto"}[rng.Intn(3)]
			val := "true"
			m, _ := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: key, NewValue: &val}})
			comps = append(comps, op.AnnotationBoundary{Boundary: m})
			annKey = key
		} else if annKey != "" && rng.Intn(3) == 0 {
			m, _ := op.NewAnnotationBoundaryMap([]string{annKey}, nil)
			comps = append(comps, op.AnnotationBoundary{Boundary: m})
			annKey = ""
		}
	}
	closeAnnotation := func() {
		if annKey != "" {
			m, _ := op.NewAnnotationBoundaryMap([]string{annKey}, nil)
			comps = append(comps, op.AnnotationBoundary{Boundary: m})
			annKey = ""
		}
	}

	// Emit 2-5 top-level items (characters or elements).
	n := 2 + rng.Intn(4)
	for i := 0; i < n; i++ {
		switch rng.Intn(3) {
		case 0: // element with optional character children
			closeAnnotation()
			elemType := []string{"p", "line", "li"}[rng.Intn(3)]
			var attrMap map[string]string
			if rng.Intn(2) == 0 {
				attrMap = map[string]string{"a": []string{"0", "1"}[rng.Intn(2)]}
			}
			a, _ := op.NewAttributes(attrMap)
			comps = append(comps, op.ElementStart{Type: elemType, Attributes: a})
			// 0-2 character children inside the element
			nc := rng.Intn(3)
			for j := 0; j < nc; j++ {
				maybeAnnotation()
				comps = append(comps, op.Characters{Text: randText(rng, 2)})
			}
			closeAnnotation()
			comps = append(comps, op.ElementEnd{})
		default: // characters (cases 1 and 2)
			maybeAnnotation()
			comps = append(comps, op.Characters{Text: randText(rng, 3)})
		}
	}
	closeAnnotation()

	return op.NewDocOp(comps)
}

// docItem represents one traversable item in a document for op generation.
// Characters are broken into individual runes; element tags are single items.
type docItem struct {
	kind    docItemKind
	r       rune              // kindChar: the character at this position
	elem    string            // kindElemStart: element type
	attrMap map[string]string // kindElemStart: element attributes
}

type docItemKind int

const (
	kindChar      docItemKind = iota
	kindElemStart             // element open tag; counts as 1 item
	kindElemEnd               // element close tag; counts as 1 item
)

// decomposeDoc walks d's components and returns a flat slice of items, one per
// document position. AnnotationBoundary components are zero-width and skipped.
func decomposeDoc(d op.DocOp) []docItem {
	var items []docItem
	for _, c := range d.Components() {
		switch v := c.(type) {
		case op.Characters:
			for _, r := range v.Text {
				items = append(items, docItem{kind: kindChar, r: r})
			}
		case op.ElementStart:
			attrMap := map[string]string{}
			for _, attr := range v.Attributes.All() {
				attrMap[attr.Name] = attr.Value
			}
			items = append(items, docItem{kind: kindElemStart, elem: v.Type, attrMap: attrMap})
		case op.ElementEnd:
			items = append(items, docItem{kind: kindElemEnd})
		case op.AnnotationBoundary:
			// zero-width — no document position
		}
	}
	return items
}

// randomOp generates a valid mutating DocOp over the given document by walking
// its items: for each character run it randomly retains or deletes, and
// interspersed insertions are added. Element tags are always retained to avoid
// unbalanced delete-element pairs. Validity is confirmed by Compose; on failure
// it falls back to a pure retain-all identity op.
func randomOp(rng *rand.Rand, d op.DocOp) op.DocOp {
	const maxRetries = 20
	for attempt := 0; attempt < maxRetries; attempt++ {
		o, err := tryRandomOp(rng, d)
		if err == nil {
			return o
		}
	}
	// Last resort: identity (retain all).
	n := d.DocumentLength()
	if n == 0 {
		return op.NewDocOp(nil)
	}
	return op.NewDocOp([]op.Component{op.Retain{Count: n}})
}

func tryRandomOp(rng *rand.Rand, d op.DocOp) (op.DocOp, error) {
	items := decomposeDoc(d)
	var comps []op.Component

	annKey := "" // tracks any currently-open annotation in the generated op
	maybeInsertAnnotation := func() {
		if annKey == "" && rng.Intn(5) == 0 {
			key := []string{"style/bold", "link/auto"}[rng.Intn(2)]
			val := "x"
			m, _ := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: key, NewValue: &val}})
			comps = append(comps, op.AnnotationBoundary{Boundary: m})
			annKey = key
		} else if annKey != "" && rng.Intn(3) == 0 {
			m, _ := op.NewAnnotationBoundaryMap([]string{annKey}, nil)
			comps = append(comps, op.AnnotationBoundary{Boundary: m})
			annKey = ""
		}
	}
	closeAnnotation := func() {
		if annKey != "" {
			m, _ := op.NewAnnotationBoundaryMap([]string{annKey}, nil)
			comps = append(comps, op.AnnotationBoundary{Boundary: m})
			annKey = ""
		}
	}
	maybeInsertChars := func() {
		if rng.Intn(4) == 0 {
			maybeInsertAnnotation()
			comps = append(comps, op.Characters{Text: randText(rng, 2)})
		}
	}

	i := 0
	for i < len(items) {
		maybeInsertChars()
		item := items[i]

		switch item.kind {
		case kindChar:
			// Consume a contiguous character run of length k (1..runLen).
			j := i
			for j < len(items) && items[j].kind == kindChar {
				j++
			}
			runLen := j - i
			k := 1 + rng.Intn(runLen)
			if rng.Intn(2) == 0 {
				comps = append(comps, op.Retain{Count: k})
			} else {
				rs := make([]rune, k)
				for m := 0; m < k; m++ {
					rs[m] = items[i+m].r
				}
				closeAnnotation()
				comps = append(comps, op.DeleteCharacters{Text: string(rs)})
			}
			i += k

		case kindElemStart, kindElemEnd:
			// Always retain element tags to keep open/close balanced.
			comps = append(comps, op.Retain{Count: 1})
			i++
		}
	}
	maybeInsertChars()
	closeAnnotation()

	o := op.NewDocOp(comps)
	// Compose verifies the op covers the document exactly; skip if it doesn't.
	_, err := op.Compose(d, o)
	return o, err
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
