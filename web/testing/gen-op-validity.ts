// gen-op-validity.ts — TS→Go conformance corpus generator.
//
// For EVERY exported editor op-builder in web/src/editor/blipdoc.ts, this emits
// multiple (doc, op) cases — happy path AND the hard edge cases — that the builder
// actually produces, hex-encoding both via the shared wire codec (encodeDocOp). The
// companion Go test (internal/op/editor_op_validity_test.go) decodes each case and
// runs op.Validate(doc, op): the editor's permissive compose() accepts ops the SERVER
// submit-path validator rejects (the setAnnotationRange un-bold / cross-paragraph-clear
// bug class), so this corpus is the permanent guard that every builder's output is
// validator-valid.
//
// Run: `node web/testing/gen-op-validity.ts` (node strips the TS types). Writes the
// JSON array to internal/op/testdata/editor_op_validity.json and prints the case count.

import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { mkdirSync, writeFileSync } from "node:fs";

import { Attributes, DocOp } from "../src/wave/types.ts";
import type { Component } from "../src/wave/types.ts";
import { encodeDocOp } from "../src/wave/codec.ts";
import { compose } from "../src/wave/compose.ts";
import {
  clearFormatting,
  clearLink,
  clearStyleRange,
  deleteInlineElement,
  deleteLineMarker,
  deleteText,
  insertText,
  project,
  replaceText,
  setLineIndent,
  setLineMarkers,
  setLineType,
  setLink,
  setStyleRange,
  splitLine,
  splitLineAt,
} from "../src/editor/blipdoc.ts";

// --- doc-construction helpers (mirror blipdoc.test.ts) ---

function es(type: string, attrs: Record<string, string> = {}): Component {
  return { kind: "elementStart", type, attributes: Attributes.of(attrs) };
}
const ee: Component = { kind: "elementEnd" };
function ch(text: string): Component {
  return { kind: "characters", text };
}

// A plain one-paragraph body: <body><line/>hello world</body>.
// Offsets: 0=<body> 1=<line> 2=</line> 3..13="hello world" 14=</body>.
function plainDoc(): DocOp {
  return new DocOp([es("body"), es("line"), ee, ch("hello world"), ee]);
}

// A structured body: <body><line t=h1/>Title<line/>Body text</body>.
function structured(): DocOp {
  return new DocOp([es("body"), es("line", { t: "h1" }), ee, ch("Title"), es("line"), ee, ch("Body text"), ee]);
}

// A two-paragraph body, both plain lines: <body><line/>Alpha<line/>Bravo</body>.
function twoPlain(): DocOp {
  return new DocOp([es("body"), es("line"), ee, ch("Alpha"), es("line"), ee, ch("Bravo"), ee]);
}

// A body whose only line is a numbered list item carrying text.
function listDoc(): DocOp {
  return new DocOp([es("body"), es("line", { t: "li", listyle: "decimal" }), ee, ch("Item one"), ee]);
}

// A body with an inline image mid-text: <body><line/>ab<image attachment=att1/>cd</body>.
function inlineImageDoc(): DocOp {
  return new DocOp([
    es("body"), es("line"), ee,
    ch("ab"), es("image", { attachment: "att1" }), ee, ch("cd"),
    ee,
  ]);
}

// A body with an inline reply anchor mid-text: <body><line/>ab<reply id=r1/>cd</body>.
function inlineReplyDoc(): DocOp {
  return new DocOp([
    es("body"), es("line"), ee,
    ch("ab"), es("reply", { id: "r1" }), ee, ch("cd"),
    ee,
  ]);
}

// hex returns the lowercase hex of a DocOp's wire encoding.
function hex(d: DocOp): string {
  return Buffer.from(encodeDocOp(d)).toString("hex");
}

// --- case collection ---

interface Case {
  builder: string;
  note: string;
  doc: string; // hex(content)
  op: string; // hex(new DocOp(builderOutput))
}

const cases: Case[] = [];

// emit runs a builder, hex-encodes (doc, op), and records the case. A builder that
// throws for an edge case (precondition unmet) is skipped — we only keep the ops the
// builder actually produces.
function emit(builder: string, note: string, content: DocOp, build: () => Component[]): void {
  let components: Component[];
  try {
    components = build();
  } catch {
    return; // builder rejected this case by construction; nothing to validate
  }
  cases.push({ builder, note, doc: hex(content), op: hex(new DocOp(components)) });
}

// para0 etc. — convenience accessors for a doc's projected paragraphs.
function paras(d: DocOp) {
  return project(d).paragraphs;
}

