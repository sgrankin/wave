// Shared data model for the Wave client: a faithful TypeScript port of the Go
// op/waveop/version/id data types. This module is the contract every other
// module builds on — the OT algebra (compose/transform), the codec, clientcc,
// and the transport. It carries the value types and their self-contained algebra
// (attribute/annotation maps, hashed version, ids); the DocOp-level algorithms
// (compose, transform, normalize, equal, invert) live in their own modules.
//
// Go references: internal/op/{component,document,attributes,annotations}.go,
// internal/waveop/{operation,delta}.go, internal/version/version.go,
// internal/id/{participant,id,uri}.go.

// ---------------------------------------------------------------------------
// Attributes — an immutable, name-sorted, unique set of XML attribute pairs.
// (Port of op.Attributes.)
// ---------------------------------------------------------------------------

export interface Attribute {
  readonly name: string;
  readonly value: string;
}

export class Attributes {
  // Sorted by name, names unique. Treat as read-only.
  readonly attrs: readonly Attribute[];

  private constructor(attrs: readonly Attribute[]) {
    this.attrs = attrs;
  }

  static empty(): Attributes {
    return new Attributes([]);
  }

  /** Build from a name→value record, validating and sorting by name. */
  static of(m: Record<string, string>): Attributes {
    const attrs: Attribute[] = [];
    for (const name of Object.keys(m)) {
      if (name === "") throw new Error("op: empty attribute name");
      attrs.push({ name, value: m[name]! });
    }
    attrs.sort((a, b) => cmpStr(a.name, b.name));
    return new Attributes(attrs);
  }

  /** Build from already-validated pairs (e.g. codec decode), sorting by name. */
  static fromPairs(pairs: readonly Attribute[]): Attributes {
    const attrs = pairs.slice().sort((a, b) => cmpStr(a.name, b.name));
    return new Attributes(attrs);
  }

  get length(): number {
    return this.attrs.length;
  }

  get(name: string): string | undefined {
    for (const a of this.attrs) {
      if (a.name === name) return a.value;
      if (a.name > name) break;
    }
    return undefined;
  }

  all(): Attribute[] {
    return this.attrs.slice();
  }

  equal(other: Attributes): boolean {
    if (this.attrs.length !== other.attrs.length) return false;
    for (let i = 0; i < this.attrs.length; i++) {
      const a = this.attrs[i]!;
      const b = other.attrs[i]!;
      if (a.name !== b.name || a.value !== b.value) return false;
    }
    return true;
  }

  /**
   * Apply an update: each change sets its attribute to newValue, or removes it
   * when newValue is null; attributes not named are unchanged. The change's
   * oldValue must match current state (compose-time compatibility check) — a
   * mismatch throws. (Port of AttributesImpl.updateWith.)
   */
  updateWith(u: AttributesUpdate): Attributes {
    const m = new Map<string, string>();
    for (const a of this.attrs) m.set(a.name, a.value);
    for (const c of u.updates) {
      const cur = m.get(c.name);
      const present = cur !== undefined;
      if (present) {
        if (c.oldValue === null || c.oldValue !== cur) {
          throw new Error(`updateWith: old value mismatch for attribute ${c.name}`);
        }
      } else if (c.oldValue !== null) {
        throw new Error(`updateWith: attribute ${c.name} expected present but absent`);
      }
      if (c.newValue === null) m.delete(c.name);
      else m.set(c.name, c.newValue);
    }
    const pairs: Attribute[] = [];
    for (const [name, value] of m) pairs.push({ name, value });
    return Attributes.fromPairs(pairs);
  }
}

// ---------------------------------------------------------------------------
// AttributesUpdate — name-sorted attribute mutations. (Port of op.AttributesUpdate.)
// A null oldValue means "was absent"; a null newValue means "remove".
// ---------------------------------------------------------------------------

export interface AttributeChange {
  readonly name: string;
  readonly oldValue: string | null;
  readonly newValue: string | null;
}

export class AttributesUpdate {
  readonly updates: readonly AttributeChange[]; // sorted by name, unique

