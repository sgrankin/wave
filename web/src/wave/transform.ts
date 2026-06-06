// DocOp-level operational transform (the op-level OT, TP1). A faithful port of
// the Go reference transformer (internal/op/transform*.go), itself a port of the
// Wave Java NoninsertionTransformer / InsertionTransformer family.
//
// Transform reconciles a concurrent client and server operation, both valid
// against the same document, into (clientOp', serverOp') such that
//
//   apply(apply(D, server), clientOp') == apply(apply(D, client), serverOp')
//
// (the TP1 convergence property). Concurrent insertions at the same position are
// tie-broken with the CLIENT first.
//
// The algorithm decomposes each operation into an insertion-only part and an
// insertion-free part and runs four sub-transforms, then composes the results.
// The resolution order and tie-breaking MUST match the Go server exactly so that
// client and server converge.
//
// Go references: internal/op/transform.go, transform_insnon.go,
// transform_noninsertion.go, transform_annotation.go, builder.go, compose.go.

import {
  AnnotationBoundaryMap,
  Attributes,
  AttributesUpdate,
  DocOp,
  runeCount,
} from "./types.ts";
import type { AnnotationChange, AttributeChange, Component } from "./types.ts";
import { compose } from "./compose.ts";

// ---------------------------------------------------------------------------
// rune helpers — characters are counted and split by Unicode code point, to match
// Go's utf8.RuneCountInString / []rune slicing.
// ---------------------------------------------------------------------------

function runeLen(s: string): number {
  return runeCount(s);
}

function firstRunes(s: string, n: number): string {
  let i = 0;
  let out = "";
  for (const ch of s) {
    if (i >= n) break;
    out += ch;
    i++;
  }
  return out;
}

function restRunes(s: string, n: number): string {
  let i = 0;
  let out = "";
  for (const ch of s) {
    if (i >= n) out += ch;
    i++;
  }
  return out;
}

function ptrEqual(a: string | null, b: string | null): boolean {
  if (a === null && b === null) return true;
  if (a === null || b === null) return false;
  return a === b;
}

// ---------------------------------------------------------------------------
// builder — accumulates components into a normalized DocOp: adjacent retains,
// characters, and deleteCharacters are merged; zero-width pieces are dropped; and
// consecutive annotation boundaries are coalesced into one. Ports op.builder.
// ---------------------------------------------------------------------------

class Builder {
  private out: Component[] = [];
  // a not-yet-emitted, accumulating annotation boundary
  private pendingEnds: Set<string> | null = null;
  private pendingChanges: Map<string, AnnotationChange> | null = null;

  annotationBoundary(m: AnnotationBoundaryMap): void {
    if (m.empty) return;
    if (this.pendingEnds === null || this.pendingChanges === null) {
      this.pendingEnds = new Set<string>();
      this.pendingChanges = new Map<string, AnnotationChange>();
    }
    for (const k of m.endKeys) {
      this.pendingChanges.delete(k);
      this.pendingEnds.add(k);
    }
    for (const c of m.changes) {
      this.pendingEnds.delete(c.key);
      this.pendingChanges.set(c.key, c);
    }
  }

  // flushAnnotation emits the accumulated annotation boundary (if any) before any
  // item-bearing component is appended.
  private flushAnnotation(): void {
    if (this.pendingEnds === null || this.pendingChanges === null) return;
    const ends = this.pendingEnds;
    const changes = this.pendingChanges;
    this.pendingEnds = null;
    this.pendingChanges = null;
    if (ends.size === 0 && changes.size === 0) return;
    const endKeys = Array.from(ends);
    const chg = Array.from(changes.values());
    this.out.push({ kind: "annotationBoundary", boundary: AnnotationBoundaryMap.of(endKeys, chg) });
  }

  retain(n: number): void {
    if (n <= 0) return;
    this.flushAnnotation();
    const k = this.out.length - 1;
    if (k >= 0) {
      const last = this.out[k]!;
      if (last.kind === "retain") {
        this.out[k] = { kind: "retain", count: last.count + n };
        return;
      }
    }
    this.out.push({ kind: "retain", count: n });
  }

  characters(s: string): void {
    if (s === "") return;
    this.flushAnnotation();
    const k = this.out.length - 1;
    if (k >= 0) {
      const last = this.out[k]!;
      if (last.kind === "characters") {
        this.out[k] = { kind: "characters", text: last.text + s };
        return;
      }
    }
    this.out.push({ kind: "characters", text: s });
  }

  deleteCharacters(s: string): void {
    if (s === "") return;
    this.flushAnnotation();
    const k = this.out.length - 1;
    if (k >= 0) {
      const last = this.out[k]!;
      if (last.kind === "deleteCharacters") {
        this.out[k] = { kind: "deleteCharacters", text: last.text + s };
        return;
      }
    }
    this.out.push({ kind: "deleteCharacters", text: s });
  }

  elementStart(typ: string, attrs: Attributes): void {
    this.flushAnnotation();
    this.out.push({ kind: "elementStart", type: typ, attributes: attrs });
  }

  elementEnd(): void {
    this.flushAnnotation();
    this.out.push({ kind: "elementEnd" });
  }

