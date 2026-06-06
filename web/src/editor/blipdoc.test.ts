import { test } from "node:test";
import assert from "node:assert/strict";

import { AnnotationBoundaryMap, Attributes, DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import { compose } from "../wave/compose.ts";
import {
  caretToOffset,
  deleteText,
  insertText,
  paragraphText,
  project,
  splitLine,
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

test("textBetween rejects ranges crossing element items", () => {
  const content = structured();
  // [0,4) covers <body>,<line>,</line>,'T' — crosses elements.
  assert.throws(() => textBetween(content, 0, 4));
});
