package agent

import "time"

// This file is the blip.edited debounce/coalescer: a burst of per-delta edits to
// the SAME blip collapses into ONE blip.edited carrying the latest text, instead
// of one event per content delta flooding an external LLM harness. It is an
// emission-TIMING change only — the wire shape of blip.edited is unchanged and no
// new event kinds are introduced.
//
// The coalescer is PURE: no clock, no timer, no goroutine. Every method takes the
// current time as a parameter, so its correctness is unit-tested with hand-fed
// time.Time values (zero real time). Run owns a single real *time.Timer driven
// from nextDeadline() and holds the coalescer as a Run-LOCAL variable, so every
// access is in the single Run goroutine and is race-free by construction.

const (
	// coalesceWindow is the quiescence debounce: a buffered edit flushes once the
	// blip has been quiet (no further edit) for this long.
	coalesceWindow = 400 * time.Millisecond
	// coalesceMaxAge is the hard cap from the first un-flushed edit for a blip, so
	// sustained typing still emits about every 2s rather than starving the harness.
	coalesceMaxAge = 2000 * time.Millisecond
)

// pendingEdit is the buffered, latest-wins blip.edited for one blip plus the times
// that drive its flush deadline.
type pendingEdit struct {
	ev        Event     // latest-wins blip.edited (newest Version + Text + Author)
	firstSeen time.Time // wall time of the first un-flushed edit (drives the max-age cap)
	lastSeen  time.Time // wall time of the most recent edit (drives the quiescence window)
}

// coalescer buffers ONLY blip.edited, keyed by BlipID, latest-wins. Every other
// kind is immediate and flushes all pending edits ahead of it (global
// flush-before-immediate). order holds BlipIDs in first-seen order for a
// deterministic, stable flush order — NOT map iteration order, NOT sorted by
// BlipID — and a re-add of an already-pending blip keeps its original slot (no
// move-to-back, so a long-typed blip is not starved relative to a fresh one).
type coalescer struct {
	window, maxAge time.Duration
	order          []string
	pending        map[string]*pendingEdit
}

// newCoalescer builds a coalescer with the given quiescence window and max-age cap.
func newCoalescer(window, maxAge time.Duration) *coalescer {
	return &coalescer{window: window, maxAge: maxAge, pending: map[string]*pendingEdit{}}
}

// add ingests one delta's events in Extract order and returns the events to emit
// NOW, in order. A blip.edited is buffered (latest-wins) and not returned; any
// other (immediate) kind first flushes ALL pending edits, then is appended — so an
// edit buffered earlier in THIS SAME delta is emitted ahead of a later immediate
// event in the same delta (the @mention case).
func (c *coalescer) add(now time.Time, events []Event) []Event {
	var out []Event
	for _, ev := range events {
		if ev.Kind == BlipEdited {
			c.put(now, ev)
			continue
		}
		out = append(out, c.flushAll()...)
		out = append(out, ev)
	}
	return out
}

// put buffers a blip.edited latest-wins. An existing slot keeps its order position
// and firstSeen; ev replaces the buffered one only if its Version is not older
// (the monotonic guard keeps latest-wins defensive even though Updates() is the
// in-order applied stream).
func (c *coalescer) put(now time.Time, ev Event) {
	if p, ok := c.pending[ev.BlipID]; ok {
		if ev.Version >= p.ev.Version {
			p.ev = ev
		}
		p.lastSeen = now
		return
	}
	c.order = append(c.order, ev.BlipID)
	c.pending[ev.BlipID] = &pendingEdit{ev: ev, firstSeen: now, lastSeen: now}
}

// flushDue returns and removes every pending edit whose deadline is at or before
// now, preserving first-seen order. order is rebuilt to keep only the still-pending
// BlipIDs.
func (c *coalescer) flushDue(now time.Time) []Event {
	var out []Event
	kept := c.order[:0:0]
	for _, id := range c.order {
		p := c.pending[id]
		if !c.deadline(p).After(now) {
			out = append(out, p.ev)
			delete(c.pending, id)
			continue
		}
		kept = append(kept, id)
	}
	c.order = kept
	return out
}

// flushAll returns and removes ALL pending edits in first-seen order.
func (c *coalescer) flushAll() []Event {
	if len(c.order) == 0 {
		return nil
	}
	out := make([]Event, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.pending[id].ev)
		delete(c.pending, id)
	}
	c.order = c.order[:0]
	return out
}

// nextDeadline is the earliest deadline over all pending edits; ok is false iff
// the buffer is empty.
func (c *coalescer) nextDeadline() (time.Time, bool) {
	var earliest time.Time
	ok := false
	for _, p := range c.pending {
		d := c.deadline(p)
		if !ok || d.Before(earliest) {
			earliest, ok = d, true
		}
	}
	return earliest, ok
}

// deadline is when a pending edit must flush: the earlier of its quiescence window
// (from lastSeen) and its max-age cap (from firstSeen).
func (c *coalescer) deadline(p *pendingEdit) time.Time {
	q := p.lastSeen.Add(c.window)
	cap := p.firstSeen.Add(c.maxAge)
	if cap.Before(q) {
		return cap
	}
	return q
}