  deleteElementStart(typ: string, attrs: Attributes): void {
    this.flushAnnotation();
    this.out.push({ kind: "deleteElementStart", type: typ, attributes: attrs });
  }

  deleteElementEnd(): void {
    this.flushAnnotation();
    this.out.push({ kind: "deleteElementEnd" });
  }

  replaceAttributes(oldAttrs: Attributes, newAttrs: Attributes): void {
    this.flushAnnotation();
    this.out.push({ kind: "replaceAttributes", oldAttributes: oldAttrs, newAttributes: newAttrs });
  }

  updateAttributes(u: AttributesUpdate): void {
    this.flushAnnotation();
    this.out.push({ kind: "updateAttributes", update: u });
  }

  finish(): DocOp {
    this.flushAnnotation();
    return new DocOp(this.out);
  }
}

// ---------------------------------------------------------------------------
// Top-level transform.
// ---------------------------------------------------------------------------

/**
 * Transform a concurrent client and server op into [clientPrime, serverPrime].
 * Throws Error on invalid input (e.g. mismatched lengths / incompatible ops).
 */
export function transform(clientOp: DocOp, serverOp: DocOp): [DocOp, DocOp] {
  // Decompose both operations into insertion (i) and noninsertion (n) parts.
  const [ci0, cn0] = decompose(clientOp);
  const [si0, sn0] = decompose(serverOp);

  // Four sub-transforms, structured as in Transformer.transform:
  //       ci0     cn0
  //   si0     si1     si2
  //       ci1     cn1
  //   sn0     sn1     sn2
  //       ci2     cn2
  const [ci1, si1] = insertionTransform(ci0, si0);
  const [ci2, sn1] = insertionNoninsertionTransform(ci1, sn0);
  const [si2, cn1] = insertionNoninsertionTransform(si1, cn0);
  const [cn2, sn2] = noninsertionTransform(cn1, sn1);

  const clientPrime = compose(ci2, cn2);
  const serverPrime = compose(si2, sn2);
  return [clientPrime, serverPrime];
}

// decompose splits op into an insertion-only operation and an insertion-free
// operation, such that compose(insertion, noninsertion) == op. Annotations go
// only into the non-insertion part.
function decompose(op: DocOp): [DocOp, DocOp] {
  const ins = new Builder();
  const non = new Builder();
  for (const c of op.components) {
    switch (c.kind) {
      case "retain":
        ins.retain(c.count);
        non.retain(c.count);
        break;
      case "characters":
        ins.characters(c.text);
        non.retain(runeLen(c.text));
        break;
      case "elementStart":
        ins.elementStart(c.type, c.attributes);
        non.retain(1);
        break;
      case "elementEnd":
        ins.elementEnd();
        non.retain(1);
        break;
      case "deleteCharacters":
        ins.retain(runeLen(c.text));
        non.deleteCharacters(c.text);
        break;
      case "deleteElementStart":
        ins.retain(1);
        non.deleteElementStart(c.type, c.attributes);
        break;
      case "deleteElementEnd":
        ins.retain(1);
        non.deleteElementEnd();
        break;
      case "replaceAttributes":
        ins.retain(1);
        non.replaceAttributes(c.oldAttributes, c.newAttributes);
        break;
      case "updateAttributes":
        ins.retain(1);
        non.updateAttributes(c.update);
        break;
      case "annotationBoundary":
        non.annotationBoundary(c.boundary);
        break;
    }
  }
  return [ins.finish(), non.finish()];
}

// ---------------------------------------------------------------------------
// positionTracker / relativePosition — tracks two cursors' positions relative to
// each other on the shared input document. Side 1 (sign +1) adds to position;
// side 2 (sign -1) subtracts. Each side's get() returns its own position relative
// to the other; a negative value means that side is behind.
// ---------------------------------------------------------------------------

class PositionTracker {
  position = 0;
}

class RelativePosition {
  private readonly t: PositionTracker;
  private readonly sign: number;
  constructor(t: PositionTracker, sign: number) {
    this.t = t;
    this.sign = sign;
  }
  increase(amount: number): void {
    this.t.position += this.sign * amount;
  }
  get(): number {
    return this.sign * this.t.position;
  }
}

// ---------------------------------------------------------------------------
// insertion × insertion transform (transform.go: InsertionTransformer.Target).
// ---------------------------------------------------------------------------

class InsTarget {
  out = new Builder();
  // set after construction
  other!: InsTarget;
  readonly rel: RelativePosition;
  constructor(rel: RelativePosition) {
    this.rel = rel;
  }

  retain(itemCount: number): void {
    const oldPos = this.rel.get();
    this.rel.increase(itemCount);
    if (this.rel.get() < 0) {
      // Still behind the other side: retain the whole range on both outputs.
      this.out.retain(itemCount);
      this.other.out.retain(itemCount);
    } else if (oldPos < 0) {
      // Was behind, now caught up: retain only the overlapping portion.
      this.out.retain(-oldPos);
      this.other.out.retain(-oldPos);
    }
    // else already ahead: emit nothing.
  }

  characters(chars: string): void {
    this.out.characters(chars);
    this.other.out.retain(runeLen(chars)); // other side skips over this side's new content
  }

