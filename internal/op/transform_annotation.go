package op

// annotationTracker tracks annotation state for one side of a noninsertion
// transform and emits the boundary adjustments that keep both transformed
// operations' annotations consistent (ports NoninsertionTransformer.Annotation-
// Tracker plus the per-side process implementations).
//
// Four maps, which an implementer must NOT conflate (spec §Annotation transform):
//   - tracked:     updated eagerly when this side READS a boundary (register);
//     consulted by the opposing side's process decisions.
//   - active:      updated only when a boundary is COMMITTED to output; used
//     solely by commence/concludeDeletion.
//   - temporary:   per-deletion scratch saving the active state to restore.
//   - propagating: annotations committed since the last sync, awaiting carry
//     into a deletion region.
//
// temporary and propagating distinguish "key present with nil value" from
// "key absent" (Java stores null ValueUpdates with the key present), so they
// hold *valueUpdate and presence is tested with the comma-ok idiom.
type annotationTracker struct {
	tracked     map[string]valueUpdate
	active      map[string]valueUpdate
	temporary   map[string]*valueUpdate
	propagating map[string]*valueUpdate
	out         *builder
	other       *annotationTracker
	side        annSide
}

type annSide int

const (
	clientSide annSide = iota
	serverSide
)

func newAnnotationTracker(out *builder, side annSide) *annotationTracker {
	return &annotationTracker{
		tracked:     map[string]valueUpdate{},
		active:      map[string]valueUpdate{},
		temporary:   map[string]*valueUpdate{},
		propagating: map[string]*valueUpdate{},
		out:         out,
		side:        side,
	}
}

// register records a read boundary into tracked, then runs the per-side
// transform to emit the committed boundaries.
func (t *annotationTracker) register(m AnnotationBoundaryMap) {
	for _, k := range m.endKeys {
		delete(t.tracked, k)
	}
	for _, ch := range m.changes {
		t.tracked[ch.Key] = valueUpdate{old: ch.OldValue, new: ch.NewValue}
	}
	t.process(m)
}

// commit writes a boundary to this side's output and updates active/propagating.
func (t *annotationTracker) commit(m AnnotationBoundaryMap) {
	for _, key := range m.endKeys {
		if _, ok := t.propagating[key]; !ok {
			t.propagating[key] = t.activePtr(key)
		}
		delete(t.active, key)
	}
	for _, ch := range m.changes {
		old, hasOld := t.active[ch.Key]
		if !hasOld || !ptrEqual(old.old, ch.OldValue) || !ptrEqual(old.new, ch.NewValue) {
			if _, ok := t.propagating[ch.Key]; !ok {
				t.propagating[ch.Key] = t.activePtr(ch.Key)
			}
			t.active[ch.Key] = valueUpdate{old: ch.OldValue, new: ch.NewValue}
		}
	}
	t.out.annotationBoundary(m)
}

// activePtr returns a pointer copy of the active entry for key, or nil if absent
// (mirrors Java's propagating.put(key, active.get(key)), which may store null).
func (t *annotationTracker) activePtr(key string) *valueUpdate {
	if v, ok := t.active[key]; ok {
		vc := v
		return &vc
	}
	return nil
}

func (t *annotationTracker) sync() { t.propagating = map[string]*valueUpdate{} }

// commenceDeletion emits the annotation changes needed to bring the deletion
// point to its inherited value before the deleted content is written.
func (t *annotationTracker) commenceDeletion() {
	other := t.other
	var changes []AnnotationChange
	for key, updPtr := range other.propagating {
		forCombining, hasForCombining := t.active[key]
		// Save current active value (possibly nil) for restoration in conclude.
		if hasForCombining {
			fc := forCombining
			t.temporary[key] = &fc
		} else {
			t.temporary[key] = nil
		}
		if updPtr != nil {
			var oldVal *string
			switch {
			case hasForCombining:
				oldVal = forCombining.old
			default:
				if oa, ok := other.active[key]; ok {
					oldVal = oa.new
				} else {
					oldVal = updPtr.old
				}
			}
			changes = append(changes, AnnotationChange{Key: key, OldValue: oldVal, NewValue: updPtr.new})
		} else if oa, ok := other.active[key]; ok {
			var newVal *string
			if hasForCombining {
				newVal = forCombining.new
			} else {
				newVal = oa.old
			}
			changes = append(changes, AnnotationChange{Key: key, OldValue: oa.new, NewValue: newVal})
		}
	}
	t.commitChanges(nil, changes)
}

