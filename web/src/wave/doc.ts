// Reads a document — represented as an insertion-only DocOp (a
// DocInitialization) — as a navigable element tree. This is the read-side
// projection of the operation model: the indexed mutable document model is not
// ported, so structure is recovered by walking the initialization's components.
//
// Annotations are a parallel layer and are ignored by this structural reader.
//
// Go reference: internal/doc/reader.go.

import type { Attributes, Component, DocOp } from "./types.ts";

// Node is a document tree node: either an Element or Text. They are discriminated
// by the `kind` field ("element" | "text"), the cleanest erasable-syntax shape —
// no class hierarchy, just plain tagged objects.
export type Node = Element | Text;

// Element is an XML-like element with a tag, attributes, and ordered children.
export interface Element {
  readonly kind: "element";
  readonly type: string;
  readonly attributes: Attributes;
  // Mutated during read while the element is open on the stack.
  children: Node[];
}

// Text is a run of character data.
export interface Text {
  readonly kind: "text";
  readonly data: string;
}

// read parses a DocInitialization into its top-level nodes (its forest, usually
// a single root element). It throws if content is not insertion-only (contains
// retains or deletions) or its elements are unbalanced. (Port of doc.Read.)
export function read(content: DocOp): Node[] {
  const roots: Node[] = [];
  const stack: Element[] = [];
  const add = (n: Node): void => {
    if (stack.length === 0) {
      roots.push(n);
    } else {
      stack[stack.length - 1]!.children.push(n);
    }
  };
  for (const c of content.components) {
    switch (c.kind) {
      case "elementStart": {
        const el: Element = { kind: "element", type: c.type, attributes: c.attributes, children: [] };
        add(el);
        stack.push(el);
        break;
      }
      case "elementEnd":
        if (stack.length === 0) {
          throw new Error("doc: unbalanced element end");
        }
        stack.pop();
        break;
      case "characters":
        add({ kind: "text", data: c.text });
        break;
      case "annotationBoundary":
        // Parallel annotation layer; not part of the structural tree.
        break;
      default:
        throw new Error(`doc: not a document initialization: unexpected ${(c as Component).kind} component`);
    }
  }
  if (stack.length !== 0) {
    throw new Error(`doc: ${stack.length} unclosed element(s)`);
  }
  return roots;
}

// root parses content and returns its single root element, throwing if there is
// not exactly one top-level element (stray top-level text is an error).
// (Port of doc.Root.)
export function root(content: DocOp): Element {
  const roots = read(content);
  if (roots.length !== 1) {
    throw new Error(`doc: expected a single root element, found ${roots.length} top-level nodes`);
  }
  const r = roots[0]!;
  if (r.kind !== "element") {
    throw new Error("doc: root node is text, not an element");
  }
  return r;
}

// attr returns the value of the named attribute, or undefined if absent.
// (Port of Element.Attr; the Go (string, bool) is collapsed to string|undefined.)
export function attr(el: Element, name: string): string | undefined {
  return el.attributes.get(name);
}

// childElements returns el's element children in order, skipping text nodes.
// (Port of Element.ChildElements.)
export function childElements(el: Element): Element[] {
  const els: Element[] = [];
  for (const c of el.children) {
    if (c.kind === "element") els.push(c);
  }
  return els;
}

// elementText returns the concatenation of el's immediate text children (not
// recursive). (Port of Element.Text.)
export function elementText(el: Element): string {
  let s = "";
  for (const c of el.children) {
    if (c.kind === "text") s += c.data;
  }
  return s;
}