// ============================================================================
// Annotation builders: setStyleRange / clearStyleRange / setLink / clearLink /
// clearFormatting. Edge cases: whole-doc, zero-length, cross-paragraph (null gap
// at the <line> marker), adjacency to a same-value run, mixed pre-existing values.
// ============================================================================

// -- setStyleRange --
{
  const c = plainDoc();
  const p = paras(c)[0]!;
  const t0 = p.textStart;
  const tEnd = p.textStart + p.textLength;
  emit("setStyleRange", "bold whole paragraph", c, () => setStyleRange(c, t0, tEnd, "fontWeight", "bold"));
  emit("setStyleRange", "bold sub-range (first 5)", c, () => setStyleRange(c, t0, t0 + 5, "fontWeight", "bold"));
  emit("setStyleRange", "zero-length range (no-op)", c, () => setStyleRange(c, t0 + 3, t0 + 3, "fontWeight", "bold"));

  // adjacency: bold "world" first, then bold "hello " — range ends where doc is ALREADY bold.
  const w = compose(c, new DocOp(setStyleRange(c, t0 + 6, tEnd, "fontWeight", "bold")));
  emit("setStyleRange", "bold adjacent to existing same-value bold run", w, () =>
    setStyleRange(w, t0, t0 + 6, "fontWeight", "bold"));

  // mixed pre-existing: italic the middle, then bold the whole run (mixed fontStyle under).
  const mid = compose(c, new DocOp(setStyleRange(c, t0 + 3, t0 + 5, "fontStyle", "italic")));
  emit("setStyleRange", "bold over a region with mixed pre-existing fontStyle", mid, () =>
    setStyleRange(mid, t0, tEnd, "fontWeight", "bold"));

  // mixed pre-existing SAME key: bold half, heavy other half, then set bold over whole.
  const half = compose(c, new DocOp(setStyleRange(c, t0, t0 + 5, "fontWeight", "bold")));
  const mixedSame = compose(half, new DocOp(setStyleRange(half, t0 + 5, tEnd, "fontWeight", "heavy")));
  emit("setStyleRange", "set bold over mixed bold/heavy run", mixedSame, () =>
    setStyleRange(mixedSame, t0, tEnd, "fontWeight", "bold"));
}
{
  // cross-paragraph set: bold across two paragraphs (crosses the null gap at <line>).
  const c = twoPlain(); // body0 line1 lineEnd2 Alpha[3..8) line8 lineEnd9 Bravo[10..15) bodyEnd15
  const pa = paras(c);
  const from = pa[0]!.textStart; // 3
  const to = pa[1]!.textStart + pa[1]!.textLength; // 15
  emit("setStyleRange", "bold spanning two paragraphs (crosses null <line> gap)", c, () =>
    setStyleRange(c, from, to, "fontWeight", "bold"));
}

// -- clearStyleRange --
{
  const c = plainDoc();
  const p = paras(c)[0]!;
  const t0 = p.textStart;
  const tEnd = p.textStart + p.textLength;

  // whole-doc clear of a fully-bold run — the null-past-the-run case (the original bug).
  const bold = compose(c, new DocOp(setStyleRange(c, t0, tEnd, "fontWeight", "bold")));
  emit("clearStyleRange", "clear bold over whole bold run (doc null past run)", bold, () =>
    clearStyleRange(bold, t0, tEnd, "fontWeight"));
  emit("clearStyleRange", "clear sub-range of a bold run", bold, () =>
    clearStyleRange(bold, t0, t0 + 5, "fontWeight"));
  emit("clearStyleRange", "zero-length clear (no-op)", bold, () =>
    clearStyleRange(bold, t0 + 3, t0 + 3, "fontWeight"));

  // clear ADJACENT to an already-cleared (null) region: bold only "hello", clear over whole.
  const partial = compose(c, new DocOp(setStyleRange(c, t0, t0 + 5, "fontWeight", "bold")));
  emit("clearStyleRange", "clear over a run with mixed bold/null (set half then clear whole)", partial, () =>
    clearStyleRange(partial, t0, tEnd, "fontWeight"));

  // mixed pre-existing values: bold half, heavy other half, clear the whole.
  const half = compose(c, new DocOp(setStyleRange(c, t0, t0 + 5, "fontWeight", "bold")));
  const mixed = compose(half, new DocOp(setStyleRange(half, t0 + 5, tEnd, "fontWeight", "heavy")));
  emit("clearStyleRange", "clear over mixed bold/heavy run", mixed, () =>
    clearStyleRange(mixed, t0, tEnd, "fontWeight"));
}
{
  // cross-paragraph clear: bold EACH paragraph separately (markers stay null), clear across.
  const c = twoPlain();
  const pa = paras(c);
  const a0 = pa[0]!.textStart; // 3
  const aEnd = pa[0]!.textStart + pa[0]!.textLength; // 8
  const b0 = pa[1]!.textStart; // 10
  const bEnd = pa[1]!.textStart + pa[1]!.textLength; // 15
  let doc = compose(c, new DocOp(setStyleRange(c, a0, aEnd, "fontWeight", "bold")));
  doc = compose(doc, new DocOp(setStyleRange(doc, b0, bEnd, "fontWeight", "bold")));
  emit("clearStyleRange", "clear bold spanning two paragraphs (crosses null <line> gap)", doc, () =>
    clearStyleRange(doc, a0, bEnd, "fontWeight"));
}

