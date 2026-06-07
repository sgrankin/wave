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
	"unicode/utf8"

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
	return BlipContentWithText("")
}

// BlipContentWithText returns the content (a DocInitialization) of a freshly
// created blip whose single line holds text: <body><line/>text</body>. An empty
// text yields the same structure as InitialBlipContent (no character data, since a
// Characters component must be non-empty).
func BlipContentWithText(text string) op.DocOp {
	none := mustAttrs(nil)
	comps := []op.Component{
		op.ElementStart{Type: "body", Attributes: none},
		op.ElementStart{Type: "line", Attributes: none},
		op.ElementEnd{}, // line
	}
	if text != "" {
		comps = append(comps, op.Characters{Text: text})
	}
	comps = append(comps, op.ElementEnd{}) // body
	return op.NewDocOp(comps)
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

// AppendBlipToThread appends an empty <blip id=blipID/> to the end of the thread
// identified by threadID — the empty string selects the root thread (the children
// of <conversation> itself). It returns the operation; apply it with
// op.Apply(manifest, result). It returns an error if no such thread exists.
//
// This generalises AppendBlipToRootThread (which is AppendBlipToThread with an
// empty threadID); the dedicated root helper is retained as the common case.
func AppendBlipToThread(manifest op.DocOp, threadID, blipID string) (op.DocOp, error) {
	close, ok := elementCloseOffset(manifest, func(tag, id string) bool {
		if threadID == "" {
			return tag == tagConversation
		}
		return tag == tagThread && id == threadID
	})
	if !ok {
		return op.DocOp{}, fmt.Errorf("conv: no thread %q in manifest", threadID)
	}
	n := manifest.DocumentLength()
	return op.NewDocOp([]op.Component{
		op.Retain{Count: close},
		op.ElementStart{Type: tagBlip, Attributes: mustAttrs(map[string]string{attrID: blipID})},
		op.ElementEnd{},
		op.Retain{Count: n - close},
	}), nil
}

// ReplyToBlip creates a new reply thread under the blip parentBlipID, containing a
// single new blip newBlipID. The thread's id equals the new blip's id (the Wave
// convention: a reply thread is identified by its first blip). When inline is
// true the thread is marked inline="true". It returns the operation; apply it
// with op.Apply(manifest, result). It returns an error if no such blip exists.
//
// This authors only the manifest mutation; the caller pairs it with a blip
// operation that initialises newBlipID's content (see InitialBlipContent) in the
// same wavelet delta.
func ReplyToBlip(manifest op.DocOp, parentBlipID, newBlipID string, inline bool) (op.DocOp, error) {
	close, ok := elementCloseOffset(manifest, func(tag, id string) bool {
		return tag == tagBlip && id == parentBlipID
	})
	if !ok {
		return op.DocOp{}, fmt.Errorf("conv: no blip %q in manifest", parentBlipID)
	}
	threadAttrs := map[string]string{attrID: newBlipID}
	if inline {
		threadAttrs[attrInline] = boolTrue
	}
	n := manifest.DocumentLength()
	return op.NewDocOp([]op.Component{
		op.Retain{Count: close},
		op.ElementStart{Type: tagThread, Attributes: mustAttrs(threadAttrs)},
		op.ElementStart{Type: tagBlip, Attributes: mustAttrs(map[string]string{attrID: newBlipID})},
		op.ElementEnd{}, // blip
		op.ElementEnd{}, // thread
		op.Retain{Count: n - close},
	}), nil
}

// elementCloseOffset returns the document offset of the ElementEnd item that
// closes the first element (by close order) whose (tag, id) satisfies pred, where
// id is the element's "id" attribute ("" if absent). The returned offset is the
// index of that ElementEnd item — i.e. the insertion point for appending a child
// to the very end of that element. ok is false if no element matches. It assumes
// a well-formed initialization (balanced elements, no retains/deletions), which
// every manifest is.
func elementCloseOffset(manifest op.DocOp, pred func(tag, id string) bool) (int, bool) {
	type frame struct{ tag, id string }
	var stack []frame
	pos := 0
	for _, c := range manifest.Components() {
		switch c := c.(type) {
		case op.ElementStart:
			id, _ := c.Attributes.Get(attrID)
			stack = append(stack, frame{c.Type, id})
			pos++
		case op.ElementEnd:
			if len(stack) == 0 {
				return 0, false // unbalanced; not a valid manifest
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if pred(top.tag, top.id) {
				return pos, true
			}
			pos++
		case op.Characters:
			pos += utf8.RuneCountInString(c.Text)
		default:
			// AnnotationBoundary is zero-width; other components never appear in an
			// initialization. Ignore.
		}
	}
	return 0, false
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
