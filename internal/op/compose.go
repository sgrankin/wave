package op

import (
	"fmt"
	"unicode/utf8"
)

// Compose returns the single DocOp equivalent to applying op1 then op2. op2 must
// be valid against the document op1 produces: op1's output length must equal
// op2's input length. (Ports Composer.compose.)
func Compose(op1, op2 DocOp) (result DocOp, err error) {
	if got, want := op1.outputLength(), op2.inputLength(); got != want {
		return DocOp{}, fmt.Errorf("op: cannot compose: op1 output length %d != op2 input length %d", got, want)
	}
	defer func() {
		if r := recover(); r != nil {
			if ce, ok := r.(composeError); ok {
				err = fmt.Errorf("op: %s", string(ce))
				return
			}
			panic(r)
		}
	}()
	c := &composer{
		preAnn:  map[string]valueUpdate{},
		postAnn: map[string]valueUpdate{},
		kind:    defaultPre,
	}
	return c.run(op1, op2), nil
}

type composeError string

// valueUpdate is an (old, new) annotation value pair, like the Java ValueUpdate.
type valueUpdate struct {
	old *string
	new *string
}

type targetKind int

const (
	// Pre-states consume op1 (the first operation) components.
	defaultPre targetKind = iota
	retainPre
	deleteCharsPre
	// Post-states consume op2 (the second operation) components.
	retainPost
	charsPost
	elementStartPost
	elementEndPost
	replaceAttrsPost
	updateAttrsPost
	finisherPost
)

func (k targetKind) isPost() bool { return k >= retainPost }

// composer is the compose state machine: a current target (a pre- or post-state
// with its payload) plus the queued/active annotation state for each side.
type composer struct {
	out builder

	preAnn    map[string]valueUpdate
	postAnn   map[string]valueUpdate
	preQueue  []AnnotationBoundaryMap
	postQueue []AnnotationBoundaryMap

	kind targetKind
	// payloads (interpreted per kind):
	count    int              // retainPre, retainPost
	chars    string           // charsPost, deleteCharsPre
	elemType string           // elementStartPost
	attrs    Attributes       // elementStartPost
	oldAttrs Attributes       // replaceAttrsPost
	newAttrs Attributes       // replaceAttrsPost
	update   AttributesUpdate // updateAttrsPost
}

func (c *composer) run(op1, op2 DocOp) DocOp {
	comps1, comps2 := op1.components, op2.components
	i, j := 0, 0
	for i < len(comps1) {
		c.applyPre(comps1[i])
		i++
		for c.kind.isPost() {
			if j >= len(comps2) {
				panic(composeError("compose: op2 too short for op1 output"))
			}
			c.applyPost(comps2[j])
			j++
		}
	}
	if j < len(comps2) {
		c.kind = finisherPost
		for j < len(comps2) {
			c.applyPost(comps2[j])
			j++
		}
	}
	c.flushBoth()
	return c.out.finish()
}

// --- annotation queue handling (ports the pre/postAnnotationQueue unqueue) ---

func (c *composer) flushPre() {
	for _, m := range c.preQueue {
		c.preUnqueue(m)
	}
	c.preQueue = c.preQueue[:0]
}

func (c *composer) flushPost() {
	for _, m := range c.postQueue {
		c.postUnqueue(m)
	}
	c.postQueue = c.postQueue[:0]
}

func (c *composer) flushBoth() {
	c.flushPre()
	c.flushPost()
}

// preUnqueue flushes one of op1's queued boundaries (Composer.preAnnotationQueue).
func (c *composer) preUnqueue(m AnnotationBoundaryMap) {
	var ends []string
	var changes []AnnotationChange
	for _, key := range m.endKeys {
		if post, ok := c.postAnn[key]; ok {
			changes = append(changes, AnnotationChange{Key: key, OldValue: post.old, NewValue: post.new})
		} else {
			ends = append(ends, key)
		}
		delete(c.preAnn, key)
	}
	for _, ch := range m.changes {
		newVal := ch.NewValue
		if post, ok := c.postAnn[ch.Key]; ok {
			newVal = post.new
		}
		changes = append(changes, AnnotationChange{Key: ch.Key, OldValue: ch.OldValue, NewValue: newVal})
		c.preAnn[ch.Key] = valueUpdate{old: ch.OldValue, new: ch.NewValue}
	}
	c.emitBoundary(ends, changes)
}

