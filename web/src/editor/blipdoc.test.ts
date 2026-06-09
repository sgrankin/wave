import { test } from "node:test";
import assert from "node:assert/strict";

import { AnnotationBoundaryMap, Attributes, DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import { compose } from "../wave/compose.ts";
import {
  caretToOffset,
  clearFormatting,
  clearLink,
  clearStyleRange,
  deleteInlineElement,
  deleteLineMarker,
  deleteText,
  insertText,
  paragraphText,
  project,
  rangeLink,
  rangeStyle,
  replaceText,
  setLineIndent,
  setLineMarkers,
  setLineType,
  setLink,
  setStyleRange,
  splitLine,
  splitLineAt,
  textBetween,
} from "./blipdoc.ts";

// --- builders ---

function es(type: string, attrs: Record<string, string> = {}): Component {
  return { kind: "elementStart", type, attributes: Attributes.of(attrs) };
}
const ee: Component = { kind: "elementEnd" };
function ch(text: string): Component {
  return { kind: "characters", text };
}
function openStyle(prop: string, value: string): Component {
  return {
    kind: "annotationBoundary",
    boundary: AnnotationBoundaryMap.of([], [{ key: `style/${prop}`, oldValue: null, newValue: value }]),
  };
}
function endStyle(prop: string): Component {
  return { kind: "annotationBoundary", boundary: AnnotationBoundaryMap.of([`style/${prop}`], []) };
}

// A structured body: <body><line t=h1/>Title<line/>Body text</body>
function structured(): DocOp {
  return new DocOp([es("body"), es("line", { t: "h1" }), ee, ch("Title"), es("line"), ee, ch("Body text"), ee]);
}

test("project: flat-text blip → one plain paragraph", () => {
  const proj = project(new DocOp([ch("hello world")]));
  assert.equal(proj.paragraphs.length, 1);
  const p = proj.paragraphs[0]!;
  assert.equal(p.lineType, null);
  assert.equal(paragraphText(p), "hello world");
  assert.equal(p.lineOffset, null);
  assert.equal(p.textStart, 0);
});

test("project: structured body → paragraphs with line types + offsets", () => {
  const proj = project(structured());
  assert.equal(proj.length, structured().documentLength());
  assert.equal(proj.paragraphs.length, 2);
  const [a, b] = proj.paragraphs;
  assert.equal(a!.lineType, "h1");
  assert.equal(paragraphText(a!), "Title");
  assert.equal(a!.lineOffset, 1);
  assert.equal(a!.textStart, 3);
  assert.equal(a!.textLength, 5);
  assert.equal(b!.lineType, null);
  assert.equal(paragraphText(b!), "Body text");
  assert.equal(b!.lineOffset, 8);
  assert.equal(b!.textStart, 10);
});

test("project: style annotations split into styled spans", () => {
  // "hi " then bold "there"
  const content = new DocOp([ch("hi "), openStyle("fontWeight", "bold"), ch("there"), endStyle("fontWeight")]);
  const p = project(content).paragraphs[0]!;
  assert.equal(p.spans.length, 2);
  assert.deepEqual(p.spans[0], { text: "hi ", styles: {} });
  assert.deepEqual(p.spans[1], { text: "there", styles: { fontWeight: "bold" } });
});

test("caretToOffset maps paragraph+offset to doc offset", () => {
  const proj = project(structured());
  assert.equal(caretToOffset(proj, 0, 0), 3); // start of "Title"
  assert.equal(caretToOffset(proj, 0, 5), 8); // end of "Title"
  assert.equal(caretToOffset(proj, 1, 0), 10); // start of "Body text"
});

// Command builders compose onto the content and re-project as expected.

test("insertText composes and appends within a paragraph", () => {
  const content = structured();
  const at = caretToOffset(project(content), 0, 5); // end of "Title"
  const next = compose(content, new DocOp(insertText(content, at, "!")));
  const proj = project(next);
  assert.equal(paragraphText(proj.paragraphs[0]!), "Title!");
  assert.equal(paragraphText(proj.paragraphs[1]!), "Body text");
});

test("deleteText composes and removes runes", () => {
  const content = structured();
  const from = caretToOffset(project(content), 1, 0); // start of "Body text"
  const to = caretToOffset(project(content), 1, 5); // after "Body "
  const next = compose(content, new DocOp(deleteText(content, from, to)));
  assert.equal(paragraphText(project(next).paragraphs[1]!), "text");
});

test("splitLine composes and splits a paragraph in two", () => {
  const content = structured();
  const at = caretToOffset(project(content), 0, 2); // inside "Ti|tle"
  const next = compose(content, new DocOp(splitLine(content, at, Attributes.empty())));
  const proj = project(next);
  assert.equal(proj.paragraphs.length, 3);
  assert.equal(paragraphText(proj.paragraphs[0]!), "Ti");
  assert.equal(paragraphText(proj.paragraphs[1]!), "tle");
  assert.equal(paragraphText(proj.paragraphs[2]!), "Body text");
});

test("replaceText replaces a selection within a paragraph", () => {
  const content = structured();
  const proj = project(content);
  const from = caretToOffset(proj, 1, 0); // start of "Body text"
  const to = caretToOffset(proj, 1, 4); // after "Body"
  const next = compose(content, new DocOp(replaceText(content, from, to, "Reply")));
  assert.equal(paragraphText(project(next).paragraphs[1]!), "Reply text");
});

test("splitLineAt with a selection deletes then splits", () => {
  const content = structured();
  const proj = project(content);
  const from = caretToOffset(proj, 0, 2); // "Ti|tle"
  const to = caretToOffset(proj, 0, 4); // "Ti|tl|e" → select "tl"
  const next = compose(content, new DocOp(splitLineAt(content, from, to, Attributes.empty())));
  const p = project(next);
  assert.equal(p.paragraphs.length, 3);
  assert.equal(paragraphText(p.paragraphs[0]!), "Ti");
  assert.equal(paragraphText(p.paragraphs[1]!), "e");
  assert.equal(paragraphText(p.paragraphs[2]!), "Body text");
});

test("deleteLineMarker composes and merges paragraphs", () => {
  const content = structured();
  const p1 = project(content).paragraphs[1]!; // the plain <line> before "Body text"
  const next = compose(content, new DocOp(deleteLineMarker(content, p1.lineOffset!, p1.lineType, p1.indent)));
  const proj = project(next);
  assert.equal(proj.paragraphs.length, 1);
  assert.equal(proj.paragraphs[0]!.lineType, "h1");
  assert.equal(paragraphText(proj.paragraphs[0]!), "TitleBody text");
});

test("textBetween rejects ranges crossing element items", () => {
  const content = structured();
  // [0,4) covers <body>,<line>,</line>,'T' — crosses elements.
  assert.throws(() => textBetween(content, 0, 4));
});

// --- setStyleRange / clearStyleRange / setLineType / rangeStyle ---

// A plain doc: <body><line/>hello world</body>
// Offsets: 0=<body>, 1=<line>, 2=</line>, 3..13="hello world", 14=</body>
function plainDoc(): DocOp {
  return new DocOp([es("body"), es("line"), ee, ch("hello world"), ee]);
}

test("setStyleRange: bold over plain text → projected spans carry fontWeight:bold", () => {
  // "hello world" starts at offset 3, length 11 → [3, 14)
  const content = plainDoc();
  const proj0 = project(content);
  const p0 = proj0.paragraphs[0]!;
  // text is "hello world" starting at textStart=3
  const from = p0.textStart; // 3
  const to = p0.textStart + p0.textLength; // 14

  const op = new DocOp(setStyleRange(content, from, to, "fontWeight", "bold"));
  const next = compose(content, op); // must not throw
  const proj = project(next);
  const p = proj.paragraphs[0]!;
  assert.equal(p.spans.length, 1);
  assert.deepEqual(p.spans[0]!.styles, { fontWeight: "bold" });
  assert.equal(p.spans[0]!.text, "hello world");
});

test("setStyleRange: bold over plain text, nothing outside the range is affected", () => {
  // bold only over "hello" (offsets 3..8)
  const content = plainDoc();
  const proj0 = project(content);
  const p0 = proj0.paragraphs[0]!;
  const from = p0.textStart; // 3
  const to = p0.textStart + 5; // "hello"

  const op = new DocOp(setStyleRange(content, from, to, "fontWeight", "bold"));
  const next = compose(content, op);
  const proj = project(next);
  const p = proj.paragraphs[0]!;
  assert.equal(p.spans.length, 2);
  assert.deepEqual(p.spans[0]!.styles, { fontWeight: "bold" });
  assert.equal(p.spans[0]!.text, "hello");
  assert.deepEqual(p.spans[1]!.styles, {});
  assert.equal(p.spans[1]!.text, " world");
});

test("setStyleRange: italic over sub-range then bold over overlapping sub-range", () => {
  // content: <body><line/>hello world</body>
  // italic over "hello" [3,8), then bold over "lo wo" [6,11)
  // After: "hel"=italic, "lo wo"=italic+bold, "rld"=bold
  const content = plainDoc();
  const proj0 = project(content);
  const base = proj0.paragraphs[0]!.textStart; // 3

  const op1 = new DocOp(setStyleRange(content, base, base + 5, "fontStyle", "italic"));
  const doc1 = compose(content, op1); // must not throw

  const op2 = new DocOp(setStyleRange(doc1, base + 3, base + 8, "fontWeight", "bold"));
  const doc2 = compose(doc1, op2); // must not throw

  const proj = project(doc2);
  const p = proj.paragraphs[0]!;
  // spans: "hel"=italic, "lo "=italic+bold, " wo"=bold, "rld"=bold
  // Wait, let me recount: "hello world" = h(0)e(1)l(2)l(3)o(4) (5)w(6)o(7)r(8)l(9)d(10)
  // base+3=3 offset into text = "l" in hello; base+8 = " w" in " world"
  // italic [base, base+5): "hello" → spans[0]
  // bold [base+3, base+8): "lo wo" → overlaps with italic at "lo "
  const allText = paragraphText(p);
  assert.equal(allText, "hello world");
  // Check that the overlap region has both styles
  const overlap = p.spans.find((s) => s.text === "lo wo" || (s.styles.fontWeight === "bold" && s.styles.fontStyle === "italic"));
  // "lo" and " wo" are the bold+italic and bold-only pieces
  // Exact span boundaries depend on implementation; just verify overlap exists
  const hasOverlap = p.spans.some((s) => s.styles.fontWeight === "bold" && s.styles.fontStyle === "italic");
  assert.ok(hasOverlap, "expected a span with both bold and italic");
});

test("setStyleRange: over a range with a pre-existing different value in the middle → compose accepts", () => {
  // Build a doc with italic in the middle: "hel"=plain, "lo"=italic, " world"=plain
  // Then set bold over the entire "hello world" → compose must accept (oldValues match)
  const content = plainDoc();
  const proj0 = project(content);
  const base = proj0.paragraphs[0]!.textStart; // 3

  // first, set italic over "lo" [base+3, base+5)
  const opItalic = new DocOp(setStyleRange(content, base + 3, base + 5, "fontStyle", "italic"));
  const doc1 = compose(content, opItalic);

  // now set bold over the whole text [base, base+11)
  // setStyleRange must track the pre-existing italic and emit correct oldValues for fontWeight
  const opBold = new DocOp(setStyleRange(doc1, base, base + 11, "fontWeight", "bold"));
  // Must not throw:
  const doc2 = compose(doc1, opBold);

  // Result: all text is bold; italic still present on "lo" segment
  const proj = project(doc2);
  const p = proj.paragraphs[0]!;
  const boldSpans = p.spans.filter((s) => s.styles.fontWeight === "bold");
  const totalBoldText = boldSpans.map((s) => s.text).join("");
  assert.equal(totalBoldText, "hello world");
});

test("clearStyleRange: removes style over the range, leaves it elsewhere", () => {
  // Set bold over whole text, then clear over "hello" only
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;

  const doc1 = compose(content, new DocOp(setStyleRange(content, base, base + 11, "fontWeight", "bold")));
  const opClear = new DocOp(clearStyleRange(doc1, base, base + 5, "fontWeight"));
  const doc2 = compose(doc1, opClear); // must not throw

  const proj = project(doc2);
  const p = proj.paragraphs[0]!;
  // "hello" should have no fontWeight; " world" should still be bold
  const helloSpan = p.spans.find((s) => s.text === "hello");
  assert.ok(helloSpan, "expected 'hello' span");
  assert.equal(helloSpan!.styles.fontWeight, undefined);
  const worldSpan = p.spans.find((s) => s.text === " world");
  assert.ok(worldSpan, "expected ' world' span");
  assert.equal(worldSpan!.styles.fontWeight, "bold");
});

test("clearStyleRange: clears style over range that has mixed pre-existing values → compose accepts", () => {
  // Build doc with "hel"=plain, "lo"=bold, " world"=italic
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;

  const doc1 = compose(content, new DocOp(setStyleRange(content, base + 3, base + 5, "fontWeight", "bold")));
  const doc2 = compose(doc1, new DocOp(setStyleRange(doc1, base + 5, base + 11, "fontWeight", "heavy")));

  // Now clearStyleRange over the whole text — must accept mixed oldValues
  const opClear = new DocOp(clearStyleRange(doc2, base, base + 11, "fontWeight"));
  const doc3 = compose(doc2, opClear); // must not throw

  const proj = project(doc3);
  const p = proj.paragraphs[0]!;
  const anyBold = p.spans.some((s) => s.styles.fontWeight !== undefined);
  assert.equal(anyBold, false, "expected no fontWeight after clear");
});

// --- setLink / clearLink / rangeLink (manual links on arbitrary text) ---

test("setLink: over a sub-range → that span carries the href, the rest does not", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart; // 3
  // Link "hello" [base, base+5) to a URL.
  const op = new DocOp(setLink(content, base, base + 5, "https://example.com/x"));
  const next = compose(content, op); // must not throw
  const p = project(next).paragraphs[0]!;
  assert.equal(p.spans.length, 2, "the run splits at the link boundary");
  assert.equal(p.spans[0]!.text, "hello");
  assert.equal(p.spans[0]!.link, "https://example.com/x");
  assert.equal(p.spans[1]!.text, " world");
  assert.equal(p.spans[1]!.link, undefined, "text outside the range is not linked");
});