// -- setLink --
{
  const c = plainDoc();
  const p = paras(c)[0]!;
  const t0 = p.textStart;
  const tEnd = p.textStart + p.textLength;
  emit("setLink", "link whole paragraph", c, () => setLink(c, t0, tEnd, "https://example.com/x"));
  emit("setLink", "link sub-range", c, () => setLink(c, t0, t0 + 5, "https://example.com/x"));
  emit("setLink", "zero-length link (no-op)", c, () => setLink(c, t0 + 2, t0 + 2, "https://e.co"));

  // adjacency: link "world", then link "hello " with the SAME url (range ends in same value).
  const w = compose(c, new DocOp(setLink(c, t0 + 6, tEnd, "https://same.co")));
  emit("setLink", "link adjacent to existing same-url run", w, () => setLink(w, t0, t0 + 6, "https://same.co"));

  // mixed pre-existing: link first half to url A, then set url B over the whole range.
  const a = compose(c, new DocOp(setLink(c, t0, t0 + 5, "https://a.co")));
  emit("setLink", "set url over region with mixed pre-existing url/null", a, () =>
    setLink(a, t0, tEnd, "https://b.co"));
}
{
  const c = twoPlain();
  const pa = paras(c);
  emit("setLink", "link spanning two paragraphs (crosses null <line> gap)", c, () =>
    setLink(c, pa[0]!.textStart, pa[1]!.textStart + pa[1]!.textLength, "https://x.co"));
}

// -- clearLink --
{
  const c = plainDoc();
  const p = paras(c)[0]!;
  const t0 = p.textStart;
  const tEnd = p.textStart + p.textLength;
  const linked = compose(c, new DocOp(setLink(c, t0, tEnd, "https://e.co")));
  emit("clearLink", "clear link over whole linked run", linked, () => clearLink(linked, t0, tEnd));
  emit("clearLink", "clear sub-range of a linked run", linked, () => clearLink(linked, t0, t0 + 5));
  emit("clearLink", "zero-length clear (no-op)", linked, () => clearLink(linked, t0 + 2, t0 + 2));

  // mixed: link only first half, clear over the whole range (mixed url/null).
  const partial = compose(c, new DocOp(setLink(c, t0, t0 + 5, "https://e.co")));
  emit("clearLink", "clear over mixed link/null run", partial, () => clearLink(partial, t0, tEnd));
}
{
  // cross-paragraph: link each paragraph separately, clear across both.
  const c = twoPlain();
  const pa = paras(c);
  const a0 = pa[0]!.textStart;
  const aEnd = pa[0]!.textStart + pa[0]!.textLength;
  const b0 = pa[1]!.textStart;
  const bEnd = pa[1]!.textStart + pa[1]!.textLength;
  let doc = compose(c, new DocOp(setLink(c, a0, aEnd, "https://e.co")));
  doc = compose(doc, new DocOp(setLink(doc, b0, bEnd, "https://e.co")));
  emit("clearLink", "clear link spanning two paragraphs (crosses null <line> gap)", doc, () =>
    clearLink(doc, a0, bEnd));
}

