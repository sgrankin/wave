// The conversation model: reads the conversation structure out of a wavelet's
// "conversation" manifest document and generates the operations that author
// conversations (create the manifest, append blips, initialise blip content).
// It is built on the read-side document projection (./doc) and the operation
// model (./types); applying the authored ops to wavelet state is the caller's
// job.
//
// Go reference: internal/conv/manifest.go.
// Spec: docs/specs/01-data-model.md §3 (conversation model), §8.3 (creating a blip).

import { Attributes, DocOp } from "./types.ts";
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