  elementStart(typ: string, attrs: Attributes): void {
    this.out.elementStart(typ, attrs);
    this.other.out.retain(1);
  }

  elementEnd(): void {
    this.out.elementEnd();
    this.other.out.retain(1);
  }

  apply(c: Component): void {
    switch (c.kind) {
      case "retain":
        this.retain(c.count);
        break;
      case "characters":
        this.characters(c.text);
        break;
      case "elementStart":
        this.elementStart(c.type, c.attributes);
        break;
      case "elementEnd":
        this.elementEnd();
        break;
      default:
        throw new Error(`op: insertion transform: unexpected non-insertion component ${c.kind}`);
    }
  }
}

// insertionTransform transforms two insertion-only operations. The client is
// processed first within each step, so concurrent insertions at the same position
// place the client's content first in both outputs.
function insertionTransform(clientOp: DocOp, serverOp: DocOp): [DocOp, DocOp] {
  const pt = new PositionTracker();
  const client = new InsTarget(new RelativePosition(pt, 1));
  const server = new InsTarget(new RelativePosition(pt, -1));
  client.other = server;
  server.other = client;

  const cc = clientOp.components;
  const sc = serverOp.components;
  let ci = 0;
  let si = 0;
  while (ci < cc.length) {
    client.apply(cc[ci]!);
    ci++;
    while (client.rel.get() > 0) {
      if (si >= sc.length) {
        throw new Error("op: insertion transform: ran out of server components");
      }
      server.apply(sc[si]!);
      si++;
    }
  }
  while (si < sc.length) {
    server.apply(sc[si]!);
    si++;
  }
  return [client.out.finish(), server.out.finish()];
}

// ---------------------------------------------------------------------------
// insertion × noninsertion transform (transform_insnon.go).
// ---------------------------------------------------------------------------

// insertionNoninsertionTransform transforms an insertion-form operation against
// an insertion-free operation. It returns [insertionOp', noninsertionOp'].
//
// Mechanism: the noninsertion side, on reading a component, sets up a pending
// "range cache" (its effect) and resolves whatever range overlap is immediately
// available; the insertion side's retain then drives that cache to emit the
// noninsertion effect over the region it advances across. Insertions that land
// inside a deleted *element* (depth > 0) are absorbed. Deleted character runs do
// not absorb insertions.
function insertionNoninsertionTransform(insertionOp: DocOp, noninsertionOp: DocOp): [DocOp, DocOp] {
  const pt = new PositionTracker();
  const st = new InsNonState(new RelativePosition(pt, 1), new RelativePosition(pt, -1));

  const ic = insertionOp.components;
  const nc = noninsertionOp.components;
  let ii = 0;
  let ni = 0;
  while (ii < ic.length) {
    st.applyInsertion(ic[ii]!);
    ii++;
    while (st.insPos.get() > 0) {
      if (ni >= nc.length) {
        throw new Error("op: insertion-noninsertion transform: ran out of noninsertion components");
      }
      st.applyNoninsertion(nc[ni]!);
      ni++;
    }
  }
  while (ni < nc.length) {
    st.applyNoninsertion(nc[ni]!);
    ni++;
  }
  return [st.insOut.finish(), st.nonOut.finish()];
}

// InsNonState holds both outputs, both relative cursors, the element-deletion
// depth, and the noninsertion side's pending range cache.
class InsNonState {
  insOut = new Builder();
  nonOut = new Builder();
  depth = 0;
  // the noninsertion side's pending effect; defaults to resolveRetain.
  cache: (itemCount: number) => void;
  readonly insPos: RelativePosition;
  readonly nonPos: RelativePosition;

  constructor(insPos: RelativePosition, nonPos: RelativePosition) {
    this.insPos = insPos;
    this.nonPos = nonPos;
    this.cache = (n) => this.resolveRetain(n);
  }

  // resolveRetain is the default range cache: both outputs retain.
  private resolveRetain(itemCount: number): void {
    this.nonOut.retain(itemCount);
    this.insOut.retain(itemCount);
  }

  applyInsertion(c: Component): void {
    switch (c.kind) {
      case "retain": {
        const oldPos = this.insPos.get();
        this.insPos.increase(c.count);
        if (this.insPos.get() < 0) {
          this.cache(c.count);
        } else if (oldPos < 0) {
          this.cache(-oldPos);
        }
        break;
      }
      case "characters":
        if (this.depth > 0) {
          this.nonOut.deleteCharacters(c.text);
        } else {
          this.insOut.characters(c.text);
          this.nonOut.retain(runeLen(c.text));
        }
        break;
      case "elementStart":
        if (this.depth > 0) {
          this.nonOut.deleteElementStart(c.type, c.attributes);
        } else {
          this.insOut.elementStart(c.type, c.attributes);
          this.nonOut.retain(1);
        }
        break;
      case "elementEnd":
        if (this.depth > 0) {
          this.nonOut.deleteElementEnd();
        } else {
          this.insOut.elementEnd();
          this.nonOut.retain(1);
        }
        break;
      default:
        throw new Error(`op: insertion-noninsertion transform: unexpected component ${c.kind} in insertion op`);
    }
  }

