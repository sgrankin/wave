package conv

import (
	"fmt"
	"unicode/utf8"

	"github.com/sgrankin/wave/internal/op"
)

// StateDocumentID is the id of a wavelet's structured key/value STATE document — a
// shared, OT-native memory store for agents (and, later, clients), distinct from the
// prose blips and the conversation manifest. It reuses the DocOp/transform/snapshot
// machinery unchanged (it is just another document). See
// docs/architecture/11-agent-structured-state.md.
const StateDocumentID = "state"

const (
	tagState = "state"
	tagEntry = "e"
	attrKey  = "k"
	attrVal  = "v"
)

// State size caps — defensive bounds so a buggy/abusive writer cannot bloat the state
// document without limit. The set builder errors (emits no op) rather than exceed them.
const (
	MaxStateKeys      = 256
	MaxStateValueSize = 4096 // bytes, per value
)

// EmptyState returns the content (a DocInitialization) of a fresh, empty state
// document: <state></state>.
func EmptyState() op.DocOp {
	none := mustAttrs(nil)
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: tagState, Attributes: none},
		op.ElementEnd{},
	})
}

// ReadState projects a state document's content into a key→value map. Permissive:
// non-<e> elements and entries lacking a "k" attribute are ignored; if a duplicate key
// exists (only a concurrent first-set of the same key can produce that), the LATER
// entry wins (document order).
func ReadState(content op.DocOp) map[string]string {
	out := map[string]string{}
	for _, c := range content.Components() {
		es, ok := c.(op.ElementStart)
		if !ok || es.Type != tagEntry {
			continue
		}
		k, hasK := es.Attributes.Get(attrKey)
		if !hasK {
			continue
		}
		v, _ := es.Attributes.Get(attrVal)
		out[k] = v
	}
	return out
}

// SetStateValue returns the operation that sets key=value in the state document: it
// updates the <e k=key> entry's "v" in place when present, else inserts a new
// <e k=key v=value/> entry before the closing </state>. Apply it with
// op.Apply(state, result). It errors if value exceeds MaxStateValueSize, or if a new
// key would push the entry count past MaxStateKeys, or if the document is malformed.
func SetStateValue(state op.DocOp, key, value string) (op.DocOp, error) {
	if len(value) > MaxStateValueSize {
		return op.DocOp{}, fmt.Errorf("conv: state value for key %q exceeds %d bytes", key, MaxStateValueSize)
	}
	start, curVal, hasVal, found := stateEntry(state, key)
	if found {
		var oldP *string
		if hasVal {
			ov := curVal
			oldP = &ov
		}
		nv := value
		upd, err := op.NewAttributesUpdate([]op.AttributeChange{{Name: attrVal, OldValue: oldP, NewValue: &nv}})
		if err != nil {
			return op.DocOp{}, err
		}
		n := state.DocumentLength()
		return op.NewDocOp([]op.Component{
			op.Retain{Count: start},
			op.UpdateAttributes{Update: upd},
			op.Retain{Count: n - start - 1},
		}), nil
	}
	if countEntries(state) >= MaxStateKeys {
		return op.DocOp{}, fmt.Errorf("conv: state already has %d keys (max)", MaxStateKeys)
	}
	close, ok := elementCloseOffset(state, func(tag, id string) bool { return tag == tagState })
	if !ok {
		return op.DocOp{}, fmt.Errorf("conv: malformed state document (no <state>)")
	}
	// key/value are agent-controlled, so build the attributes with the erroring
	// constructor (NOT mustAttrs, which PANICS on invalid UTF-8 — a server-crash DoS).
	entryAttrs, err := op.NewAttributes(map[string]string{attrKey: key, attrVal: value})
	if err != nil {
		return op.DocOp{}, fmt.Errorf("conv: invalid state key/value: %w", err)
	}
	n := state.DocumentLength()
	return op.NewDocOp([]op.Component{
		op.Retain{Count: close},
		op.ElementStart{Type: tagEntry, Attributes: entryAttrs},
		op.ElementEnd{},
		op.Retain{Count: n - close},
	}), nil
}

// DeleteStateValue returns the operation that removes the <e k=key> entry from the
// state document (deleting its element, echoing its exact current attributes so compose
// accepts it). Apply with op.Apply(state, result). It errors if the key is absent.
func DeleteStateValue(state op.DocOp, key string) (op.DocOp, error) {
	start, curVal, hasVal, found := stateEntry(state, key)
	if !found {
		return op.DocOp{}, fmt.Errorf("conv: no state key %q", key)
	}
	attrs := map[string]string{attrKey: key}
	if hasVal {
		attrs[attrVal] = curVal
	}
	// key/curVal come from an already-stored (valid) entry, but use the erroring
	// constructor anyway for consistency with SetStateValue (never panic on a builder).
	echo, err := op.NewAttributes(attrs)
	if err != nil {
		return op.DocOp{}, fmt.Errorf("conv: invalid state key: %w", err)
	}
	n := state.DocumentLength()
	return op.NewDocOp([]op.Component{
		op.Retain{Count: start},
		op.DeleteElementStart{Type: tagEntry, Attributes: echo},
		op.DeleteElementEnd{},
		op.Retain{Count: n - start - 2},
	}), nil
}

// stateEntry locates the first <e k=key> entry: its ElementStart doc offset, its
// current "v" value (and whether "v" is present), and whether it was found.
func stateEntry(state op.DocOp, key string) (start int, curVal string, hasVal, found bool) {
	pos := 0
	for _, c := range state.Components() {
		switch c := c.(type) {
		case op.ElementStart:
			if !found && c.Type == tagEntry {
				if k, ok := c.Attributes.Get(attrKey); ok && k == key {
					start, found = pos, true
					curVal, hasVal = c.Attributes.Get(attrVal)
				}
			}
			pos++
		case op.ElementEnd:
			pos++
		case op.Characters:
			pos += utf8.RuneCountInString(c.Text)
		}
	}
	return
}

// countEntries returns the number of <e> entries in the state document.
func countEntries(state op.DocOp) int {
	n := 0
	for _, c := range state.Components() {
		if es, ok := c.(op.ElementStart); ok && es.Type == tagEntry {
			n++
		}
	}
	return n
}