// postUnqueue flushes one of op2's queued boundaries (Composer.postAnnotationQueue).
func (c *composer) postUnqueue(m AnnotationBoundaryMap) {
	var ends []string
	var changes []AnnotationChange
	for _, key := range m.endKeys {
		if pre, ok := c.preAnn[key]; ok {
			changes = append(changes, AnnotationChange{Key: key, OldValue: pre.old, NewValue: pre.new})
		} else {
			ends = append(ends, key)
		}
		delete(c.postAnn, key)
	}
	for _, ch := range m.changes {
		oldVal := ch.OldValue
		if pre, ok := c.preAnn[ch.Key]; ok {
			oldVal = pre.old
		}
		changes = append(changes, AnnotationChange{Key: ch.Key, OldValue: oldVal, NewValue: ch.NewValue})
		c.postAnn[ch.Key] = valueUpdate{old: ch.OldValue, new: ch.NewValue}
	}
	c.emitBoundary(ends, changes)
}

func (c *composer) emitBoundary(ends []string, changes []AnnotationChange) {
	if len(ends) == 0 && len(changes) == 0 {
		return
	}
	m, err := NewAnnotationBoundaryMap(ends, changes)
	if err != nil {
		panic(composeError("compose: produced invalid annotation boundary: " + err.Error()))
	}
	c.out.annotationBoundary(m)
}

// --- applyPre: feed an op1 component while in a pre-state ---

func (c *composer) applyPre(comp Component) {
	// Base PreTarget behavior: op1 deletions pass straight through; boundaries queue.
	switch v := comp.(type) {
	case DeleteCharacters:
		c.flushPre()
		c.out.deleteCharacters(v.Text)
		return
	case DeleteElementStart:
		c.flushPre()
		c.out.deleteElementStart(v.Type, v.Attributes)
		return
	case DeleteElementEnd:
		c.flushPre()
		c.out.deleteElementEnd()
		return
	case AnnotationBoundary:
		c.preQueue = append(c.preQueue, v.Boundary)
		return
	}

	switch c.kind {
	case defaultPre:
		c.defaultPre(comp)
	case retainPre:
		c.retainPre(comp)
	case deleteCharsPre:
		c.deleteCharsPre(comp)
	default:
		panic(composeError("compose: internal error: applyPre in post-state"))
	}
}

func (c *composer) defaultPre(comp Component) {
	switch v := comp.(type) {
	case Retain:
		c.kind, c.count = retainPost, v.Count
	case Characters:
		c.kind, c.chars = charsPost, v.Text
	case ElementStart:
		c.kind, c.elemType, c.attrs = elementStartPost, v.Type, v.Attributes
	case ElementEnd:
		c.kind = elementEndPost
	case ReplaceAttributes:
		c.kind, c.oldAttrs, c.newAttrs = replaceAttrsPost, v.OldAttributes, v.NewAttributes
	case UpdateAttributes:
		c.kind, c.update = updateAttrsPost, v.Update
	default:
		panic(composeError("compose: unexpected op1 component"))
	}
}

func (c *composer) retainPre(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		if v.Count <= c.count {
			c.out.retain(v.Count)
			c.cancelRetainPre(v.Count)
		} else {
			c.out.retain(c.count)
			c.kind, c.count = retainPost, v.Count-c.count
		}
	case Characters:
		if n := runeLen(v.Text); n <= c.count {
			c.out.characters(v.Text)
			c.cancelRetainPre(n)
		} else {
			c.out.characters(firstRunes(v.Text, c.count))
			c.kind, c.chars = charsPost, restRunes(v.Text, c.count)
		}
	case ElementStart:
		c.out.elementStart(v.Type, v.Attributes)
		c.cancelRetainPre(1)
	case ElementEnd:
		c.out.elementEnd()
		c.cancelRetainPre(1)
	case ReplaceAttributes:
		c.out.replaceAttributes(v.OldAttributes, v.NewAttributes)
		c.cancelRetainPre(1)
	case UpdateAttributes:
		c.out.updateAttributes(v.Update)
		c.cancelRetainPre(1)
	default:
		panic(composeError("compose: unexpected op1 component"))
	}
}

