// Package conv is the conversation model: it reads the conversation structure
// out of a wavelet's "conversation" manifest document and generates the
// operations that author conversations (create the manifest, append blips,
// initialise blip content). It is built on the read-side document projection
// (package doc) and the operation algebra (package op); applying the authored
// ops to wavelet state is the caller's job (package wavelet).
//
// Spec: docs/specs/01-data-model.md §3 (conversation model), §8.3 (creating a blip).
package conv

import (
	"fmt"

	"github.com/sgrankin/wave/internal/doc"
	"github.com/sgrankin/wave/internal/op"
)

// ManifestDocumentID is the id of the conversation manifest document.
const ManifestDocumentID = "conversation"

const (
	tagConversation = "conversation"
	tagBlip         = "blip"
	tagThread       = "thread"

	attrID            = "id"
	attrInline        = "inline"
	attrDelete        = "deleted"
	attrSort          = "sort"
	attrAnchorWavelet = "anchorWavelet"
	attrAnchorBlip    = "anchorBlip"

	boolTrue = "true"
)

// Manifest is the parsed conversation structure read from the manifest document.
//
// Not every schema-permitted detail is parsed: <peer> links and the extended
// anchor attributes (anchorManifestOffset/anchorVersion/anchorOffset) are not
// represented here, matching the Java DocumentBasedManifest (which exposes only
// anchorWavelet/anchorBlip). Code that needs them, or that round-trips a
// manifest back to a document, must read the raw document rather than this
// struct. (Sort is read here as a convenience though the Java reads it outside
// the manifest object.)
type Manifest struct {
	AnchorWavelet string // empty if not anchored
	AnchorBlip    string // empty if not anchored
	Sort          string // empty if unset
	RootThread    Thread // the implicit root thread (blips directly in <conversation>)
}

// Thread is a sequence of blips. The root thread has an empty ID; reply threads
// have an ID equal to their first blip's id, and may be inline.
type Thread struct {
	ID     string
	Inline bool
	Blips  []Blip
}

// Blip is a blip entry in the manifest: its id, deleted flag, and reply threads.
type Blip struct {
	ID      string
	Deleted bool
	Threads []Thread
}

// ReadManifest parses a manifest document's content into its structure. It is
// permissive: it does not validate the manifest schema or the conversation
// invariants (C1–C5) — schema-invalid structure (e.g. a stray <thread> directly
// under <conversation>) is silently ignored rather than rejected.
func ReadManifest(content op.DocOp) (*Manifest, error) {
	root, err := doc.Root(content)
	if err != nil {
		return nil, err
	}
	if root.Type != tagConversation {
		return nil, fmt.Errorf("conv: manifest root is <%s>, want <conversation>", root.Type)
	}
	m := &Manifest{}
	m.AnchorWavelet, _ = root.Attr(attrAnchorWavelet)
	m.AnchorBlip, _ = root.Attr(attrAnchorBlip)
	m.Sort, _ = root.Attr(attrSort)
	m.RootThread = readThread(root, "", false)
	return m, nil
}

// readThread reads the blip children of el as a thread's blips.
func readThread(el *doc.Element, id string, inline bool) Thread {
	th := Thread{ID: id, Inline: inline}
	for _, child := range el.ChildElements() {
		if child.Type == tagBlip {
			th.Blips = append(th.Blips, readBlip(child))
		}
	}
	return th
}

// readBlip reads a <blip> element: its id, deleted flag, and reply threads.
func readBlip(el *doc.Element) Blip {
	b := Blip{}
	b.ID, _ = el.Attr(attrID)
	if d, ok := el.Attr(attrDelete); ok {
		b.Deleted = d == boolTrue
	}
	for _, child := range el.ChildElements() {
		if child.Type != tagThread {
			continue
		}
		tid, _ := child.Attr(attrID)
		inline := false
		if v, ok := child.Attr(attrInline); ok {
			inline = v == boolTrue
		}
		b.Threads = append(b.Threads, readThread(child, tid, inline))
	}
	return b
}

// --- authoring ---

// EmptyManifest returns the content (a DocInitialization) of a fresh, empty
// conversation manifest: <conversation></conversation> (spec §8.1).
func EmptyManifest() op.DocOp {
	none := mustAttrs(nil)
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: tagConversation, Attributes: none},
		op.ElementEnd{},
	})
}

// InitialBlipContent returns the content (a DocInitialization) of a freshly
// created blip: <body><line/></body> (spec §8.3; note no <head> is emitted).
func InitialBlipContent() op.DocOp {
	none := mustAttrs(nil)
	return op.NewDocOp([]op.Component{
		op.ElementStart{Type: "body", Attributes: none},
		op.ElementStart{Type: "line", Attributes: none},
		op.ElementEnd{}, // line
		op.ElementEnd{}, // body
	})
}

// AppendBlipToRootThread returns the operation that appends <blip id="blipID">
// </blip> to the end of the root thread of the given manifest content (just
// before the closing </conversation>). Apply it with op.Apply(manifest, result).
func AppendBlipToRootThread(manifest op.DocOp, blipID string) op.DocOp {
	n := manifest.DocumentLength() // includes the final </conversation>
	return op.NewDocOp([]op.Component{
		op.Retain{Count: n - 1},
		op.ElementStart{Type: tagBlip, Attributes: mustAttrs(map[string]string{attrID: blipID})},
		op.ElementEnd{},
		op.Retain{Count: 1},
	})
}

// mustAttrs builds attributes from a map; it panics only on invalid UTF-8 in a
// name/value, which authored constants never contain.
func mustAttrs(m map[string]string) op.Attributes {
	a, err := op.NewAttributes(m)
	if err != nil {
		panic(fmt.Sprintf("conv: invalid attributes %v: %v", m, err))
	}
	return a
}