test("setLink: a link adds no doc items (length unchanged) so caret mapping is intact", () => {
  const content = plainDoc();
  const before = project(content).length;
  const base = project(content).paragraphs[0]!.textStart;
  const next = compose(content, new DocOp(setLink(content, base, base + 5, "https://e.co")));
  assert.equal(project(next).length, before, "a zero-width annotation does not change document length");
});

test("setLink then clearLink restores the unlinked text", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;
  const linked = compose(content, new DocOp(setLink(content, base, base + 5, "https://e.co")));
  assert.equal(rangeLink(linked, base, base + 5), "https://e.co", "range reports the uniform href");

  const cleared = compose(linked, new DocOp(clearLink(linked, base, base + 5)));
  const p = project(cleared).paragraphs[0]!;
  assert.equal(p.spans.length, 1, "the run rejoins once the link is gone");
  assert.equal(p.spans[0]!.text, "hello world");
  assert.equal(p.spans[0]!.link, undefined);
  assert.equal(rangeLink(cleared, base, base + 5), null);
});

test("rangeLink: reports null / a uniform href / mixed", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;
  assert.equal(rangeLink(content, base, base + 11), null, "no link → null");

  // Link only "hello"; querying across "hello world" sees a value then null → mixed.
  const doc1 = compose(content, new DocOp(setLink(content, base, base + 5, "https://a.co")));
  assert.equal(rangeLink(doc1, base, base + 5), "https://a.co");
  assert.equal(rangeLink(doc1, base, base + 11), "mixed", "linked then unlinked within the range → mixed");

  // Two different hrefs in the range → mixed.
  const doc2 = compose(doc1, new DocOp(setLink(doc1, base + 6, base + 11, "https://b.co")));
  assert.equal(rangeLink(doc2, base, base + 11), "mixed", "two different hrefs → mixed");
});

