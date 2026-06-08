import { test } from "node:test";
import assert from "node:assert/strict";

import { Attributes, DocOp } from "../wave/types.ts";
import type { Component } from "../wave/types.ts";
import { project, splitLineAt, setLineType, lineAttributes } from "./blipdoc.ts";

function es(type: string, attrs: Record<string, string> = {}): Component {
  return { kind: "elementStart", type, attributes: Attributes.of(attrs) };
}
const ee: Component = { kind: "elementEnd" };
function ch(text: string): Component {
  return { kind: "characters", text };
}

// An li line whose only content is an inline image widget (no text):
// <body><line t=li/><image attachment=a1/></body>
function liWithOnlyImage(): DocOp {
  return new DocOp([es("body"), es("line", { t: "li" }), ee, es("image", { attachment: "a1" }), ee, ee]);
}

test("THROWAWAY: li containing only a widget reports textLength 0 (triggers exit branch)", () => {
  const proj = project(liWithOnlyImage());
  // find the li paragraph
  const li = proj.paragraphs.find((p) => p.lineType === "li");
  assert.ok(li, "expected an li paragraph");
  assert.equal(li!.textLength, 0, "textLength counts only text runes, not the widget");
  // paragraphEnd accounts for the widget's 2 items even though textLength is 0
  assert.equal(li!.paragraphEnd, li!.textStart + 2, "the image occupies 2 doc items");
  assert.equal(li!.images.length, 1, "exactly one image widget present");
  // => blip-view's `range.collapsed && para.textLength === 0` is TRUE here, so Enter
  // fires setLineType(li -> null) (exit) rather than splitting. The image stays put
  // and the line silently loses its bullet — not a new line. That's the bug.
});

test("THROWAWAY: exit op (li->null) is structurally valid for a widget-only li", () => {
  const content = liWithOnlyImage();
  const proj = project(content);
  const li = proj.paragraphs.find((p) => p.lineType === "li")!;
  // mirrors blip-view: setLineType(content, lineOffset, "li", null)
  const ops = setLineType(content, li.lineOffset!, "li", null);
  // op is well-formed (covers the doc); we just assert it builds + has an update.
  assert.ok(ops.some((c) => c.kind === "updateAttributes"));
});

test("THROWAWAY: splitLineAt with li attrs over a heading-less plain line builds", () => {
  // continuation path: splitLineAt with lineAttributes("li", indent)
  const content = new DocOp([es("body"), es("line", { t: "li" }), ee, ch("abc"), ee]);
  const attrs = lineAttributes("li", 0);
  // caret after "ab" → split there; should be a valid op (no throw)
  const ops = splitLineAt(content, /*from*/ 5, /*to*/ 5, attrs);
  assert.ok(ops.some((c) => c.kind === "elementStart" && c.type === "line"));
});

// Non-collapsed selection Enter inside a list (selection spanning text only):
// splitLineAt deletes [lo,hi) then inserts the <line t=li/>. Should build cleanly.
test("THROWAWAY: non-collapsed Enter in li deletes selection + inserts li line", () => {
  const content = new DocOp([es("body"), es("line", { t: "li" }), ee, ch("hello"), ee]);
  // lo/hi: select "ell" inside hello. textStart for the li paragraph:
  const proj = project(content);
  const li = proj.paragraphs.find((p) => p.lineType === "li")!;
  const lo = li.textStart + 1;
  const hi = li.textStart + 4;
  const ops = splitLineAt(content, lo, hi, lineAttributes("li", li.indent));
  assert.ok(ops.some((c) => c.kind === "deleteCharacters"));
  assert.ok(ops.some((c) => c.kind === "elementStart" && c.type === "line"));
});
