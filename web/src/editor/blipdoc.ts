// The rich blip document model for the editor: project a blip's content DocOp
// (Wave's <body>/<line> line-container with style annotations) into paragraphs of
// styled text spans, and build the content ops that edit it (insert/delete text,
// split a line). This is the pure, DOM-free core — the BlipView component renders
// the projection and turns user actions into these ops. Correctness is checked
// against the ported composer (compose(content, command) == expected).
//
// Wave's body model (spec 10 §document structure): a document stores a <body>
// containing <line/> elements (empty paragraph *markers*); the text of a paragraph
// follows its <line> as siblings up to the next <line>. Formatting is a parallel
// annotation layer — style/<prop> key ranges over the item stream — NOT nested
// tags. A "doc offset" below is an item position in the DocOp stream: each
// character is one item (counted in runes), each element start/end is one item;
// annotation boundaries are zero-width.

import { AnnotationBoundaryMap, Attributes, AttributesUpdate, runeCount } from "../wave/types.ts";
import type { Component, DocOp } from "../wave/types.ts";

const BODY = "body";
const LINE = "line";
const REPLY = "reply"; // inline-reply anchor element (conv.tagReply)
const IMAGE = "image"; // inline image/attachment element (conv.tagImage)
const ATTACHMENT_ATTR = "attachment";
const STYLE_PREFIX = "style/";
// LINK_KEY is the annotation whose value is the URL a manual link points to (a link
// on arbitrary text, distinct from render-time auto-linkification of literal URLs).
// Modeled as a single annotation key carrying the href, like OG Wave's link/manual.
const LINK_KEY = "link/manual";

/** A contiguous run of text sharing the same active style annotations. */
export interface Span {
  readonly text: string;
  /** CSS property → value, from style/<prop> annotations active over this run. */
  readonly styles: Readonly<Record<string, string>>;
  /** The href of a manual link (link/manual annotation) active over this run, if any
   *  (absent when there is no link). A link adds no doc items (a zero-width annotation),
   *  so caret/offset mapping is unaffected; the renderer wraps the run in an <a>. */
  readonly link?: string;
}

/**
 * An inline item within a paragraph, in document order: a styled text run, or an
 * inline element (reply anchor / image). `offset` is the doc offset of the item's
 * FIRST doc item (a text run: its first rune; a widget: its elementStart). An empty
 * widget element (`<reply></reply>`, `<image></image>`) occupies exactly 2 doc items
 * (elementStart + elementEnd) — this is what lets a widget sit MID-text and keep the
 * caret↔offset mapping exact (the renderer interleaves it, the DOM walk counts it as
 * 2 items). The widget item-count is declared to the DOM walk via a data-doc-items
 * attribute, so it never has to recount.
 */
export type InlineItem =
  | { readonly kind: "text"; readonly span: Span; readonly offset: number }
  | { readonly kind: "reply"; readonly id: string; readonly offset: number }
  | { readonly kind: "image"; readonly attachment: string; readonly offset: number };

/** A paragraph: a <line> marker (its type/indent) and the text that follows it. */
export interface Paragraph {
  /** The <line t="..."> value (e.g. "h1", "li"), or null for a plain line. */
  readonly lineType: string | null;
  /** The <line i="..."> indent level (0 if unset). */
  readonly indent: number;
  /** Doc offset of the <line> element-start item, or null for text before any line. */
  readonly lineOffset: number | null;
  /** Doc offset where this paragraph's text begins (just after the <line> marker). */
  readonly textStart: number;
  /** Text length of the paragraph in runes (text items only; widgets are NOT counted
   *  here — they are accounted for via their doc offsets and `paragraphEnd`). */
  readonly textLength: number;
  /** Doc offset just past the LAST item of this paragraph: textStart + textLength +
   *  2*widgetCount (each inline widget = 2 items). Use this — NOT textStart+textLength —
   *  as the paragraph's end when a paragraph can contain mid-text/trailing widgets. */
  readonly paragraphEnd: number;
  /** The paragraph's inline content (text runs + reply/image widgets) in document
   *  order. THE source of truth; spans/anchors/images below are derived views. */
  readonly items: readonly InlineItem[];
  /** Derived: the text runs (kind:"text"), in order. */
  readonly spans: readonly Span[];
  /** Derived: inline-reply thread ids anchored within this paragraph, in document
   *  order (the <reply id> markers). */
  readonly anchors: readonly string[];
  /** Derived: inline image attachment ids (<image attachment=...>), in order. */
  readonly images: readonly string[];
}