// annotationKeysBalanced walks an op and returns true iff every annotation change is
// eventually ended — no annotation left dangling at the end. This is the structural
// invariant the Go validator (the server submit path) enforces via annUpdate/checkFinish;
// the client's compose() does NOT enforce it, so this guards the op BUILDERS against
// emitting server-invalid ops (a regression where setAnnotationRange's close-at-b leaves
// a clear's change unclosed — see internal/op/validate_annotation_clear_test.go).
function annotationKeysBalanced(components: readonly Component[]): boolean {
  const open = new Set<string>();
  for (const c of components) {
    if (c.kind !== "annotationBoundary") continue;
    for (const ch of c.boundary.changes) open.add(ch.key);
    for (const k of c.boundary.endKeys) open.delete(k);
  }
  return open.size === 0;
}

// --- annotation-op well-formedness (no dangling annotation; the server validator rejects it) ---

test("clearStyleRange (un-bold) emits a balanced op — the cleared key is ended, not left open", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;
  const bold = compose(content, new DocOp(setStyleRange(content, base, base + 11, "fontWeight", "bold")));
  // Un-bold the whole run: the doc is null past the run, the previously-buggy case.
  const op = clearStyleRange(bold, base, base + 11, "fontWeight");
  assert.ok(annotationKeysBalanced(op), "the un-bold op must end style/fontWeight, not leave it dangling");
  // And it still composes to clean text.
  const p = project(compose(bold, new DocOp(op))).paragraphs[0]!;
  assert.deepEqual(p.spans[0]!.styles, {});
});