  applyNoninsertion(c: Component): void {
    switch (c.kind) {
      case "retain":
        this.resolveRange(c.count, (n) => this.resolveRetain(n));
        this.cache = (n) => this.resolveRetain(n);
        break;
      case "deleteCharacters": {
        let chars = c.text;
        const cache = (itemCount: number): void => {
          this.nonOut.deleteCharacters(firstRunes(chars, itemCount));
          chars = restRunes(chars, itemCount);
        };
        if (this.resolveRange(runeLen(c.text), cache) >= 0) {
          this.cache = cache;
        }
        break;
      }
      case "deleteElementStart": {
        const typ = c.type;
        const attrs = c.attributes;
        const cache = (_itemCount: number): void => {
          this.nonOut.deleteElementStart(typ, attrs);
          this.depth++;
        };
        if (this.resolveRange(1, cache) === 0) {
          this.cache = cache;
        }
        break;
      }
      case "deleteElementEnd": {
        const cache = (_itemCount: number): void => {
          this.nonOut.deleteElementEnd();
          this.depth--;
        };
        if (this.resolveRange(1, cache) === 0) {
          this.cache = cache;
        }
        break;
      }
      case "replaceAttributes": {
        const oldA = c.oldAttributes;
        const newA = c.newAttributes;
        const cache = (_itemCount: number): void => {
          this.nonOut.replaceAttributes(oldA, newA);
          this.insOut.retain(1);
        };
        if (this.resolveRange(1, cache) === 0) {
          this.cache = cache;
        }
        break;
      }
      case "updateAttributes": {
        const u = c.update;
        const cache = (_itemCount: number): void => {
          this.nonOut.updateAttributes(u);
          this.insOut.retain(1);
        };
        if (this.resolveRange(1, cache) === 0) {
          this.cache = cache;
        }
        break;
      }
      case "annotationBoundary":
        this.nonOut.annotationBoundary(c.boundary);
        break;
      default:
        throw new Error(`op: insertion-noninsertion transform: unexpected component in noninsertion op`);
    }
  }

  // resolveRange advances the noninsertion cursor by size and resolves the freshly
  // created cache over whatever overlap is immediately available, returning the
  // portion resolved (>= 0) or -1 if the whole range was resolved here.
  private resolveRange(size: number, cache: (n: number) => void): number {
    const oldPosition = this.nonPos.get();
    this.nonPos.increase(size);
    if (this.nonPos.get() > 0) {
      if (oldPosition < 0) {
        cache(-oldPosition);
      }
      return -oldPosition;
    }
    cache(size);
    return -1;
  }
}

// ---------------------------------------------------------------------------
// noninsertion × noninsertion transform (transform_noninsertion.go).
// ---------------------------------------------------------------------------

// TransformError reports an incompatible pair of insertion-free operations.
class TransformError extends Error {}

const INCOMPATIBLE_MSG = "noninsertion transform: incompatible operations";

// noninsertionTransform transforms two insertion-free operations, resolving the
// client/server component table and the annotation algebra. Returns [clientOp',
// serverOp'].
function noninsertionTransform(clientOp: DocOp, serverOp: DocOp): [DocOp, DocOp] {
  const pt = new PositionTracker();
  const clientOut = new Builder();
  const serverOut = new Builder();
  const clientTracker = new AnnotationTracker(clientOut, "client");
  const serverTracker = new AnnotationTracker(serverOut, "server");
  clientTracker.other = serverTracker;
  serverTracker.other = clientTracker;

  const client = new NonTarget(clientOut, new RelativePosition(pt, 1), clientTracker);
  const server = new NonTarget(serverOut, new RelativePosition(pt, -1), serverTracker);
  client.other = server;
  server.other = client;
  client.rangeCache = new RetainCache(client);
  server.rangeCache = new RetainCache(server);

  const cc = clientOp.components;
  const sc = serverOp.components;
  let ci = 0;
  let si = 0;
  while (ci < cc.length) {
    client.apply(cc[ci]!);
    ci++;
    while (client.rel.get() > 0) {
      if (si >= sc.length) {
        throw new Error("op: noninsertion transform: ran out of server components");
      }
      server.apply(sc[si]!);
      si++;
    }
  }
  while (si < sc.length) {
    server.apply(sc[si]!);
    si++;
  }
  return [clientOut.finish(), serverOut.finish()];
}

// NonTarget processes one side of the noninsertion transform.
class NonTarget {
  other!: NonTarget;
  rangeCache!: RangeCache;
  depth = 0;
  readonly out: Builder;
  readonly rel: RelativePosition;
  readonly tracker: AnnotationTracker;

  constructor(out: Builder, rel: RelativePosition, tracker: AnnotationTracker) {
    this.out = out;
    this.rel = rel;
    this.tracker = tracker;
  }