  private constructor(updates: readonly AttributeChange[]) {
    this.updates = updates;
  }

  static empty(): AttributesUpdate {
    return new AttributesUpdate([]);
  }

  /** Build from changes, sorting by name and rejecting duplicate names. */
  static of(changes: readonly AttributeChange[]): AttributesUpdate {
    const cp = changes.slice().sort((a, b) => cmpStr(a.name, b.name));
    for (let i = 0; i < cp.length; i++) {
      if (cp[i]!.name === "") throw new Error("op: empty attribute name in update");
      if (i > 0 && cp[i - 1]!.name === cp[i]!.name) {
        throw new Error(`op: duplicate attribute name ${cp[i]!.name} in update`);
      }
    }
    return new AttributesUpdate(cp);
  }

  get length(): number {
    return this.updates.length;
  }

  all(): AttributeChange[] {
    return this.updates.slice();
  }

  equal(other: AttributesUpdate): boolean {
    if (this.updates.length !== other.updates.length) return false;
    for (let i = 0; i < this.updates.length; i++) {
      const a = this.updates[i]!;
      const b = other.updates[i]!;
      if (a.name !== b.name || a.oldValue !== b.oldValue || a.newValue !== b.newValue) return false;
    }
    return true;
  }

  /**
   * The update equivalent to applying this then u2: for a key changed by both,
   * (this.old, u2.new); u2's expected old must equal this's new (else throw).
   * Keys changed by only one side pass through. (Port of ImmutableUpdateMap.composeWith.)
   */
  composeWith(u2: AttributesUpdate): AttributesUpdate {
    const m = new Map<string, { old: string | null; new: string | null }>();
    for (const c of this.updates) m.set(c.name, { old: c.oldValue, new: c.newValue });
    for (const c of u2.updates) {
      const p = m.get(c.name);
      if (p !== undefined) {
        if (p.new !== c.oldValue) {
          throw new Error(`composeWith: old value mismatch for attribute ${c.name}`);
        }
        m.set(c.name, { old: p.old, new: c.newValue });
      } else {
        m.set(c.name, { old: c.oldValue, new: c.newValue });
      }
    }
    const changes: AttributeChange[] = [];
    for (const [name, p] of m) changes.push({ name, oldValue: p.old, newValue: p.new });
    changes.sort((a, b) => cmpStr(a.name, b.name));
    return new AttributesUpdate(changes);
  }

  /** The update that reverses this one (swap each old/new). */
  invert(): AttributesUpdate {
    const inv = this.updates.map((c) => ({ name: c.name, oldValue: c.newValue, newValue: c.oldValue }));
    return new AttributesUpdate(inv);
  }
}

// ---------------------------------------------------------------------------
// AnnotationBoundaryMap — annotation state change at a point: keys whose ranges
// end here, and keys whose values change here. (Port of op.AnnotationBoundaryMap.)
// ---------------------------------------------------------------------------

export interface AnnotationChange {
  readonly key: string;
  readonly oldValue: string | null;
  readonly newValue: string | null;
}

export class AnnotationBoundaryMap {
  readonly endKeys: readonly string[]; // sorted, unique
  readonly changes: readonly AnnotationChange[]; // sorted by key, unique

  private constructor(endKeys: readonly string[], changes: readonly AnnotationChange[]) {
    this.endKeys = endKeys;
    this.changes = changes;
  }

  static empty(): AnnotationBoundaryMap {
    return new AnnotationBoundaryMap([], []);
  }

  /** Build and validate (sort ends + changes; reject duplicates / shared keys / bad keys). */
  static of(endKeys: readonly string[], changes: readonly AnnotationChange[]): AnnotationBoundaryMap {
    const ends = endKeys.slice().sort(cmpStr);
    for (let i = 0; i < ends.length; i++) {
      validateAnnotationKey(ends[i]!);
      if (i > 0 && ends[i - 1] === ends[i]) throw new Error(`op: duplicate end key ${ends[i]}`);
    }
    const chg = changes.slice().sort((a, b) => cmpStr(a.key, b.key));
    for (let i = 0; i < chg.length; i++) {
      validateAnnotationKey(chg[i]!.key);
      if (i > 0 && chg[i - 1]!.key === chg[i]!.key) throw new Error(`op: duplicate change key ${chg[i]!.key}`);
    }
    // No key in both sets.
    let i = 0;
    let j = 0;
    while (i < ends.length && j < chg.length) {
      const e = ends[i]!;
      const c = chg[j]!.key;
      if (e === c) throw new Error(`op: key ${e} in both end and change sets`);
      else if (e < c) i++;
      else j++;
    }
    return new AnnotationBoundaryMap(ends, chg);
  }