// -- clearFormatting --
{
  const c = plainDoc();
  const p = paras(c)[0]!;
  const t0 = p.textStart;
  const tEnd = p.textStart + p.textLength;

  // pile bold + color + link on the whole run, then clear all.
  let doc = compose(c, new DocOp(setStyleRange(c, t0, tEnd, "fontWeight", "bold")));
  doc = compose(doc, new DocOp(setStyleRange(doc, t0, tEnd, "color", "#e11d48")));
  doc = compose(doc, new DocOp(setLink(doc, t0, tEnd, "https://example.com")));
  emit("clearFormatting", "clear bold+color+link over whole run", doc, () => clearFormatting(doc, t0, tEnd));
  emit("clearFormatting", "clear formatting over sub-range", doc, () => clearFormatting(doc, t0, t0 + 5));
  emit("clearFormatting", "clear formatting on already-clean text (no-op)", c, () =>
    clearFormatting(c, t0, tEnd));
  emit("clearFormatting", "zero-length clear formatting (no-op)", doc, () =>
    clearFormatting(doc, t0 + 3, t0 + 3));

  // mixed pre-existing: bold one half, color the other, link the middle, clear all.
  let m = compose(c, new DocOp(setStyleRange(c, t0, t0 + 5, "fontWeight", "bold")));
  m = compose(m, new DocOp(setStyleRange(m, t0 + 5, tEnd, "color", "#000")));
  m = compose(m, new DocOp(setLink(m, t0 + 2, t0 + 8, "https://mid.co")));
  emit("clearFormatting", "clear over a region with mixed bold/color/link segments", m, () =>
    clearFormatting(m, t0, tEnd));
}
{
  // cross-paragraph clearFormatting: bold each paragraph separately, clear across both.
  const c = twoPlain();
  const pa = paras(c);
  const a0 = pa[0]!.textStart;
  const aEnd = pa[0]!.textStart + pa[0]!.textLength;
  const b0 = pa[1]!.textStart;
  const bEnd = pa[1]!.textStart + pa[1]!.textLength;
  let doc = compose(c, new DocOp(setStyleRange(c, a0, aEnd, "fontWeight", "bold")));
  doc = compose(doc, new DocOp(setStyleRange(doc, b0, bEnd, "fontWeight", "bold")));
  emit("clearFormatting", "clear formatting spanning two paragraphs (crosses null <line> gap)", doc, () =>
    clearFormatting(doc, a0, bEnd));
}

// ============================================================================
// Line builders: setLineType / setLineMarkers / setLineIndent. Exercise plain,
// h1, list-item lines; indent 0→1→2→0; toggle list styles; and oldValue matching
// against docs that already carry the attributes.
// ============================================================================

// -- setLineType --
{
  const c = structured(); // p0 = h1 line, p1 = plain line
  const pa = paras(c);
  const h1Off = pa[0]!.lineOffset!;
  const plainOff = pa[1]!.lineOffset!;
  emit("setLineType", "plain line → h2", c, () => setLineType(c, plainOff, null, "h2"));
  emit("setLineType", "h1 line → plain", c, () => setLineType(c, h1Off, "h1", null));
  emit("setLineType", "h1 line → h2 (replace value)", c, () => setLineType(c, h1Off, "h1", "h2"));
  emit("setLineType", "no-op (h1→h1 emits an updateAttributes with matching old/new)", c, () =>
    setLineType(c, h1Off, "h1", "h1"));
}
{
  const c = listDoc(); // a list item
  const off = paras(c)[0]!.lineOffset!;
  emit("setLineType", "list item (t=li) → h1 (old=li)", c, () => setLineType(c, off, "li", "h1"));
}

// -- setLineMarkers --
{
  const c = plainDoc();
  const off = paras(c)[0]!.lineOffset!;
  emit("setLineMarkers", "plain → numbered list (t:null→li, listyle:null→decimal)", c, () =>
    setLineMarkers(c, off, null, "li", null, "decimal"));
  emit("setLineMarkers", "plain → bullet list (t:null→li only)", c, () =>
    setLineMarkers(c, off, null, "li", null, null));
  emit("setLineMarkers", "no-op (all unchanged → whole-doc retain)", c, () =>
    setLineMarkers(c, off, null, null, null, null));

  // numbered → bullet (drop listyle); bullet → plain (clear t); over docs that HAVE the attrs.
  const numbered = compose(c, new DocOp(setLineMarkers(c, off, null, "li", null, "decimal")));
  emit("setLineMarkers", "numbered → bullet (drop listyle only)", numbered, () =>
    setLineMarkers(numbered, off, "li", "li", "decimal", null));
  const bullet = compose(numbered, new DocOp(setLineMarkers(numbered, off, "li", "li", "decimal", null)));
  emit("setLineMarkers", "bullet → plain (clear t)", bullet, () =>
    setLineMarkers(bullet, off, "li", null, null, null));
}
{
  // over a doc that already is a numbered list (oldValue matching on both attrs).
  const c = listDoc();
  const off = paras(c)[0]!.lineOffset!;
  emit("setLineMarkers", "numbered list → plain (clear t and listyle)", c, () =>
    setLineMarkers(c, off, "li", null, "decimal", null));
  emit("setLineMarkers", "numbered list → bullet (clear listyle, keep t=li)", c, () =>
    setLineMarkers(c, off, "li", "li", "decimal", null));
}