  apply(c: Component): void {
    switch (c.kind) {
      case "retain":
        this.retain(c.count);
        break;
      case "deleteCharacters":
        this.deleteCharacters(c.text);
        break;
      case "deleteElementStart":
        this.deleteElementStart(c.type, c.attributes);
        break;
      case "deleteElementEnd":
        this.deleteElementEnd();
        break;
      case "replaceAttributes":
        this.replaceAttributes(c.oldAttributes, c.newAttributes);
        break;
      case "updateAttributes":
        this.updateAttributes(c.update);
        break;
      case "annotationBoundary":
        this.tracker.register(c.boundary);
        break;
      default:
        throw new TransformError("noninsertion transform: unexpected insertion component");
    }
  }

  private retain(n: number): void {
    this.resolveRange(n, (size, c) => c.resolveRetain(size));
    this.rangeCache = new RetainCache(this);
  }

  private deleteCharacters(chars: string): void {
    const res = this.resolveRange(runeLen(chars), (size, c) => {
      c.resolveDeleteCharacters(firstRunes(chars, size));
    });
    if (res >= 0) {
      this.rangeCache = new DeleteCharactersCache(this, restRunes(chars, res));
    }
  }

  private deleteElementStart(typ: string, attrs: Attributes): void {
    if (this.resolveRange(1, (_size, c) => c.resolveDeleteElementStart(typ, attrs)) === 0) {
      this.rangeCache = new DeleteElementStartCache(this, typ, attrs);
    }
  }

  private deleteElementEnd(): void {
    if (this.resolveRange(1, (_size, c) => c.resolveDeleteElementEnd()) === 0) {
      this.rangeCache = new DeleteElementEndCache(this);
    }
  }

  private replaceAttributes(oldA: Attributes, newA: Attributes): void {
    if (this.resolveRange(1, (_size, c) => c.resolveReplaceAttributes(oldA, newA)) === 0) {
      this.rangeCache = new ReplaceAttributesCache(this, oldA, newA);
    }
  }

  private updateAttributes(u: AttributesUpdate): void {
    if (this.resolveRange(1, (_size, c) => c.resolveUpdateAttributes(u)) === 0) {
      this.rangeCache = new UpdateAttributesCache(this, u);
    }
  }

  // resolveRange advances this target's cursor by size and resolves whatever
  // overlap is available against the OTHER target's pending range cache, returning
  // the resolved portion (>= 0) or -1 if the whole range resolved here.
  resolveRange(size: number, resolve: (size: number, cache: RangeCache) => void): number {
    const oldPosition = this.rel.get();
    this.rel.increase(size);
    if (this.rel.get() > 0) {
      if (oldPosition < 0) {
        resolve(-oldPosition, this.other.rangeCache);
      }
      return -oldPosition;
    }
    resolve(size, this.other.rangeCache);
    return -1;
  }

  syncAnnotations(): void {
    this.tracker.sync();
    this.other.tracker.sync();
  }

  doDeleteCharacters(chars: string): void {
    this.tracker.commenceDeletion();
    this.out.deleteCharacters(chars);
    this.tracker.concludeDeletion();
  }

  doDeleteElementStart(typ: string, attrs: Attributes): void {
    this.tracker.commenceDeletion();
    this.out.deleteElementStart(typ, attrs);
    this.tracker.concludeDeletion();
  }

  doDeleteElementEnd(): void {
    this.tracker.commenceDeletion();
    this.out.deleteElementEnd();
    this.tracker.concludeDeletion();
  }
}

// --- range caches: the client×server resolution table ---
//
// A target's rangeCache holds the effect of the component it last read. When the
// OTHER target reads a component, it resolves against this cache: the (cache type,
// incoming component type) pair selects the output. Within a cache, the owning
// target is `t`; otherTarget is `t.other`.

interface RangeCache {
  resolveRetain(itemCount: number): void;
  resolveDeleteCharacters(chars: string): void;
  resolveDeleteElementStart(typ: string, attrs: Attributes): void;
  resolveDeleteElementEnd(): void;
  resolveReplaceAttributes(oldA: Attributes, newA: Attributes): void;
  resolveUpdateAttributes(u: AttributesUpdate): void;
}

// incompatible* default methods reject the pairing. Concrete caches override the
// compatible cases and fall back to these.
function incompatibleRetain(): void {
  throw new TransformError(INCOMPATIBLE_MSG);
}
function incompatibleDeleteCharacters(): void {
  throw new TransformError(INCOMPATIBLE_MSG);
}
function incompatibleDeleteElementStart(): void {
  throw new TransformError(INCOMPATIBLE_MSG);
}
function incompatibleDeleteElementEnd(): void {
  throw new TransformError(INCOMPATIBLE_MSG);
}
function incompatibleReplaceAttributes(): void {
  throw new TransformError(INCOMPATIBLE_MSG);
}
function incompatibleUpdateAttributes(): void {
  throw new TransformError(INCOMPATIBLE_MSG);
}

