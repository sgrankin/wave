# 02 — Operational Transformation (OT)

## Purpose & scope

This subsystem defines the complete set of mutations that can be applied to Wave
documents (DocOp), and the three fundamental algorithms — **Transform**,
**Compose**, and **Invert** — that make concurrent collaborative editing
convergent.  It also covers the wavelet-level operations (add/remove participant,
blip mutation) that wrap document operations.

Readers who need only the wire format should skim this doc and read spec 04.
Readers implementing a Wave server or client must understand this spec in full.

---

## Concepts & glossary

**Item**: the unit of position in a Wave document.  Every character is one item;
every element-start tag is one item; every element-end tag is one item.
Annotations are not items — they are metadata on the items around them.

**Document**: a sequence of items that forms a tree of XML-like elements and
text nodes, plus a set of annotation key-value spans.

**DocOp**: a document operation — a cursor that reads through the original
document's items from left to right, either skipping (retain), inserting, or
deleting each item.  A DocOp is an ordered list of *components*.

**DocInitialization**: a special-case DocOp that builds a document from nothing.
It contains only insertion components (characters, elementStart, elementEnd,
annotationBoundary) and never retain/delete components.

**Insertion-only operation**: a DocOp whose every range-affecting component is
either retain or an insertion (characters/elementStart/elementEnd).
Produced by Decomposer.

**Insertion-free (non-insertion) operation**: a DocOp with no
characters/elementStart/elementEnd insertions — only retain, delete, and
attribute-change components.

**TP1** (the OT diamond property): if two operations C (client) and S (server)
both start from the same document state D, then
`apply(apply(D, C), S') == apply(apply(D, S), C')` where `(C', S') = transform(C, S)`.

**TP2**: TP2 is *not* required by Wave.  The transform is designed to satisfy
TP1 only.  Wave uses a single-server, Jupiter-style OT protocol (see spec 03),
which only needs TP1.

**TransformException**: thrown when two operations are structurally incompatible
for transformation (e.g. length mismatch, incompatible deletions).

---

## Data structures

### AnnotationBoundaryMap

Describes an annotation state change at a point in the document.

```
AnnotationBoundaryMap {
  // Keys whose annotation range is ending here. Strictly sorted.
  endKeys:     []string

  // Keys whose annotation value is changing here. Strictly sorted.
  // The same key must not appear in both endKeys and changeKeys.
  changeKeys:    []string
  changeOldVals: []string|null   // expected current value; null = not set
  changeNewVals: []string|null   // value to set;          null = clear
}
```

Invariants:
- All keys (in both endKeys and changeKeys) must be strictly sorted
  (lexicographic, ascending).
- A key may not appear in both lists.
- Keys must be valid UTF-16 and must not contain `?` or `@`.
- Values (old and new) must be valid UTF-16 (or null).
- An `annotationBoundary` component must never be immediately followed by
  another `annotationBoundary` component (no adjacent boundaries).

### Attributes

A sorted map from attribute name (XML Name) to non-null string value.
Keys are strictly sorted ascending.  All keys and values must be valid UTF-16.

### AttributesUpdate

A sorted map of attribute mutations: key → (oldValue, newValue).
Keys are strictly sorted ascending.
A null newValue removes the attribute; a null oldValue means it was absent.

### DocOp component types

A DocOp is an ordered sequence of components.  Each component is one of:

| Component            | Parameters                       | Items consumed (input) | Items produced (output) |
|----------------------|----------------------------------|------------------------|-------------------------|
| `retain(n)`          | n: positive int                  | n                      | n                       |
| `characters(s)`      | s: non-empty UTF-16 string       | 0                      | len(s)                  |
| `elementStart(t, a)` | t: XML tag name; a: Attributes   | 0                      | 1                       |
| `elementEnd()`       | —                                | 0                      | 1                       |
| `deleteCharacters(s)`| s: non-empty UTF-16 string       | len(s)                 | 0                       |
| `deleteElementStart(t,a)` | t: tag; a: Attributes       | 1                      | 0                       |
| `deleteElementEnd()` | —                                | 1                      | 0                       |
| `replaceAttributes(oldA, newA)` | both Attributes        | 1                      | 1                       |
| `updateAttributes(u)` | u: AttributesUpdate             | 1                      | 1                       |
| `annotationBoundary(m)` | m: AnnotationBoundaryMap      | 0                      | 0                       |

`annotationBoundary` is zero-width — it marks where annotation ranges open or
close without consuming or producing any items.

### WaveletOperation hierarchy

```
WaveletOperation (abstract)
  ├── NoOp                         — no effect
  ├── AddParticipant(participantId)
  ├── RemoveParticipant(participantId)
  ├── WaveletBlipOperation(blipId, BlipOperation)
  │     └── BlipOperation (abstract)
  │           └── BlipContentOperation(docOp: DocOp)
  └── VersionUpdateOp              — server metadata only, no document effect
```

Each WaveletOperation carries a `WaveletOperationContext`:
- `creator`: ParticipantId of the author
- `timestamp`: milliseconds since epoch
- `versionIncrement`: how much to add to the wavelet version after applying
- `hashedVersion`: optional — the resulting hashed version

---

## Algorithms & behavior

### Document model invariants

A valid document is a forest of XML elements with text nodes and annotations.
The item sequence satisfies:

1. Element start/end tags are properly nested (no interleaving).
2. Characters appear only at valid positions (not inside an element-start item,
   which doesn't exist as a position — characters are between element boundaries
   or at the text level).

### DocOp well-formedness rules

A DocOp is *well-formed* if and only if:

1. `retain(n)` requires `n > 0`.
2. `characters(s)`, `deleteCharacters(s)` require `s` non-empty and containing
   no surrogate code units (each char must be a complete UTF-16 scalar).
3. `elementStart`, `deleteElementStart`, `elementEnd`, `deleteElementEnd` must
   be properly nested — every start must have a matching end within the same op
   (or within the document context they apply to).
