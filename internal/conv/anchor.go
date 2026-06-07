package conv

import (
	"fmt"
	"unicode/utf8"

	"github.com/sgrankin/wave/internal/op"
)

// tagReply is the inline-reply anchor element placed in a blip body to mark where
// an inline reply thread is rooted (ports Blips.THREAD_INLINE_ANCHOR_TAGNAME).
const tagReply = "reply"

// ReplyAnchor is an inline-reply anchor found in a blip body: the reply thread's
// id and the document offset of the anchor element (its ElementStart position, an
// item offset usable as a Retain count).
type ReplyAnchor struct {
	ThreadID string
	Offset   int
}

// InsertReplyAnchor returns an operation that inserts an inline-reply anchor
// <reply id="threadID"/> into a blip body at the given document offset (an item
// offset, e.g. a caret position from the editor). Apply it to the parent blip's
// content with op.Apply.
//
// It is the body half of an inline reply: pair it with ReplyToBlip(manifest, …,
// inline=true) on the manifest document and InitialBlipContent for the new blip,
// all in one wavelet delta. threadID is the reply thread's id, which equals the
// new (first) reply blip's id and the manifest <thread id>. (Ports
// Blips.createInlineReplyAnchor.)
func InsertReplyAnchor(body op.DocOp, threadID string, offset int) (op.DocOp, error) {
	n := body.DocumentLength()
	if offset < 0 || offset > n {
		return op.DocOp{}, fmt.Errorf("conv: reply anchor offset %d out of range [0,%d]", offset, n)
	}
	comps := make([]op.Component, 0, 4)
	if offset > 0 {
		comps = append(comps, op.Retain{Count: offset})
	}
	comps = append(comps,
		op.ElementStart{Type: tagReply, Attributes: mustAttrs(map[string]string{attrID: threadID})},
		op.ElementEnd{})
	if offset < n {
		comps = append(comps, op.Retain{Count: n - offset})
	}
	return op.NewDocOp(comps), nil
}

// ReadReplyAnchors returns the inline-reply anchors (<reply id=.../>) in a blip
// body, in document order, each with the item offset of its <reply> element. The
// offset positions the inline thread within the parent blip's rendered text.
// (Ports Blips.findAnchors; like the Java reader, the first anchor wins if a
// thread id somehow appears twice.) It assumes a well-formed body initialization.
func ReadReplyAnchors(body op.DocOp) []ReplyAnchor {
	var anchors []ReplyAnchor
	seen := map[string]bool{}
	pos := 0
	for _, c := range body.Components() {
		switch c := c.(type) {
		case op.ElementStart:
			if c.Type == tagReply {
				id, _ := c.Attributes.Get(attrID)
				if !seen[id] {
					seen[id] = true
					anchors = append(anchors, ReplyAnchor{ThreadID: id, Offset: pos})
				}
			}
			pos++
		case op.ElementEnd:
			pos++
		case op.Characters:
			pos += utf8.RuneCountInString(c.Text)
		default:
			// AnnotationBoundary is zero-width; other components never appear in an
			// initialization. Ignore.
		}
	}
	return anchors
}
