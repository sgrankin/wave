// The conversation model: reads the conversation structure out of a wavelet's
// "conversation" manifest document and generates the operations that author
// conversations (create the manifest, append blips, initialise blip content).
// It is built on the read-side document projection (./doc) and the operation
// model (./types); applying the authored ops to wavelet state is the caller's
// job.
//
// Go reference: internal/conv/manifest.go.
// Spec: docs/specs/01-data-model.md §3 (conversation model), §8.3 (creating a blip).

import { Attributes, DocOp, runeCount } from "./types.ts";
import type { Component } from "./types.ts";
import { attr, childElements, root } from "./doc.ts";
import type { Element } from "./doc.ts";

// ManifestDocumentID is the id of the conversation manifest document.
export const ManifestDocumentID = "conversation";

const tagConversation = "conversation";
const tagBlip = "blip";
const tagThread = "thread";

const attrID = "id";
const attrInline = "inline";
const attrDelete = "deleted";
const attrSort = "sort";
const attrAnchorWavelet = "anchorWavelet";
const attrAnchorBlip = "anchorBlip";

const boolTrue = "true";

// Manifest is the parsed conversation structure read from the manifest document.
//
// Not every schema-permitted detail is parsed: <peer> links and the extended
// anchor attributes (anchorManifestOffset/anchorVersion/anchorOffset) are not
// represented here, matching the Java DocumentBasedManifest (which exposes only
// anchorWavelet/anchorBlip). Code that needs them, or that round-trips a manifest
// back to a document, must read the raw document rather than this object. (Sort
// is read here as a convenience though the Java reads it outside the manifest
// object.) Empty strings mean "not present" (mirroring the Go zero value).
export interface Manifest {
  anchorWavelet: string; // empty if not anchored
  anchorBlip: string; // empty if not anchored
  sort: string; // empty if unset
  rootThread: Thread; // the implicit root thread (blips directly in <conversation>)
}

// Thread is a sequence of blips. The root thread has an empty id; reply threads
// have an id equal to their first blip's id, and may be inline.
export interface Thread {
  id: string;
  inline: boolean;
  blips: Blip[];
}

// Blip is a blip entry in the manifest: its id, deleted flag, and reply threads.
export interface Blip {
  id: string;
  deleted: boolean;
  threads: Thread[];
}

// readManifest parses a manifest document's content into its structure. It is
// permissive: it does not validate the manifest schema or the conversation
// invariants (C1–C5) — schema-invalid structure (e.g. a stray <thread> directly
// under <conversation>) is silently ignored rather than rejected. It throws if
// the content is not a single-rooted document or its root is not <conversation>.
// (Port of conv.ReadManifest.)
export function readManifest(content: DocOp): Manifest {
  const el = root(content);
  if (el.type !== tagConversation) {
    throw new Error(`conv: manifest root is <${el.type}>, want <conversation>`);
  }
  return {
    anchorWavelet: attr(el, attrAnchorWavelet) ?? "",
    anchorBlip: attr(el, attrAnchorBlip) ?? "",
    sort: attr(el, attrSort) ?? "",
    rootThread: readThread(el, "", false),
  };
}

// readThread reads the blip children of el as a thread's blips.
function readThread(el: Element, id: string, inline: boolean): Thread {
  const th: Thread = { id, inline, blips: [] };
  for (const child of childElements(el)) {
    if (child.type === tagBlip) {
      th.blips.push(readBlip(child));
    }
  }
  return th;
}

// readBlip reads a <blip> element: its id, deleted flag, and reply threads.
function readBlip(el: Element): Blip {
  const b: Blip = { id: attr(el, attrID) ?? "", deleted: false, threads: [] };
  const d = attr(el, attrDelete);
  if (d !== undefined) {
    b.deleted = d === boolTrue;
  }
  for (const child of childElements(el)) {
    if (child.type !== tagThread) continue;
    const tid = attr(child, attrID) ?? "";
    let inline = false;
    const v = attr(child, attrInline);
    if (v !== undefined) {
      inline = v === boolTrue;
    }
    b.threads.push(readThread(child, tid, inline));
  }
  return b;
}

// --- authoring ---

// emptyManifest returns the content (a DocInitialization) of a fresh, empty
// conversation manifest: <conversation></conversation> (spec §8.1).
// (Port of conv.EmptyManifest.)
export function emptyManifest(): DocOp {
  const none = Attributes.empty();
  return new DocOp([
    { kind: "elementStart", type: tagConversation, attributes: none },
    { kind: "elementEnd" },
  ]);
}

// initialBlipContent returns the content (a DocInitialization) of a freshly
// created blip: <body><line/></body> (spec §8.3; note no <head> is emitted).
// (Port of conv.InitialBlipContent.)
export function initialBlipContent(): DocOp {
  const none = Attributes.empty();
  return new DocOp([
    { kind: "elementStart", type: "body", attributes: none },
    { kind: "elementStart", type: "line", attributes: none },
    { kind: "elementEnd" }, // line
    { kind: "elementEnd" }, // body
  ]);
}

