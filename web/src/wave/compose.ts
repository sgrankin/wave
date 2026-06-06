// DocOp composition: the single DocOp equivalent to applying op1 then op2.
// A faithful port of the Go op.Compose state machine (internal/op/compose.go),
// which in turn ports the Java Composer. The composer walks op1 (the "pre" side)
// and op2 (the "post" side) in lockstep through a current target (a pre- or
// post-state with its payload) plus queued/active annotation state for each side.
//
// Output goes through a normalizing builder (inlined here — the Go builder in
// internal/op/builder.go) so the result never has adjacent retains/characters/
// deleteCharacters or adjacent annotation boundaries.

import { AnnotationBoundaryMap, Attributes, AttributesUpdate, DocOp, runeCount } from "./types.ts";
import type { AnnotationChange, Component } from "./types.ts";

// Compose returns the single DocOp equivalent to applying op1 then op2. op2 must
// be valid against the document op1 produces: op1's output length must equal
// op2's input length. (Ports op.Compose / Composer.compose.)
export function compose(op1: DocOp, op2: DocOp): DocOp {
  const got = op1.outputLength();
  const want = op2.inputLength();
  if (got !== want) {
    throw new Error(`op: cannot compose: op1 output length ${got} != op2 input length ${want}`);
  }
  const c = new Composer();
  return c.run(op1, op2);
}

// valueUpdate is an (old, new) annotation value pair, like the Java ValueUpdate.
interface ValueUpdate {
  old: string | null;
  new: string | null;
}

// targetKind enumerates the composer's current target state. Pre-states consume
// op1 (the first operation) components; post-states consume op2 (the second).
type TargetKind =
  | "defaultPre"
  | "retainPre"
  | "deleteCharsPre"
  | "retainPost"
  | "charsPost"
  | "elementStartPost"
  | "elementEndPost"
  | "replaceAttrsPost"
  | "updateAttrsPost"
  | "finisherPost";

// String comparison by code unit (matches Go's byte-wise string sort for the
// ASCII keys used here, and types.ts's internal cmpStr).
function cmpStr(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function isPost(k: TargetKind): boolean {
  switch (k) {
    case "retainPost":
    case "charsPost":
    case "elementStartPost":
    case "elementEndPost":
    case "replaceAttrsPost":
    case "updateAttrsPost":
    case "finisherPost":
      return true;
    default:
      return false;
  }
}

// ---------------------------------------------------------------------------
// Normalizing builder (port of internal/op/builder.go). Merges adjacent
// retains/characters/deleteCharacters, drops zero-width pieces, and coalesces
// consecutive annotation boundaries into one.
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

  // flushAnnotation emits the accumulated annotation boundary (if any) as a single
  // component before any item-bearing component is appended. The pending state
  // already keeps the end/change key sets disjoint, so only sorting is needed.
  private flushAnnotation(): void {
    if (this.pendingEnds === null || this.pendingChanges === null) return;
    const ends = this.pendingEnds;
    const changes = this.pendingChanges;
    this.pendingEnds = null;
    this.pendingChanges = null;
    if (ends.size === 0 && changes.size === 0) return;
    const endKeys = Array.from(ends).sort(cmpStr);
    const changeList = Array.from(changes.values()).sort((a, b) => cmpStr(a.key, b.key));
    this.out.push({ kind: "annotationBoundary", boundary: AnnotationBoundaryMap.of(endKeys, changeList) });
  }

  retain(n: number): void {
    if (n <= 0) return;
    this.flushAnnotation();
    const last = this.out[this.out.length - 1];
    if (last !== undefined && last.kind === "retain") {
      this.out[this.out.length - 1] = { kind: "retain", count: last.count + n };
      return;
    }
    this.out.push({ kind: "retain", count: n });
  }

  characters(s: string): void {
    if (s === "") return;
    this.flushAnnotation();
    const last = this.out[this.out.length - 1];
    if (last !== undefined && last.kind === "characters") {
      this.out[this.out.length - 1] = { kind: "characters", text: last.text + s };
      return;
    }
    this.out.push({ kind: "characters", text: s });
  }

  deleteCharacters(s: string): void {
    if (s === "") return;
    this.flushAnnotation();
    const last = this.out[this.out.length - 1];
    if (last !== undefined && last.kind === "deleteCharacters") {
      this.out[this.out.length - 1] = { kind: "deleteCharacters", text: last.text + s };
      return;
    }
    this.out.push({ kind: "deleteCharacters", text: s });
  }

  elementStart(type: string, attributes: Attributes): void {
    this.flushAnnotation();
    this.out.push({ kind: "elementStart", type, attributes });
  }

  elementEnd(): void {
    this.flushAnnotation();
    this.out.push({ kind: "elementEnd" });
  }

  deleteElementStart(type: string, attributes: Attributes): void {
    this.flushAnnotation();
    this.out.push({ kind: "deleteElementStart", type, attributes });
  }

  deleteElementEnd(): void {
    this.flushAnnotation();
    this.out.push({ kind: "deleteElementEnd" });
  }

  replaceAttributes(oldAttributes: Attributes, newAttributes: Attributes): void {
    this.flushAnnotation();
    this.out.push({ kind: "replaceAttributes", oldAttributes, newAttributes });
  }

  updateAttributes(update: AttributesUpdate): void {
    this.flushAnnotation();
    this.out.push({ kind: "updateAttributes", update });
  }

  // finish emits any trailing annotation boundary and returns the built DocOp.
  finish(): DocOp {
    this.flushAnnotation();
    return new DocOp(this.out);
  }
}