// -- setLineIndent --
{
  const c = plainDoc();
  const off = paras(c)[0]!.lineOffset!;
  const i1 = compose(c, new DocOp(setLineIndent(c, off, 0, 1)));
  const i2 = compose(i1, new DocOp(setLineIndent(i1, off, 1, 2)));
  emit("setLineIndent", "indent 0→1 (attribute appears)", c, () => setLineIndent(c, off, 0, 1));
  emit("setLineIndent", "indent 1→2 (increment, old i=1)", i1, () => setLineIndent(i1, off, 1, 2));
  emit("setLineIndent", "indent 2→0 (attribute removed, old i=2)", i2, () => setLineIndent(i2, off, 2, 0));
  emit("setLineIndent", "no-op 0→0 (whole-doc retain)", c, () => setLineIndent(c, off, 0, 0));
}
{
  // indent on an h1 line — orthogonal to type; old i absent.
  const c = structured();
  const off = paras(c)[0]!.lineOffset!;
  emit("setLineIndent", "indent an h1 line 0→1", c, () => setLineIndent(c, off, 0, 1));
}

// ============================================================================
// Text / structure builders: insertText / deleteText / replaceText / splitLineAt /
// splitLine / deleteLineMarker / deleteInlineElement.
// ============================================================================

// -- insertText --
{
  const c = structured();
  const pa = paras(c);
  const p0 = pa[0]!; // "Title" at textStart 3, len 5
  emit("insertText", "insert at paragraph start", c, () => insertText(c, p0.textStart, "X"));
  emit("insertText", "insert mid-text", c, () => insertText(c, p0.textStart + 2, "X"));
  emit("insertText", "insert at paragraph end", c, () => insertText(c, p0.textStart + p0.textLength, "!"));
  emit("insertText", "insert multi-char string", c, () => insertText(c, p0.textStart + p0.textLength, "abc"));
  emit("insertText", "insert at doc start (offset 0)", c, () => insertText(c, 0, "Z"));
}

// -- deleteText --
{
  const c = structured();
  const pa = paras(c);
  const p0 = pa[0]!; // Title [3,8)
  const p1 = pa[1]!; // Body text [10,19)
  emit("deleteText", "delete a sub-range", c, () => deleteText(c, p0.textStart, p0.textStart + 2));
  emit("deleteText", "delete a whole paragraph's text", c, () =>
    deleteText(c, p1.textStart, p1.textStart + p1.textLength));
  emit("deleteText", "delete from mid to paragraph end", c, () =>
    deleteText(c, p0.textStart + 2, p0.textStart + p0.textLength));
  emit("deleteText", "zero-length delete (no-op)", c, () => deleteText(c, p0.textStart, p0.textStart));
  // Across a paragraph boundary the range spans the <line> element items — deleteText
  // throws (textBetween rejects non-character items); emit() catches and skips it. Kept
  // to document that deleteText refuses cross-paragraph by design.
  emit("deleteText", "across paragraph boundary (builder rejects; skipped)", c, () =>
    deleteText(c, p0.textStart + 2, p1.textStart + 2));
}

// -- replaceText --
{
  const c = structured();
  const pa = paras(c);
  const p1 = pa[1]!; // Body text [10,19)
  emit("replaceText", "replace a selection with text", c, () =>
    replaceText(c, p1.textStart, p1.textStart + 4, "Reply"));
  emit("replaceText", "replace whole paragraph text", c, () =>
    replaceText(c, p1.textStart, p1.textStart + p1.textLength, "New"));
  emit("replaceText", "replace as pure insert (from==to)", c, () =>
    replaceText(c, p1.textStart, p1.textStart, "ins"));
  emit("replaceText", "replace as pure delete (text empty)", c, () =>
    replaceText(c, p1.textStart, p1.textStart + 4, ""));
}