/** The projected, renderable view of a blip body. */
export interface BlipProjection {
  readonly paragraphs: readonly Paragraph[];
  /** Total doc item length (== content.documentLength()). */
  readonly length: number;
}

interface MutParagraph {
  lineType: string | null;
  indent: number;
  lineOffset: number | null;
  textStart: number;
  textLength: number;
  widgetCount: number; // inline widgets (reply/image) in this paragraph; each = 2 doc items
  items: InlineItem[]; // ordered inline content (text runs + widgets); source of truth
}

/**
 * project walks a blip content DocInitialization into paragraphs of styled spans.
 * It is lenient: a document with no <body>/<line> (e.g. a flat-text blip — just
 * Characters) projects to a single plain paragraph, so legacy flat blips render
 * too. Non-style annotations are tracked but only style/<prop> keys surface (as
 * CSS) on spans.
 */
export function project(content: DocOp): BlipProjection {
  const paras: MutParagraph[] = [];
  const active = new Map<string, string>(); // annotation key → value (null/cleared keys removed)
  const stack: string[] = [];
  let pos = 0;
  let cur: MutParagraph | null = null;

  const ensureParagraph = (): MutParagraph => {
    if (cur === null) {
      // Text before any <line> (flat blip, or leading text): implicit plain paragraph.
      cur = { lineType: null, indent: 0, lineOffset: null, textStart: pos, textLength: 0, widgetCount: 0, items: [] };
      paras.push(cur);
    }
    return cur;
  };

  for (const c of content.components) {
    switch (c.kind) {
      case "elementStart": {
        if (c.type === LINE) {
          cur = {
            lineType: c.attributes.get("t") ?? null,
            indent: parseIndent(c.attributes),
            lineOffset: pos,
            textStart: pos + 1, // text begins after the (empty) <line> marker's start+end
            textLength: 0,
            widgetCount: 0,
            items: [],
          };
          paras.push(cur);
        } else if (c.type === REPLY) {
          // An inline-reply anchor: record it on the current paragraph at its doc
          // offset (pos, the elementStart). It occupies 2 doc items (start+end), so
          // the DOM caret mapping counts it; text after it stays in this paragraph.
          const p = ensureParagraph();
          p.items.push({ kind: "reply", id: c.attributes.get("id") ?? "", offset: pos });
          p.widgetCount += 1;
        } else if (c.type === IMAGE) {
          // An inline image: same 2-item, exact-offset treatment as a reply anchor.
          const p = ensureParagraph();
          p.items.push({ kind: "image", attachment: c.attributes.get(ATTACHMENT_ATTR) ?? "", offset: pos });
          p.widgetCount += 1;
        }
        stack.push(c.type);
        pos += 1;
        break;
      }
      case "elementEnd": {
        const top = stack.pop();
        pos += 1;
        if (top === LINE && cur !== null) cur.textStart = pos; // text starts after </line>
        break;
      }
      case "characters": {
        const p = ensureParagraph();
        const styles = currentStyles(active);
        const link = active.get(LINK_KEY); // the href active over this run, if any
        appendTextItem(p, c.text, styles, link, pos); // record the run at its doc offset
        p.textLength += runeCount(c.text);
        pos += runeCount(c.text);
        break;
      }
      case "annotationBoundary": {
        for (const ch of c.boundary.changes) {
          if (ch.newValue === null) active.delete(ch.key);
          else active.set(ch.key, ch.newValue);
        }
        for (const key of c.boundary.endKeys) active.delete(key);
        break; // zero-width
      }
      default:
        // Deletions/retains/attribute ops never appear in an initialization; ignore
        // defensively (a malformed content op would surface elsewhere).
        break;
    }
  }
  void BODY; // body element is just a container; tracked via the stack
  return { paragraphs: paras.map(finalizeParagraph), length: pos };
}

