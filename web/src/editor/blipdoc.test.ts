import { test } from "node:test";
import assert from "node:assert/strict";

import { AnnotationBoundaryMap, Attributes, DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import { compose } from "../wave/compose.ts";
import {
  caretToOffset,
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
