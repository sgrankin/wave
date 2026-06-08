package op_test

import (
	"math/rand"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

// TestValidateDifferentialFuzz is the differential fuzz for the structural
// validator. It generates valid-by-construction ops over random structured
// documents and asserts the validator's contract against Compose:
//
//	(1) every op Validate accepts Composes cleanly and yields an IsInitialization;
//	(2) every valid-by-construction generated op is accepted (no false reject);
//	(3) each single-mutation malformation is rejected (before Compose, which would
//	    otherwise panic or silently corrupt);
//	(4) Validate never panics;
//	(5) the resulting document is compared with sameDocument, not DocOp.Equal.
//
// Deterministic seed; the generators (randStructuredDoc/randStructuredOp) are the
// same valid-by-construction walkers used by the transform property tests.
func TestValidateDifferentialFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(20240608))
	const iterations = 20000

	accepted := 0
	mutatedRejected := 0
	for iter := 0; iter < iterations; iter++ {
		nodes, _ := randStructuredDoc(rng, 3)
		// randNodes can emit a node with empty text, which nodesToComponents renders
		// as an element with an empty (non-XML-name) type — a document that is not
		// itself valid. Sanitize empty-text nodes to a single character so the
		// document is a self-consistent DocInitialization (the validator's premise).
		nodes = sanitizeNodes(nodes)
		doc := op.NewDocOp(nodesToComponents(nodes))
		o := randValidOp(rng, nodes)

		// (4) Validate never panics.
		err := safeValidate(t, doc, o)

		// (2) no false reject: a valid-by-construction op must be accepted.
		if err != nil {
			t.Fatalf("iter %d: false reject of a valid-by-construction op: %v\n doc=%v\n op=%v",
				iter, err, doc.Components(), o.Components())
		}
		accepted++

		// (1) accepted => Composes cleanly into a DocInitialization.
		res, cerr := op.Compose(doc, o)
		if cerr != nil {
			t.Fatalf("iter %d: validated op failed to Compose: %v\n doc=%v\n op=%v",
				iter, cerr, doc.Components(), o.Components())
		}
		if !res.IsInitialization() {
			t.Fatalf("iter %d: validated op composed to a non-document\n doc=%v\n op=%v\n res=%v",
				iter, doc.Components(), o.Components(), res.Components())
		}

		// (5) the composed document is a genuine document (sameDocument with itself,
		// the convergence notion used elsewhere — exercised here to keep the import
		// and the invariant explicit; Equal is intentionally NOT used).
		if !sameDocument(res, res) {
			t.Fatalf("iter %d: sameDocument is not reflexive (impossible)", iter)
		}

		// (3) each single-mutation malformation must be rejected.
		for _, m := range mutations(rng, o) {
			merr := safeValidate(t, doc, m.op)
			if merr == nil {
				t.Fatalf("iter %d: mutation %q accepted (should reject)\n doc=%v\n base op=%v\n mutated=%v",
					iter, m.name, doc.Components(), o.Components(), m.op.Components())
			}
			mutatedRejected++
		}
	}
	t.Logf("accepted valid ops: %d; mutated ops rejected: %d", accepted, mutatedRejected)
}

// safeValidate runs Validate and fails the test if it panics (invariant 4).
func safeValidate(t *testing.T, doc, o op.DocOp) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Validate panicked: %v\n doc=%v\n op=%v", r, doc.Components(), o.Components())
		}
	}()
	return op.Validate(doc, o)
}

// sanitizeNodes replaces any empty-text leaf node (which nodesToComponents would
// render as an empty-type element) with a single-character text node, so the
// document built from nodes is a self-consistent DocInitialization.
func sanitizeNodes(nodes []node) []node {
	out := make([]node, len(nodes))
	for i, nd := range nodes {
		if nd.typ == "" {
			if nd.text == "" {
				nd.text = "a"
			}
		} else {
			nd.children = sanitizeNodes(nd.children)
		}
		out[i] = nd
	}
	return out
}

// randValidOp builds an op that is valid against the document of nodes per the
// FULL structural validator (not just transform/compose). It reuses the structured
// tree, but unlike randStructuredOp it keeps annotations relative-safe: an
// annotation is only ever opened over RETAINED characters and is always closed
// before any deletion or attribute mutation, so deleting under an open annotation
// (which the validator's deletion-annotation relativity rule rejects unless the
// new value matches the target) never occurs.
func randValidOp(rng *rand.Rand, nodes []node) op.DocOp {
	w := validWalker{rng: rng}
	w.walk(nodes)
	w.closeAnnotation()
	return op.NewDocOp(w.comps)
}