func (c *composer) cancelRetainPre(size int) {
	if size < c.count {
		c.count -= size
	} else {
		c.kind = defaultPre
	}
}

func (c *composer) deleteCharsPre(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		if v.Count <= runeLen(c.chars) {
			c.out.deleteCharacters(firstRunes(c.chars, v.Count))
			c.cancelDeleteCharsPre(v.Count)
		} else {
			c.out.deleteCharacters(c.chars)
			c.kind, c.count = retainPost, v.Count-runeLen(c.chars)
		}
	case Characters:
		if n := runeLen(v.Text); n <= runeLen(c.chars) {
			c.cancelDeleteCharsPre(n) // insert cancels an equal amount of delete
		} else {
			c.kind, c.chars = charsPost, restRunes(v.Text, runeLen(c.chars))
		}
	default:
		panic(composeError("compose: illegal composition (insert/attr over deleted chars)"))
	}
}

func (c *composer) cancelDeleteCharsPre(size int) {
	if size < runeLen(c.chars) {
		c.chars = restRunes(c.chars, size)
	} else {
		c.kind = defaultPre
	}
}

// --- applyPost: feed an op2 component while in a post-state ---

func (c *composer) applyPost(comp Component) {
	// Base PostTarget behavior: op2 insertions pass straight through; boundaries queue.
	switch v := comp.(type) {
	case Characters:
		c.flushPost()
		c.out.characters(v.Text)
		return
	case ElementStart:
		c.flushPost()
		c.out.elementStart(v.Type, v.Attributes)
		return
	case ElementEnd:
		c.flushPost()
		c.out.elementEnd()
		return
	case AnnotationBoundary:
		c.postQueue = append(c.postQueue, v.Boundary)
		return
	}

	switch c.kind {
	case retainPost:
		c.retainPost(comp)
	case charsPost:
		c.charsPost(comp)
	case elementStartPost:
		c.elementStartPost(comp)
	case elementEndPost:
		c.elementEndPost(comp)
	case replaceAttrsPost:
		c.replaceAttrsPost(comp)
	case updateAttrsPost:
		c.updateAttrsPost(comp)
	case finisherPost:
		panic(composeError("compose: op2 has trailing non-insertion after op1 ended"))
	default:
		panic(composeError("compose: internal error: applyPost in pre-state"))
	}
}

func (c *composer) retainPost(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		if v.Count <= c.count {
			c.out.retain(v.Count)
			c.cancelRetainPost(v.Count)
		} else {
			c.out.retain(c.count)
			c.kind, c.count = retainPre, v.Count-c.count
		}
	case DeleteCharacters:
		if n := runeLen(v.Text); n <= c.count {
			c.out.deleteCharacters(v.Text)
			c.cancelRetainPost(n)
		} else {
			c.out.deleteCharacters(firstRunes(v.Text, c.count))
			c.kind, c.chars = deleteCharsPre, restRunes(v.Text, c.count)
		}
	case DeleteElementStart:
		c.out.deleteElementStart(v.Type, v.Attributes)
		c.cancelRetainPost(1)
	case DeleteElementEnd:
		c.out.deleteElementEnd()
		c.cancelRetainPost(1)
	case ReplaceAttributes:
		c.out.replaceAttributes(v.OldAttributes, v.NewAttributes)
		c.cancelRetainPost(1)
	case UpdateAttributes:
		c.out.updateAttributes(v.Update)
		c.cancelRetainPost(1)
	default:
		panic(composeError("compose: unexpected op2 component"))
	}
}