  get empty(): boolean {
    return this.endKeys.length === 0 && this.changes.length === 0;
  }

  /** Swap each change's old/new (for inversion); end keys unaffected. */
  swap(): AnnotationBoundaryMap {
    const changes = this.changes.map((c) => ({ key: c.key, oldValue: c.newValue, newValue: c.oldValue }));
    return new AnnotationBoundaryMap(this.endKeys, changes);
  }

  equal(other: AnnotationBoundaryMap): boolean {
    if (this.endKeys.length !== other.endKeys.length || this.changes.length !== other.changes.length) return false;
    for (let i = 0; i < this.endKeys.length; i++) if (this.endKeys[i] !== other.endKeys[i]) return false;
    for (let i = 0; i < this.changes.length; i++) {
      const a = this.changes[i]!;
      const b = other.changes[i]!;
      if (a.key !== b.key || a.oldValue !== b.oldValue || a.newValue !== b.newValue) return false;
    }
    return true;
  }
}

function validateAnnotationKey(k: string): void {
  if (k === "") throw new Error("op: empty annotation key");
  if (k.includes("?") || k.includes("@")) throw new Error(`op: annotation key ${k} contains '?' or '@'`);
}

// ---------------------------------------------------------------------------
// Component — one element of a DocOp's ordered sequence (discriminated union).
// (Port of op.Component and its implementations.)
// ---------------------------------------------------------------------------

export type Component =
  | { readonly kind: "retain"; readonly count: number }
  | { readonly kind: "characters"; readonly text: string }
  | { readonly kind: "elementStart"; readonly type: string; readonly attributes: Attributes }
  | { readonly kind: "elementEnd" }
  | { readonly kind: "deleteCharacters"; readonly text: string }
  | { readonly kind: "deleteElementStart"; readonly type: string; readonly attributes: Attributes }
  | { readonly kind: "deleteElementEnd" }
  | { readonly kind: "replaceAttributes"; readonly oldAttributes: Attributes; readonly newAttributes: Attributes }
  | { readonly kind: "updateAttributes"; readonly update: AttributesUpdate }
  | { readonly kind: "annotationBoundary"; readonly boundary: AnnotationBoundaryMap };

/** Number of runes in s (Unicode code points), matching Go utf8.RuneCountInString. */
export function runeCount(s: string): number {
  let n = 0;
  for (const _ of s) n++;
  return n;
}

/** Existing document items the component consumes. (Port of op.inputItems.) */
export function inputItems(c: Component): number {
  switch (c.kind) {
    case "retain":
      return c.count;
    case "deleteCharacters":
      return runeCount(c.text);
    case "deleteElementStart":
    case "deleteElementEnd":
    case "replaceAttributes":
    case "updateAttributes":
      return 1;
    default: // characters, elementStart, elementEnd, annotationBoundary
      return 0;
  }
}

/** Items the component produces in the resulting document. (Port of op.outputItems.) */
export function outputItems(c: Component): number {
  switch (c.kind) {
    case "retain":
      return c.count;
    case "characters":
      return runeCount(c.text);
    case "elementStart":
    case "elementEnd":
    case "replaceAttributes":
    case "updateAttributes":
      return 1;
    default: // deleteCharacters, deleteElementStart, deleteElementEnd, annotationBoundary
      return 0;
  }
}

// ---------------------------------------------------------------------------
// DocOp — immutable ordered sequence of components. Algebra (compose/transform/
// normalize/equal/invert) lives in sibling modules to avoid import cycles.
// ---------------------------------------------------------------------------

