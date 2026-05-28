package op_test

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sgrankin/wave/internal/op"
)

// sameDocument reports whether two DocInitializations represent the same
// document: the same item sequence (characters and elements) with the same
// EFFECTIVE annotation value at each item. This is the correct notion of OT
// convergence — unlike DocOp.Equal (a component-list comparison), it ignores
// operationally-irrelevant differences in annotation boundary representation,
// e.g. a redundant change re-asserting a key's current value (the "extraneous
// annotations" the transform may legitimately emit).
func sameDocument(a, b op.DocOp) bool {
	return docTokens(a) == docTokens(b)
}

// docTokens flattens a document into a string: one line per item (each rune of a
// character run is its own item), tagged with the effective annotation snapshot
// at that position. Only NewValue determines the resulting annotation state, so
// old values are ignored.
func docTokens(doc op.DocOp) string {
	cur := map[string]*string{} // current annotation values; absent => unannotated
	var sb strings.Builder
	for _, c := range doc.Components() {
		switch v := c.(type) {
		case op.AnnotationBoundary:
			for _, k := range v.Boundary.EndKeys() {
				delete(cur, k)
			}
			for _, ch := range v.Boundary.Changes() {
				if ch.NewValue == nil {
					delete(cur, ch.Key)
				} else {
					cur[ch.Key] = ch.NewValue
				}
			}
		case op.Characters:
			snap := annSnapshot(cur)
			for _, r := range v.Text {
				fmt.Fprintf(&sb, "char %q %s\n", string(r), snap)
			}
		case op.ElementStart:
			fmt.Fprintf(&sb, "es %s %s | %s\n", v.Type, attrSnapshot(v.Attributes), annSnapshot(cur))
		case op.ElementEnd:
			fmt.Fprintf(&sb, "ee %s\n", annSnapshot(cur))
		}
	}
	return sb.String()
}

func annSnapshot(cur map[string]*string) string {
	if len(cur) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(cur))
	for k := range cur {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%s", k, derefOr(cur[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func attrSnapshot(a op.Attributes) string {
	all := a.All()
	parts := make([]string, len(all))
	for i, at := range all {
		parts[i] = at.Name + "=" + at.Value
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func derefOr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
