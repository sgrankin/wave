package op_test

import (
	"math/rand"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

var fuzzAlphabet = []rune("abcXY😀é")

func randText(rng *rand.Rand, maxRunes int) string {
	n := rng.Intn(maxRunes + 1)
	r := make([]rune, n)
	for i := range r {
		r[i] = fuzzAlphabet[rng.Intn(len(fuzzAlphabet))]
	}
	return string(r)
}

// randomCharOp builds a random operation valid against a character-only document
// of the given text: it walks every rune choosing retain or delete, with random
// insertions interspersed. Such ops are always mutually transformable.
func randomCharOp(rng *rand.Rand, text string) op.DocOp {
	runes := []rune(text)
	var comps []op.Component
	maybeInsert := func() {
		if rng.Intn(3) == 0 {
			if s := randText(rng, 2); s != "" {
				comps = append(comps, op.Characters{Text: s})
			}
		}
	}
	i := 0
	for i < len(runes) {
		maybeInsert()
		k := 1 + rng.Intn(len(runes)-i)
		if rng.Intn(2) == 0 {
			comps = append(comps, op.Retain{Count: k})
		} else {
			comps = append(comps, op.DeleteCharacters{Text: string(runes[i : i+k])})
		}
		i += k
	}
	maybeInsert()
	return op.NewDocOp(comps)
}

// TestTransformTP1Property fuzzes the full Transform on random character ops and
// asserts TP1 convergence on every pair. Deterministic seed for reproducibility.
func TestTransformTP1Property(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const iterations = 5000
	for iter := 0; iter < iterations; iter++ {
		text := randText(rng, 6)
		if text == "" {
			text = "a"
		}
		doc := op.NewDocOp([]op.Component{op.Characters{Text: text}})
		client := randomCharOp(rng, text)
		server := randomCharOp(rng, text)

		cPrime, sPrime, err := op.Transform(client, server)
		if err != nil {
			t.Fatalf("iter %d: Transform error: %v\n doc=%q\n client=%v\n server=%v",
				iter, err, text, client.Components(), server.Components())
		}

		afterClient, err := op.Apply(doc, client)
		if err != nil {
			t.Fatalf("iter %d: apply client: %v", iter, err)
		}
		afterServer, err := op.Apply(doc, server)
		if err != nil {
			t.Fatalf("iter %d: apply server: %v", iter, err)
		}
		left, err := op.Apply(afterClient, sPrime)
		if err != nil {
			t.Fatalf("iter %d: apply server' (doc=%q client=%v server'=%v): %v",
				iter, text, client.Components(), sPrime.Components(), err)
		}
		right, err := op.Apply(afterServer, cPrime)
		if err != nil {
			t.Fatalf("iter %d: apply client' (doc=%q server=%v client'=%v): %v",
				iter, text, server.Components(), cPrime.Components(), err)
		}
		if !sameDocument(left, right) {
			t.Fatalf("iter %d: TP1 violated\n doc=%q\n client=%v\n server=%v\n left=%v\n right=%v",
				iter, text, client.Components(), server.Components(), left.Components(), right.Components())
		}
	}
}

// TestTransformAgainstIdentityProperty checks that transforming a random op
// against the identity (retain-all) leaves the op's effect intact: applying the
// op then the transformed identity equals applying the op alone.
func TestTransformAgainstIdentityProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for iter := 0; iter < 2000; iter++ {
		text := randText(rng, 6)
		if text == "" {
			text = "a"
		}
		doc := op.NewDocOp([]op.Component{op.Characters{Text: text}})
		client := randomCharOp(rng, text)
		identity := op.NewDocOp([]op.Component{op.Retain{Count: len([]rune(text))}})

		cPrime, sPrime, err := op.Transform(client, identity)
		if err != nil {
			t.Fatalf("iter %d: Transform: %v", iter, err)
		}
		afterClient, _ := op.Apply(doc, client)
		got, err := op.Apply(afterClient, sPrime)
		if err != nil {
			t.Fatalf("iter %d: apply identity': %v", iter, err)
		}
		if !got.Equal(afterClient) {
			t.Fatalf("iter %d: transforming against identity changed the result\n got=%v want=%v",
				iter, got.Components(), afterClient.Components())
		}
		// The transformed client should still reproduce the client's effect.
		afterIdentity, _ := op.Apply(doc, identity)
		viaServer, err := op.Apply(afterIdentity, cPrime)
		if err != nil {
			t.Fatalf("iter %d: apply client': %v", iter, err)
		}
		if !viaServer.Equal(afterClient) {
			t.Fatalf("iter %d: client' diverged from client\n got=%v want=%v",
				iter, viaServer.Components(), afterClient.Components())
		}
	}
}

