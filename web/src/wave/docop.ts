// DocOp-level helpers: the canonical-form algorithms that sit on top of the
// shared data model. A faithful port of internal/op/document.go (Normalize,
// Invert, Equal, componentEqual) plus the normalizing builder from
// internal/op/builder.go.
//
// The original Java is the source of truth; this mirrors the Go reference's
// control flow and edge cases exactly.

import { AnnotationBoundaryMap, DocOp } from "./types.ts";
import type { AnnotationChange, Component } from "./types.ts";

// ---------------------------------------------------------------------------
// builder — accumulates components into a normalized DocOp: adjacent retains,
// characters, and deleteCharacters are merged; zero-width pieces are dropped;
// and consecutive annotation boundaries are coalesced into one (so the output
// never has adjacent boundaries, per the well-formedness rules). This is the
// stand-in for OperationNormalizer + DocOpBuffer.
//
// Normalization affects only the canonical form of the output, not its meaning:
// applying a normalized op and its un-normalized equivalent yields identical
// documents. We do not reproduce the Java normalizer's annotation-state elision
// (dropping redundant value changes) — unnecessary without byte-level Java
// interop and irrelevant to convergence.
// ---------------------------------------------------------------------------

// pendingAnnotation accumulates annotation boundary changes until the next
// item-bearing component forces them to be emitted.
interface PendingAnnotation {
  readonly ends: Set<string>;
  readonly changes: Map<string, AnnotationChange>;
}

class Builder {
  private out: Component[] = [];
  // A not-yet-emitted, accumulating annotation boundary.
  private pending: PendingAnnotation | null = null;

  private annotationBoundary(m: AnnotationBoundaryMap): void {
    if (m.empty) return;
    if (this.pending === null) {
      this.pending = { ends: new Set<string>(), changes: new Map<string, AnnotationChange>() };
    }
    for (const k of m.endKeys) {
      this.pending.changes.delete(k);
      this.pending.ends.add(k);
    }
    for (const c of m.changes) {
      this.pending.ends.delete(c.key);
      this.pending.changes.set(c.key, c);
    }
  }

  // flushAnnotation emits the accumulated annotation boundary (if any) as a
  // single component before any item-bearing component is appended. The pending
  // state already guarantees the boundary's invariants (end and change key sets
  // are kept disjoint as entries are added, keys come from validated maps), so
  // only sorting is needed — AnnotationBoundaryMap.of re-sorts/validates.
  private flushAnnotation(): void {
    if (this.pending === null) return;
    const p = this.pending;
    this.pending = null;
    if (p.ends.size === 0 && p.changes.size === 0) return;
    const ends = Array.from(p.ends);
    const changes = Array.from(p.changes.values());
    this.out.push({
      kind: "annotationBoundary",
      boundary: AnnotationBoundaryMap.of(ends, changes),
    });
  }