// ---------------------------------------------------------------------------
// Composer state machine.
// ---------------------------------------------------------------------------

class Composer {
  private readonly out = new Builder();

  private readonly preAnn = new Map<string, ValueUpdate>();
  private readonly postAnn = new Map<string, ValueUpdate>();
  private preQueue: AnnotationBoundaryMap[] = [];
  private postQueue: AnnotationBoundaryMap[] = [];

  private kind: TargetKind = "defaultPre";
  // payloads (interpreted per kind):
  private count = 0; // retainPre, retainPost
  private chars = ""; // charsPost, deleteCharsPre
  private elemType = ""; // elementStartPost
  private attrs: Attributes = Attributes.empty(); // elementStartPost
  private oldAttrs: Attributes = Attributes.empty(); // replaceAttrsPost
  private newAttrs: Attributes = Attributes.empty(); // replaceAttrsPost
  private update: AttributesUpdate = AttributesUpdate.empty(); // updateAttrsPost

  run(op1: DocOp, op2: DocOp): DocOp {
    const comps1 = op1.components;
    const comps2 = op2.components;
    let i = 0;
    let j = 0;
    while (i < comps1.length) {
      this.applyPre(comps1[i]!);
      i++;
      while (isPost(this.kind)) {
        if (j >= comps2.length) {
          throw new Error("op: compose: op2 too short for op1 output");
        }
        this.applyPost(comps2[j]!);
        j++;
      }
    }
    if (j < comps2.length) {
      this.kind = "finisherPost";
      while (j < comps2.length) {
        this.applyPost(comps2[j]!);
        j++;
      }
    }
    this.flushBoth();
    return this.out.finish();
  }

  // --- annotation queue handling (ports the pre/postAnnotationQueue unqueue) ---

  private flushPre(): void {
    for (const m of this.preQueue) this.preUnqueue(m);
    this.preQueue = [];
  }

  private flushPost(): void {
    for (const m of this.postQueue) this.postUnqueue(m);
    this.postQueue = [];
  }

  private flushBoth(): void {
    this.flushPre();
    this.flushPost();
  }

  // preUnqueue flushes one of op1's queued boundaries (Composer.preAnnotationQueue).
  private preUnqueue(m: AnnotationBoundaryMap): void {
    const ends: string[] = [];
    const changes: AnnotationChange[] = [];
    for (const key of m.endKeys) {
      const post = this.postAnn.get(key);
      if (post !== undefined) {
        changes.push({ key, oldValue: post.old, newValue: post.new });
      } else {
        ends.push(key);
      }
      this.preAnn.delete(key);
    }
    for (const ch of m.changes) {
      let newVal = ch.newValue;
      const post = this.postAnn.get(ch.key);
      if (post !== undefined) {
        newVal = post.new;
      }
      changes.push({ key: ch.key, oldValue: ch.oldValue, newValue: newVal });
      this.preAnn.set(ch.key, { old: ch.oldValue, new: ch.newValue });
    }
    this.emitBoundary(ends, changes);
  }