4. Insertions (`characters`, `elementStart`, `elementEnd`) must not appear
   inside an active deletion scope (deleteElementStart…deleteElementEnd block),
   and deletions must not appear inside an active insertion scope.
5. `retain` must not appear inside an insertion or deletion scope.
6. `replaceAttributes` and `updateAttributes` must not appear inside an
   insertion or deletion scope.
7. Two `annotationBoundary` components must not be adjacent.
8. At the end of the op, all open annotation ranges (those started by
   `annotationBoundary.changeKeys`) must have been ended by a subsequent
   `annotationBoundary.endKeys`.  Likewise, ended keys must have been started.
9. Attribute key/value maps must have strictly sorted keys (ascending).
10. Annotation keys must be strictly sorted within each `annotationBoundary`
    component (both lists separately); the same key must not appear in both
    lists.

A DocOp is *valid against a specific document* if additionally:

- The total items consumed equals the document length (i.e. the op covers
  exactly the entire document — no partial ops).
- `retain(n)` does not extend past the end of the document.
- `deleteCharacters(s)` matches the actual characters at the cursor position.
- `deleteElementStart(t, a)` matches the actual element type and attributes.
- `deleteElementEnd` matches an element close at the cursor.
- Annotation `oldValue` fields match the actual annotation state in the document.
- Annotation `newValue` fields during deletion must be consistent with the
  inherited annotation values (the value the surrounding context would have
  after the deleted region is removed).

### Schema & validation semantics

Beyond structural well-formedness and validity-against-a-document (above), an op
may be checked against a **document schema** — a per-document-type set of
constraints on which elements, attributes, and characters are permitted.  This
is a layer on top of OT, not part of the transform/compose/invert algorithms,
but it determines whether an op is *accepted* into a document.

> **The box server does NOT enforce schema.**  All server-side wavelet documents
> are built with `SchemaCollection.empty()`, which yields
> `NO_SCHEMA_CONSTRAINTS` for every document (`WaveletDataUtil`, `RobotWaveletData`,
> `OperationContextImpl`, each carrying a TODO referencing issue 109).  Schema is
> therefore **client-side input validation only**; the server never rejects a
> delta for a schema violation.  A Go reimplementation must **not** add a
> server-side schema gate.

#### (a) DocumentSchema contract

A schema answers four queries about a document type (an XML tag name, or `null`
for the top level):

- `permitsChild(parentTagOrNull, childTag) → bool` — may `childTag` appear
  directly under `parentTagOrNull` (or at top level if null)?
- `permitsAttribute(tag, attr) → bool` — may an element of type `tag` carry an
  attribute named `attr`?
- `permitsAttribute(tag, attr, value) → bool` — is the specific `value`
  permitted for that attribute? (Some types add explicit value constraints,
  e.g. integer ranges.)
- `permittedCharacters(tagOrNull) → {NONE, BLIP_TEXT, ANY}` — what text content
  is permitted directly inside this type.
- `getRequiredInitialChildren(tagOrNull) → ordered list of tags` — tags that must
  appear, in order, at the start of this type (may be empty).

`DocOpValidator.validate(violations, schema, doc, op)` checks the **resulting**
document against this schema (via `DocOpAutomaton`), in addition to the
structural well-formedness and against-document rules already specified above.
A `null` schema is treated as `NO_SCHEMA_CONSTRAINTS`, which permits everything
(`permittedCharacters = ANY`, no required children, all children/attributes
permitted).

#### (b) Character coercion (`PermittedCharacters.coerceString`)

When text is inserted into a context with a character constraint, the text is
coerced per codepoint to a well-formed, schema-valid string:

- **NONE**: no text permitted at all (coercion is an error).
- **BLIP_TEXT**: per codepoint —
  - `\t` (tab) → four spaces;
  - `\n` or `\r` → a single space;
  - a supplementary (astral, > U+FFFF) codepoint → U+FFFD (replacement char);
  - a codepoint that is "good for blip" → kept as-is;
  - any other codepoint → U+FFFD;
  - an unpaired surrogate → U+FFFD.
- **ANY**: per codepoint —
  - a supplementary codepoint → U+FFFD;
  - a valid codepoint → kept as-is;
  - an invalid codepoint → U+FFFD;
  - an unpaired surrogate → U+FFFD.

Note for Go/UTF-8: the Java reference does not yet support astral codepoints and
replaces every supplementary codepoint with U+FFFD; a Go port must reproduce this
supplementary → U+FFFD behavior to stay bit-compatible, even though Go strings
can natively carry astral runes.

#### (c) Schema selection

`ConversationSchemas.getSchemaForId(waveletId, documentId)` chooses the schema
for a conversational wavelet's document:

- **BLIP** schema if `waveletId` is conversational **and** `documentId` is a blip
  id;
- **MANIFEST** schema if `documentId` is the manifest document id (`"conversation"`);
- otherwise `NO_SCHEMA_CONSTRAINTS`.

The element/attribute constraint **tables** for these two schemas are documented
in spec 01 (the blip document structure in [01 §3.3](01-data-model.md) and the
manifest in [01 §3.2](01-data-model.md)); they are not duplicated here.

Sibling `SchemaProvider`s exist for other document families:

- `UserDataSchemas` — the per-user supplement documents (see [01 §6](01-data-model.md));
- `AccountSchemas` — account documents.

These are combined via `SchemaCollection`, which queries each provider in turn
and returns the single non-`NO_SCHEMA_CONSTRAINTS` schema that matches (throwing
if more than one matches, to keep schema selection order-independent), or
`NO_SCHEMA_CONSTRAINTS` if none match.  `SchemaCollection.empty()` (no providers)
always yields `NO_SCHEMA_CONSTRAINTS`.

### Applying a DocOp

Applying a DocOp to a document is a left-to-right scan:

