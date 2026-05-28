package op

import "fmt"

// transformError reports an incompatible pair of insertion-free operations
// (the Java InternalTransformException → TransformException). It is raised via
// panic inside the noninsertion transform and recovered at the boundary.
type transformError string

// noninsertionTransform transforms two insertion-free operations, resolving the
// client/server component table and the annotation algebra (ports
// NoninsertionTransformer). It returns (clientOp', serverOp').
func noninsertionTransform(clientOp, serverOp DocOp) (cOut DocOp, sOut DocOp, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch e := r.(type) {
			case transformError:
				err = fmt.Errorf("op: %s", string(e))
			case composeError:
				// updateWith / composeWith reject incompatible attribute states.
				err = fmt.Errorf("op: noninsertion transform: %s", string(e))
			default:
				panic(r)
			}
		}
	}()

	pt := &positionTracker{}
	tr := &nonTransform{}
	tr.clientTracker = newAnnotationTracker(&tr.clientOut, clientSide)
	tr.serverTracker = newAnnotationTracker(&tr.serverOut, serverSide)
	tr.clientTracker.other = tr.serverTracker
	tr.serverTracker.other = tr.clientTracker

	client := &nonTarget{out: &tr.clientOut, rel: relativePosition{t: pt, sign: 1}, tracker: tr.clientTracker}
	server := &nonTarget{out: &tr.serverOut, rel: relativePosition{t: pt, sign: -1}, tracker: tr.serverTracker}
	client.other = server
	server.other = client
	client.rangeCache = &retainCache{t: client}
	server.rangeCache = &retainCache{t: server}

	cc, sc := clientOp.components, serverOp.components
	ci, si := 0, 0
	for ci < len(cc) {
		client.apply(cc[ci])
		ci++
		for client.rel.get() > 0 {
			if si >= len(sc) {
				return DocOp{}, DocOp{}, fmt.Errorf("op: noninsertion transform: ran out of server components")
			}
			server.apply(sc[si])
			si++
		}
	}
	for si < len(sc) {
		server.apply(sc[si])
		si++
	}
	return tr.clientOut.finish(), tr.serverOut.finish(), nil
}

// nonTransform owns the two output builders and the two annotation trackers.
type nonTransform struct {
	clientOut     builder
	serverOut     builder
	clientTracker *annotationTracker
	serverTracker *annotationTracker
}

// nonTarget processes one side of the noninsertion transform.
type nonTarget struct {
	out        *builder
	rel        relativePosition
	tracker    *annotationTracker
	other      *nonTarget
	rangeCache rangeCache
	depth      int
}

func (t *nonTarget) apply(c Component) {
	switch v := c.(type) {
	case Retain:
		t.retain(v.Count)
	case DeleteCharacters:
		t.deleteCharacters(v.Text)
	case DeleteElementStart:
		t.deleteElementStart(v.Type, v.Attributes)
	case DeleteElementEnd:
		t.deleteElementEnd()
	case ReplaceAttributes:
		t.replaceAttributes(v.OldAttributes, v.NewAttributes)
	case UpdateAttributes:
		t.updateAttributes(v.Update)
	case AnnotationBoundary:
		t.tracker.register(v.Boundary)
	default:
		panic(transformError("noninsertion transform: unexpected insertion component"))
	}
}

func (t *nonTarget) retain(n int) {
	t.resolveRange(n, func(size int, c rangeCache) { c.resolveRetain(size) })
	t.rangeCache = &retainCache{t: t}
}

func (t *nonTarget) deleteCharacters(chars string) {
	res := t.resolveRange(runeLen(chars), func(size int, c rangeCache) {
		c.resolveDeleteCharacters(firstRunes(chars, size))
	})
	if res >= 0 {
		t.rangeCache = &deleteCharactersCache{t: t, characters: restRunes(chars, res)}
	}
}

func (t *nonTarget) deleteElementStart(typ string, attrs Attributes) {
	if t.resolveRange(1, func(size int, c rangeCache) { c.resolveDeleteElementStart(typ, attrs) }) == 0 {
		t.rangeCache = &deleteElementStartCache{t: t, typ: typ, attrs: attrs}
	}
}

func (t *nonTarget) deleteElementEnd() {
	if t.resolveRange(1, func(size int, c rangeCache) { c.resolveDeleteElementEnd() }) == 0 {
		t.rangeCache = &deleteElementEndCache{t: t}
	}
}

func (t *nonTarget) replaceAttributes(oldA, newA Attributes) {
	if t.resolveRange(1, func(size int, c rangeCache) { c.resolveReplaceAttributes(oldA, newA) }) == 0 {
		t.rangeCache = &replaceAttributesCache{t: t, oldA: oldA, newA: newA}
	}
}