// concludeDeletion restores the annotation state saved in temporary after the
// deleted content has been written.
func (t *annotationTracker) concludeDeletion() {
	var ends []string
	var changes []AnnotationChange
	for key, updPtr := range t.temporary {
		if updPtr != nil {
			changes = append(changes, AnnotationChange{Key: key, OldValue: updPtr.old, NewValue: updPtr.new})
		} else {
			ends = append(ends, key)
		}
	}
	t.sync()
	t.commitChanges(ends, changes)
	t.temporary = map[string]*valueUpdate{}
}

// commitChanges builds a boundary from ends/changes and commits it. The inputs
// are disjoint and validity-preserving by construction, so a build error is an
// internal invariant violation.
func (t *annotationTracker) commitChanges(ends []string, changes []AnnotationChange) {
	m, err := NewAnnotationBoundaryMap(ends, changes)
	if err != nil {
		panic(transformError("noninsertion transform: invalid annotation boundary: " + err.Error()))
	}
	t.commit(m)
}

// process emits the transformed boundaries for a read map, writing to both this
// side's and the opposing side's outputs. The two sides are asymmetric.
func (t *annotationTracker) process(m AnnotationBoundaryMap) {
	switch t.side {
	case clientSide:
		t.clientProcess(m)
	case serverSide:
		t.serverProcess(m)
	}
}

// clientProcess: the client always emits an end for each ended key; if the
// server is tracking a key, the client's change uses the server's tracked new
// value as its old, and the server emits the complementary boundary.
func (t *annotationTracker) clientProcess(m AnnotationBoundaryMap) {
	server := t.other
	var clientEnds []string
	var clientChanges []AnnotationChange
	var serverEnds []string
	var serverChanges []AnnotationChange
	for _, key := range m.endKeys {
		clientEnds = append(clientEnds, key)
		if sv, ok := server.tracked[key]; ok {
			serverChanges = append(serverChanges, AnnotationChange{Key: key, OldValue: sv.old, NewValue: sv.new})
		}
	}
	for _, ch := range m.changes {
		if sv, ok := server.tracked[ch.Key]; ok {
			clientChanges = append(clientChanges, AnnotationChange{Key: ch.Key, OldValue: sv.new, NewValue: ch.NewValue})
			serverEnds = append(serverEnds, ch.Key)
		} else {
			clientChanges = append(clientChanges, AnnotationChange{Key: ch.Key, OldValue: ch.OldValue, NewValue: ch.NewValue})
		}
	}
	t.commitChanges(clientEnds, clientChanges)
	server.commitChanges(serverEnds, serverChanges)
}

// serverProcess: the server emits an end for an ended key only when the client
// is not tracking it; if the client is tracking, the client emits the change
// instead and the server drops it.
func (t *annotationTracker) serverProcess(m AnnotationBoundaryMap) {
	client := t.other
	var serverEnds []string
	var serverChanges []AnnotationChange
	var clientChanges []AnnotationChange
	for _, key := range m.endKeys {
		if cv, ok := client.tracked[key]; ok {
			clientChanges = append(clientChanges, AnnotationChange{Key: key, OldValue: cv.old, NewValue: cv.new})
		} else {
			serverEnds = append(serverEnds, key)
		}
	}
	for _, ch := range m.changes {
		if cv, ok := client.tracked[ch.Key]; ok {
			clientChanges = append(clientChanges, AnnotationChange{Key: ch.Key, OldValue: ch.NewValue, NewValue: cv.new})
		} else {
			serverChanges = append(serverChanges, AnnotationChange{Key: ch.Key, OldValue: ch.OldValue, NewValue: ch.NewValue})
		}
	}
	t.commitChanges(serverEnds, serverChanges)
	client.commitChanges(nil, clientChanges)
}