// -- splitLineAt --
{
  const c = structured();
  const pa = paras(c);
  const p0 = pa[0]!; // Title [3,8)
  emit("splitLineAt", "split mid-text, no selection", c, () =>
    splitLineAt(c, p0.textStart + 2, p0.textStart + 2, Attributes.empty()));
  emit("splitLineAt", "split at paragraph start", c, () =>
    splitLineAt(c, p0.textStart, p0.textStart, Attributes.empty()));
  emit("splitLineAt", "split at paragraph end", c, () =>
    splitLineAt(c, p0.textStart + p0.textLength, p0.textStart + p0.textLength, Attributes.empty()));
  emit("splitLineAt", "split with a selection (delete then insert <line>)", c, () =>
    splitLineAt(c, p0.textStart + 2, p0.textStart + 4, Attributes.empty()));
  emit("splitLineAt", "split inserting a typed <line> (t=h2)", c, () =>
    splitLineAt(c, p0.textStart + 2, p0.textStart + 2, Attributes.of({ t: "h2" })));
}

// -- splitLine --
{
  const c = structured();
  const pa = paras(c);
  const p0 = pa[0]!;
  emit("splitLine", "insert <line> mid-text", c, () => splitLine(c, p0.textStart + 2, Attributes.empty()));
  emit("splitLine", "insert <line> at paragraph start", c, () => splitLine(c, p0.textStart, Attributes.empty()));
  emit("splitLine", "insert <line> at paragraph end", c, () =>
    splitLine(c, p0.textStart + p0.textLength, Attributes.empty()));
  emit("splitLine", "insert a typed <line> (t=h1)", c, () =>
    splitLine(c, p0.textStart + 2, Attributes.of({ t: "h1" })));
}

// -- deleteLineMarker --
{
  const c = structured(); // second <line> is plain, at lineOffset of paragraph 1
  const p1 = paras(c)[1]!;
  emit("deleteLineMarker", "delete the second (plain) <line>, merging paragraphs", c, () =>
    deleteLineMarker(c, p1.lineOffset!, p1.lineType, p1.indent, p1.listStyle));
}
{
  // delete a numbered list marker — echoes t=li AND listyle=decimal.
  const base = plainDoc();
  const off0 = paras(base)[0]!.lineOffset!;
  // make a two-line doc with a numbered second line so deleting it merges into the first.
  const c = new DocOp([
    es("body"), es("line"), ee, ch("Alpha"),
    es("line", { t: "li", listyle: "decimal" }), ee, ch("Item"),
    ee,
  ]);
  void off0;
  const p1 = paras(c)[1]!;
  emit("deleteLineMarker", "delete a numbered list <line> (echoes t=li,listyle=decimal)", c, () =>
    deleteLineMarker(c, p1.lineOffset!, p1.lineType, p1.indent, p1.listStyle));
}
{
  // delete an indented h2 line marker — echoes t and i attributes.
  const c = new DocOp([
    es("body"), es("line"), ee, ch("Alpha"),
    es("line", { t: "h2", i: "2" }), ee, ch("Sub"),
    ee,
  ]);
  const p1 = paras(c)[1]!;
  emit("deleteLineMarker", "delete an indented h2 <line> (echoes t=h2,i=2)", c, () =>
    deleteLineMarker(c, p1.lineOffset!, p1.lineType, p1.indent, p1.listStyle));
}

// -- deleteInlineElement --
{
  const c = inlineImageDoc(); // <body><line/>ab<image attachment=att1/>cd</body>
  const p = paras(c)[0]!;
  // the image is the inline item at the offset just after "ab".
  const imgItem = p.items.find((it) => it.kind === "image")!;
  emit("deleteInlineElement", "delete a mid-text inline image", c, () =>
    deleteInlineElement(c, imgItem.offset, "image", Attributes.of({ attachment: "att1" })));
}
{
  const c = inlineReplyDoc(); // <body><line/>ab<reply id=r1/>cd</body>
  const p = paras(c)[0]!;
  const replyItem = p.items.find((it) => it.kind === "reply")!;
  emit("deleteInlineElement", "delete a mid-text inline reply anchor", c, () =>
    deleteInlineElement(c, replyItem.offset, "reply", Attributes.of({ id: "r1" })));
}

// --- write the corpus ---

const here = dirname(fileURLToPath(import.meta.url));
const outDir = resolve(here, "../../internal/op/testdata");
mkdirSync(outDir, { recursive: true });
const outPath = resolve(outDir, "editor_op_validity.json");
writeFileSync(outPath, JSON.stringify(cases, null, 2) + "\n");
console.log(`wrote ${cases.length} cases to ${outPath}`);