- Maintain a cursor at position 0 in the input document.
- For each component:
  - `retain(n)`: advance cursor n positions, copying those items to output.
  - `characters(s)`: insert string s at the current output position.
  - `elementStart(t, a)`: insert an element-start item at the current output position.
  - `elementEnd()`: insert an element-end item at the current output position.
  - `deleteCharacters(s)`: delete len(s) characters starting at cursor; advance cursor.
  - `deleteElementStart(t, a)`: delete the element-start at cursor; advance cursor by 1.
  - `deleteElementEnd()`: delete the element-end at cursor; advance cursor by 1.
  - `replaceAttributes(old, new)`: replace the element-start at cursor with one
    having attributes `new`; advance cursor by 1; output the replaced element.
  - `updateAttributes(u)`: apply u to the attributes of the element-start at cursor;
    advance cursor by 1; output the updated element.
  - `annotationBoundary(m)`: update the annotation state at the current output
    position: for each entry in `m.endKeys`, close that annotation; for each
    entry in `m.changeKeys`, set the annotation to the new value.

---

### Transform

`Transform(clientOp, serverOp) → (clientOp', serverOp')`

Guarantees TP1: `apply(apply(D, clientOp), serverOp') == apply(apply(D, serverOp), clientOp')`.

#### High-level algorithm

The transform uses a decompose-then-transform-in-stages approach:

```
1. Decompose clientOp → (ci, cn)   // ci=insertion-only, cn=insertion-free
2. Decompose serverOp → (si, sn)   // si=insertion-only, sn=insertion-free

3. (ci', si') = InsertionTransform(ci, si)
4. (ci'', sn') = InsertionNoninsertionTransform(ci', sn)
5. (si'', cn') = InsertionNoninsertionTransform(si', cn)
6. (cn'', sn'') = NoninsertionTransform(cn', sn')

7. result clientOp' = compose(ci'', cn'')
   result serverOp' = compose(si'', sn'')
```

The compose in step 7 uses `Composer.compose`.

#### Decompose

`Decompose(op) → (insertionOp, noninsertionOp)`

Walk through op components left-to-right.  For each:

| Original component         | insertionOp gets         | noninsertionOp gets  |
|----------------------------|--------------------------|----------------------|
| `retain(n)`                | `retain(n)`              | `retain(n)`          |
| `characters(s)`            | `characters(s)`          | `retain(len(s))`     |
| `elementStart(t, a)`       | `elementStart(t, a)`     | `retain(1)`          |
| `elementEnd()`             | `elementEnd()`           | `retain(1)`          |
| `deleteCharacters(s)`      | `retain(len(s))`         | `deleteCharacters(s)`|
| `deleteElementStart(t, a)` | `retain(1)`              | `deleteElementStart(t, a)` |
| `deleteElementEnd()`       | `retain(1)`              | `deleteElementEnd()` |
| `replaceAttributes(o, n)`  | `retain(1)`              | `replaceAttributes(o, n)` |
| `updateAttributes(u)`      | `retain(1)`              | `updateAttributes(u)` |
| `annotationBoundary(m)`    | _(not output)_           | `annotationBoundary(m)` |

Annotations go only into the non-insertion part.

#### InsertionTransform (insertion-only vs insertion-only)

**Context**: both ops contain only `retain`, `characters`, `elementStart`,
`elementEnd`.

**The tie-breaking rule for concurrent insertions at the same position**:
The **client op goes first**.  When two insertions land at the same cursor
position, the client's insertion is placed before the server's insertion
in both resulting operations.

This is implemented via a shared signed `position` counter (PositionTracker),
initially 0.  It exposes two views:

- **Side 1 (client)**: `increase(n)` does `position += n`; `get()` returns
  `position`.
- **Side 2 (server)**: `increase(n)` does `position -= n`; `get()` returns
  `-position`.

Thus each side's `get()` is its own cursor position relative to the other side:
a negative value means this side is *behind* the other.

Walk through clientOp and serverOp in a coordinated loop.  Process one client
component, then drain server components while `clientPosition.get() > 0`.

When processing a component for side X:

- `retain(itemCount)`: advance X's cursor and emit retain to **both** outputs
  for the portion of the range that overlaps the region X was behind on:
  1. `oldPos = X.position.get()`
  2. `X.position.increase(itemCount)`  // advances the shared cursor
  3. if `X.position.get() < 0` (X is still behind the other side): emit
     `retain(itemCount)` to **both** the X-output and the Y-output;
  4. else if `oldPos < 0` (X was behind, now caught up or ahead): emit
     `retain(-oldPos)` (only the overlapping portion) to **both** outputs;
  5. else (X was already at or ahead of the other side): emit nothing.
- `characters(s)` / `elementStart` / `elementEnd`: emit the insertion to
  the X-output immediately, and emit `retain(size)` to the **Y-output only**,
  regardless of cursor position.  (Insertions do **not** advance the shared
  position counter.)

Note the contrast: `retain` writes to **both** outputs, whereas an insertion
writes the insert to the X-output and a matching retain to the Y-output only.

**Loop**:

```
clientIndex = serverIndex = 0
while clientIndex < clientOp.size():
    apply clientOp[clientIndex++] to clientTarget
    while clientPosition.get() > 0:
        if serverIndex >= serverOp.size():
            throw TransformException("Ran out of server op components …")
        apply serverOp[serverIndex++] to serverTarget
# drain any remaining server components
while serverIndex < serverOp.size():
    apply serverOp[serverIndex++] to serverTarget
```

The key invariant: when the client emits an insertion, it writes to the
client-output and writes a retain of the same size to the server-output.
This means the server's op, after transform, must skip over the client's
new content.  Symmetrically for server insertions.

The tie-breaking rule emerges from the loop order: client components are
processed first within each iteration, so if both sides are at the same
logical position and both have an insertion, the client's insertion is
output before the server's insertion in both outputs.

**Invariant**: After InsertionTransform, the resulting (ci', si') are both
insertion-only ops, and they apply to the same logical input positions.

#### InsertionNoninsertionTransform (insertion-only vs insertion-free)

**Context**: first op is insertion-only; second op is insertion-free
(has retain, delete, replaceAttributes, updateAttributes).

Walk through the insertion op and the noninsertion op in a coordinated loop
(same position tracker approach).

When the noninsertion op has an active deletion that encompasses the current
cursor position:

