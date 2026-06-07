package conv

import (
	"fmt"
	"unicode/utf8"

	"github.com/sgrankin/wave/internal/op"
)

// Inline image/attachment element in a blip body: <image attachment="<id>"/>.
// Ports the Java ImageConstants (TAGNAME="image", ATTACHMENT_ATTRIBUTE
// ="attachment"). The attachment value is the attachment store's id; the client
// renders it from the /attachments/<id> download URL.
const (
	tagImage       = "image"
	attrAttachment = "attachment"
)

// ImageRef is an inline image found in a blip body: its attachment id and the
// document offset of the <image> element.
type ImageRef struct {
	AttachmentID string
	Offset       int
}

// InsertImage returns an operation that inserts an inline image element
// <image attachment="attachmentID"/> into a blip body at the given document
// offset (an item offset; the editor uses a line boundary so the element sits
// past the paragraph text and does not shift any intra-paragraph caret, like
// InsertReplyAnchor). Apply it by composing onto the parent blip's content.
func InsertImage(body op.DocOp, attachmentID string, offset int) (op.DocOp, error) {
	n := body.DocumentLength()
	if offset < 0 || offset > n {
		return op.DocOp{}, fmt.Errorf("conv: image offset %d out of range [0,%d]", offset, n)
	}
	comps := make([]op.Component, 0, 4)
	if offset > 0 {
		comps = append(comps, op.Retain{Count: offset})
	}
	comps = append(comps,
		op.ElementStart{Type: tagImage, Attributes: mustAttrs(map[string]string{attrAttachment: attachmentID})},
		op.ElementEnd{})
	if offset < n {
		comps = append(comps, op.Retain{Count: n - offset})
	}
	return op.NewDocOp(comps), nil
}

// ReadImages returns the inline images (<image attachment=.../>) in a blip body,
// in document order, each with the item offset of its <image> element.
func ReadImages(body op.DocOp) []ImageRef {
	var images []ImageRef
	pos := 0
	for _, c := range body.Components() {
		switch c := c.(type) {
		case op.ElementStart:
			if c.Type == tagImage {
				att, _ := c.Attributes.Get(attrAttachment)
				images = append(images, ImageRef{AttachmentID: att, Offset: pos})
			}
			pos++
		case op.ElementEnd:
			pos++
		case op.Characters:
			pos += utf8.RuneCountInString(c.Text)
		default:
			// AnnotationBoundary is zero-width; ignore.
		}
	}
	return images
}