// finalizeParagraph converts the mutable build paragraph into the public Paragraph,
// deriving the spans/anchors/images convenience views from the ordered items[] (the
// source of truth) and computing paragraphEnd. Each widget contributes 2 doc items.
function finalizeParagraph(m: MutParagraph): Paragraph {
  const spans: Span[] = [];
  const anchors: string[] = [];
  const images: string[] = [];
  for (const it of m.items) {
    if (it.kind === "text") spans.push(it.span);
    else if (it.kind === "reply") anchors.push(it.id);
    else images.push(it.attachment);
  }
  return {
    lineType: m.lineType,
    indent: m.indent,
    lineOffset: m.lineOffset,
    textStart: m.textStart,
    textLength: m.textLength,
    paragraphEnd: m.textStart + m.textLength + 2 * m.widgetCount,
    items: m.items,
    spans,
    anchors,
    images,
  };
}

function parseIndent(attrs: Attributes): number {
  const v = attrs.get("i");
  if (v === undefined) return 0;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

function currentStyles(active: Map<string, string>): Record<string, string> {
  const styles: Record<string, string> = {};
  for (const [key, value] of active) {
    if (key.startsWith(STYLE_PREFIX)) styles[key.slice(STYLE_PREFIX.length)] = value;
  }
  return styles;
}

// appendTextItem appends a styled text run at doc offset `offset`, coalescing into the
// previous item ONLY if it is also a text run with the same styles AND the same link
// href. A widget item, a style change, or a link boundary between two runs therefore
// breaks the run, preserving document order.
function appendTextItem(
  p: MutParagraph,
  text: string,
  styles: Record<string, string>,
  link: string | undefined,
  offset: number,
): void {
  const last = p.items[p.items.length - 1];
  if (
    last !== undefined &&
    last.kind === "text" &&
    last.span.link === link &&
    sameStyles(last.span.styles, styles)
  ) {
    p.items[p.items.length - 1] = {
      kind: "text",
      span: mkSpan(last.span.text + text, last.span.styles, last.span.link),
      offset: last.offset, // keep the run's start offset
    };
  } else {
    p.items.push({ kind: "text", span: mkSpan(text, styles, link), offset });
  }
}

// mkSpan builds a Span, omitting `link` entirely when there is none so a plain run is
// exactly { text, styles } (keeps projections minimal and equality with non-link fixtures).
function mkSpan(text: string, styles: Readonly<Record<string, string>>, link: string | undefined): Span {
  return link === undefined ? { text, styles } : { text, styles, link };
}

function sameStyles(a: Readonly<Record<string, string>>, b: Readonly<Record<string, string>>): boolean {
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (const k of ka) if (a[k] !== b[k]) return false;
  return true;
}

// --- caret mapping ---

/**
 * caretToOffset maps a paragraph index + in-paragraph rune offset to a doc offset.
 * TEXT-ONLY: it ignores inline widgets (it clamps to textLength), so it must NOT be
 * used for caret mapping in paragraphs that can contain reply/image elements — the DOM
 * walk (domToOffset/docItemsBefore) is the item-aware mapping. Retained for the blipdoc
 * unit tests (no production callers).
 */
export function caretToOffset(proj: BlipProjection, para: number, textOffset: number): number {
  const p = proj.paragraphs[para];
  if (p === undefined) return proj.length;
  return p.textStart + Math.max(0, Math.min(textOffset, p.textLength));
}

/** paragraphText returns the concatenated text of a paragraph (text items in order). */
export function paragraphText(p: Paragraph): string {
  let s = "";
  for (const it of p.items) if (it.kind === "text") s += it.span.text;
  return s;
}

// --- command builders (return content-op components; input length == content.documentLength()) ---

function retain(n: number): Component[] {
  return n > 0 ? [{ kind: "retain", count: n }] : [];
}

/** insertText builds the content op inserting text at doc offset `at`. */
export function insertText(content: DocOp, at: number, text: string): Component[] {
  const len = content.documentLength();
  const a = clamp(at, 0, len);
  return [...retain(a), { kind: "characters", text }, ...retain(len - a)];
}

/**
 * deleteText builds the content op deleting the runes in [from, to) (text only —
 * the range must not span element items). The exact deleted text is read from the
 * projection (DeleteCharacters must echo the document).
 */
export function deleteText(content: DocOp, from: number, to: number): Component[] {
  const len = content.documentLength();
  const a = clamp(from, 0, len);
  const b = clamp(to, a, len);
  if (b === a) return [];
  const text = textBetween(content, a, b);
  return [...retain(a), { kind: "deleteCharacters", text }, ...retain(len - b)];
}

/**
 * replaceText builds the content op that replaces the text in [from, to) with
 * `text` (text-only range). Subsumes insert (from==to), delete-range (text==""),
 * and replace. Throws if the range spans a non-character item.
 */
export function replaceText(content: DocOp, from: number, to: number, text: string): Component[] {
  const len = content.documentLength();
  const a = clamp(from, 0, len);
  const b = clamp(to, a, len);
  const out: Component[] = [...retain(a)];
  if (b > a) {
    const del = textBetween(content, a, b);
    if (del !== "") out.push({ kind: "deleteCharacters", text: del });
  }
  if (text !== "") out.push({ kind: "characters", text });
  out.push(...retain(len - b));
  return out;
}

/**
 * splitLineAt deletes the selection [from, to) (if any) and inserts an empty
 * <line/> marker at `from` — the Enter command. Throws if the selection spans a
 * non-character item.
 */
export function splitLineAt(content: DocOp, from: number, to: number, attributes: Attributes): Component[] {
  const len = content.documentLength();
  const a = clamp(from, 0, len);
  const b = clamp(to, a, len);
  const out: Component[] = [...retain(a)];
  if (b > a) {
    const del = textBetween(content, a, b);
    if (del !== "") out.push({ kind: "deleteCharacters", text: del });
  }
  out.push({ kind: "elementStart", type: LINE, attributes }, { kind: "elementEnd" });
  out.push(...retain(len - b));
  return out;
}

/** splitLine builds the content op inserting an empty <line/> marker at `at`. */
export function splitLine(content: DocOp, at: number, attributes: Attributes): Component[] {
  const len = content.documentLength();
  const a = clamp(at, 0, len);
  return [
    ...retain(a),
    { kind: "elementStart", type: LINE, attributes },
    { kind: "elementEnd" },
    ...retain(len - a),
  ];
}

/**
 * deleteLineMarker builds the content op removing the empty <line/> marker at
 * lineOffset (its ElementStart+ElementEnd, 2 items), merging the paragraph it
 * begins into the previous one. The marker's type/indent must match the document
 * (DeleteElementStart echoes the attributes), so they are passed from the
 * projected paragraph. NOTE: only the t/i attributes are reconstructed — a line
 * carrying other attributes would not match (acceptable for the current line set).
 */
export function deleteLineMarker(content: DocOp, lineOffset: number, lineType: string | null, indent: number): Component[] {
  const len = content.documentLength();
  const a = clamp(lineOffset, 0, len);
  return [
    ...retain(a),
    { kind: "deleteElementStart", type: LINE, attributes: lineAttributes(lineType, indent) },
    { kind: "deleteElementEnd" },
    ...retain(len - a - 2),
  ];
}

/**
 * deleteInlineElement builds the content op removing an inline element (a <reply> or
 * <image>) at `offset` — its ElementStart+ElementEnd, 2 items. The DeleteElementStart
 * must echo the element's exact tag + attributes (the reply's id / the image's
 * attachment) or compose() rejects it, so they are passed from the projected item.
 */
export function deleteInlineElement(content: DocOp, offset: number, type: string, attributes: Attributes): Component[] {
  const len = content.documentLength();
  const a = clamp(offset, 0, len);
  return [
    ...retain(a),
    { kind: "deleteElementStart", type, attributes },
    { kind: "deleteElementEnd" },
    ...retain(len - a - 2),
  ];
}

/** lineAttributes reconstructs a <line>'s attributes from its type and indent. */
export function lineAttributes(lineType: string | null, indent: number): Attributes {
  const m: Record<string, string> = {};
  if (lineType !== null) m.t = lineType;
  if (indent > 0) m.i = String(indent);
  return Attributes.of(m);
}

/**
 * textBetween extracts the document's character items in [from, to) (used to build
 * a DeleteCharacters payload). Errors if the range covers a non-character item.
 */
export function textBetween(content: DocOp, from: number, to: number): string {
  let pos = 0;
  let out = "";
  for (const c of content.components) {
    if (pos >= to) break;
    switch (c.kind) {
      case "characters": {
        const rs = [...c.text];
        for (const r of rs) {
          if (pos >= from && pos < to) out += r;
          pos += 1;
        }
        break;
      }
      case "elementStart":
      case "elementEnd": {
        if (pos >= from && pos < to) {
          throw new Error(`blipdoc: delete range [${from},${to}) spans a non-character item at ${pos}`);
        }
        pos += 1;
        break;
      }
      default:
        break; // annotation boundaries are zero-width
    }
  }
  return out;
}

function clamp(n: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(n, hi));
}