- Insertions from the insertion op that fall inside a deleted region are
  absorbed: they appear in the noninsertion-output as *deletes*, not in the
  insertion-output as inserts.  The inserted content is immediately deleted.
- Specifically: `characters(s)` inside a delete → noninsertion-output gets
  `deleteCharacters(s)`, insertion-output gets nothing (the insert effectively
  vanishes).  Likewise for elementStart/elementEnd inside deletes.

When the noninsertion op is retaining at the cursor:

- Insertions from the insertion op: insertion-output keeps the insert,
  noninsertion-output gets a retain of the same size (as if the noninsertion
  op skips over the new content).

When the noninsertion op has an **attribute component** at the cursor and the
cursor is **not** inside an active deletion (deletion depth == 0):

- `replaceAttributes(o, n)`:
  - noninsertion-output gets `replaceAttributes(o, n)` (unchanged);
  - insertion-output gets `retain(1)`.
- `updateAttributes(u)`:
  - noninsertion-output gets `updateAttributes(u)` (unchanged);
  - insertion-output gets `retain(1)`.

This mirrors the retain case (each attribute component occupies exactly one
item): the insertion side emits `retain(1)` while the noninsertion side passes
its attribute change through unchanged.  (If the cursor were inside an active
deletion (depth > 0), these attribute components are not reachable in a
well-formed op: a deleted element-start increments the depth and the matching
delete must close it, so only retain/insert components interact with the
deletion region.)

**Invariant**: After InsertionNoninsertionTransform, both outputs apply to
consistent document states.

#### NoninsertionTransform (insertion-free vs insertion-free)

**Context**: both ops contain only `retain`, `deleteCharacters`,
`deleteElementStart`, `deleteElementEnd`, `replaceAttributes`, `updateAttributes`,
`annotationBoundary`.  No insertions.

Walk through both ops with the position tracker.  For each aligned region,
apply the following resolution table (client component vs server component):

| Client \ Server       | retain(n)                             | deleteCharacters        | deleteElementStart                    | deleteElementEnd        | replaceAttributes(o,n)                  | updateAttributes(u)                         |
|-----------------------|---------------------------------------|-------------------------|---------------------------------------|-------------------------|-----------------------------------------|---------------------------------------------|
| **retain(n)**         | both output retain(n)                 | server: deleteChars; client: nothing (chars already gone) | server: deleteElementStart; client: nothing | server: deleteElementEnd; client: nothing | server: replaceAttributes; client: retain(1) | server: updateAttributes; client: retain(1) |
| **deleteCharacters**  | client: deleteChars; server: nothing  | both: nothing (same chars deleted; shorter one wins) | INVALID — incompatible types     | INVALID                 | INVALID                                 | INVALID                                     |
| **deleteElementStart**| client: deleteElementStart; server: nothing | INVALID          | both: nothing (both deleted same element) | INVALID             | client: deleteElementStart(t, newA); server: nothing | client: deleteElementStart(t, u(a)); server: nothing |
| **deleteElementEnd**  | client: deleteElementEnd; server: nothing | INVALID           | INVALID                               | both: nothing           | INVALID                                 | INVALID                                     |
| **replaceAttributes** | client: replaceAttributes; server: retain(1) | INVALID         | server: deleteElementStart(t, clientNewA); client: nothing | INVALID | client: replaceAttributes(serverNewA, clientNewA); server: retain(1) | client: replaceAttributes(u(clientOld), clientNewA); server: retain(1) |
| **updateAttributes(u)** | client: updateAttributes; server: retain(1) | INVALID         | server: deleteElementStart(t, u(a)); client: nothing | INVALID    | client: retain(1); server: replaceAttributes(u(serverOld), serverNew) | _composed update: see below_               |

Notes:
- "nothing" in the output means that side's output receives nothing for this
  region (the items were deleted by the other side).
- INVALID means the two ops are structurally inconsistent — this raises a
  TransformException in the production code.
- When both sides delete the same element start/end/characters, both outputs
  receive nothing (the deletion is idempotent from both perspectives).
- When one side deletes and the other retains, the retaining side gets nothing
  (the item is gone) and the deleting side keeps its deletion.
- `u(a)` means "apply update u to attributes a".
- `u(clientOld)` means "apply server updateAttributes u to the client's old
  attribute set."
- When both sides do `updateAttributes(uc)` and `updateAttributes(us)`:
  - The client output gets `updateAttributes(uc')` where `uc'` has the same
    new values as `uc` but the old values are updated to reflect that `us`
    has already been applied.
  - The server output gets `updateAttributes(us')` = the subset of `us` whose
    keys are not in `uc` (keys in `uc` take precedence; the client wins).