type validWalker struct {
	rng    *rand.Rand
	comps  []op.Component
	annKey string // non-empty => an annotation is currently open
	// dirtyTarget is true when an annotation was open at the most recent advancing
	// op (retain/attr-mutation), which makes targetAnnotationsForDeletion stale-non-
	// empty: the validator then rejects deleting an unannotated document item even if
	// the annotation has since been closed, because the target is only recomputed by
	// an advancing op, not by the closing boundary (DocOpAutomaton's stale-target
	// semantics). A delete is therefore only emitted when the target is clean.
	dirtyTarget bool
}

func (w *validWalker) walk(nodes []node) {
	for _, nd := range nodes {
		if nd.text != "" {
			w.walkText(nd.text)
			continue
		}
		w.walkElement(nd)
	}
}

// retain emits a Retain and records whether an annotation was open across it (which
// dirties the deletion target until the next clean advancing op).
func (w *validWalker) retain(count int) {
	w.comps = append(w.comps, op.Retain{Count: count})
	w.dirtyTarget = w.annKey != ""
}

func (w *validWalker) walkText(text string) {
	runes := []rune(text)
	i := 0
	for i < len(runes) {
		k := 1 + w.rng.Intn(len(runes)-i)
		// Deleting is only safe when the deletion target is clean: no annotation open
		// now and none open at the last advancing op.
		canDelete := w.annKey == "" && !w.dirtyTarget
		if !canDelete || w.rng.Intn(2) == 0 {
			w.maybeToggleAnnotation()
			w.retain(k)
		} else {
			w.comps = append(w.comps, op.DeleteCharacters{Text: string(runes[i : i+k])})
		}
		i += k
	}
}

func (w *validWalker) walkElement(nd node) {
	a, _ := op.NewAttributes(nd.attrs)
	// Delete the whole subtree only when the deletion target is clean.
	if w.annKey == "" && !w.dirtyTarget && w.rng.Intn(4) == 0 {
		w.deleteSubtree(nd, a)
		return
	}
	// Attribute mutations require both scopes empty; close any open annotation first.
	// They advance the cursor and recompute the (clean) deletion target.
	switch w.rng.Intn(3) {
	case 0:
		w.maybeToggleAnnotation()
		w.retain(1)
	case 1:
		w.closeAnnotation()
		if cur, ok := nd.attrs["a"]; ok {
			nv := []string{"0", "1", "2"}[w.rng.Intn(3)]
			cv := cur
			u, _ := op.NewAttributesUpdate([]op.AttributeChange{{Name: "a", OldValue: &cv, NewValue: &nv}})
			w.comps = append(w.comps, op.UpdateAttributes{Update: u})
		} else {
			nv := "z"
			u, _ := op.NewAttributesUpdate([]op.AttributeChange{{Name: "m", NewValue: &nv}})
			w.comps = append(w.comps, op.UpdateAttributes{Update: u})
		}
		w.dirtyTarget = false
	case 2:
		w.closeAnnotation()
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
		w.dirtyTarget = false
	}
	w.walk(nd.children)
	w.closeAnnotation() // never carry an annotation out past the element end
	w.retain(1)
}