// appendBlipToRootThread returns the operation that appends <blip id="blipID">
// </blip> to the end of the root thread of the given manifest content (just
// before the closing </conversation>). Apply it by composing it onto the
// manifest: compose(manifest, result). (Port of conv.AppendBlipToRootThread.)
export function appendBlipToRootThread(manifest: DocOp, blipID: string): DocOp {
  const n = manifest.documentLength(); // includes the final </conversation>
  return new DocOp([
    { kind: "retain", count: n - 1 },
    { kind: "elementStart", type: tagBlip, attributes: Attributes.of({ [attrID]: blipID }) },
    { kind: "elementEnd" },
    { kind: "retain", count: 1 },
  ]);
}

// appendBlipToThread returns the operation appending an empty <blip id="blipID"/>
// to the end of the thread identified by threadID — the empty string selects the
// root thread (the children of <conversation> itself). It throws if no such
// thread exists. Generalises appendBlipToRootThread. (Port of
// conv.AppendBlipToThread.)
export function appendBlipToThread(manifest: DocOp, threadID: string, blipID: string): DocOp {
  const close = elementCloseOffset(manifest, (tag, id) =>
    threadID === "" ? tag === tagConversation : tag === tagThread && id === threadID,
  );
  if (close === null) throw new Error(`conv: no thread ${JSON.stringify(threadID)} in manifest`);
  const n = manifest.documentLength();
  return new DocOp([
    { kind: "retain", count: close },
    { kind: "elementStart", type: tagBlip, attributes: Attributes.of({ [attrID]: blipID }) },
    { kind: "elementEnd" },
    { kind: "retain", count: n - close },
  ]);
}

// replyToBlip returns the operation creating a new reply thread under the blip
// parentBlipID, containing a single new blip newBlipID. The thread's id equals
// the new blip's id (the Wave convention: a reply thread is identified by its
// first blip); inline marks it inline="true". It throws if no such blip exists.
// The caller pairs this manifest mutation with a blip operation initialising
// newBlipID's content (initialBlipContent) in the same wavelet delta. (Port of
// conv.ReplyToBlip.)
export function replyToBlip(manifest: DocOp, parentBlipID: string, newBlipID: string, inline: boolean): DocOp {
  const close = elementCloseOffset(manifest, (tag, id) => tag === tagBlip && id === parentBlipID);
  if (close === null) throw new Error(`conv: no blip ${JSON.stringify(parentBlipID)} in manifest`);
  const threadAttrs: Record<string, string> = { [attrID]: newBlipID };
  if (inline) threadAttrs[attrInline] = boolTrue;
  const n = manifest.documentLength();
  return new DocOp([
    { kind: "retain", count: close },
    { kind: "elementStart", type: tagThread, attributes: Attributes.of(threadAttrs) },
    { kind: "elementStart", type: tagBlip, attributes: Attributes.of({ [attrID]: newBlipID }) },
    { kind: "elementEnd" }, // blip
    { kind: "elementEnd" }, // thread
    { kind: "retain", count: n - close },
  ]);
}

// elementCloseOffset returns the document offset of the ElementEnd item that
// closes the first element (by close order) whose (tag, id) satisfies pred, where
// id is the element's "id" attribute ("" if absent) — i.e. the insertion point
// for appending a child to the very end of that element. Returns null if no
// element matches. Assumes a well-formed initialization (balanced elements, no
// retains/deletions), which every manifest is. (Port of conv.elementCloseOffset.)
function elementCloseOffset(manifest: DocOp, pred: (tag: string, id: string) => boolean): number | null {
  const stack: Array<{ tag: string; id: string }> = [];
  let pos = 0;
  for (const c of manifest.components as readonly Component[]) {
    switch (c.kind) {
      case "elementStart":
        stack.push({ tag: c.type, id: c.attributes.get(attrID) ?? "" });
        pos += 1;
        break;
      case "elementEnd": {
        const top = stack.pop();
        if (top === undefined) return null; // unbalanced; not a valid manifest
        if (pred(top.tag, top.id)) return pos;
        pos += 1;
        break;
      }
      case "characters":
        pos += runeCount(c.text);
        break;
      default:
        // annotationBoundary is zero-width; nothing else appears in an initialization.
        break;
    }
  }
  return null;
}

// newBlipID generates a fresh, unique blip id in the Wave "b+<token>" form. The
// token is random (not a hash); ids only need to be unique within a wavelet.
export function newBlipID(): string {
  const b = new Uint8Array(9);
  crypto.getRandomValues(b);
  let s = "";
  for (const x of b) s += x.toString(16).padStart(2, "0");
  return "b+" + s;
}