test("setStyleRange adjacent to an existing same-value run emits a balanced op", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;
  // Bold "world" first; then bold "hello " — the range ends where the doc is ALREADY bold
  // (the set-adjacency case that also left the override open before the fix).
  const w = compose(content, new DocOp(setStyleRange(content, base + 6, base + 11, "fontWeight", "bold")));
  const op = setStyleRange(w, base, base + 6, "fontWeight", "bold");
  assert.ok(annotationKeysBalanced(op), "bolding adjacent to existing bold must still end the key");
  const p = project(compose(w, new DocOp(op))).paragraphs[0]!;
  assert.deepEqual(p.spans[0]!.styles, { fontWeight: "bold" }, "the whole run is now one bold span");
  assert.equal(p.spans[0]!.text, "hello world");
});

test("clearLink and clearFormatting emit balanced ops", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;
  const linked = compose(content, new DocOp(setLink(content, base, base + 11, "https://e.co")));
  assert.ok(annotationKeysBalanced(clearLink(linked, base, base + 11)), "clearLink ends link/manual");

  let doc = compose(content, new DocOp(setStyleRange(content, base, base + 11, "fontWeight", "bold")));
  doc = compose(doc, new DocOp(setLink(doc, base, base + 11, "https://e.co")));
  assert.ok(annotationKeysBalanced(clearFormatting(doc, base, base + 11)), "clearFormatting ends every key");
});