func (c *composer) cancelRetainPost(size int) {
	if size < c.count {
		c.count -= size
	} else {
		c.kind = defaultPre
	}
}

func (c *composer) charsPost(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		if v.Count <= runeLen(c.chars) {
			c.out.characters(firstRunes(c.chars, v.Count))
			c.cancelCharsPost(v.Count)
		} else {
			c.out.characters(c.chars)
			c.kind, c.count = retainPre, v.Count-runeLen(c.chars)
		}
	case DeleteCharacters:
		if n := runeLen(v.Text); n <= runeLen(c.chars) {
			c.cancelCharsPost(n) // inserted chars deleted again cancel out
		} else {
			c.kind, c.chars = deleteCharsPre, restRunes(v.Text, runeLen(c.chars))
		}
	default:
		panic(composeError("compose: illegal composition (delete-element/attr over inserted chars)"))
	}
}

func (c *composer) cancelCharsPost(size int) {
	if size < runeLen(c.chars) {
		c.chars = restRunes(c.chars, size)
	} else {
		c.kind = defaultPre
	}
}

func (c *composer) elementStartPost(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		c.out.elementStart(c.elemType, c.attrs)
		c.retainTail(v.Count)
	case DeleteElementStart:
		c.kind = defaultPre // insert then delete cancels
	case ReplaceAttributes:
		c.out.elementStart(c.elemType, v.NewAttributes)
		c.kind = defaultPre
	case UpdateAttributes:
		c.out.elementStart(c.elemType, c.attrs.updateWith(v.Update))
		c.kind = defaultPre
	default:
		panic(composeError("compose: illegal composition on inserted element start"))
	}
}

func (c *composer) elementEndPost(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		c.out.elementEnd()
		c.retainTail(v.Count)
	case DeleteElementEnd:
		c.kind = defaultPre // insert then delete cancels
	default:
		panic(composeError("compose: illegal composition on inserted element end"))
	}
}

func (c *composer) replaceAttrsPost(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		c.out.replaceAttributes(c.oldAttrs, c.newAttrs)
		c.retainTail(v.Count)
	case DeleteElementStart:
		c.out.deleteElementStart(v.Type, c.oldAttrs)
		c.kind = defaultPre
	case ReplaceAttributes:
		c.out.replaceAttributes(c.oldAttrs, v.NewAttributes)
		c.kind = defaultPre
	case UpdateAttributes:
		c.out.replaceAttributes(c.oldAttrs, c.newAttrs.updateWith(v.Update))
		c.kind = defaultPre
	default:
		panic(composeError("compose: illegal composition on replaceAttributes"))
	}
}

func (c *composer) updateAttrsPost(comp Component) {
	c.flushBoth()
	switch v := comp.(type) {
	case Retain:
		c.out.updateAttributes(c.update)
		c.retainTail(v.Count)
	case DeleteElementStart:
		c.out.deleteElementStart(v.Type, v.Attributes.updateWith(c.update.invert()))
		c.kind = defaultPre
	case ReplaceAttributes:
		c.out.replaceAttributes(v.OldAttributes.updateWith(c.update.invert()), v.NewAttributes)
		c.kind = defaultPre
	case UpdateAttributes:
		c.out.updateAttributes(c.update.composeWith(v.Update))
		c.kind = defaultPre
	default:
		panic(composeError("compose: illegal composition on updateAttributes"))
	}
}

// retainTail handles the single-item attribute/element post-targets after the
// item is emitted: if the driving retain covered more than this 1 item, the
// remainder becomes a pending pre-side retain; otherwise return to default.
func (c *composer) retainTail(retainCount int) {
	if retainCount > 1 {
		c.kind, c.count = retainPre, retainCount-1
	} else {
		c.kind = defaultPre
	}
}

// rune helpers: characters are counted and split by rune (see component.go).

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func firstRunes(s string, n int) string {
	r := []rune(s)
	return string(r[:n])
}

func restRunes(s string, n int) string {
	r := []rune(s)
	return string(r[n:])
}