export class DocOp {
  readonly components: readonly Component[];

  constructor(components: readonly Component[]) {
    this.components = components.slice();
  }

  static empty(): DocOp {
    return new DocOp([]);
  }

  get size(): number {
    return this.components.length;
  }

  inputLength(): number {
    let n = 0;
    for (const c of this.components) n += inputItems(c);
    return n;
  }

  outputLength(): number {
    let n = 0;
    for (const c of this.components) n += outputItems(c);
    return n;
  }

  /** Document length for a DocInitialization (== output length). */
  documentLength(): number {
    return this.outputLength();
  }

  /** Whether this is insertion-only (a document rather than a mutating op). */
  isInitialization(): boolean {
    for (const c of this.components) {
      switch (c.kind) {
        case "characters":
        case "elementStart":
        case "elementEnd":
        case "annotationBoundary":
          break;
        default:
          return false;
      }
    }
    return true;
  }
}

// ---------------------------------------------------------------------------
// HashedVersion — a version number paired with a history hash. The client never
// computes the hash chain (the server does); it stores and echoes the hash.
// (Port of version.HashedVersion.)
// ---------------------------------------------------------------------------

export class HashedVersion {
  readonly version: number;
  readonly historyHash: Uint8Array; // possibly empty (unsigned)

  constructor(version: number, historyHash: Uint8Array) {
    this.version = version;
    this.historyHash = historyHash;
  }

  static unsigned(version: number): HashedVersion {
    return new HashedVersion(version, new Uint8Array(0));
  }

  signed(): boolean {
    return this.historyHash.length > 0;
  }

  equal(other: HashedVersion): boolean {
    return this.version === other.version && bytesEqual(this.historyHash, other.historyHash);
  }

  /** Order by version, then by history hash treating bytes as SIGNED (matches Go). */
  compare(other: HashedVersion): number {
    if (this.version !== other.version) return this.version < other.version ? -1 : 1;
    const a = this.historyHash;
    const b = other.historyHash;
    const n = Math.min(a.length, b.length);
    for (let i = 0; i < n; i++) {
      if (a[i] !== b[i]) {
        const sa = (a[i]! << 24) >> 24; // to int8
        const sb = (b[i]! << 24) >> 24;
        return sa < sb ? -1 : 1;
      }
    }
    return a.length < b.length ? -1 : a.length > b.length ? 1 : 0;
  }
}

export function bytesEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

// ---------------------------------------------------------------------------
// Participant + WaveletName (Port of id.ParticipantID, id.WaveletName, id.uri.)
// A participant is the normalized lowercase "name@domain" address.
// ---------------------------------------------------------------------------

export type Participant = string;

/** Validate ('@' structure) and normalize (lowercase) an address. */
export function participant(address: string): Participant {
  const i = address.indexOf("@");
  if (i < 0) throw new Error(`id: participant address ${address} missing '@'`);
  if (i >= address.length - 1) throw new Error(`id: participant address ${address} missing domain`);
  if (address.indexOf("@", i + 1) >= 0) throw new Error(`id: participant address ${address} has multiple '@'`);
  return address.toLowerCase();
}

const WAVE_URI_SCHEME = "wave";

export class WaveletName {
  readonly waveDomain: string;
  readonly waveId: string;
  readonly waveletDomain: string;
  readonly waveletId: string;

  constructor(waveDomain: string, waveId: string, waveletDomain: string, waveletId: string) {
    this.waveDomain = waveDomain;
    this.waveId = waveId;
    this.waveletDomain = waveletDomain;
    this.waveletId = waveletId;
  }

  /** Modern 4-token serialization; matching wavelet domain elides to '~'. */
  serialize(): string {
    const wd = this.waveletDomain === this.waveDomain ? "~" : this.waveletDomain;
    return `${this.waveDomain}/${this.waveId}/${wd}/${this.waveletId}`;
  }