func (t *nonTarget) updateAttributes(u AttributesUpdate) {
	if t.resolveRange(1, func(size int, c rangeCache) { c.resolveUpdateAttributes(u) }) == 0 {
		t.rangeCache = &updateAttributesCache{t: t, update: u}
	}
}

// resolveRange advances this target's cursor by size and resolves whatever
// overlap is available against the OTHER target's pending range cache, returning
// the resolved portion (>= 0) or -1 if the whole range resolved here.
func (t *nonTarget) resolveRange(size int, resolve func(size int, cache rangeCache)) int {
	oldPosition := t.rel.get()
	t.rel.increase(size)
	if t.rel.get() > 0 {
		if oldPosition < 0 {
			resolve(-oldPosition, t.other.rangeCache)
		}
		return -oldPosition
	}
	resolve(size, t.other.rangeCache)
	return -1
}

func (t *nonTarget) syncAnnotations() {
	t.tracker.sync()
	t.other.tracker.sync()
}

func (t *nonTarget) doDeleteCharacters(chars string) {
	t.tracker.commenceDeletion()
	t.out.deleteCharacters(chars)
	t.tracker.concludeDeletion()
}

func (t *nonTarget) doDeleteElementStart(typ string, attrs Attributes) {
	t.tracker.commenceDeletion()
	t.out.deleteElementStart(typ, attrs)
	t.tracker.concludeDeletion()
}

func (t *nonTarget) doDeleteElementEnd() {
	t.tracker.commenceDeletion()
	t.out.deleteElementEnd()
	t.tracker.concludeDeletion()
}

// --- range caches: the client×server resolution table ---
//
// A target's rangeCache holds the effect of the component it last read. When the
// OTHER target reads a component, it resolves against this cache: the (cache
// type, incoming component type) pair selects the output. Within a cache, the
// owning target is the one that created it; otherTarget is its opposite.

type rangeCache interface {
	resolveRetain(itemCount int)
	resolveDeleteCharacters(chars string)
	resolveDeleteElementStart(typ string, attrs Attributes)
	resolveDeleteElementEnd()
	resolveReplaceAttributes(oldA, newA Attributes)
	resolveUpdateAttributes(u AttributesUpdate)
}

// incompatibleCache provides default resolve methods that reject the pairing.
// Concrete caches embed it and override the compatible cases.
type incompatibleCache struct{}

func (incompatibleCache) resolveRetain(int)              { panic(transformError(incompatibleMsg)) }
func (incompatibleCache) resolveDeleteCharacters(string) { panic(transformError(incompatibleMsg)) }
func (incompatibleCache) resolveDeleteElementStart(string, Attributes) {
	panic(transformError(incompatibleMsg))
}
func (incompatibleCache) resolveDeleteElementEnd() { panic(transformError(incompatibleMsg)) }
func (incompatibleCache) resolveReplaceAttributes(Attributes, Attributes) {
	panic(transformError(incompatibleMsg))
}
func (incompatibleCache) resolveUpdateAttributes(AttributesUpdate) {
	panic(transformError(incompatibleMsg))
}

const incompatibleMsg = "noninsertion transform: incompatible operations"

// retainCache: the owner retained at this position; implements every pairing.
type retainCache struct{ t *nonTarget }

func (c *retainCache) resolveRetain(itemCount int) {
	c.t.syncAnnotations()
	c.t.out.retain(itemCount)
	c.t.other.out.retain(itemCount)
}
func (c *retainCache) resolveDeleteCharacters(chars string) { c.t.other.doDeleteCharacters(chars) }
func (c *retainCache) resolveDeleteElementStart(typ string, attrs Attributes) {
	c.t.other.doDeleteElementStart(typ, attrs)
	c.t.other.depth++
}
func (c *retainCache) resolveDeleteElementEnd() {
	c.t.other.doDeleteElementEnd()
	c.t.other.depth--
}
func (c *retainCache) resolveReplaceAttributes(oldA, newA Attributes) {
	c.t.syncAnnotations()
	c.t.out.retain(1)
	c.t.other.out.replaceAttributes(oldA, newA)
}
func (c *retainCache) resolveUpdateAttributes(u AttributesUpdate) {
	c.t.syncAnnotations()
	c.t.out.retain(1)
	c.t.other.out.updateAttributes(u)
}

// deleteCharactersCache: the owner deleted characters here.
type deleteCharactersCache struct {
	incompatibleCache
	t          *nonTarget
	characters string
}

func (c *deleteCharactersCache) resolveRetain(itemCount int) {
	c.t.doDeleteCharacters(firstRunes(c.characters, itemCount))
	c.characters = restRunes(c.characters, itemCount)
}
func (c *deleteCharactersCache) resolveDeleteCharacters(chars string) {
	c.characters = restRunes(c.characters, runeLen(chars)) // both deleted the same run
}