// --- structured-document fuzzing (review finding 3) ---
//
// We generate a document as a tree, then generate valid ops by walking that
// tree recursively. Deletion is decided per-element-subtree (delete the whole
// element start+children+end, or keep it), which keeps deleteElementStart/
// deleteElementEnd balanced by construction. Within a kept element we recurse,
// retaining/deleting character runs, mutating element-start attributes, and
// opening/closing annotations over character ranges.

// node is a document tree node: a character run (text != "") or an element.
type node struct {
	text     string
	typ      string
	attrs    map[string]string
	children []node
}

func randStructuredDoc(rng *rand.Rand, maxDepth int) ([]node, op.DocOp) {
	nodes := randNodes(rng, maxDepth)
	return nodes, op.NewDocOp(nodesToComponents(nodes))
}

func randNodes(rng *rand.Rand, depth int) []node {
	n := rng.Intn(3)
	var out []node
	for i := 0; i < n; i++ {
		if depth > 0 && rng.Intn(2) == 0 {
			out = append(out, node{
				typ:      []string{"p", "x", "li"}[rng.Intn(3)],
				attrs:    randAttrMap(rng),
				children: randNodes(rng, depth-1),
			})
		} else {
			out = append(out, node{text: randText(rng, 3)})
		}
	}
	if len(out) == 0 {
		out = append(out, node{text: "a"})
	}
	return out
}

func randAttrMap(rng *rand.Rand) map[string]string {
	m := map[string]string{}
	if rng.Intn(2) == 0 {
		m["a"] = []string{"0", "1"}[rng.Intn(2)]
	}
	if rng.Intn(2) == 0 {
		m["b"] = "0"
	}
	return m
}

func nodesToComponents(nodes []node) []op.Component {
	var comps []op.Component
	for _, nd := range nodes {
		if nd.text != "" {
			comps = append(comps, op.Characters{Text: nd.text})
			continue
		}
		a, _ := op.NewAttributes(nd.attrs)
		comps = append(comps, op.ElementStart{Type: nd.typ, Attributes: a})
		comps = append(comps, nodesToComponents(nd.children)...)
		comps = append(comps, op.ElementEnd{})
	}
	return comps
}

func randStructuredOp(rng *rand.Rand, nodes []node) op.DocOp {
	w := opWalker{rng: rng}
	w.walk(nodes)
	w.closeDanglingAnnotation()
	return op.NewDocOp(w.comps)
}

type opWalker struct {
	rng    *rand.Rand
	comps  []op.Component
	annKey string // non-empty => an annotation is currently open
}

func (w *opWalker) walk(nodes []node) {
	for _, nd := range nodes {
		if nd.text != "" {
			w.walkText(nd.text)
			continue
		}
		w.walkElement(nd)
	}
}

func (w *opWalker) walkText(text string) {
	w.maybeToggleAnnotation()
	runes := []rune(text)
	i := 0
	for i < len(runes) {
		k := 1 + w.rng.Intn(len(runes)-i)
		seg := string(runes[i : i+k])
		if w.rng.Intn(2) == 0 {
			w.comps = append(w.comps, op.Retain{Count: k})
		} else {
			w.comps = append(w.comps, op.DeleteCharacters{Text: seg})
		}
		i += k
	}
}

func (w *opWalker) walkElement(nd node) {
	a, _ := op.NewAttributes(nd.attrs)
	// 1/4 chance: delete the whole subtree (balanced by construction).
	if w.rng.Intn(4) == 0 {
		w.closeDanglingAnnotation()
		w.deleteSubtree(nd, a)
		return
	}
	switch w.rng.Intn(3) {
	case 0:
		w.comps = append(w.comps, op.Retain{Count: 1})
	case 1:
		if cur, ok := nd.attrs["a"]; ok {
			nv := []string{"0", "1", "2"}[w.rng.Intn(3)]
			cv := cur
			u, _ := op.NewAttributesUpdate([]op.AttributeChange{{Name: "a", OldValue: &cv, NewValue: &nv}})
			w.comps = append(w.comps, op.UpdateAttributes{Update: u})
		} else {
			nm := []string{"m", "n"}[w.rng.Intn(2)]
			nv := "z"
			u, _ := op.NewAttributesUpdate([]op.AttributeChange{{Name: nm, NewValue: &nv}})
			w.comps = append(w.comps, op.UpdateAttributes{Update: u})
		}
	case 2:
		newMap := map[string]string{}
		for k, v := range nd.attrs {
			newMap[k] = v
		}
		if w.rng.Intn(2) == 0 {
			newMap["a"] = "9"
		} else {
			delete(newMap, "b")
		}
		newA, _ := op.NewAttributes(newMap)
		w.comps = append(w.comps, op.ReplaceAttributes{OldAttributes: a, NewAttributes: newA})
	}
	w.walk(nd.children)
	w.closeDanglingAnnotation() // never carry an annotation out past the element end
	w.comps = append(w.comps, op.Retain{Count: 1})
}