  static parse(s: string): WaveletName {
    if (s.endsWith("/")) throw new Error(`id: wavelet name ${s} has trailing '/'`);
    const t = s.split("/");
    if (t.length !== 4) throw new Error(`id: wavelet name ${s} must have 4 tokens`);
    const [waveDomain, waveLocal, waveletDomainTok, waveletLocal] = t as [string, string, string, string];
    if (waveletDomainTok === waveDomain) throw new Error(`id: wavelet name ${s} has un-normalised domains`);
    const waveletDomain = waveletDomainTok === "~" ? waveDomain : waveletDomainTok;
    return new WaveletName(waveDomain, waveLocal, waveletDomain, waveletLocal);
  }

  /** The wavelet URI that seeds the version-zero history hash (id.WaveletNameToURI). */
  uri(): string {
    const wavePrefix = this.waveletDomain !== this.waveDomain ? this.waveDomain + "!" : "";
    return `${WAVE_URI_SCHEME}://${this.waveletDomain}/${wavePrefix}${percentEncode(this.waveId)}/${percentEncode(this.waveletId)}`;
  }

  /** Version 0: history hash is the raw UTF-8 bytes of the wavelet URI (NO digest). */
  zeroVersion(): HashedVersion {
    return new HashedVersion(0, new TextEncoder().encode(this.uri()));
  }
}

// RFC 3986 path-segment unreserved + sub-delims + ":" + "@" pass through; every
// other byte (incl. UTF-8 continuation bytes) is %XX. (Port of id.percentEncode.)
const NOT_ESCAPED: boolean[] = (() => {
  const t: boolean[] = new Array(256).fill(false);
  const mark = (s: string) => {
    for (const ch of s) t[ch.charCodeAt(0)] = true;
  };
  mark("ABCDEFGHIJKLMNOPQRSTUVWXYZ");
  mark("abcdefghijklmnopqrstuvwxyz");
  mark("0123456789");
  mark(":@!$&'()*+,;=-._~");
  return t;
})();

function percentEncode(s: string): string {
  const bytes = new TextEncoder().encode(s);
  let clean = true;
  for (const b of bytes) if (!NOT_ESCAPED[b]) { clean = false; break; }
  if (clean) return s;
  const hex = "0123456789ABCDEF";
  let out = "";
  for (const b of bytes) {
    if (NOT_ESCAPED[b]) out += String.fromCharCode(b);
    else out += "%" + hex[b >> 4] + hex[b & 0xf];
  }
  return out;
}

// ---------------------------------------------------------------------------
// Wavelet-level operations. (Port of waveop.{Context,Operation,delta}.)
// ---------------------------------------------------------------------------

export const NO_TIMESTAMP = -1;

export interface Context {
  readonly creator: Participant;
  readonly timestamp: number;
  readonly versionIncrement: number;
  readonly hashedVersion: HashedVersion | null;
}

// UpdateContributorMethod, encoded on the wire as 0/1/2 (matches Go iota).
export const CONTRIBUTOR_ADD = 0;
export const CONTRIBUTOR_REMOVE = 1;
export const CONTRIBUTOR_NONE = 2;
export type ContributorMethod = 0 | 1 | 2;

export interface BlipContentOp {
  readonly ctx: Context;
  readonly contentOp: DocOp;
  readonly method: ContributorMethod;
}

export type Operation =
  | { readonly kind: "blip"; readonly blipId: string; readonly op: BlipContentOp }
  | { readonly kind: "addParticipant"; readonly ctx: Context; readonly participant: Participant }
  | { readonly kind: "removeParticipant"; readonly ctx: Context; readonly participant: Participant }
  | { readonly kind: "noOp"; readonly ctx: Context };

/** The context of any wavelet operation. */
export function opContext(o: Operation): Context {
  return o.kind === "blip" ? o.op.ctx : o.ctx;
}

export interface WaveletDelta {
  readonly author: Participant;
  readonly targetVersion: HashedVersion;
  readonly ops: readonly Operation[];
}

export function newWaveletDelta(author: Participant, targetVersion: HashedVersion, ops: readonly Operation[]): WaveletDelta {
  return { author, targetVersion, ops: ops.slice() };
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

/** String comparison by UTF-16 code unit (matches Go's byte-wise sort for ASCII keys). */
function cmpStr(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}