// deleteElementStartCache: the owner deleted an element start here.
type deleteElementStartCache struct {
	incompatibleCache
	t     *nonTarget
	typ   string
	attrs Attributes
}

func (c *deleteElementStartCache) resolveRetain(itemCount int) {
	c.t.doDeleteElementStart(c.typ, c.attrs)
	c.t.depth++
}
func (c *deleteElementStartCache) resolveDeleteElementStart(typ string, attrs Attributes) {
	c.t.depth++
	c.t.other.depth++ // both deleted the same element start
}
func (c *deleteElementStartCache) resolveReplaceAttributes(oldA, newA Attributes) {
	c.t.doDeleteElementStart(c.typ, newA)
	c.t.depth++
}
func (c *deleteElementStartCache) resolveUpdateAttributes(u AttributesUpdate) {
	c.t.doDeleteElementStart(c.typ, c.attrs.updateWith(u))
	c.t.depth++
}

// deleteElementEndCache: the owner deleted an element end here.
type deleteElementEndCache struct {
	incompatibleCache
	t *nonTarget
}

func (c *deleteElementEndCache) resolveRetain(itemCount int) {
	c.t.doDeleteElementEnd()
	c.t.depth--
}
func (c *deleteElementEndCache) resolveDeleteElementEnd() {
	c.t.depth--
	c.t.other.depth--
}

// replaceAttributesCache: the owner replaced attributes here.
type replaceAttributesCache struct {
	incompatibleCache
	t          *nonTarget
	oldA, newA Attributes
}

func (c *replaceAttributesCache) resolveRetain(itemCount int) {
	c.t.syncAnnotations()
	c.t.out.replaceAttributes(c.oldA, c.newA)
	c.t.other.out.retain(1)
}
func (c *replaceAttributesCache) resolveDeleteElementStart(typ string, attrs Attributes) {
	c.t.other.doDeleteElementStart(typ, c.newA)
	c.t.other.depth++
}
func (c *replaceAttributesCache) resolveReplaceAttributes(oldA, newA Attributes) {
	c.t.syncAnnotations()
	c.t.out.replaceAttributes(newA, c.newA)
	c.t.other.out.retain(1)
}
func (c *replaceAttributesCache) resolveUpdateAttributes(u AttributesUpdate) {
	c.t.syncAnnotations()
	c.t.out.replaceAttributes(c.oldA.updateWith(u), c.newA)
	c.t.other.out.retain(1)
}

// updateAttributesCache: the owner updated attributes here.
type updateAttributesCache struct {
	incompatibleCache
	t      *nonTarget
	update AttributesUpdate
}

func (c *updateAttributesCache) resolveRetain(itemCount int) {
	c.t.syncAnnotations()
	c.t.out.updateAttributes(c.update)
	c.t.other.out.retain(1)
}
func (c *updateAttributesCache) resolveDeleteElementStart(typ string, attrs Attributes) {
	c.t.other.doDeleteElementStart(typ, attrs.updateWith(c.update))
	c.t.other.depth++
}
func (c *updateAttributesCache) resolveReplaceAttributes(oldA, newA Attributes) {
	c.t.syncAnnotations()
	c.t.out.retain(1)
	c.t.other.out.replaceAttributes(oldA.updateWith(c.update), newA)
}
func (c *updateAttributesCache) resolveUpdateAttributes(u AttributesUpdate) {
	c.t.syncAnnotations()
	// The owner's update wins for shared keys; its old values are adjusted to
	// reflect u (the other side's update) having been applied first.
	updated := make(map[string]*string, u.Len())
	for _, ch := range u.updates {
		updated[ch.Name] = ch.NewValue
	}
	ownerKeys := make(map[string]bool, c.update.Len())
	changes := make([]AttributeChange, 0, c.update.Len())
	for _, ch := range c.update.updates {
		ownerKeys[ch.Name] = true
		newOld := ch.OldValue
		if nv, ok := updated[ch.Name]; ok {
			newOld = nv
		}
		changes = append(changes, AttributeChange{Name: ch.Name, OldValue: newOld, NewValue: ch.NewValue})
	}
	ownerUpdate, err := NewAttributesUpdate(changes)
	if err != nil {
		panic(transformError("noninsertion transform: " + err.Error()))
	}
	c.t.out.updateAttributes(ownerUpdate)
	// The other side keeps only the keys the owner did not touch.
	excl := make([]AttributeChange, 0, u.Len())
	for _, ch := range u.updates {
		if !ownerKeys[ch.Name] {
			excl = append(excl, ch)
		}
	}
	otherUpdate, err := NewAttributesUpdate(excl)
	if err != nil {
		panic(transformError("noninsertion transform: " + err.Error()))
	}
	c.t.other.out.updateAttributes(otherUpdate)
}