// retainCache: the owner retained at this position; implements every pairing.
class RetainCache implements RangeCache {
  private readonly t: NonTarget;
  constructor(t: NonTarget) {
    this.t = t;
  }
  resolveRetain(itemCount: number): void {
    this.t.syncAnnotations();
    this.t.out.retain(itemCount);
    this.t.other.out.retain(itemCount);
  }
  resolveDeleteCharacters(chars: string): void {
    this.t.other.doDeleteCharacters(chars);
  }
  resolveDeleteElementStart(typ: string, attrs: Attributes): void {
    this.t.other.doDeleteElementStart(typ, attrs);
    this.t.other.depth++;
  }
  resolveDeleteElementEnd(): void {
    this.t.other.doDeleteElementEnd();
    this.t.other.depth--;
  }
  resolveReplaceAttributes(oldA: Attributes, newA: Attributes): void {
    this.t.syncAnnotations();
    this.t.out.retain(1);
    this.t.other.out.replaceAttributes(oldA, newA);
  }
  resolveUpdateAttributes(u: AttributesUpdate): void {
    this.t.syncAnnotations();
    this.t.out.retain(1);
    this.t.other.out.updateAttributes(u);
  }
}

// deleteCharactersCache: the owner deleted characters here.
class DeleteCharactersCache implements RangeCache {
  private readonly t: NonTarget;
  private characters: string;
  constructor(t: NonTarget, characters: string) {
    this.t = t;
    this.characters = characters;
  }
  resolveRetain(itemCount: number): void {
    this.t.doDeleteCharacters(firstRunes(this.characters, itemCount));
    this.characters = restRunes(this.characters, itemCount);
  }
  resolveDeleteCharacters(chars: string): void {
    this.characters = restRunes(this.characters, runeLen(chars)); // both deleted the same run
  }
  resolveDeleteElementStart = incompatibleDeleteElementStart;
  resolveDeleteElementEnd = incompatibleDeleteElementEnd;
  resolveReplaceAttributes = incompatibleReplaceAttributes;
  resolveUpdateAttributes = incompatibleUpdateAttributes;
}

// deleteElementStartCache: the owner deleted an element start here.
class DeleteElementStartCache implements RangeCache {
  private readonly t: NonTarget;
  private readonly typ: string;
  private readonly attrs: Attributes;
  constructor(t: NonTarget, typ: string, attrs: Attributes) {
    this.t = t;
    this.typ = typ;
    this.attrs = attrs;
  }
  resolveRetain(_itemCount: number): void {
    this.t.doDeleteElementStart(this.typ, this.attrs);
    this.t.depth++;
  }
  resolveDeleteElementStart(_typ: string, _attrs: Attributes): void {
    this.t.depth++;
    this.t.other.depth++; // both deleted the same element start
  }
  resolveReplaceAttributes(_oldA: Attributes, newA: Attributes): void {
    this.t.doDeleteElementStart(this.typ, newA);
    this.t.depth++;
  }
  resolveUpdateAttributes(u: AttributesUpdate): void {
    this.t.doDeleteElementStart(this.typ, this.attrs.updateWith(u));
    this.t.depth++;
  }
  resolveDeleteCharacters = incompatibleDeleteCharacters;
  resolveDeleteElementEnd = incompatibleDeleteElementEnd;
}

// deleteElementEndCache: the owner deleted an element end here.
class DeleteElementEndCache implements RangeCache {
  private readonly t: NonTarget;
  constructor(t: NonTarget) {
    this.t = t;
  }
  resolveRetain(_itemCount: number): void {
    this.t.doDeleteElementEnd();
    this.t.depth--;
  }
  resolveDeleteElementEnd(): void {
    this.t.depth--;
    this.t.other.depth--;
  }
  resolveDeleteCharacters = incompatibleDeleteCharacters;
  resolveDeleteElementStart = incompatibleDeleteElementStart;
  resolveReplaceAttributes = incompatibleReplaceAttributes;
  resolveUpdateAttributes = incompatibleUpdateAttributes;
}

// replaceAttributesCache: the owner replaced attributes here.
class ReplaceAttributesCache implements RangeCache {
  private readonly t: NonTarget;
  private readonly oldA: Attributes;
  private readonly newA: Attributes;
  constructor(t: NonTarget, oldA: Attributes, newA: Attributes) {
    this.t = t;
    this.oldA = oldA;
    this.newA = newA;
  }
  resolveRetain(_itemCount: number): void {
    this.t.syncAnnotations();
    this.t.out.replaceAttributes(this.oldA, this.newA);
    this.t.other.out.retain(1);
  }
  resolveDeleteElementStart(typ: string, _attrs: Attributes): void {
    this.t.other.doDeleteElementStart(typ, this.newA);
    this.t.other.depth++;
  }
  resolveReplaceAttributes(_oldA: Attributes, newA: Attributes): void {
    this.t.syncAnnotations();
    this.t.out.replaceAttributes(newA, this.newA);
    this.t.other.out.retain(1);
  }
  resolveUpdateAttributes(u: AttributesUpdate): void {
    this.t.syncAnnotations();
    this.t.out.replaceAttributes(this.oldA.updateWith(u), this.newA);
    this.t.other.out.retain(1);
  }
  resolveDeleteCharacters = incompatibleDeleteCharacters;
  resolveDeleteElementEnd = incompatibleDeleteElementEnd;
}