  private retain(n: number): void {
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

  private characters(s: string): void {
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

  private deleteCharacters(s: string): void {
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

  // add feeds an existing component through the normalizing builder.
  add(c: Component): void {
    switch (c.kind) {
      case "retain":
        this.retain(c.count);
        break;
      case "characters":
        this.characters(c.text);
        break;
      case "elementStart":
        this.flushAnnotation();
        this.out.push(c);
        break;
      case "elementEnd":
        this.flushAnnotation();
        this.out.push(c);
        break;
      case "deleteCharacters":
        this.deleteCharacters(c.text);
        break;
      case "deleteElementStart":
        this.flushAnnotation();
        this.out.push(c);
        break;
      case "deleteElementEnd":
        this.flushAnnotation();
        this.out.push(c);
        break;
      case "replaceAttributes":
        this.flushAnnotation();
        this.out.push(c);
        break;
      case "updateAttributes":
        this.flushAnnotation();
        this.out.push(c);
        break;
      case "annotationBoundary":
        this.annotationBoundary(c.boundary);
        break;
    }
  }

  // finish emits any trailing annotation boundary and returns the built DocOp.
  finish(): DocOp {
    this.flushAnnotation();
    return new DocOp(this.out);
  }
}

// ---------------------------------------------------------------------------
// Public helpers
// ---------------------------------------------------------------------------

/**
 * Returns the canonical form of d: adjacent retains/characters/deleteCharacters
 * merged, zero-width pieces dropped, consecutive annotation boundaries
 * coalesced. (Port of DocOp.Normalize.)
 */
export function normalize(d: DocOp): DocOp {
  const b = new Builder();
  for (const c of d.components) b.add(c);
  return b.finish();
}

/**
 * Returns the operation that exactly undoes d: applying d then invert(d) to a
 * document leaves it unchanged. It is a per-component mapping; component order
 * is preserved. (Port of Invert.)
 */
export function invert(d: DocOp): DocOp {
  const out: Component[] = new Array(d.components.length);
  for (let i = 0; i < d.components.length; i++) {
    const c = d.components[i]!;
    switch (c.kind) {
      case "retain":
        out[i] = c;
        break;
      case "characters":
        out[i] = { kind: "deleteCharacters", text: c.text };
        break;
      case "elementStart":
        out[i] = { kind: "deleteElementStart", type: c.type, attributes: c.attributes };
        break;
      case "elementEnd":
        out[i] = { kind: "deleteElementEnd" };
        break;
      case "deleteCharacters":
        out[i] = { kind: "characters", text: c.text };
        break;
      case "deleteElementStart":
        out[i] = { kind: "elementStart", type: c.type, attributes: c.attributes };
        break;
      case "deleteElementEnd":
        out[i] = { kind: "elementEnd" };
        break;
      case "replaceAttributes":
        out[i] = { kind: "replaceAttributes", oldAttributes: c.newAttributes, newAttributes: c.oldAttributes };
        break;
      case "updateAttributes":
        out[i] = { kind: "updateAttributes", update: c.update.invert() };
        break;
      case "annotationBoundary":
        out[i] = { kind: "annotationBoundary", boundary: c.boundary.swap() };
        break;
    }
  }
  return new DocOp(out);
}

/** Reports whether two components are equal. (Port of componentEqual.) */
export function componentEqual(a: Component, b: Component): boolean {
  switch (a.kind) {
    case "retain":
      return b.kind === "retain" && a.count === b.count;
    case "characters":
      return b.kind === "characters" && a.text === b.text;
    case "elementStart":
      return b.kind === "elementStart" && a.type === b.type && a.attributes.equal(b.attributes);
    case "elementEnd":
      return b.kind === "elementEnd";
    case "deleteCharacters":
      return b.kind === "deleteCharacters" && a.text === b.text;
    case "deleteElementStart":
      return b.kind === "deleteElementStart" && a.type === b.type && a.attributes.equal(b.attributes);
    case "deleteElementEnd":
      return b.kind === "deleteElementEnd";
    case "replaceAttributes":
      return (
        b.kind === "replaceAttributes" &&
        a.oldAttributes.equal(b.oldAttributes) &&
        a.newAttributes.equal(b.newAttributes)
      );
    case "updateAttributes":
      return b.kind === "updateAttributes" && a.update.equal(b.update);
    case "annotationBoundary":
      return b.kind === "annotationBoundary" && a.boundary.equal(b.boundary);
  }
}

/**
 * Reports whether a and b are equivalent operations (equal after
 * normalization). This is the comparison used to check OT convergence.
 * (Port of DocOp.Equal.)
 */
export function docOpEqual(a: DocOp, b: DocOp): boolean {
  const ca = normalize(a).components;
  const cb = normalize(b).components;
  if (ca.length !== cb.length) return false;
  for (let i = 0; i < ca.length; i++) {
    if (!componentEqual(ca[i]!, cb[i]!)) return false;
  }
  return true;
}