- When client does `replaceAttributes(oldC, newC)` and server does
  `replaceAttributes(oldS, newS)`:
  - Client output: `replaceAttributes(newS, newC)` (client's new attrs win,
    but old must reflect server's replacement).
  - Server output: `retain(1)` (server's replace is superseded by client's).
- When client does `replaceAttributes(oldC, newC)` and server does
  `updateAttributes(us)`:
  - Client output: `replaceAttributes(us(oldC), newC)` — the old-attrs field
    reflects the server's update to what was there.
  - Server output: `retain(1)` — server's update is superseded.
- When client does `updateAttributes(uc)` and server does
  `replaceAttributes(oldS, newS)`:
  - Client output: `retain(1)` — client's update is superseded by server's replace.
  - Server output: `replaceAttributes(uc(oldS), newS)` — server's old-attrs
    field reflects the client's update.

**Annotation transform** during NoninsertionTransform:

Annotations are tracked by an `AnnotationTracker` per side (a client tracker and
a server tracker).  Each tracker maintains two distinct maps that an implementer
**must not conflate**:

- `tracked` — updated **eagerly** every time that side *reads* an
  `annotationBoundary` component (the `register` step: remove each `endKey`, and
  for each `changeKey` store `{oldValue, newValue}`).  This is the map consulted
  by the transform's per-key decisions below — always the **opposing** side's
  `tracked` map.
- `active` — updated only when a boundary is *committed* (written to output).
  `active` is used solely by the deletion-boundary logic
  (`commenceDeletion`/`concludeDeletion`); it is **not** used by the end/change
  transform decision.

The two maps diverge while the operations are walked: a boundary that has been
read but not yet committed appears in `tracked` but not in `active`.  Consulting
`active` instead of `tracked` for the end/change decisions below would produce
incorrect results.

The end/change transform is **asymmetric** between the two sides.  When an
`annotationBoundary(m)` component is processed:

*On the client side* (`clientAnnotationTracker.process`):

- For each key K in `m.endKeys`:
  - The client's output **always** gets an `end(K)`.
  - Additionally, if the server is tracking K (K in `serverAnnotationTracker.tracked`),
    the server's output gets a `change(K)` restoring the server's tracked
    `(oldValue, newValue)`.
- For each key K in `m.changeKeys` with the client's `(oldC, newC)`:
  - The client's output gets a `change(K)` with `newValue = newC`.
  - If the server is tracking K (value `serverV`): the change's
    `oldValue = serverV.newValue` (the value after the server's annotation), and
    the server's output gets an `end(K)`.
  - Otherwise: the change's `oldValue = oldC` (pass through), and the server
    output gets nothing for K.

*On the server side* (`serverAnnotationTracker.process`) — note the asymmetry in
the end-key handling:

- For each key K in `m.endKeys`:
  - If the client is tracking K (K in `clientAnnotationTracker.tracked`, value
    `clientV`): the client's output gets a `change(K)` with the client's tracked
    `(clientV.oldValue, clientV.newValue)`, and the **server emits nothing for K**
    (the end is dropped).
  - Otherwise (client not tracking K): the server's output gets an `end(K)`.
- For each key K in `m.changeKeys` with the server's `(oldS, newS)`:
  - If the client is tracking K (value `clientV`): the client's output gets a
    `change(K)` with `oldValue = newS` and `newValue = clientV.newValue`, and the
    server output gets nothing for K.
  - Otherwise: the server's output gets a `change(K)` with `(oldS, newS)`
    (pass through).

In short: the client **always** emits an end for each ended key; the server emits
an end for an ended key **only when the other (client) side is not tracking it** —
if the client is tracking K, the server emits nothing for K and the client emits
a change instead.

During deletions (`deleteCharacters`, `deleteElementStart/End`), the annotation
tracker must emit annotation boundary adjustments to ensure the resulting
document has consistent annotation values.  Specifically:

- Before a deletion, `commenceDeletion()` emits any annotation changes needed
  to bring the annotation state at the deletion point to the inherited value
  (what the surrounding context should have after the deleted content is gone).
- After a deletion, `concludeDeletion()` emits annotation changes to restore
  the previous annotation state.

**INVARIANT (TP1)**: For any valid client op C and server op S applying to
the same document state:
```
apply(apply(D, S), C') == apply(apply(D, C), S')
where (C', S') = Transform(C, S)
```

---

### Compose

`Compose(op1, op2) → op`

Returns the single DocOp equivalent to applying op1 followed by op2.
op2 must be valid against the document that op1 produces (op1's output length
must equal op2's input length).

**Algorithm** (Composer state machine):

Walk through op1 and op2 simultaneously, keeping a "pending" state that
describes what the *pre* (op1) side is waiting for from the *post* (op2) side.

For each component pair:

| op1 (pre) component   | op2 (post) component   | Composed output          | Notes                                              |
|-----------------------|------------------------|--------------------------|----------------------------------------------------|
| retain(n)             | retain(m)              | retain(min(n,m))         | If n > m, retain(m) and keep retain(n-m) pending. |
| retain(n)             | characters(s)          | characters(s), then drain retain | Insert s, reduce pending retain by len(s). |
| retain(n)             | elementStart(t, a)     | elementStart(t, a)       | Reduce pending retain by 1.                        |
| retain(n)             | elementEnd()           | elementEnd()             | Reduce pending retain by 1.                        |
| retain(n)             | deleteCharacters(s)    | deleteCharacters(s)      | Reduce pending retain by len(s).                   |
| retain(n)             | deleteElementStart     | deleteElementStart       | Reduce pending retain by 1.                        |
| retain(n)             | deleteElementEnd       | deleteElementEnd         | Reduce pending retain by 1.                        |
| retain(n)             | replaceAttributes      | replaceAttributes        | Reduce pending retain by 1.                        |
| retain(n)             | updateAttributes       | updateAttributes         | Reduce pending retain by 1.                        |
| characters(s)         | retain(m)              | characters(s[0:m])       | If len(s) > m, keep remaining pending.             |
| characters(s)         | deleteCharacters(d)    | partial cancel — see note | If len(d) < len(s), keep remaining insert `characters(s[len(d):])` pending. If equal, both cancel (nothing). If len(d) > len(s), all of `s` cancels and the excess `deleteCharacters(d[len(s):])` is carried as pending against op1. |
| elementStart(t, a)    | retain(1)              | elementStart(t, a)       |                                                    |
| elementStart(t, a)    | deleteElementStart     | _(nothing)_              | Insert then immediately delete cancels out.        |
| elementStart(t, a)    | replaceAttributes(o,n) | elementStart(t, n)       | Use new attrs from replace.                        |
| elementStart(t, a)    | updateAttributes(u)    | elementStart(t, a.updateWith(u)) |                                          |
| elementEnd()          | retain(1)              | elementEnd()             |                                                    |
| elementEnd()          | deleteElementEnd()     | _(nothing)_              | Insert then immediately delete cancels out.        |
| deleteCharacters(s)   | retain(m)              | deleteCharacters(s[0:m]) | If len(s) > m, keep remaining pending.             |
| deleteElementStart    | retain(1)              | deleteElementStart       |                                                    |
| deleteElementEnd      | retain(1)              | deleteElementEnd         |                                                    |
| replaceAttributes(o,n)| retain(1)              | replaceAttributes(o, n)  |                                                    |
| replaceAttributes(o,n)| deleteElementStart     | deleteElementStart(t, o) | Use old attrs from pre's replace.                  |
| replaceAttributes(o,n)| replaceAttributes(o2, n2) | replaceAttributes(o, n2) | Pre's old, post's new.                         |
| replaceAttributes(o,n)| updateAttributes(u)    | replaceAttributes(o, n.updateWith(u)) |                                        |
| updateAttributes(u1)  | retain(1)              | updateAttributes(u1)     |                                                    |
| updateAttributes(u1)  | deleteElementStart(t,a)| deleteElementStart(t, a.updateWith(invert(u1))) | Reconstruct pre-u1 state.          |
| updateAttributes(u1)  | replaceAttributes(o,n) | replaceAttributes(o.updateWith(invert(u1)), n)  | Pre-u1 old.                        |
| updateAttributes(u1)  | updateAttributes(u2)   | updateAttributes(u1.composeWith(u2)) |                                        |

`invert(u)` swaps each (oldValue, newValue) pair.

Any pair not listed (e.g., `characters` vs `deleteElementStart`) is an illegal
composition and raises an `OperationException`.

**Annotation composition** during Compose:

The Composer maintains two maps that record which annotation keys are currently
active (open) on each side: `preAnnotations` (keys active in op1) and
`postAnnotations` (keys active in op2).  Annotation boundaries are **queued** per
side and flushed (`unqueue`) when a non-annotation component arrives.  The two
flush handlers are symmetric (swap pre/post):

When op1's queued boundary is flushed (`preAnnotationQueue.unqueue`):

- For each key K that op1 **ends**:
  - If op2 currently has K active (K in `postAnnotations`), emit a `change(K)`
    using op2's `(oldValue, newValue)`; otherwise emit an `end(K)`.
  - Remove K from `preAnnotations`.
- For each key K that op1 **changes** to `(old1, new1)`:
  - Emit a `change(K)` with `oldValue = old1` and
    `newValue = (op2's newValue if K is active in postAnnotations, else new1)`.
  - Record `preAnnotations[K] = (old1, new1)`.

When op2's queued boundary is flushed (`postAnnotationQueue.unqueue`):

- For each key K that op2 **ends**:
  - If op1 currently has K active (K in `preAnnotations`), emit a `change(K)`
    using op1's `(oldValue, newValue)`; otherwise emit an `end(K)`.
  - Remove K from `postAnnotations`.
- For each key K that op2 **changes** to `(old2, new2)`:
  - Emit a `change(K)` with `newValue = new2` and
    `oldValue = (op1's oldValue if K is active in preAnnotations, else old2)`.
  - Record `postAnnotations[K] = (old2, new2)`.

Consequences: if op1 sets K to `(old1, new1)` and op2 later sets K to
`(old2, new2)`, the composed change for K resolves to `(old1, new2)` (the
intermediate value disappears).  If op1 changes K and op2 ends K, the composed
output is a definite `change` (using op2's queued boundary handler, since K is
active in `preAnnotations`), **not** an ambiguous "may be an end".  Keys
mentioned by only one side pass through with that side's values.

**Composing a sequence**: `DocOpCollector.composeAll()` uses a tree-composition
algorithm (binary tree of pairwise composes), achieving O(n log N) rather than
O(n²) for N operations of total size n.

---

### Invert

`Invert(op) → op'`

Returns an operation that exactly undoes op.  Applying op then op' to a
document leaves it unchanged.

The inversion rules are trivially reversible at the component level:

| Component              | Inverse                       |
|------------------------|-------------------------------|
| `retain(n)`            | `retain(n)`                   |
| `characters(s)`        | `deleteCharacters(s)`         |
| `elementStart(t, a)`   | `deleteElementStart(t, a)`    |
| `elementEnd()`         | `deleteElementEnd()`          |
| `deleteCharacters(s)`  | `characters(s)`               |
| `deleteElementStart(t, a)` | `elementStart(t, a)`     |
| `deleteElementEnd()`   | `elementEnd()`                |
| `replaceAttributes(o, n)` | `replaceAttributes(n, o)` |
| `updateAttributes(u)`  | `updateAttributes(invert(u))` |
| `annotationBoundary(m)` | `annotationBoundary(swap(m))` |

Where `invert(u)` swaps each entry's (oldValue, newValue) to (newValue, oldValue),
and `swap(m)` swaps the oldValue/newValue in each changeKey entry.

The order of components is preserved (no reversal of the component list).

**Note**: Invert is a simple per-component mapping; the component list order
does not change.  The inverse of a valid op is a valid op for the resulting
document (the one after applying the original op).

---

### Wavelet-level transform

`Transform(clientWaveletOp, serverWaveletOp) → (clientOp', serverOp')`

Dispatch on the operation types:

| Client \ Server           | WaveletBlipOperation(same blipId) | WaveletBlipOperation(diff blipId) | AddParticipant(P) | RemoveParticipant(P) |
|---------------------------|-----------------------------------|------------------------------------|-------------------|-----------------------|
| **WaveletBlipOperation(same blipId)** | Transform inner DocOps | identity (no conflict)          | identity          | identity              |
| **WaveletBlipOperation(diff blipId)** | identity (no conflict)  | identity                         | identity          | identity              |
| **AddParticipant(P)**     | identity                          | identity                          | if same P: both→NoOp; else identity | TransformException if same P; else identity |
| **RemoveParticipant(P)**  | if server removes clientOp's creator: RemovedAuthorException | identity | TransformException if same P; else identity | if same P: both→NoOp; else identity |

Concretely:

1. If both ops are `WaveletBlipOperation` for the **same blip**:
   - Extract inner `DocOp`s.
   - Call `Transformer.transform(clientDocOp, serverDocOp)`.
   - Wrap results back in `WaveletBlipOperation`.

2. If both ops are `WaveletBlipOperation` for **different blips**:
   - Identity transform: (clientOp, serverOp) unchanged.

3. If server is `RemoveParticipant(P)` and `P == clientOp.context.creator`:
   - Throw `RemovedAuthorException`: the client's operation author has been
     concurrently removed.

4. If both ops are `RemoveParticipant(P)` for the same P:
   - Both become `NoOp` (idempotent removal).

5. If both ops are `AddParticipant(P)` for the same P:
   - Both become `NoOp` (idempotent addition).

6. If one is `AddParticipant(P)` and the other is `RemoveParticipant(P)`:
   - Throw `TransformException`: concurrent add+remove of the same participant
     is ambiguous.

7. All other mismatched combinations: identity transform.

---

### Normalization

A DocOp is **normalized** if:

1. No two adjacent `retain(a)` and `retain(b)` — they must be merged into
   `retain(a+b)`.
2. No two adjacent `characters(s1)` and `characters(s2)` — they must be
   merged.
3. No two adjacent `deleteCharacters(s1)` and `deleteCharacters(s2)` — merge.
4. No empty components: `retain(0)`, `characters("")`, etc. are not permitted.
5. No adjacent `annotationBoundary` components.

The `OperationNormalizer` pipeline chains:
- `RangeNormalizer`: merges adjacent retains, merges adjacent character runs,
  merges adjacent deleteCharacter runs, suppresses zero-length components.
- `AnnotationsNormalizer`: suppresses redundant annotation boundaries
  (boundaries that set a key to the same value it already has, or end a key
  that was not open).

**When normalization is applied**: all transform and compose outputs pass
through `OperationNormalizer.createNormalizer(target)`, which is
`AnnotationsNormalizer(RangeNormalizer(target))`.  Raw buffers (`DocOpBuffer`,
`UncheckedDocOpBuffer`) accumulate components before flushing.

---

### Undo (AggregateOperation / WaveAggregateOp)

The undo system wraps sequences of wavelet operations into `AggregateOperation`
objects that aggregate participant additions/removals and per-document DocOps.

**Invert**: `AggregateOperation.invert()`:
- Swaps `participantsToAdd` ↔ `participantsToRemove`.
- For each document's `DocOpList`, composes the list to a single op, inverts
  it via `DocOpInverter.invert()`, and uses that as the new op list.
- Returns a new `AggregateOperation`.

**Compose** (of aggregate ops): merges participant sets (add/remove cancel each
other), concatenates DocOp lists per document ID, and eliminates add+remove
pairs for the same participant.

**Transform** (of aggregate ops): dispatches doc-level transform via
`Transformer.transform` for matching document IDs; participant transforms use
the same idempotence/error rules as the wavelet-level transform.

The `WaveAggregateOp` layer adds a creator identity to each aggregate operation
and groups ops by creator for compose.  Its invert reverses the op-pair list
and inverts each contained `AggregateOperation`.

---

## Wire / storage formats

DocOps are serialized in the protobuf format defined in `wave/model/proto/` as
part of wavelet deltas.  See spec 04 for the exact protobuf message shapes.

At the conceptual level, each DocOp is serialized as an ordered list of
protobuf `DocOp.Component` messages, one per component.  Component types are
encoded as a oneof/discriminated union.

---

## Interfaces / APIs

```
// Document-level transform
OperationPair<DocOp> Transformer.transform(DocOp clientOp, DocOp serverOp)
    throws TransformException

// Composition
DocOp Composer.compose(DocOp op1, DocOp op2) throws OperationException
DocInitialization Composer.compose(DocInitialization op1, DocOp op2)
    throws OperationException
DocOp Composer.compose(Iterable<DocOp> operations)

// Inversion
DocOp DocOpInverter.invert(DocOp input)

// Validation
boolean DocOpValidator.isWellFormed(ViolationCollector v, DocOp op)
ValidationResult DocOpValidator.validate(ViolationCollector v,
    DocumentSchema schema, AutomatonDocument doc, DocOp op)

// Wavelet-level transform (two flavors, same semantics)
OperationPair<WaveletOperation> Transform.transform(
    WaveletOperation clientOp, WaveletOperation serverOp)
    throws TransformException

OperationPair<CoreWaveletOperation> CoreTransform.transform(
    CoreWaveletOperation clientOp, ParticipantId clientAuthor,
    CoreWaveletOperation serverOp, ParticipantId serverAuthor)
    throws TransformException
```

`TransformException` signals structural incompatibility.
`RemovedAuthorException` (a subclass of `TransformException`) signals the
specific case where the client's author was concurrently removed.

---

## Edge cases & failure modes

**Client op longer than server op**: If the client retain/delete sum exceeds
the server's, `InsertionTransformer` or `NoninsertionTransformer` throws
`TransformException("Ran out of server op components…")`.

**Incompatible concurrent deletions**: e.g., one side deletes characters while
the other side deletes an element at the same position.  The `RangeCache`
resolution methods throw `InternalTransformException("Incompatible operations
in transformation")`, which is wrapped in `TransformException`.

**Illegal composition**: e.g., composing `characters` in op1 with
`deleteElementStart` in op2 (type mismatch).  Raises `OperationException
("Illegal composition")`.

**Composing ops with mismatched lengths**: op1's output length ≠ op2's input
length.  Detected by the Composer state machine; raises `OperationException
("Document size mismatch")`.

**Concurrent add+remove of same participant**: raises `TransformException`.

**Removed author exception**: if server removes the creator of the client's
operation, the server's transform throws `RemovedAuthorException`.  The client
protocol layer must handle this (see spec 03).

**Empty DocOp**: A DocOp with no components (size 0) is well-formed and
represents identity on the empty document.  It cannot be applied to any
non-empty document.

**Annotations during deletion**: annotations in the deleted region must be
explicitly reset in the op.  An op that deletes a region carrying annotation
K=V must include an `annotationBoundary` that sets K to the inherited value
(or ends K if K wasn't set in the inherited context).  The `DocOpAutomaton`
validates this.

---

## Open questions / ambiguities

1. **Annotation transform exactness**: The Transformer.java source code contains
   a TODO comment: "Tweak the behaviour of this transformer to exactly match
   the reference implementation in the tests.  Specifically, the optimized
   implementation in this class may output extraneous annotations which have no
   operational effect."  This means the optimized production transformer and
   the `ReferenceTransformer` (in the test directory) may produce
   **structurally different but semantically equivalent** results.  A Go
   reimplementation should target semantic equivalence (TP1 is satisfied),
   not byte-identical output.

2. **ReferenceTransformer vs production Transformer**: There are two transformer
   implementations.  `ReferenceTransformer` (in `testing/reference/`) uses a
   6-way decomposition (insertion, preservation, deletion) and a more complex
   loop to handle annotation tameness.  The production `Transformer` uses a
   2-way decomposition (insertion, non-insertion).  Both are claimed to satisfy
   TP1.  Only the production `Transformer` is used at runtime.

3. **AnnotationBoundaryMap ordering**: The spec says end keys and change keys
   must each be strictly sorted within their respective lists, but there is no
   constraint on the *relative* ordering between the two lists.  Implementations
   must sort each list independently.

4. **Surrogate handling**: The `deleteCharacters` and `characters` components
   reject strings containing surrogate code units.  This is a Java-specific
   UTF-16 constraint.  In Go (which uses UTF-8 strings), the equivalent
   constraint is: no lone surrogate code points; strings must be valid UTF-8.
   Any Go serialization/deserialization layer must validate this on the boundary.

5. **Normalization is not canonical**: two semantically identical operations
   may produce different normalized forms if they were constructed differently.
   The normalizer does not exhaustively canonicalize — it merges adjacent
   same-type range components and suppresses no-op annotations, but does not
   reorder or further simplify.

6. **`updateAttributes` transform tie-breaking**: When both sides do
   `updateAttributes`, the client's new values win for overlapping keys.  The
   server's `updateAttributes` for overlapping keys is dropped.  This is the
   only attribute-update tie-breaking rule; it is *not* symmetric.

7. **Version metadata in WaveletOperationContext**: the `versionIncrement` and
   `hashedVersion` fields are metadata for the concurrency protocol (spec 03)
   and do not affect the OT algorithms themselves.

---

## Source references

| Path | Role |
|------|------|
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/DocOp.java` | DocOp interface (component accessors) |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/DocOpCursor.java` | Visitor interface for applying a DocOp |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/DocOpComponentType.java` | Enumeration of all component types |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/DocInitializationCursor.java` | Visitor interface for initialization ops |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/AnnotationBoundaryMap.java` | Annotation boundary descriptor |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/AttributesUpdate.java` | Attribute mutation descriptor |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/Transformer.java` | **Production transform entry point** |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/Decomposer.java` | Decompose into insertion + non-insertion |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/InsertionTransformer.java` | Transform insertion-only vs insertion-only |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/InsertionNoninsertionTransformer.java` | Transform insertion vs non-insertion |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/NoninsertionTransformer.java` | Transform non-insertion vs non-insertion (full resolution table + annotation tracking) |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/Composer.java` | Compose two DocOps |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/DocOpInverter.java` | Invert a DocOp |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/OperationNormalizer.java` | Normalization pipeline factory |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/RangeNormalizer.java` | Merges adjacent same-type range components |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/AnnotationsNormalizer.java` | Suppresses redundant annotation boundaries |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/PositionTracker.java` | Shared cursor position tracker used by all transformers |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/algorithm/DocOpCollector.java` | Tree-compose of N operations in O(n log N) |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/automaton/DocOpAutomaton.java` | State machine for validating DocOps against a document |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/impl/DocOpValidator.java` | Validation entry point (well-formedness + schema validation) |
| `wave/src/main/java/org/waveprotocol/wave/model/document/operation/automaton/DocumentSchema.java` | DocumentSchema contract + PermittedCharacters (NONE/BLIP_TEXT/ANY) + coerceString + NO_SCHEMA_CONSTRAINTS |
| `wave/src/main/java/org/waveprotocol/wave/model/schema/conversation/ConversationSchemas.java` | Schema selection for conversational wavelets; hard-coded BLIP/MANIFEST constraints |
| `wave/src/main/java/org/waveprotocol/wave/model/schema/SchemaCollection.java` | Combines SchemaProviders; `empty()` yields NO_SCHEMA_CONSTRAINTS |
| `wave/src/main/java/org/waveprotocol/wave/model/schema/supplement/UserDataSchemas.java` | Schema provider for per-user supplement documents |
| `wave/src/main/java/org/waveprotocol/wave/model/schema/account/AccountSchemas.java` | Schema provider for account documents |
| `wave/src/main/java/org/waveprotocol/box/server/util/WaveletDataUtil.java` | Server builds wavelet docs with `SchemaCollection.empty()` (no schema enforcement; TODO issue 109) |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/core/CoreTransform.java` | Wavelet-level transform (core/federation variant) |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/wave/Transform.java` | Wavelet-level transform (client/server variant) |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/wave/WaveletOperation.java` | WaveletOperation base class |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/wave/WaveletBlipOperation.java` | Wraps a BlipContentOperation for a specific blip |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/wave/AddParticipant.java` | Add participant op |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/wave/RemoveParticipant.java` | Remove participant op (reverse records position) |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/OperationPair.java` | Two-tuple of (clientOp, serverOp) returned by transform |
| `wave/src/main/java/org/waveprotocol/wave/model/operation/TransformException.java` | Thrown on structural transform incompatibility |
| `wave/src/main/java/org/waveprotocol/wave/model/wave/undo/AggregateOperation.java` | Aggregated wavelet op for undo, with compose/transform/invert |
| `wave/src/main/java/org/waveprotocol/wave/model/wave/undo/WaveAggregateOp.java` | Aggregate op with creator attribution |
| `wave/src/test/java/org/waveprotocol/wave/model/operation/testing/reference/ReferenceTransformer.java` | Reference (slower, 6-decomposition) transformer for testing |
| `wave/src/test/java/org/waveprotocol/wave/model/operation/testing/reference/InsertionInsertionTransformer.java` | Reference insertion-insertion transformer (shows tie-break ordering clearly) |