// deleteSubtree emits a balanced delete of an entire element subtree.
func (w *opWalker) deleteSubtree(nd node, a op.Attributes) {
	w.comps = append(w.comps, op.DeleteElementStart{Type: nd.typ, Attributes: a})
	for _, c := range nd.children {
		if c.text != "" {
			w.comps = append(w.comps, op.DeleteCharacters{Text: c.text})
			continue
		}
		ca, _ := op.NewAttributes(c.attrs)
		w.deleteSubtree(c, ca)
	}
	w.comps = append(w.comps, op.DeleteElementEnd{})
}

func (w *opWalker) maybeToggleAnnotation() {
	if w.annKey == "" {
		if w.rng.Intn(2) == 0 {
			key := []string{"style/bold", "style/italic"}[w.rng.Intn(2)]
			val := []string{"true", "false", "x"}[w.rng.Intn(3)]
			m, _ := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: key, NewValue: &val}})
			w.comps = append(w.comps, op.AnnotationBoundary{Boundary: m})
			w.annKey = key
		}
		return
	}
	switch w.rng.Intn(3) {
	case 0: // change value (exercises the propagating non-nil branch)
		val := []string{"true", "false", "x"}[w.rng.Intn(3)]
		m, _ := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: w.annKey, NewValue: &val}})
		w.comps = append(w.comps, op.AnnotationBoundary{Boundary: m})
	case 1:
		w.closeDanglingAnnotation()
	}
}

func (w *opWalker) closeDanglingAnnotation() {
	if w.annKey == "" {
		return
	}
	m, _ := op.NewAnnotationBoundaryMap([]string{w.annKey}, nil)
	w.comps = append(w.comps, op.AnnotationBoundary{Boundary: m})
	w.annKey = ""
}

// TestTransformTP1StructuredProperty fuzzes Transform on random ops over
// structured documents (nested elements + attribute updates/replaces +
// annotations), asserting TP1 on every convergent pair. Pairs the transform
// reports as incompatible are skipped; a base-apply failure indicates a
// generator bug, a prime-apply or inequality indicates a transform bug.
func TestTransformTP1StructuredProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20240528))
	const iterations = 50000
	checked := 0
	for iter := 0; iter < iterations; iter++ {
		nodes, doc := randStructuredDoc(rng, 3)
		client := randStructuredOp(rng, nodes)
		server := randStructuredOp(rng, nodes)

		cPrime, sPrime, err := op.Transform(client, server)
		if err != nil {
			continue // legitimately incompatible concurrent ops
		}
		afterClient, err := op.Apply(doc, client)
		if err != nil {
			t.Fatalf("iter %d: apply client (generator produced invalid op?): %v\n client=%v",
				iter, err, client.Components())
		}
		afterServer, err := op.Apply(doc, server)
		if err != nil {
			t.Fatalf("iter %d: apply server (generator produced invalid op?): %v\n server=%v",
				iter, err, server.Components())
		}
		left, err := op.Apply(afterClient, sPrime)
		if err != nil {
			t.Fatalf("iter %d: apply server': %v\n client=%v\n server=%v\n sPrime=%v",
				iter, err, client.Components(), server.Components(), sPrime.Components())
		}
		right, err := op.Apply(afterServer, cPrime)
		if err != nil {
			t.Fatalf("iter %d: apply client': %v\n client=%v\n server=%v\n cPrime=%v",
				iter, err, client.Components(), server.Components(), cPrime.Components())
		}
		if !sameDocument(left, right) {
			t.Fatalf("iter %d: TP1 violated\n client=%v\n server=%v\n cPrime=%v\n sPrime=%v\n left=%v\n right=%v",
				iter, client.Components(), server.Components(),
				cPrime.Components(), sPrime.Components(), left.Components(), right.Components())
		}
		checked++
	}
	t.Logf("convergent structured pairs checked: %d / %d", checked, iterations)
}