// updateAttributesCache: the owner updated attributes here.
class UpdateAttributesCache implements RangeCache {
  private readonly t: NonTarget;
  private readonly update: AttributesUpdate;
  constructor(t: NonTarget, update: AttributesUpdate) {
    this.t = t;
    this.update = update;
  }
  resolveRetain(_itemCount: number): void {
    this.t.syncAnnotations();
    this.t.out.updateAttributes(this.update);
    this.t.other.out.retain(1);
  }
  resolveDeleteElementStart(typ: string, attrs: Attributes): void {
    this.t.other.doDeleteElementStart(typ, attrs.updateWith(this.update));
    this.t.other.depth++;
  }
  resolveReplaceAttributes(oldA: Attributes, newA: Attributes): void {
    this.t.syncAnnotations();
    this.t.out.retain(1);
    this.t.other.out.replaceAttributes(oldA.updateWith(this.update), newA);
  }
  resolveUpdateAttributes(u: AttributesUpdate): void {
    this.t.syncAnnotations();
    // The owner's update wins for shared keys; its old values are adjusted to
    // reflect u (the other side's update) having been applied first.
    const updated = new Map<string, string | null>();
    for (const ch of u.updates) {
      updated.set(ch.name, ch.newValue);
    }
    const ownerKeys = new Set<string>();
    const changes: AttributeChange[] = [];
    for (const ch of this.update.updates) {
      ownerKeys.add(ch.name);
      let newOld = ch.oldValue;
      if (updated.has(ch.name)) {
        newOld = updated.get(ch.name)!;
      }
      changes.push({ name: ch.name, oldValue: newOld, newValue: ch.newValue });
    }
    const ownerUpdate = makeUpdate(changes);
    this.t.out.updateAttributes(ownerUpdate);
    // The other side keeps only the keys the owner did not touch.
    const excl: AttributeChange[] = [];
    for (const ch of u.updates) {
      if (!ownerKeys.has(ch.name)) {
        excl.push(ch);
      }
    }
    const otherUpdate = makeUpdate(excl);
    this.t.other.out.updateAttributes(otherUpdate);
  }
  resolveDeleteCharacters = incompatibleDeleteCharacters;
  resolveDeleteElementEnd = incompatibleDeleteElementEnd;
}

// makeUpdate builds an AttributesUpdate, mapping a build error to a TransformError
// (the inputs are validity-preserving by construction).
function makeUpdate(changes: AttributeChange[]): AttributesUpdate {
  try {
    return AttributesUpdate.of(changes);
  } catch (e) {
    throw new TransformError("noninsertion transform: " + (e instanceof Error ? e.message : String(e)));
  }
}

// ---------------------------------------------------------------------------
// annotation transform (transform_annotation.go).
// ---------------------------------------------------------------------------

type AnnSide = "client" | "server";

// valueUpdate is an (old, new) annotation value pair. A field of null is a real
// null value, distinct from the key being absent.
interface ValueUpdate {
  old: string | null;
  new: string | null;
}

// AnnotationTracker tracks annotation state for one side of a noninsertion
// transform and emits the boundary adjustments that keep both transformed
// operations' annotations consistent.
//
// Four maps, which must NOT be conflated:
//   - tracked:     updated eagerly when this side READS a boundary (register);
//     consulted by the opposing side's process decisions.
//   - active:      updated only when a boundary is COMMITTED to output; used solely
//     by commence/concludeDeletion.
//   - temporary:   per-deletion scratch saving the active state to restore.
//   - propagating: annotations committed since the last sync, awaiting carry into
//     a deletion region.
//
// temporary and propagating distinguish "key present with null value" from "key
// absent"; presence is tested with map.has(), the stored value may be null.
class AnnotationTracker {
  private tracked = new Map<string, ValueUpdate>();
  private active = new Map<string, ValueUpdate>();
  private temporary = new Map<string, ValueUpdate | null>();
  propagating = new Map<string, ValueUpdate | null>();
  other!: AnnotationTracker;
  private readonly out: Builder;
  private readonly side: AnnSide;

  constructor(out: Builder, side: AnnSide) {
    this.out = out;
    this.side = side;
  }

  // register records a read boundary into tracked, then runs the per-side
  // transform to emit the committed boundaries.
  register(m: AnnotationBoundaryMap): void {
    for (const k of m.endKeys) {
      this.tracked.delete(k);
    }
    for (const ch of m.changes) {
      this.tracked.set(ch.key, { old: ch.oldValue, new: ch.newValue });
    }
    this.process(m);
  }

  // commit writes a boundary to this side's output and updates active/propagating.
  private commit(m: AnnotationBoundaryMap): void {
    for (const key of m.endKeys) {
      if (!this.propagating.has(key)) {
        this.propagating.set(key, this.activePtr(key));
      }
      this.active.delete(key);
    }
    for (const ch of m.changes) {
      const old = this.active.get(ch.key);
      const hasOld = this.active.has(ch.key);
      if (!hasOld || !ptrEqual(old!.old, ch.oldValue) || !ptrEqual(old!.new, ch.newValue)) {
        if (!this.propagating.has(ch.key)) {
          this.propagating.set(ch.key, this.activePtr(ch.key));
        }
        this.active.set(ch.key, { old: ch.oldValue, new: ch.newValue });
      }
    }
    this.out.annotationBoundary(m);
  }