// --- annotation range scanning ---

/**
 * scanAnnotationKey walks a content DocInitialization tracking the active value
 * of a single annotation key K, calling `cb` at each position where K's value
 * changes (including position 0 with the initial null). Returns the final active
 * value and the segments map: an array of {start, end, value} for each constant
 * K-value run. The shared helper behind setAnnotationRange / rangeAnnotation (and so
 * the style + link range builders/queries that wrap them).
 */
interface KSegment {
  start: number; // doc item offset where this value begins
  value: string | null;
}

function scanAnnotationKey(content: DocOp, key: string): KSegment[] {
  const segments: KSegment[] = [{ start: 0, value: null }];
  let pos = 0;
  let curValue: string | null = null;

  for (const c of content.components) {
    switch (c.kind) {
      case "annotationBoundary": {
        // Check endKeys
        for (const k of c.boundary.endKeys) {
          if (k === key) {
            curValue = null;
            segments.push({ start: pos, value: null });
          }
        }
        // Check changes
        for (const ch of c.boundary.changes) {
          if (ch.key === key) {
            curValue = ch.newValue;
            segments.push({ start: pos, value: ch.newValue });
          }
        }
        break; // zero-width
      }
      case "retain":
        pos += c.count;
        break;
      case "characters":
        pos += runeCount(c.text);
        break;
      case "elementStart":
      case "elementEnd":
        pos += 1;
        break;
      default:
        break;
    }
  }
  void curValue; // curValue is the final active value; segments encode all transitions
  return segments;
}

