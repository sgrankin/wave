package waveop

import (
	"strings"

	"github.com/sgrankin/wave/internal/op"
)

// "Worthiness" decides whether a blip edit updates the blip's metadata
// (contributors and last-modified version/time). Trivial edits — opening or
// closing an inline-reply anchor, or changing only presence/spell/link/
// translation/language annotations — are not worthy, and edits to system
// documents are never worthy. (Ports WorthyChangeChecker.)

// inlineReplyAnchorTag is the element tag of an inline-reply anchor; inserting
// or deleting only such an anchor is not a worthy change.
const inlineReplyAnchorTag = "reply"

// unworthyAnnotationPrefixes are annotation-key prefixes whose changes do not by
// themselves make an edit worthy (AnnotationConstants USER/SPELLY/LINK/ROSY/
// LANGUAGE prefixes).
var unworthyAnnotationPrefixes = []string{"user", "spell", "link", "tr", "lang"}

// IsWorthyChange reports whether a document operation is worthy of updating blip
// metadata. An op is unworthy if it consists only of retains, inline-reply
// anchor element starts/ends, element ends, and presence/spell/link/translation/
// language annotation changes; any character edit, attribute change, non-anchor
// element, or other annotation change makes it worthy. (Ports
// WorthyChangeChecker.isWorthy.)
func IsWorthyChange(d op.DocOp) bool {
	for _, c := range d.Components() {
		switch v := c.(type) {
		case op.Retain, op.ElementEnd, op.DeleteElementEnd:
			// Not worthy on their own.
		case op.Characters, op.DeleteCharacters, op.ReplaceAttributes, op.UpdateAttributes:
			return true
		case op.ElementStart:
			if v.Type != inlineReplyAnchorTag {
				return true
			}
		case op.DeleteElementStart:
			if v.Type != inlineReplyAnchorTag {
				return true
			}
		case op.AnnotationBoundary:
			for _, ch := range v.Boundary.Changes() {
				if !strPtrEqual(ch.OldValue, ch.NewValue) && !hasUnworthyPrefix(ch.Key) {
					return true
				}
			}
		}
	}
	return false
}

// IsWorthyBlipID reports whether edits to the document with this id can ever be
// worthy. System documents — attachments ("attach*"), the "mini" document, and
// translation documents ("tr+") — are never worthy. (Ports
// WorthyChangeChecker.isBlipIdWorthy.)
func IsWorthyBlipID(docID string) bool {
	return !strings.HasPrefix(docID, "attach") && docID != "mini" && !strings.HasPrefix(docID, "tr+")
}

// UpdatesBlipMetadata reports whether applying this operation to the blip with
// blipID should update the blip's metadata: true only when the change is worthy
// and the blip id is one whose edits count. (Ports
// BlipOperation.isWorthyOfAttribution / updatesBlipMetadata.)
func (b BlipContentOperation) UpdatesBlipMetadata(blipID string) bool {
	return IsWorthyChange(b.ContentOp) && IsWorthyBlipID(blipID)
}

func hasUnworthyPrefix(key string) bool {
	for _, p := range unworthyAnnotationPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// strPtrEqual reports null-safe equality of two annotation values.
func strPtrEqual(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}