  // postUnqueue flushes one of op2's queued boundaries (Composer.postAnnotationQueue).
  private postUnqueue(m: AnnotationBoundaryMap): void {
    const ends: string[] = [];
    const changes: AnnotationChange[] = [];
    for (const key of m.endKeys) {
      const pre = this.preAnn.get(key);
      if (pre !== undefined) {
        changes.push({ key, oldValue: pre.old, newValue: pre.new });
      } else {
        ends.push(key);
      }
      this.postAnn.delete(key);
    }
    for (const ch of m.changes) {
      let oldVal = ch.oldValue;
      const pre = this.preAnn.get(ch.key);
      if (pre !== undefined) {
        oldVal = pre.old;
      }
      changes.push({ key: ch.key, oldValue: oldVal, newValue: ch.newValue });
      this.postAnn.set(ch.key, { old: ch.oldValue, new: ch.newValue });
    }
    this.emitBoundary(ends, changes);
  }

  private emitBoundary(ends: string[], changes: AnnotationChange[]): void {
    if (ends.length === 0 && changes.length === 0) return;
    let m: AnnotationBoundaryMap;
    try {
      m = AnnotationBoundaryMap.of(ends, changes);
    } catch (e) {
      throw new Error(`op: compose: produced invalid annotation boundary: ${(e as Error).message}`);
    }
    this.out.annotationBoundary(m);
  }

  // --- applyPre: feed an op1 component while in a pre-state ---

  private applyPre(comp: Component): void {
    // Base PreTarget behavior: op1 deletions pass straight through; boundaries queue.
    switch (comp.kind) {
      case "deleteCharacters":
        this.flushPre();
        this.out.deleteCharacters(comp.text);
        return;
      case "deleteElementStart":
        this.flushPre();
        this.out.deleteElementStart(comp.type, comp.attributes);
        return;
      case "deleteElementEnd":
        this.flushPre();
        this.out.deleteElementEnd();
        return;
      case "annotationBoundary":
        this.preQueue.push(comp.boundary);
        return;
    }

    switch (this.kind) {
      case "defaultPre":
        this.defaultPre(comp);
        return;
      case "retainPre":
        this.retainPre(comp);
        return;
      case "deleteCharsPre":
        this.deleteCharsPre(comp);
        return;
      default:
        throw new Error("op: compose: internal error: applyPre in post-state");
    }
  }

  private defaultPre(comp: Component): void {
    switch (comp.kind) {
      case "retain":
        this.kind = "retainPost";
        this.count = comp.count;
        return;
      case "characters":
        this.kind = "charsPost";
        this.chars = comp.text;
        return;
      case "elementStart":
        this.kind = "elementStartPost";
        this.elemType = comp.type;
        this.attrs = comp.attributes;
        return;
      case "elementEnd":
        this.kind = "elementEndPost";
        return;
      case "replaceAttributes":
        this.kind = "replaceAttrsPost";
        this.oldAttrs = comp.oldAttributes;
        this.newAttrs = comp.newAttributes;
        return;
      case "updateAttributes":
        this.kind = "updateAttrsPost";
        this.update = comp.update;
        return;
      default:
        throw new Error("op: compose: unexpected op1 component");
    }
  }