/**
 * valueAt returns the active value of K at position `pos` given the K-segments
 * (as returned by scanAnnotationKey). The segments array is sorted by start
 * position; we find the last segment whose start <= pos.
 */
function valueAt(segments: KSegment[], pos: number): string | null {
  let v: string | null = null;
  for (const seg of segments) {
    if (seg.start > pos) break;
    v = seg.value;
  }
  return v;
}

/**
 * setAnnotationRange returns content-op components that set annotation key K to
 * `value` (or remove it when `value` is null) over the text range [from, to).
 * Annotation oldValues match the document's actual values at each boundary, so
 * compose() accepts the op without throwing.
 *
 * For each interior segment of [from, to) where the document has a different
 * K-value, we emit a boundary that re-opens the key with the new value (with
 * the correct oldValue for that segment). At `to` we restore whatever value the
 * document had there. The generic core behind setStyleRange / clearStyleRange /
 * setLink / clearLink — value=null reproduces the clear path exactly.
 */
function setAnnotationRange(
  content: DocOp,
  from: number,
  to: number,
  K: string,
  value: string | null,
): Component[] {
  const len = content.documentLength();
  const a = clamp(from, 0, len);
  const b = clamp(to, a, len);
  if (a === b) return [...retain(len)];

  const segments = scanAnnotationKey(content, K);

  const out: Component[] = [];

  // retain up to `a`
  out.push(...retain(a));

  // open boundary at `a`: change from current K-value to `value`
  const vAtA = valueAt(segments, a);
  if (vAtA !== value) {
    out.push({
      kind: "annotationBoundary",
      boundary: AnnotationBoundaryMap.of([], [{ key: K, oldValue: vAtA, newValue: value }]),
    });
  }

  // for each segment transition point strictly inside (a, b), emit a retain + boundary
  let prevPos = a;
  for (const seg of segments) {
    const p = seg.start;
    if (p <= a) continue; // before or at `a`
    if (p >= b) break; // at or after `b`
    // Segment transition at p inside (a, b): retain from prevPos to p, then re-set key
    out.push(...retain(p - prevPos));
    prevPos = p;
    // oldValue at p is seg.value (the new value the document has here)
    // We must only emit a boundary if the key value is not already `value`
    if (seg.value !== value) {
      out.push({
        kind: "annotationBoundary",
        boundary: AnnotationBoundaryMap.of([], [{ key: K, oldValue: seg.value, newValue: value }]),
      });
    }
  }

  // retain from prevPos to `b`
  out.push(...retain(b - prevPos));

  // close boundary at `b`: restore the document's K-value at `b`
  const vAtB = valueAt(segments, b);
  if (vAtB === value) {
    // Document already has `value` at `b`; annotation continues naturally — no close.
  } else if (vAtB === null) {
    // Restore to null → endKey terminates the range.
    out.push({
      kind: "annotationBoundary",
      boundary: AnnotationBoundaryMap.of([K], []),
    });
  } else {
    // Restore to a different non-null value.
    out.push({
      kind: "annotationBoundary",
      boundary: AnnotationBoundaryMap.of([], [{ key: K, oldValue: value, newValue: vAtB }]),
    });
  }

  // retain to end
  out.push(...retain(len - b));

  return out;
}