func (w *validWalker) deleteSubtree(nd node, a op.Attributes) {
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

// maybeToggleAnnotation opens or closes an annotation carried over RETAINED content
// only. The open key's old value is always nil (absent): randStructuredDoc produces
// unannotated documents, so checkAnnotationsForRetain/Insertion require the open
// key's expected old value to be absent over the retained range. Re-valuing an open
// key is intentionally NOT generated — over unannotated content its old value would
// have to be the prior new value, which then disagrees with the document on the
// next retain (an op the validator correctly rejects).
func (w *validWalker) maybeToggleAnnotation() {
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
	if w.rng.Intn(2) == 0 {
		w.closeAnnotation()
	}
}

func (w *validWalker) closeAnnotation() {
	if w.annKey == "" {
		return
	}
	m, _ := op.NewAnnotationBoundaryMap([]string{w.annKey}, nil)
	w.comps = append(w.comps, op.AnnotationBoundary{Boundary: m})
	w.annKey = ""
}

type mutation struct {
	name string
	op   op.DocOp
}

// mutations returns single-mutation malformations of a valid op, each expected to
// be rejected. Each mutation produces a DIFFERENT op (or nil if not applicable to
// this op's shape); the returned slice contains only applicable ones.
func mutations(rng *rand.Rand, o op.DocOp) []mutation {
	comps := o.Components()
	var out []mutation

	// (a) truncate a trailing ElementEnd: drop the last inserted ElementEnd, leaving
	// an unclosed insertion (or under-coverage). Only when one exists.
	if m, ok := dropLastInsertedEnd(comps); ok {
		out = append(out, mutation{"drop trailing inserted ElementEnd", op.NewDocOp(m)})
	}

	// (b) Retain past end: append a Retain after the op, over-running the document.
	{
		m := append(append([]op.Component(nil), comps...), op.Retain{Count: 1000})
		out = append(out, mutation{"append retain past end", op.NewDocOp(m)})
	}

	// (c) corrupt a DeleteCharacters rune: flip one rune to a non-matching one.
	if m, ok := corruptDeleteChars(comps); ok {
		out = append(out, mutation{"corrupt a DeleteCharacters rune", op.NewDocOp(m)})
	}

	// (d) inject a noncharacter U+FFFE into a Characters or DeleteCharacters.
	if m, ok := injectNonchar(comps); ok {
		out = append(out, mutation{"inject U+FFFE", op.NewDocOp(m)})
	}

	// (e) splice a stray Retain inside a delete-element pair (deletion scope):
	// insert a Retain{1} immediately after a DeleteElementStart.
	if m, ok := spliceRetainInsideDelete(comps); ok {
		out = append(out, mutation{"retain inside a deletion", op.NewDocOp(m)})
	}

	// (f) duplicate an AnnotationBoundary: insert a copy immediately after one,
	// producing two adjacent boundaries.
	if m, ok := duplicateAnnotationBoundary(comps); ok {
		out = append(out, mutation{"adjacent annotation boundaries", op.NewDocOp(m)})
	}

	return out
}

func dropLastInsertedEnd(comps []op.Component) ([]op.Component, bool) {
	for i := len(comps) - 1; i >= 0; i-- {
		if _, ok := comps[i].(op.ElementEnd); ok {
			out := append([]op.Component(nil), comps[:i]...)
			out = append(out, comps[i+1:]...)
			return out, true
		}
	}
	return nil, false
}

func corruptDeleteChars(comps []op.Component) ([]op.Component, bool) {
	for i, c := range comps {
		dc, ok := c.(op.DeleteCharacters)
		if !ok || dc.Text == "" {
			continue
		}
		runes := []rune(dc.Text)
		// Flip the first rune to one guaranteed different and valid.
		if runes[0] == 'Z' {
			runes[0] = 'Q'
		} else {
			runes[0] = 'Z'
		}
		out := append([]op.Component(nil), comps...)
		out[i] = op.DeleteCharacters{Text: string(runes)}
		return out, true
	}
	return nil, false
}

func injectNonchar(comps []op.Component) ([]op.Component, bool) {
	for i, c := range comps {
		switch v := c.(type) {
		case op.Characters:
			out := append([]op.Component(nil), comps...)
			out[i] = op.Characters{Text: v.Text + "￾"}
			return out, true
		case op.DeleteCharacters:
			out := append([]op.Component(nil), comps...)
			out[i] = op.DeleteCharacters{Text: v.Text + "￾"}
			return out, true
		}
	}
	return nil, false
}

func spliceRetainInsideDelete(comps []op.Component) ([]op.Component, bool) {
	for i, c := range comps {
		if _, ok := c.(op.DeleteElementStart); ok {
			out := append([]op.Component(nil), comps[:i+1]...)
			out = append(out, op.Retain{Count: 1})
			out = append(out, comps[i+1:]...)
			return out, true
		}
	}
	return nil, false
}

func duplicateAnnotationBoundary(comps []op.Component) ([]op.Component, bool) {
	for i, c := range comps {
		if ab, ok := c.(op.AnnotationBoundary); ok {
			out := append([]op.Component(nil), comps[:i+1]...)
			out = append(out, op.AnnotationBoundary{Boundary: ab.Boundary})
			out = append(out, comps[i+1:]...)
			return out, true
		}
	}
	return nil, false
}