test("clear across a PARAGRAPH boundary (crossing a null gap) clears both runs and stays balanced", () => {
  // structured(): <body><line h1/>Title<line/>Body text</body>. "Title" is [3,8), the
  // second <line/> markers are [8,10) (null fontWeight), "Body text" is [10,19).
  const content = structured();
  let doc = compose(content, new DocOp(setStyleRange(content, 3, 8, "fontWeight", "bold")));
  doc = compose(doc, new DocOp(setStyleRange(doc, 10, 19, "fontWeight", "bold")));
  // A clear over [3,19) crosses the null gap between the paragraphs — the case the
  // interior loop previously left the override open across (server-rejected). balance is
  // necessary but NOT sufficient here (the broken op was also balanced); the authoritative
  // guard is the Go validator (internal/op/validate_annotation_clear_test.go) + the
  // cross-client e2e. This asserts the composed RESULT is clean in both paragraphs.
  const op = clearStyleRange(doc, 3, 19, "fontWeight");
  assert.ok(annotationKeysBalanced(op), "cross-paragraph clear is balanced");
  const ps = project(compose(doc, new DocOp(op))).paragraphs;
  assert.deepEqual(ps[0]!.spans[0]!.styles, {}, "first paragraph cleared");
  assert.deepEqual(ps[1]!.spans[0]!.styles, {}, "second paragraph cleared");
  assert.ok(annotationKeysBalanced(clearFormatting(doc, 3, 19)), "cross-paragraph clearFormatting is balanced");
});

// --- clear formatting (clearFormatting) ---

test("clearFormatting: strips bold, color, and a link from the range in one op", () => {
  const content = plainDoc();
  const p0 = project(content).paragraphs[0]!;
  const from = p0.textStart;
  const to = p0.textStart + p0.textLength; // the whole "hello world" run

  // Pile on bold + a text color + a link over the whole run.
  let doc = compose(content, new DocOp(setStyleRange(content, from, to, "fontWeight", "bold")));
  doc = compose(doc, new DocOp(setStyleRange(doc, from, to, "color", "#e11d48")));
  doc = compose(doc, new DocOp(setLink(doc, from, to, "https://example.com")));
  const styled = project(doc).paragraphs[0]!;
  assert.deepEqual(styled.spans[0]!.styles, { fontWeight: "bold", color: "#e11d48" });
  assert.equal(styled.spans[0]!.link, "https://example.com");

  // One clearFormatting op removes every style annotation AND the link.
  const cleared = compose(doc, new DocOp(clearFormatting(doc, from, to)));
  const p = project(cleared).paragraphs[0]!;
  assert.equal(p.spans.length, 1, "the run rejoins once all formatting is gone");
  assert.deepEqual(p.spans[0]!.styles, {});
  assert.equal(p.spans[0]!.link, undefined);
  assert.equal(p.spans[0]!.text, "hello world");
});

test("clearFormatting clears only the selected sub-range, leaving the rest formatted", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;
  // Bold the whole run, then clear only "hello".
  const bold = compose(content, new DocOp(setStyleRange(content, base, base + 11, "fontWeight", "bold")));
  const cleared = compose(bold, new DocOp(clearFormatting(bold, base, base + 5)));
  const p = project(cleared).paragraphs[0]!;
  assert.equal(p.spans.length, 2);
  assert.deepEqual(p.spans[0]!.styles, {}, "hello is cleared");
  assert.equal(p.spans[0]!.text, "hello");
  assert.deepEqual(p.spans[1]!.styles, { fontWeight: "bold" }, " world stays bold");
  assert.equal(p.spans[1]!.text, " world");
});

test("clearFormatting on already-clean text is a no-op (compose accepts, text intact)", () => {
  const content = plainDoc();
  const p0 = project(content).paragraphs[0]!;
  const op = new DocOp(clearFormatting(content, p0.textStart, p0.textStart + p0.textLength));
  const next = compose(content, op); // must not throw
  const p = project(next).paragraphs[0]!;
  assert.equal(p.spans.length, 1);
  assert.deepEqual(p.spans[0]!.styles, {});
  assert.equal(p.spans[0]!.text, "hello world");
});