/**
 * setStyleRange returns content-op components that set style/<prop> = value over the
 * text range [from, to). A thin wrapper over setAnnotationRange on the style/<prop> key.
 */
export function setStyleRange(content: DocOp, from: number, to: number, prop: string, value: string): Component[] {
  return setAnnotationRange(content, from, to, STYLE_PREFIX + prop, value);
}

/**
 * clearStyleRange returns content-op components that remove style/<prop> over
 * [from, to). Equivalent to setAnnotationRange on the style/<prop> key with value=null.
 */
export function clearStyleRange(content: DocOp, from: number, to: number, prop: string): Component[] {
  return setAnnotationRange(content, from, to, STYLE_PREFIX + prop, null);
}

/**
 * setLink returns content-op components that make [from, to) a manual link to `url`
 * (a link/manual annotation carrying the href). The link adds no doc items, so the
 * caret/offset mapping is unchanged; the renderer wraps the run in an <a>.
 */
export function setLink(content: DocOp, from: number, to: number, url: string): Component[] {
  return setAnnotationRange(content, from, to, LINK_KEY, url);
}

/** clearLink removes the manual link annotation over [from, to). */
export function clearLink(content: DocOp, from: number, to: number): Component[] {
  return setAnnotationRange(content, from, to, LINK_KEY, null);
}

/**
 * rangeLink queries the active manual-link href over [from, to): the single href if
 * the whole range carries the same one, "mixed" if it varies, or null if none.
 */
export function rangeLink(content: DocOp, from: number, to: number): string | null | "mixed" {
  return rangeAnnotation(content, from, to, LINK_KEY);
}

/**
 * setLineType returns content-op components that change the `t` attribute of the
 * <line> element-start at `lineOffset` (e.g. plain↔h1↔h2↔li). Uses an
 * updateAttributes component. oldType/newType null means the attribute is absent.
 */
export function setLineType(
  content: DocOp,
  lineOffset: number,
  oldType: string | null,
  newType: string | null,
): Component[] {
  const len = content.documentLength();
  const a = clamp(lineOffset, 0, len);
  return [
    ...retain(a),
    {
      kind: "updateAttributes",
      update: AttributesUpdate.of([{ name: "t", oldValue: oldType, newValue: newType }]),
    },
    ...retain(len - a - 1),
  ];
}

/**
 * rangeAnnotation queries the active value of annotation key K over [from, to):
 * - Returns the value if uniform (same non-null value throughout).
 * - Returns null if the key is absent throughout.
 * - Returns "mixed" if the value varies within the range.
 * - For an empty range (from===to), returns the value active at that point.
 */
function rangeAnnotation(content: DocOp, from: number, to: number, K: string): string | null | "mixed" {
  const len = content.documentLength();
  const a = clamp(from, 0, len);
  const b = clamp(to, a, len);

  const segments = scanAnnotationKey(content, K);

  if (a === b) {
    return valueAt(segments, a);
  }

  const first = valueAt(segments, a);
  for (const seg of segments) {
    if (seg.start <= a) continue; // before or at start
    if (seg.start >= b) break; // at or after end
    // There's a segment change inside (a, b)
    if (seg.value !== first) return "mixed";
  }
  return first;
}

/** rangeStyle queries the active value of style/<prop> over [from, to). See rangeAnnotation. */
export function rangeStyle(content: DocOp, from: number, to: number, prop: string): string | null | "mixed" {
  return rangeAnnotation(content, from, to, STYLE_PREFIX + prop);
}