  private retainPre(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        if (comp.count <= this.count) {
          this.out.retain(comp.count);
          this.cancelRetainPre(comp.count);
        } else {
          this.out.retain(this.count);
          this.kind = "retainPost";
          this.count = comp.count - this.count;
        }
        return;
      case "characters": {
        const n = runeCount(comp.text);
        if (n <= this.count) {
          this.out.characters(comp.text);
          this.cancelRetainPre(n);
        } else {
          this.out.characters(firstRunes(comp.text, this.count));
          this.kind = "charsPost";
          this.chars = restRunes(comp.text, this.count);
        }
        return;
      }
      case "elementStart":
        this.out.elementStart(comp.type, comp.attributes);
        this.cancelRetainPre(1);
        return;
      case "elementEnd":
        this.out.elementEnd();
        this.cancelRetainPre(1);
        return;
      case "replaceAttributes":
        this.out.replaceAttributes(comp.oldAttributes, comp.newAttributes);
        this.cancelRetainPre(1);
        return;
      case "updateAttributes":
        this.out.updateAttributes(comp.update);
        this.cancelRetainPre(1);
        return;
      default:
        throw new Error("op: compose: unexpected op1 component");
    }
  }

  private cancelRetainPre(size: number): void {
    if (size < this.count) {
      this.count -= size;
    } else {
      this.kind = "defaultPre";
    }
  }

  private deleteCharsPre(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        if (comp.count <= runeCount(this.chars)) {
          this.out.deleteCharacters(firstRunes(this.chars, comp.count));
          this.cancelDeleteCharsPre(comp.count);
        } else {
          this.out.deleteCharacters(this.chars);
          this.kind = "retainPost";
          this.count = comp.count - runeCount(this.chars);
        }
        return;
      case "characters": {
        const n = runeCount(comp.text);
        if (n <= runeCount(this.chars)) {
          this.cancelDeleteCharsPre(n); // insert cancels an equal amount of delete
        } else {
          this.kind = "charsPost";
          this.chars = restRunes(comp.text, runeCount(this.chars));
        }
        return;
      }
      default:
        throw new Error("op: compose: illegal composition (insert/attr over deleted chars)");
    }
  }

  private cancelDeleteCharsPre(size: number): void {
    if (size < runeCount(this.chars)) {
      this.chars = restRunes(this.chars, size);
    } else {
      this.kind = "defaultPre";
    }
  }

  // --- applyPost: feed an op2 component while in a post-state ---

  private applyPost(comp: Component): void {
    // Base PostTarget behavior: op2 insertions pass straight through; boundaries queue.
    switch (comp.kind) {
      case "characters":
        this.flushPost();
        this.out.characters(comp.text);
        return;
      case "elementStart":
        this.flushPost();
        this.out.elementStart(comp.type, comp.attributes);
        return;
      case "elementEnd":
        this.flushPost();
        this.out.elementEnd();
        return;
      case "annotationBoundary":
        this.postQueue.push(comp.boundary);
        return;
    }

    switch (this.kind) {
      case "retainPost":
        this.retainPost(comp);
        return;
      case "charsPost":
        this.charsPost(comp);
        return;
      case "elementStartPost":
        this.elementStartPost(comp);
        return;
      case "elementEndPost":
        this.elementEndPost(comp);
        return;
      case "replaceAttrsPost":
        this.replaceAttrsPost(comp);
        return;
      case "updateAttrsPost":
        this.updateAttrsPost(comp);
        return;
      case "finisherPost":
        throw new Error("op: compose: op2 has trailing non-insertion after op1 ended");
      default:
        throw new Error("op: compose: internal error: applyPost in pre-state");
    }
  }

  private retainPost(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        if (comp.count <= this.count) {
          this.out.retain(comp.count);
          this.cancelRetainPost(comp.count);
        } else {
          this.out.retain(this.count);
          this.kind = "retainPre";
          this.count = comp.count - this.count;
        }
        return;
      case "deleteCharacters": {
        const n = runeCount(comp.text);
        if (n <= this.count) {
          this.out.deleteCharacters(comp.text);
          this.cancelRetainPost(n);
        } else {
          this.out.deleteCharacters(firstRunes(comp.text, this.count));
          this.kind = "deleteCharsPre";
          this.chars = restRunes(comp.text, this.count);
        }
        return;
      }
      case "deleteElementStart":
        this.out.deleteElementStart(comp.type, comp.attributes);
        this.cancelRetainPost(1);
        return;
      case "deleteElementEnd":
        this.out.deleteElementEnd();
        this.cancelRetainPost(1);
        return;
      case "replaceAttributes":
        this.out.replaceAttributes(comp.oldAttributes, comp.newAttributes);
        this.cancelRetainPost(1);
        return;
      case "updateAttributes":
        this.out.updateAttributes(comp.update);
        this.cancelRetainPost(1);
        return;
      default:
        throw new Error("op: compose: unexpected op2 component");
    }
  }

  private cancelRetainPost(size: number): void {
    if (size < this.count) {
      this.count -= size;
    } else {
      this.kind = "defaultPre";
    }
  }

  private charsPost(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        if (comp.count <= runeCount(this.chars)) {
          this.out.characters(firstRunes(this.chars, comp.count));
          this.cancelCharsPost(comp.count);
        } else {
          this.out.characters(this.chars);
          this.kind = "retainPre";
          this.count = comp.count - runeCount(this.chars);
        }
        return;
      case "deleteCharacters": {
        const n = runeCount(comp.text);
        if (n <= runeCount(this.chars)) {
          this.cancelCharsPost(n); // inserted chars deleted again cancel out
        } else {
          this.kind = "deleteCharsPre";
          this.chars = restRunes(comp.text, runeCount(this.chars));
        }
        return;
      }
      default:
        throw new Error("op: compose: illegal composition (delete-element/attr over inserted chars)");
    }
  }

  private cancelCharsPost(size: number): void {
    if (size < runeCount(this.chars)) {
      this.chars = restRunes(this.chars, size);
    } else {
      this.kind = "defaultPre";
    }
  }

  private elementStartPost(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        this.out.elementStart(this.elemType, this.attrs);
        this.retainTail(comp.count);
        return;
      case "deleteElementStart":
        this.kind = "defaultPre"; // insert then delete cancels
        return;
      case "replaceAttributes":
        this.out.elementStart(this.elemType, comp.newAttributes);
        this.kind = "defaultPre";
        return;
      case "updateAttributes":
        this.out.elementStart(this.elemType, this.attrs.updateWith(comp.update));
        this.kind = "defaultPre";
        return;
      default:
        throw new Error("op: compose: illegal composition on inserted element start");
    }
  }

  private elementEndPost(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        this.out.elementEnd();
        this.retainTail(comp.count);
        return;
      case "deleteElementEnd":
        this.kind = "defaultPre"; // insert then delete cancels
        return;
      default:
        throw new Error("op: compose: illegal composition on inserted element end");
    }
  }

  private replaceAttrsPost(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        this.out.replaceAttributes(this.oldAttrs, this.newAttrs);
        this.retainTail(comp.count);
        return;
      case "deleteElementStart":
        this.out.deleteElementStart(comp.type, this.oldAttrs);
        this.kind = "defaultPre";
        return;
      case "replaceAttributes":
        this.out.replaceAttributes(this.oldAttrs, comp.newAttributes);
        this.kind = "defaultPre";
        return;
      case "updateAttributes":
        this.out.replaceAttributes(this.oldAttrs, this.newAttrs.updateWith(comp.update));
        this.kind = "defaultPre";
        return;
      default:
        throw new Error("op: compose: illegal composition on replaceAttributes");
    }
  }

  private updateAttrsPost(comp: Component): void {
    this.flushBoth();
    switch (comp.kind) {
      case "retain":
        this.out.updateAttributes(this.update);
        this.retainTail(comp.count);
        return;
      case "deleteElementStart":
        this.out.deleteElementStart(comp.type, comp.attributes.updateWith(this.update.invert()));
        this.kind = "defaultPre";
        return;
      case "replaceAttributes":
        this.out.replaceAttributes(comp.oldAttributes.updateWith(this.update.invert()), comp.newAttributes);
        this.kind = "defaultPre";
        return;
      case "updateAttributes":
        this.out.updateAttributes(this.update.composeWith(comp.update));
        this.kind = "defaultPre";
        return;
      default:
        throw new Error("op: compose: illegal composition on updateAttributes");
    }
  }

  // retainTail handles the single-item attribute/element post-targets after the
  // item is emitted: if the driving retain covered more than this 1 item, the
  // remainder becomes a pending pre-side retain; otherwise return to default.
  private retainTail(retainCount: number): void {
    if (retainCount > 1) {
      this.kind = "retainPre";
      this.count = retainCount - 1;
    } else {
      this.kind = "defaultPre";
    }
  }
}

// rune helpers: characters are counted and split by rune (Unicode code point),
// matching Go's []rune / utf8.RuneCountInString.

function firstRunes(s: string, n: number): string {
  let out = "";
  let i = 0;
  for (const r of s) {
    if (i >= n) break;
    out += r;
    i++;
  }
  return out;
}

function restRunes(s: string, n: number): string {
  let out = "";
  let i = 0;
  for (const r of s) {
    if (i >= n) out += r;
    i++;
  }
  return out;
}