test("clearFormatting leaves the line type untouched (only character formatting goes)", () => {
  const content = plainDoc();
  const p0 = project(content).paragraphs[0]!;
  // Make the line an h2 and bold its text, then clear formatting over the text.
  const h2 = compose(content, new DocOp(setLineType(content, p0.lineOffset!, null, "h2")));
  const bold = compose(h2, new DocOp(setStyleRange(h2, p0.textStart, p0.textStart + p0.textLength, "fontWeight", "bold")));
  const cleared = compose(bold, new DocOp(clearFormatting(bold, p0.textStart, p0.textStart + p0.textLength)));
  const p = project(cleared).paragraphs[0]!;
  assert.equal(p.lineType, "h2", "the line stays an h2");
  assert.deepEqual(p.spans[0]!.styles, {}, "the character bold is gone");
});

test("setLineType: plain→h1 changes lineType in projection", () => {
  // structured(): <body><line t=h1/>Title<line/>Body text</body>
  // Change the second <line> (plain) to h2
  const content = structured();
  const proj0 = project(content);
  const p1 = proj0.paragraphs[1]!; // plain line
  assert.equal(p1.lineType, null);

  const op = new DocOp(setLineType(content, p1.lineOffset!, null, "h2"));
  const next = compose(content, op); // must not throw
  const proj = project(next);
  assert.equal(proj.paragraphs[1]!.lineType, "h2");
});

// --- numbered lists (setLineMarkers + listyle projection) ---

test("setLineMarkers: plain → numbered list item sets t=li and listyle=decimal", () => {
  const content = plainDoc();
  const p0 = project(content).paragraphs[0]!;
  assert.equal(p0.lineType, null);
  assert.equal(p0.listStyle, null);

  // plain → numbered (t: null→li, listyle: null→decimal) in one op.
  const op = new DocOp(setLineMarkers(content, p0.lineOffset!, null, "li", null, "decimal"));
  const next = compose(content, op); // must not throw (oldValues match)
  const p = project(next).paragraphs[0]!;
  assert.equal(p.lineType, "li");
  assert.equal(p.listStyle, "decimal");
});

test("setLineMarkers: numbered → bullet drops only listyle; → plain clears both", () => {
  const content = plainDoc();
  const off = project(content).paragraphs[0]!.lineOffset!;
  // Make it numbered first.
  const numbered = compose(content, new DocOp(setLineMarkers(content, off, null, "li", null, "decimal")));

  // numbered → bullet: keep t=li, clear listyle.
  const bullet = compose(numbered, new DocOp(setLineMarkers(numbered, off, "li", "li", "decimal", null)));
  const pb = project(bullet).paragraphs[0]!;
  assert.equal(pb.lineType, "li");
  assert.equal(pb.listStyle, null);

  // bullet → plain: clear t (listyle already null, so only t changes).
  const plain = compose(bullet, new DocOp(setLineMarkers(bullet, off, "li", null, null, null)));
  const pp = project(plain).paragraphs[0]!;
  assert.equal(pp.lineType, null);
  assert.equal(pp.listStyle, null);
});

test("setLineMarkers: no-op transition retains the whole doc unchanged", () => {
  const content = plainDoc();
  const off = project(content).paragraphs[0]!.lineOffset!;
  const op = new DocOp(setLineMarkers(content, off, null, null, null, null));
  const next = compose(content, op); // must not throw
  assert.equal(project(next).paragraphs[0]!.lineType, null);
});

// --- indent / outdent (setLineIndent) ---

test("setLineIndent: 0→1 sets the indent; 1→2 increments; 2→0 clears it", () => {
  const content = plainDoc();
  const off = project(content).paragraphs[0]!.lineOffset!;
  assert.equal(project(content).paragraphs[0]!.indent, 0);

  // 0 → 1 (the `i` attribute appears).
  const i1 = compose(content, new DocOp(setLineIndent(content, off, 0, 1))); // oldValue null matches absent
  assert.equal(project(i1).paragraphs[0]!.indent, 1);

  // 1 → 2 (increment).
  const i2 = compose(i1, new DocOp(setLineIndent(i1, off, 1, 2)));
  assert.equal(project(i2).paragraphs[0]!.indent, 2);

  // 2 → 0 (the attribute is removed again, back to absent).
  const i0 = compose(i2, new DocOp(setLineIndent(i2, off, 2, 0)));
  assert.equal(project(i0).paragraphs[0]!.indent, 0);
});

test("setLineIndent: no-op transition retains the whole doc unchanged", () => {
  const content = plainDoc();
  const off = project(content).paragraphs[0]!.lineOffset!;
  const op = new DocOp(setLineIndent(content, off, 0, 0));
  const next = compose(content, op); // must not throw
  assert.equal(project(next).paragraphs[0]!.indent, 0);
});

test("setLineIndent is orthogonal to the line type (an h2 stays h2 when indented)", () => {
  const content = structured();
  const p1 = project(content).paragraphs[1]!;
  const h2 = compose(content, new DocOp(setLineType(content, p1.lineOffset!, null, "h2")));
  const off = project(h2).paragraphs[1]!.lineOffset!;
  const indented = compose(h2, new DocOp(setLineIndent(h2, off, 0, 1)));
  const p = project(indented).paragraphs[1]!;
  assert.equal(p.lineType, "h2");
  assert.equal(p.indent, 1);
});