  // activePtr returns a copy of the active entry for key, or null if absent
  // (mirrors Java's propagating.put(key, active.get(key)), which may store null).
  private activePtr(key: string): ValueUpdate | null {
    const v = this.active.get(key);
    if (v !== undefined) {
      return { old: v.old, new: v.new };
    }
    return null;
  }

  sync(): void {
    this.propagating = new Map<string, ValueUpdate | null>();
  }

  // commenceDeletion emits the annotation changes needed to bring the deletion
  // point to its inherited value before the deleted content is written.
  commenceDeletion(): void {
    const other = this.other;
    const changes: AnnotationChange[] = [];
    for (const [key, updPtr] of other.propagating) {
      const forCombining = this.active.get(key);
      const hasForCombining = this.active.has(key);
      // Save current active value (possibly null) for restoration in conclude.
      if (hasForCombining) {
        this.temporary.set(key, { old: forCombining!.old, new: forCombining!.new });
      } else {
        this.temporary.set(key, null);
      }
      if (updPtr !== null) {
        let oldVal: string | null;
        if (hasForCombining) {
          oldVal = forCombining!.old;
        } else {
          const oa = other.active.get(key);
          if (oa !== undefined) {
            oldVal = oa.new;
          } else {
            oldVal = updPtr.old;
          }
        }
        changes.push({ key, oldValue: oldVal, newValue: updPtr.new });
      } else {
        const oa = other.active.get(key);
        if (oa !== undefined) {
          let newVal: string | null;
          if (hasForCombining) {
            newVal = forCombining!.new;
          } else {
            newVal = oa.old;
          }
          changes.push({ key, oldValue: oa.new, newValue: newVal });
        }
      }
    }
    this.commitChanges([], changes);
  }

  // concludeDeletion restores the annotation state saved in temporary after the
  // deleted content has been written.
  concludeDeletion(): void {
    const ends: string[] = [];
    const changes: AnnotationChange[] = [];
    for (const [key, updPtr] of this.temporary) {
      if (updPtr !== null) {
        changes.push({ key, oldValue: updPtr.old, newValue: updPtr.new });
      } else {
        ends.push(key);
      }
    }
    this.sync();
    this.commitChanges(ends, changes);
    this.temporary = new Map<string, ValueUpdate | null>();
  }

  // commitChanges builds a boundary from ends/changes and commits it.
  private commitChanges(ends: string[], changes: AnnotationChange[]): void {
    let m: AnnotationBoundaryMap;
    try {
      m = AnnotationBoundaryMap.of(ends, changes);
    } catch (e) {
      throw new TransformError(
        "noninsertion transform: invalid annotation boundary: " + (e instanceof Error ? e.message : String(e)),
      );
    }
    this.commit(m);
  }

  // process emits the transformed boundaries for a read map, writing to both this
  // side's and the opposing side's outputs. The two sides are asymmetric.
  private process(m: AnnotationBoundaryMap): void {
    if (this.side === "client") {
      this.clientProcess(m);
    } else {
      this.serverProcess(m);
    }
  }

  // clientProcess: the client always emits an end for each ended key; if the
  // server is tracking a key, the client's change uses the server's tracked new
  // value as its old, and the server emits the complementary boundary.
  private clientProcess(m: AnnotationBoundaryMap): void {
    const server = this.other;
    const clientEnds: string[] = [];
    const clientChanges: AnnotationChange[] = [];
    const serverEnds: string[] = [];
    const serverChanges: AnnotationChange[] = [];
    for (const key of m.endKeys) {
      clientEnds.push(key);
      const sv = server.tracked.get(key);
      if (sv !== undefined) {
        serverChanges.push({ key, oldValue: sv.old, newValue: sv.new });
      }
    }
    for (const ch of m.changes) {
      const sv = server.tracked.get(ch.key);
      if (sv !== undefined) {
        clientChanges.push({ key: ch.key, oldValue: sv.new, newValue: ch.newValue });
        serverEnds.push(ch.key);
      } else {
        clientChanges.push({ key: ch.key, oldValue: ch.oldValue, newValue: ch.newValue });
      }
    }
    this.commitChanges(clientEnds, clientChanges);
    server.commitChanges(serverEnds, serverChanges);
  }

  // serverProcess: the server emits an end for an ended key only when the client
  // is not tracking it; if the client is tracking, the client emits the change
  // instead and the server drops it.
  private serverProcess(m: AnnotationBoundaryMap): void {
    const client = this.other;
    const serverEnds: string[] = [];
    const serverChanges: AnnotationChange[] = [];
    const clientChanges: AnnotationChange[] = [];
    for (const key of m.endKeys) {
      const cv = client.tracked.get(key);
      if (cv !== undefined) {
        clientChanges.push({ key, oldValue: cv.old, newValue: cv.new });
      } else {
        serverEnds.push(key);
      }
    }
    for (const ch of m.changes) {
      const cv = client.tracked.get(ch.key);
      if (cv !== undefined) {
        clientChanges.push({ key: ch.key, oldValue: ch.newValue, newValue: cv.new });
      } else {
        serverChanges.push({ key: ch.key, oldValue: ch.oldValue, newValue: ch.newValue });
      }
    }
    this.commitChanges(serverEnds, serverChanges);
    client.commitChanges([], clientChanges);
  }
}