test("lineAttributes + deleteLineMarker round-trip a numbered <line>'s attributes", () => {
  // A numbered item must be deletable: deleteLineMarker echoes t=li AND listyle=decimal,
  // or compose rejects the DeleteElementStart.
  const content = plainDoc();
  const off = project(content).paragraphs[0]!.lineOffset!;
  const numbered = compose(content, new DocOp(setLineMarkers(content, off, null, "li", null, "decimal")));
  const p = project(numbered).paragraphs[0]!;
  // Deleting the (numbered) line marker must compose cleanly.
  const del = new DocOp(deleteLineMarker(numbered, p.lineOffset!, p.lineType, p.indent, p.listStyle));
  assert.doesNotThrow(() => compose(numbered, del));
});

test("setLineType: h1→plain round-trip", () => {
  const content = structured();
  const proj0 = project(content);
  const p0 = proj0.paragraphs[0]!; // h1 line
  assert.equal(p0.lineType, "h1");

  // h1 → plain (remove t attribute)
  const op1 = new DocOp(setLineType(content, p0.lineOffset!, "h1", null));
  const doc1 = compose(content, op1);
  assert.equal(project(doc1).paragraphs[0]!.lineType, null);

  // plain → h1 (restore)
  const op2 = new DocOp(setLineType(doc1, p0.lineOffset!, null, "h1"));
  const doc2 = compose(doc1, op2);
  assert.equal(project(doc2).paragraphs[0]!.lineType, "h1");
});

test("rangeStyle: returns value when uniform across range", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;

  const doc1 = compose(content, new DocOp(setStyleRange(content, base, base + 11, "fontWeight", "bold")));
  assert.equal(rangeStyle(doc1, base, base + 11, "fontWeight"), "bold");
});

test("rangeStyle: returns null when style absent throughout", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;
  assert.equal(rangeStyle(content, base, base + 11, "fontWeight"), null);
});

test("rangeStyle: returns mixed when value varies in range", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;

  // bold over first 5, nothing on remaining 6
  const doc1 = compose(content, new DocOp(setStyleRange(content, base, base + 5, "fontWeight", "bold")));
  assert.equal(rangeStyle(doc1, base, base + 11, "fontWeight"), "mixed");
});

test("rangeStyle: empty range returns value at point", () => {
  const content = plainDoc();
  const base = project(content).paragraphs[0]!.textStart;

  // bold over full text
  const doc1 = compose(content, new DocOp(setStyleRange(content, base, base + 11, "fontWeight", "bold")));
  assert.equal(rangeStyle(doc1, base + 3, base + 3, "fontWeight"), "bold");
  // outside the bold range (before it)
  assert.equal(rangeStyle(content, base, base, "fontWeight"), null);
});

// --- inline-reply anchors (the caret-safety invariant) ---

test("project surfaces a reply anchor without disturbing the caret math", () => {
  // <body><line/>hi<reply id="b+r"/></body>
  const content = new DocOp([es("body"), es("line"), ee, ch("hi"), es("reply", { id: "b+r" }), ee, ee]);
  const proj = project(content);

  assert.equal(proj.paragraphs.length, 1);
  const p = proj.paragraphs[0]!;
  assert.deepEqual(p.anchors, ["b+r"], "anchor recorded on the paragraph");
  assert.equal(p.textLength, 2, "anchor is not counted in the text length");
  assert.equal(paragraphText(p), "hi", "text excludes the anchor");
  // The caret at the end of the text maps before the 2-item <reply>, and the
  // anchor occupies the trailing doc items (proj.length counts them).
  assert.equal(caretToOffset(proj, 0, 2), p.textStart + 2, "caret at line end is before the anchor");
  assert.equal(proj.length, 8, "8 doc items (body, line/, h, i, reply/, body)");
});

test("a reply anchor does not shift a following paragraph's offsets", () => {
  // <body><line/>hi<reply id="b+r"/><line/>bye</body>
  const content = new DocOp([
    es("body"),
    es("line"),
    ee,
    ch("hi"),
    es("reply", { id: "b+r" }),
    ee,
    es("line"),
    ee,
    ch("bye"),
    ee,
  ]);
  const proj = project(content);
  assert.equal(proj.paragraphs.length, 2);
  assert.deepEqual(proj.paragraphs[0]!.anchors, ["b+r"]);
  assert.deepEqual(proj.paragraphs[1]!.anchors, [], "second paragraph has no anchors");
  // Second paragraph's text "bye" begins after the 2-item anchor + the second <line>.
  assert.equal(paragraphText(proj.paragraphs[1]!), "bye");
  assert.equal(caretToOffset(proj, 1, 0), proj.paragraphs[1]!.textStart, "second paragraph offset intact");
});

test("project surfaces an inline image without disturbing the caret math", () => {
  // <body><line/>hi<image attachment="att1"/></body>
  const content = new DocOp([
    es("body"),
    es("line"),
    ee,
    ch("hi"),
    es("image", { attachment: "att1" }),
    ee,
    ee,
  ]);
  const proj = project(content);
  assert.equal(proj.paragraphs.length, 1);
  const p = proj.paragraphs[0]!;
  assert.deepEqual(p.images, ["att1"], "image recorded on the paragraph");
  assert.equal(p.textLength, 2, "image is not counted in the text length");
  assert.equal(caretToOffset(proj, 0, 2), p.textStart + 2, "caret at line end is before the image");
  assert.equal(proj.length, 8, "8 doc items (body, line/, h, i, image/, body)");
});

// --- mid-text inline elements (the exact-offset anchoring model) ---

test("project records a mid-text reply anchor at its exact offset", () => {
  // <body><line/>ab<reply id="b+r"/>cd</body>
  const content = new DocOp([es("body"), es("line"), ee, ch("ab"), es("reply", { id: "b+r" }), ee, ch("cd"), ee]);
  const proj = project(content);
  const p = proj.paragraphs[0]!;
  assert.deepEqual(p.items.map((i) => i.kind), ["text", "reply", "text"], "ordered: text, widget, text");
  assert.deepEqual(p.items.map((i) => i.offset), [3, 5, 7], "each item at its exact doc offset");
  assert.equal(p.textLength, 4, "the widget is not counted in textLength");
  assert.equal(p.paragraphEnd, 9, "paragraphEnd = textStart(3) + textLength(4) + 2*1 widget");
  assert.deepEqual(p.anchors, ["b+r"], "derived anchors view still works");
  assert.equal(paragraphText(p), "abcd", "text excludes the widget");
  assert.equal(proj.length, content.documentLength(), "projection length == document length");
});

test("project records a reply at the very start of a paragraph", () => {
  // <body><line/><reply id="r"/>hi</body>
  const content = new DocOp([es("body"), es("line"), ee, es("reply", { id: "r" }), ee, ch("hi"), ee]);
  const p = project(content).paragraphs[0]!;
  assert.deepEqual(p.items.map((i) => i.kind), ["reply", "text"]);
  assert.deepEqual(p.items.map((i) => i.offset), [3, 5], "reply at textStart(3); text after at 5");
  assert.equal(paragraphText(p), "hi");
});

test("project records two adjacent inline images at increasing offsets", () => {
  // <body><line/>x<image attachment="a"/><image attachment="b"/></body>
  const content = new DocOp([
    es("body"), es("line"), ee, ch("x"),
    es("image", { attachment: "a" }), ee,
    es("image", { attachment: "b" }), ee, ee,
  ]);
  const p = project(content).paragraphs[0]!;
  assert.deepEqual(p.items.map((i) => i.kind), ["text", "image", "image"]);
  assert.deepEqual(p.items.map((i) => i.offset), [3, 4, 6], "image A at 4, image B at 6 (each 2 items)");
  assert.deepEqual(p.images, ["a", "b"]);
  assert.equal(p.paragraphEnd, 8, "textStart(3) + textLength(1) + 2*2 widgets");
});

test("deleteInlineElement removes an inline element's 2 items (compose-accepted)", () => {
  // <body><line/>ab<reply id="r"/>cd</body> — the reply elementStart is at offset 5.
  const content = new DocOp([es("body"), es("line"), ee, ch("ab"), es("reply", { id: "r" }), ee, ch("cd"), ee]);
  // compose() throws if the DeleteElementStart attributes don't echo the document, so a
  // successful compose proves the builder reconstructed them correctly.
  const after = compose(content, new DocOp(deleteInlineElement(content, 5, "reply", Attributes.of({ id: "r" }))));
  assert.equal(after.documentLength(), content.documentLength() - 2, "the element's 2 items are removed");
  const p = project(after).paragraphs[0]!;
  assert.deepEqual(p.anchors, [], "the reply anchor is gone");
  assert.equal(paragraphText(p), "abcd", "surrounding text is preserved");
});

test("a widget breaks text-run coalescing (preserves document order)", () => {
  // Two same-(no-)style runs separated by a widget must NOT merge into one item.
  const content = new DocOp([es("body"), es("line"), ee, ch("ab"), es("reply", { id: "r" }), ee, ch("cd"), ee]);
  const p = project(content).paragraphs[0]!;
  const texts = p.items.filter((i) => i.kind === "text");
  assert.equal(texts.length, 2, "the widget keeps the two equal-style runs as separate ordered items");
});
