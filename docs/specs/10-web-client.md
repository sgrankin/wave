# 10 — Web Client: Architecture, Editor, and Rendering

## Purpose & scope

This spec describes the Apache Wave browser client ("Undercurrent"): its overall
architecture, the staged loading sequence, the wave/search panels, and — most
critically — the rich-text editor: how the persistent document model maps to/from
the browser DOM, how user edits are converted into DocOps, how annotations are
rendered, and how special content elements ("doodads") work.

Client-side OT and the concurrency-control channel are covered by
[03-concurrency-control](03-concurrency-control.md) and
[04-wire-protocol](04-wire-protocol.md). This spec focuses on rendering and
editing behavior; it cites the concurrency specs rather than re-deriving them.

The client is a GWT (Java → JS) single-page application. GWT-specific mechanisms
(UiBinder, GWT module splitting, GWT scheduler) are noted as implementation
artifacts; a rebuild need not replicate them. The *contracts* they implement must
be replicated.

---

## Concepts & glossary

| Term | Definition |
|------|-----------|
| **ContentDocument** | The client-side document object: an indexed tree of ContentNodes backed by both a logical data model and live DOM nodes. The central editor abstraction. |
| **ContentNode / ContentElement / ContentTextNode** | Client-side wrapper types for document nodes. Each `ContentElement` corresponds to one element in the wave document tree; each `ContentTextNode` covers one text run. |
| **Nodelet** | The actual browser DOM node(s) that a ContentNode "owns". A ContentElement has one or two nodelets (container + impl); a ContentTextNode has one text nodelet. |
| **NodeManager** | Maintains a bidirectional map between DOM nodes and ContentNodes via expando properties on DOM nodes (called "backreferences"). |
| **ContentView** | A read-only filtered view over the ContentDocument tree. Variants: *persistent* (only what's stored), *rendered* (persistent + transparent decorations), *full* (everything including local-only paint nodes). |
| **HtmlView** | A filtered view of the raw browser DOM, aware of which nodes are transparent decorations vs. persistent content. |
| **Transparency** | DOM nodes inserted by the editor for layout/rendering purposes that are invisible to the logical document model. Marked with a property on the DOM node. |
| **Doodad** | A document element that requires custom rendering and/or event handling (gadgets, image thumbnails, inline replies, links, etc.). Registered via `ElementHandlerRegistry`. |
| **TypingExtractor** | Component that listens to DOM mutations caused by native browser typing and converts them back into DocOps (text-replacement operations). |
| **Repairer** | Corrects DOM inconsistencies (DOM out of sync with model) by re-applying the model state to the DOM. |
| **AnnotationPainter** | Deferred renderer that walks annotation ranges and injects local-only `<l:s>` (spread) and `<l:b>` (boundary) paint elements into the DOM to carry CSS styles. |
| **EditSession** | Lifecycle object that tracks which blip is currently being edited and manages the editor's attachment/detachment to a ContentDocument. |
| **FocusFrame** | The visual indicator of which blip has keyboard focus; drives both reading (read-state updates) and editing. |
| **InteractiveDocument** | Interface wrapping a ContentDocument with diff-highlighting and rendering lifecycle control (`startRendering`, `stopRendering`, `startDiffSuppression`, etc.). |
| **LazyContentDocument** | Concrete `InteractiveDocument`: loads a full `ContentDocument` on demand (when a blip is first rendered or opened for editing). |
| **Supplement** | Per-user read/unread and thread-collapse state. See [01-data-model](01-data-model.md). |
| **LocalSupplementedWave** | Client-side optimistic wrapper around the server-backed supplement that immediately marks blips as read locally while server acks catch up. |
| **WavePanel** | The root UI container for a wave: participants header, root thread, nested inline threads, reply boxes. |
| **Blip view** | The rendered HTML for one blip: header (time, authors), content document area, reply threads. |
| **BlipQueueRenderer** | Queues blip renders and flushes them incrementally, preventing long UI freezes on large waves. |
| **Stage (Zero–Three)** | The four async loading phases the client goes through before editing is available. |
| **MuxConnector** | Client-side component that opens the OperationChannel for a wave and wires incoming/outgoing operations to the in-memory model. See [03-concurrency-control](03-concurrency-control.md). |

---

## Data structures

### 10.1 ContentDocument

```
ContentDocument {
    // Underlying indexed XML tree (shared model layer):
    indexedDoc       IndexedDocumentImpl<ContentNode, ContentElement, ContentTextNode>

    // Parallel annotation store:
    annotations      RawAnnotationSet

    // DOM linkage:
    nodeManager      NodeManager         // DOM ↔ ContentNode bidirectional map
    renderedView     ContentView         // view of rendered (visible) content
    persistentView   ContentView         // view of only persistent content

    // Output:
    outgoingSink     SilentOperationSink<DocOp>   // receives local ops; wired to OT pipeline

    // Rendering registries (set once, before first render):
    registries       Registries          // ElementHandlerRegistry + AnnotationRegistry + PainterRegistry

    // State flags:
    renderingEnabled bool
    editingMode      bool
}
```

**Invariant**: the DOM under the document's root nodelet MUST always be a valid
rendering of the persistent content tree, modulo transparent decoration nodes.
Any discrepancy is a bug and must be repaired before the next user interaction.

### 10.2 ContentNode hierarchy

```
ContentNode (abstract)
    ├─ ContentElement
    │       tagName          string
    │       attributes       map<string, string>   // persistent
    │       transientData    map<int, object>       // non-persistent property bag
    │       containerNodelet Element               // outer DOM element
    │       implNodelet      Element               // may differ (e.g. for doodads with shadow DOM)
    │       children         list<ContentNode>
    └─ ContentTextNode
            text             string                // logical text
            textNodelets     []Text                // one or more DOM text nodes (browser may split)
```

Each ContentElement has exactly two nodelet pointers: a *container* nodelet
(participates in the filtered HTML view) and an *impl* nodelet (where children
are placed). For simple elements they are the same DOM node. For doodads, the
impl nodelet may be a shadow subtree that the NodeManager's HtmlView skips over.

### 10.3 NodeManager backreferences

NodeManager stores the content→DOM and DOM→content mappings using expando
properties on DOM nodes (JavaScript object properties with synthetic short names
like `_x1`, `_x2`, …). These names are generated at startup and stable for a
session. Key mappings:

- `BACKREF_NAME` on a DOM Element → its owning `ContentElement`.
- `TRANSPARENCY` on a DOM Element → skip level (`SHALLOW` or `DEEP` or `NONE`),
  meaning the node is a transparent decoration.
- `TRANSPARENT_BACKREF` on a transparent Element → its `TransparentManager`.

### 10.4 Registries

The editor uses three registries, composed into a single `Registries` object:

```
ElementHandlerRegistry   // per element tag name:
    renderers            map<tag, Renderer>          // model→DOM initial render
    eventHandlers        map<tag, NodeEventHandler>  // DOM events routed to element
    mutationHandlers     map<tag, NodeMutationHandler> // model tree mutations

AnnotationRegistry       // per annotation key prefix:
    mutationHandlers     map<prefix, AnnotationMutationHandler>
    behaviours           map<prefix, AnnotationBehaviour>  // cursor/selection biasing

PainterRegistry          // for annotation painting:
    paintFunctions       map<key-set, PaintFunction>   // key range → CSS attrs map
    boundaryFunctions    map<key-set, BoundaryFunction>
    painter              AnnotationPainter              // the deferred paint worker
```

All three are populated during Stage Two / Stage Three initialization before
any document is rendered.

### 10.5 Client model objects (Stage Two)

```
WaveViewImpl {
    waveId       WaveId
    wavelets     map<WaveletId, OpBasedWavelet>
}

ObservableConversationView   // live view of conversations from wavelet structure
    conversations  list<ObservableConversation>

WaveDocuments {
    // Registry keyed by ConversationBlip → InteractiveDocument
    // (LazyContentDocument in practice)
    docs   IdentityMap<ConversationBlip, LazyContentDocument>
}

LocalSupplementedWave {
    inner           ObservableSupplementedWave  // server-backed
    reading         ConversationBlip            // blip currently being viewed
    autoreadBlips   IdentitySet<ConversationBlip>  // locally read, awaiting ack
}
```

### 10.6 Search / digest model

```
Digest {
    waveId          WaveId
    title           string
    participants    []ParticipantId
    lastModified    Timestamp
    unreadCount     int
    blipCount       int
    snippet         string
}

Search {
    state           SEARCHING | READY
    digests         list<Digest>
    query           string
    numResults      int
}
```

---

## Algorithms & behavior

### 10.7 Staged loading sequence

The client loads in four async stages. Each stage's components are created once
and passed forward; stages do not re-initialize.

```
Stage Zero (synchronous)
    - Minimal DOM bootstrap (no wave data).
    - Sets up GWT module, CSS.

Stage One (synchronous after Zero)
    - Creates WavePanel DOM structure (empty shell).
    - Installs FocusFramePresenter (keyboard focus management).
    - Installs CollapsePresenter (thread collapse/expand).
    - Creates DOM-as-view provider (maps DOM element IDs to typed view objects).
    → Result: an empty, interactive wave panel UI.

Stage Two (async — waits for wave data from server)
    - Opens WebSocket to server.
    - Sends ProtocolOpenRequest for the target wave ID.
    - Receives initial ProtocolWaveletUpdate (snapshot) for each wavelet.
    - Builds in-memory wave model: WaveViewImpl, OpBasedWavelet per wavelet.
    - Builds ConversationView from conversation wavelet manifest.
    - Creates WaveDocuments registry (all LazyContentDocuments, not yet loaded).
    - Creates LocalSupplementedWave.
    - Creates BlipReadStateMonitor, ThreadReadStateMonitor.
    - Installs FullDomRenderer: renders the entire conversation tree to HTML
      (blip shells, thread structure, participants) in one pass.
    - Installs BlipQueueRenderer for incremental blip content rendering.
    - Opens MuxConnector: binds each wavelet to an OperationChannel.
      (See [03-concurrency-control](03-concurrency-control.md) §3.)
    - Installs Reader (focus→read-state updates).
    → Result: wave is visible and readable; diffs shown; no editing yet.

Stage Three (synchronous after Two)
    - Installs EditSession and EditController.
    - Installs EditToolbar and ViewToolbar.
    - Installs ParticipantController (add/remove participant UI).
    - Installs ReplyIndicatorController (inline reply markers).
    - Installs MenuController (blip context menus).
    - Registers doodad handlers (see §10.15).
    → Result: editing, toolbars, participant management all active.
```

**GWT note**: GWT implements stage splitting via `GWT.runAsync` to defer loading
JS bundles. A rebuild does not need this; the split points are architectural
contracts, not execution requirements.

### 10.8 Wave panel layout

```
WavePanel
├── ParticipantsView         // avatar list + add/remove
└── RootThread
    └── [Blip]*
        ├── BlipMeta         // header: author avatars, timestamp, menu button
        ├── BlipContent      // document rendering area
        │   └── [InlineThread]*   // reply threads anchored inside the document
        │       └── [Blip]*
        └── ContinuationIndicator // "Add reply" affordance at thread end
```

Each blip and thread gets a stable DOM element ID derived from the model IDs via
`ViewIdMapper`. This allows DOM-as-view lookup after initial render.

### 10.9 Initial wave rendering

After Stage Two has the wave model and DOM structure ready:

1. `FullDomRenderer.renderConversation(c)` walks the conversation tree
   top-down and produces `UiBuilder` HTML strings for each view object (blip,
   thread, participants). This is a single synchronous pass.
2. The resulting HTML is injected into the WavePanel DOM.
3. `BlipQueueRenderer` is handed a list of all blips. It renders blip *content*
   (the document area) asynchronously: N blips per scheduler tick, starting
   with the blip nearest the target blip (from URL hash or first unread).
4. For each blip, `ShallowBlipRenderer.render(blip, view)` fills in metadata
   (time, contributors, read state) without loading the full document.
5. When a blip's document area is rendered (from the queue), the
   `LazyContentDocument` for that blip is loaded: a `ContentDocument` is
   created, the initial op is applied to build the tree, and the document is
   rendered into the blip's content DOM element.

**Invariant**: A blip's `ContentDocument` must be fully initialized before the
blip's DOM content area receives keyboard focus or is opened for editing.

### 10.10 Opening and closing a wave

Opening a wave:
1. User clicks a digest in the search panel, emitting `WaveSelectionEvent(waveId)`.
2. `WebClient` tears down any existing wave stages (calls `StageTwo.reset()`).
3. Creates a new `StageTwo` for the selected wave and begins loading (§10.7).
4. The URL hash is updated to the wave ref.

Creating a new wave:
1. User clicks "New wave", emitting `WaveCreationEvent`.
2. A new wave ID is generated locally.
3. Stage Two is created with `isNewWave=true`; it creates the wave on the server
   via a delta before anything else loads.

Navigation: wave refs are encoded as the GWT History token, i.e. the URL
fragment after `#`. The token format (from `WaverefEncoder.encode`) is:
`<wave-domain>/<wave-id>[/(~|<wavelet-domain>)/<wavelet-id>[/<blip-id>]]`.
There is **no** `wave/` (or `/#/wave/`) literal prefix — the token begins
directly with the wave domain. When the wavelet's domain equals the wave's
domain, the wavelet domain is emitted as the single character `~` (and decoded
back to the wave domain); the full `<wavelet-domain>` is written only when the
domains differ. The blip/document id, if present, is the final `/<blip-id>`
segment. On load, the token is parsed and the target blip is scrolled into focus.

### 10.11 ContentDocument rendering pipeline (model → DOM)

This is the core rendering contract. When a ContentDocument is rendered:

1. **Element creation**: For each element in the document tree, the
   `ElementHandlerRegistry` is consulted by tag name to find a `Renderer`.
   The renderer creates the DOM nodelet(s) for that element.
   - Simple elements (text, paragraph) get a single DOM element.
   - Doodads may create complex subtrees.

2. **Nodelet attachment**: The ContentElement stores references to its container
   and impl nodelets. NodeManager registers the back-reference (`BACKREF_NAME`)
   property on the container nodelet pointing to the ContentElement.

3. **Transparency marking**: Decorator nodes (e.g., `<br>` sentinels in empty
   paragraphs, IME holders) are marked transparent via `NodeManager.setTransparency`.
   The `HtmlView` and `NodeManager.findNodeWrapper` skip these when translating
   from DOM position back to content position.

4. **Text rendering**: Each `ContentTextNode` is backed by one or more raw DOM
   text nodes (`Text`). The browser may split text nodes during editing; the
   editor reconciles them. NodeManager's `findTextWrapper` traverses upward from
   a DOM text node to find the owning `ContentTextNode`.

5. **Line/paragraph rendering**: Paragraphs use a `LineContainerParagraphiser`
   model where the document stores a `<body>` (line container) with `<line>`
   child elements, each representing a paragraph boundary. The renderer converts
   these into `<div>` or `<p>` elements in the DOM, collapsing the line+body
   structure. This mapping is non-trivial (see §10.12).

6. **Annotation painting**: After the initial tree render, `AnnotationPainter`
   walks all annotation ranges asynchronously and inserts local-only `<l:s>`
   (spread) and `<l:b>` (boundary) elements as CSS carriers (see §10.13).

### 10.12 Paragraph / line model

The Wave document schema stores paragraphs as:
```xml
<body>
  <line t="h1"/>Hello world
  <line/>Second paragraph
</body>
```

The `<body>` element is the *line container* and `<line>` elements are *line
elements*. A `<line>` element represents the paragraph *type* (heading, bullet,
indent) but contains no text; the text follows the `<line>` as children of
`<body>`.

The renderer (`LineContainerParagraphiser` + `DefaultParagraphHtmlRenderer`)
converts this into:
```html
<div class="body">
  <div style="font-size:1.75em; font-weight:bold">Hello world</div>
  <div>Second paragraph</div>
</div>
```

Rendering specifics (`DefaultParagraphHtmlRenderer`):
- The default paragraph block tag is `<div>` (`PARAGRAPH_IMPL_TAGNAME = "div"`);
  list items (`t="li"`) use `<li>` (`LIST_IMPL_TAGNAME = "li"`). `<p>` is only
  used if the renderer is constructed with the alternate
  `DefaultParagraphHtmlRenderer(implTagName)` constructor, which is not used on
  the default path.
- Text content is appended directly as children of the paragraph element; there
  is **no** `<span class="line-token">` wrapper (that class does not exist in the
  source).
- Heading type is **not** expressed as a `class`. The renderer sets inline style:
  `font-weight: bold` plus a `font-size` in `em`, linearly interpolated from
  `h1 = 1.75em` down to `h4 = 1.0em` (`MAX_HEADING_SIZE_EM = 1.75`,
  `MIN_HEADING_SIZE_EM = 1.0`, over `NUM_HEADING_SIZES = 4` steps).
- The only CSS class names applied are `numbered` (for `listyle="decimal"`
  ordered lists) and `bullet-type-0` / `bullet-type-1` / `bullet-type-2`
  (unordered lists, cycled by `indent % 3`).
- Indent is applied via inline `margin-left` (or `margin-right` for RTL) in px
  (`INDENT_UNIT_SIZE_PX` per level); alignment and direction are applied via
  inline `text-align` / `direction` style.

**Key attributes on `<line>`**:
- `t` (subtype): `"h1"`–`"h4"` (heading), `"li"` (list item), omitted = normal.
  There is no `"li#"` subtype — both bullet and numbered lists use `t="li"`.
- `listyle` (list style): `"decimal"` for a numbered (ordered) list; absent for a
  bullet (unordered) list. Only meaningful when `t="li"`.
- `i` (indent): integer
- `a` (alignment): `"l"`, `"r"`, `"c"`, `"j"`
- `d` (direction): `"l"`, `"r"`

**Invariant**: Every `<line>` element maps to exactly one rendered paragraph.
The "paragraph after the last line" is implicit (the text following the last
`<line>` within `<body>` up to `</body>`).

Cross-browser quirk: empty paragraphs require a `<br>` sentinel to occupy space
in the DOM. The `ParagraphHelper` variants (Webkit, IE, always-br, empty-line-br)
handle this per browser. A rebuilt editor needs equivalent logic.

### 10.13 Annotation rendering

Annotations in Wave are key-value spans over the document item range (positions
covering start tags, end tags, and characters — positions `0..size-1`, where
`size` is the document's item count; see [01-data-model](01-data-model.md) §7 and
[02-operational-transform](02-operational-transform.md)). The client renders them
as inline CSS.

**Pipeline**:
1. When annotations change (or on first render), `AnnotationPainter` is
   scheduled to run deferred (scheduler priority: medium).
2. It iterates cursor-by-cursor through all annotation ranges in the document.
3. For each range where a registered `PaintFunction` applies, it injects a
   `<l:s>` (local spread element) covering the range, setting CSS attributes
   returned by the `PaintFunction`.
4. At annotation *boundaries* (where a key's value changes), it injects a
   `<l:b>` (boundary element). Boundary elements carry no visual rendering but
   allow boundary-specific event handling (e.g., cursor positioning at link edges).
5. These `<l:s>` and `<l:b>` elements are marked transparent (`Skip.SHALLOW`)
   so the persistent content model ignores them.

**Maximum work per scheduler tick**: 80 "units" (roughly 80 annotation spans),
with deferred continuation for large documents.

**Registered paint functions** (installed in Stage Three):
- `style/*` keys → `StyleAnnotationHandler` → inline CSS properties
  (e.g., `style/fontWeight=bold` → `font-weight: bold`).
- `link/manual`, `link/auto`, `link/wave` → `LinkAnnotationHandler` →
  renders `<a href="...">` wrapper, handles click.
- `diff/added`, `diff/deleted` → `DiffAnnotationHandler` →
  highlights new/removed content.
- `user/r/<sessionId>` (and `user/e/*`, `user/d/*`) → `SelectionAnnotationHandler`
  (registered under the prefix `user`, not `selection`) → renders other users'
  carets as colored markers. See §10.14.7.
- `title` → `TitleAnnotationHandler` → marks document title text.

**Annotation behavior at cursor**: `AnnotationBehaviour` controls whether
typing at an annotation boundary extends the annotation or not, and which
direction to bias (e.g., typing next to a bold range stays bold vs. plain).

### 10.14 User edit extraction (DOM → DocOp)

This is the hardest part of the editor. The browser edits the DOM; the editor
must convert those edits into DocOps and ensure the DOM stays consistent with
the model.

#### 10.14.1 Event handling overview

The `EditorEventHandler` handles all browser events on the document's DOM:

```
Browser event → EditorImpl.onBrowserEvent()
    → EditorEventHandler.handleEvent(signal)
        → (if typing key) TypingExtractor.somethingHappened(selectionPoint)
        → (if structural key: Enter, Backspace, etc.)
              → NodeEventHandler for the element at cursor
              → EditorEventsSubHandler (structural operations)
        → (if IME composition) CompositionEventHandler → ImeExtractor
        → (if paste) PasteExtractor
```

Events are classified via `SignalEvent` (wraps native browser event) into:
- **TYPING**: printable characters → `TypingExtractor`
- **INPUT**: composition end / mobile keyboards → `TypingExtractor` flush
- **NAVIGATION**: arrow keys, Home/End → caret movement, selection
- **COMMAND**: shortcuts, Enter, Tab, Backspace, Delete → `EditorEventsSubHandler`
- **CLIPBOARD**: Cut/Copy/Paste → `PasteExtractor`

#### 10.14.2 TypingExtractor

`TypingExtractor` watches text mutations for character-level typing:

1. On each `somethingHappened(Point<Node>)` call (triggered after keydown but
   before the browser modifies the DOM, plus after mutations):
   - Identify the `ContentTextNode` wrapping the cursor text node.
   - Track a "typing state" over the affected range of DOM text nodes.

2. On `flush()` (called before any structural operation, on blur, on timer):
   - Read the current text content of the tracked DOM nodes.
   - Compute the replacement: `(start, deletedLength, newText)`.
   - Emit `typingReplace(contentPoint, length, text, range)` to the document.
   - The document converts this to a `DocOp` (`retain(start), delete(length), insert(text), retain(rest)`).
   - The DocOp is applied to the model and sent to the outgoing sink (OT pipeline).

**Invariant**: TypingExtractor must be flushed before any structural operation
is applied to the model, or the model will be corrupted.

#### 10.14.3 Structural editing (Enter, Backspace, etc.)

Structural keys are handled by `EditorEventsSubHandler` which delegates to
the `NodeEventHandler` registered for the element at the cursor:

- **Enter** in a paragraph: calls `paragraph.splitDom()` → inserts a new
  `<line>` element before the cursor position, which the model converts to
  `retain(N), insertElement(<line>), retain(M)`.
- **Backspace at paragraph start**: merges with previous paragraph →
  `retain(N-1), deleteElement(<line>), retain(M)`.
- **Tab in list context**: increases indent → `retain(N), updateAttribute(<line>, "i", indent+1), retain(M)`.
- **Enter in a doodad** (e.g., image caption): doodad's `NodeEventHandler.handleEnter` is called.

#### 10.14.4 IME / composition events

IME (input method editors, used for CJK and other composed input) temporarily
sets up a composition session:

1. `compositionStart`: `ImeExtractor` inserts a transparent `<span>` (the "IME
   container") at the cursor to isolate the composition from surrounding content.
2. During composition: browser updates the IME container's text; `EditorEventHandler`
   suppresses normal extraction.
3. `compositionEnd`: `ImeExtractor` reads the final text from the IME container,
   removes it, and emits the result as a normal typing replacement.

**Cross-browser quirk**: WebKit fires `textInput` after composition; other
browsers do not. The `weirdComposition` flag adapts the handler accordingly.
A rebuild must handle IME composition carefully.

#### 10.14.5 DOM repair

When a DOM inconsistency is detected (e.g., browser changed something the editor
didn't expect), `Repairer` corrects it:

- **HtmlInserted** (unexpected DOM node): remove the rogue node and re-render
  the model's version.
- **HtmlMissing** (expected DOM node gone): re-render the missing subtree from
  the model.
- Repairs are logged; in debug mode, they are treated as fatal errors.

**Invariant**: A repair must never generate a DocOp (it's a view correction,
not a model mutation). Model and DOM must match before the next user operation.

#### 10.14.6 Selection management

`SelectionMaintainer` tracks and preserves the logical selection across model
mutations (e.g., incoming remote ops):

1. Before applying a remote DocOp, save the current selection as a content
   point `(ContentNode, offset)`.
2. After applying, re-translate the saved point back to a DOM position and
   restore the browser selection.

`HtmlSelectionHelperImpl` translates between browser selection (`Range` API)
and content points, using NodeManager to map DOM nodes to ContentNodes.

**Aggressive selection helper**: On some browsers the selection gets moved into
transparent nodes after mutations. `AggressiveSelectionHelper` detects this and
corrects the selection to the nearest valid position.

#### 10.14.7 Selection as annotations (collaborative cursors)

`SelectionExtractor` runs during an edit session:
1. Subscribes to `EditorUpdateEvent.selectionLocationChanged`.
2. On each change, writes the current selection range and user info into the
   document's annotation set under keys (each key is built as a fixed constant
   prefix with the **session id appended at the end**; the session id is a
   globally unique value per browser tab):
   - `user/r/<sessionId>` (`USER_RANGE` + sessionId) — the selected range; value
     is the user's address. Absent when the selection is collapsed.
   - `user/e/<sessionId>` (`USER_END` + sessionId) — the selection focus/end
     ("hotspot"): starts where the blinking caret would be and extends to the
     end of the document; value is the user's address.
   - `user/d/<sessionId>` (`USER_DATA` + sessionId) — user data; **always covers
     the whole document** (positions `0..size`). Value is a comma-separated
     string `address,timestamp[,compositionState]` — **not JSON** — where
     `address` is the user's id, `timestamp` is the time in milliseconds since
     the Unix epoch (UTC) used to expire stale carets, and the optional third
     field is the user's pending IME composition state. The caret/highlight
     **display color is NOT carried in the annotation**; see step below.
3. These are written to the **persistent** annotation set via
   `context.getDocument()` (a `MutableAnnotationSet.Persistent` /
   `CMutableDocument`), so each `setAnnotation` produces a `DocOp`
   (`Nindo.setAnnotation`) that goes through the OT pipeline and **is sent to
   the server** like any other annotation op. That is exactly why remote clients
   receive them via the OT stream (next paragraph) and render remote carets.
   They are *not* genuinely local annotations: genuinely local annotations use
   the `'@'`-prefixed local key namespace (`Annotations.LOCAL = "@"`) handled by
   `LocalAnnotationSetImpl`/`ContentDocument.LocalAnnotationSet`, whose changes
   are NOT emitted as ops — the `user/*` selection keys do NOT use that prefix.
   These are transient/ephemeral persistent annotations: they are cleared
   (`setAnnotation(0, size, key, null)`) when the edit session ends, not
   stripped by the server. A Go reimplementation must route collaborative-cursor
   annotations through the same outbound OT path, not a local-only side channel.

Other users' selection annotations arrive via the OT stream as annotation ops.
`SelectionAnnotationHandler` registers under the annotation prefix `user` and
renders them as colored caret markers via the annotation painting pipeline. The
display **color is assigned locally on the receiving side**: `SelectionAnnotationHandler`
cycles through a fixed 9-entry RGB palette (`COLOURS`) via `getNextColour()`,
assigning one color per session id (unknown sessions render grey). Carets older
than `STALE_CARET_TIMEOUT_MS` (15 s, computed from the annotation `timestamp`)
are not shown.

### 10.15 Doodads (content elements)

A doodad is any document element requiring custom behavior beyond plain text.
Doodads are registered in the `ElementHandlerRegistry` by tag name.

Each doodad provides zero or more of:
- `Renderer`: how to create/update DOM nodelets from element state.
- `NodeEventHandler`: how to handle browser events (click, keydown) on the element.
- `NodeMutationHandler`: how to respond to model mutations (attribute changes, child additions).

**Built-in doodads**:

| Tag / Key | Type | Description |
|-----------|------|-------------|
| `<body>` | Element | Line container. Renders to `<div class="body">`. |
| `<line>` | Element | Paragraph boundary. Rendered as a block-level `<div>` (or `<li>` for list items). See §10.12. |
| `<image>` | Element | Image thumbnail. Renders thumbnail with loading state; caption as a child `<caption>` element. Linked to an attachment ID via `attachment` attribute. |
| `<caption>` | Element | Text caption inside an image doodad. Editable sub-region. |
| `<gadget>` | Element | Wave gadget. Renders as an `<iframe>` with the gadget URL. Attributes hold gadget state; mutations sync state to/from the iframe via the Gadget API. |
| `l:s` | Local | Annotation spread element. CSS carrier for style annotations. Transparent. |
| `l:b` | Local | Annotation boundary element. Used for link click handling. Transparent. |
| `diff/added` range | Annotation | Highlights newly added text (diff view). |
| `diff/deleted` range | Annotation | Renders deleted text as strikethrough. |
| `link/manual` etc. | Annotation | Wraps text in `<a>` tag. Click opens URL or wave. |
| `user/*` (`user/r/*`, `user/e/*`, `user/d/*`) | Annotation | Collaborative cursor markers. |
| `style/*` | Annotation | Inline text styling (bold, italic, color, etc.). |
| `title` | Annotation | Marks the wave title text within the first blip. |

**Doodad installation flow** (Stage Three):
1. `DoodadInstallers.GlobalInstaller` instances register their handlers on the
   root `Registries`.
2. For per-conversation or per-blip doodads, installers are called once the
   conversation model is available.
3. All doodad registration must happen before any document is rendered or
   edited; late registration is not supported.

**Gadget doodad detail**: Gadgets render inside sandboxed iframes. The gadget
element's attributes encode gadget state as `key=value` entries. The
`NodeMutationHandler` syncs attribute changes to the gadget via `postMessage`.
The gadget communicates state changes back by calling the gadget JS API, which
the client translates into model attribute mutations. See
[09-robots-gadgets-api](09-robots-gadgets-api.md) for the gadget protocol.

**Image thumbnail detail**: `ImageThumbnail` registers `TAGNAME = "image"` with
`ATTACHMENT_ATTR = "attachment"` holding the attachment ID. On render,
`ImageThumbnailRenderer` fetches the thumbnail URL from the attachment manager
and sets an `<img src>`. The caption is a nested `<caption>` element that is
its own editable content region.

**Inline replies**: A `<reply>` element inside a blip document links to a thread
ID. The `InlineAnchorLiveRenderer` renders the reply anchor widget at the
element's DOM position and wires it to the reply thread view.

### 10.16 Edit session lifecycle

```
[No session]
    User clicks blip or presses Enter on focused blip
        → EditSession.startEditing(blipUi)
            1. endSession() (if any prior session)
            2. Get ContentDocument from WaveDocuments for this blip
               (EditSession holds a DocumentRegistry<InteractiveDocument>)
            3. Create or reuse Editor (Editors.attachTo(document))
            4. editor.init(registries, keyBindings, settings)
            5. editor.setEditing(true)  ← enables contenteditable, installs event handlers
            6. editor.focus()
            7. SelectionExtractor.start(editor)
            8. fireOnSessionStart(editor, blipUi)
               → notifies each EditSession.Listener.onSessionStart(...);
                 DiffController.onSessionStart calls
                 InteractiveDocument.startDiffSuppression() on the blip's document.
[Editing blip X]
    User presses Shift+Enter or Escape
        → EditSession.endSession()
            1. SelectionExtractor.stop(editor)   ← clears selection annotations
            2. editor.blur()
            3. editor.setEditing(false)           ← disables contenteditable
            4. editor.removeContent()             ← detaches document
            5. editor.reset()
            6. fireOnSessionEnd(editor, blipUi)
               → notifies each EditSession.Listener.onSessionEnd(...);
                 DiffController.onSessionEnd calls
                 InteractiveDocument.stopDiffSuppression().
```

**Diff suppression is not done by EditSession directly.** `EditSession` does not
hold a reference to `InteractiveDocument` for toggling diff suppression; it only
fires `onSessionStart`/`onSessionEnd` to its registered `EditSession.Listener`s.
`DiffController` (an `EditSession.Listener`, registered via
`stageTwo.getDiffController().upgrade(edit)` → `edit.addListener(this)`, wired in
`StageThree.install`) is what calls `InteractiveDocument.startDiffSuppression()`
in `onSessionStart` and `stopDiffSuppression()` in `onSessionEnd`. (`EditSession`
does hold a `DocumentRegistry<InteractiveDocument>`, used in step 2 to obtain the
`ContentDocument`, but it does not itself toggle diff suppression.)

**Invariant**: Only one blip is in editing state at a time. Starting a new edit
session implicitly ends the previous one.

**Editor pool**: `Editors.create()` / `Editors.attachTo(document)` maintain a
pool of pre-warmed `Editor` instances to reduce session-start latency. A rebuild
may skip the pool; the behavioral contract is the one-at-a-time invariant.

### 10.17 Toolbar actions

The edit toolbar (`EditToolbar`) provides:

| Action | Implementation |
|--------|----------------|
| Bold/Italic/Underline/Strikethrough | `setAnnotation(selectionStart, selectionEnd, "style/fontWeight", "bold")` etc. |
| Font size/family | `setAnnotation(range, "style/fontSize", value)` |
| Heading (h1–h4) | `updateElement(<line>, {t: "h1"})` |
| Bullet / numbered list | Bullet: `updateElement(<line>, {t: "li"})` (no `listyle`); numbered: `updateElement(<line>, {t: "li", listyle: "decimal"})`. Toggling a list off clears both `t` and `listyle`. |
| Indent / outdent | `updateElement(<line>, {i: current±1})` |
| Align | `updateElement(<line>, {a: "l"|"r"|"c"|"j"})` |
| Insert link | `setAnnotation(range, "link/manual", url)` + boundary elements |
| Remove link | `setAnnotation(range, "link/manual", null)` |
| Insert gadget | Insert `<gadget url="..." .../>` element at cursor |
| Insert image | Insert `<image attachment="..."><caption></caption></image>` |
| Font/background color | `setAnnotation(range, "style/color", "#rrggbb")` |
| Clear formatting | Remove all `style/*` annotations on selection |

All toolbar actions go through `EditorContext` → `CMutableDocument` →
`NindoSink` → `IndexedDocumentImpl` → `outgoingOperationSink` (OT pipeline).

**Toggle buttons**: Bold, italic, etc. are toggle buttons whose visual state
reflects whether the annotation is present at the current selection. `ButtonUpdater`
subscribes to `EditorUpdateEvent.selectionChanged` and re-queries the annotation
at the selection to update toggle state.

### 10.18 Client ↔ server interaction

See [04-wire-protocol](04-wire-protocol.md) for message formats and
[03-concurrency-control](03-concurrency-control.md) for the OT protocol. This
section covers what the client *does*, not the wire details.

#### Opening a wave

1. WebSocket connects to `/socket` endpoint.
2. Client sends `ProtocolAuthenticate` (session token from cookie).
3. For each wave, client sends `ProtocolOpenRequest{waveId, waveletIdFilter}`.
4. Server streams `ProtocolWaveletUpdate` messages with snapshots (first) then
   deltas.
5. `RemoteViewServiceMultiplexer` routes updates by wave ID to the appropriate
   per-wave handler.
6. `RemoteWaveViewService` (per wave) passes updates to the
   `OperationChannelMultiplexer`, which applies them to the in-memory model.

#### Submitting operations

When a local DocOp is generated:
1. It flows through `ContentDocument.outgoingSink` → `MuxConnector` →
   `OperationChannelMultiplexer` → `OperationChannel` for the wavelet.
2. The channel queues it (if there is an in-flight op) or sends it immediately
   as `ProtocolSubmitRequest`.
3. On `ProtocolSubmitResponse`, the channel acknowledges the op and sends the
   next queued op.

See [03-concurrency-control](03-concurrency-control.md) §3 for the full queuing
and transform protocol.

#### Unsaved data indicator

`UnsavedDataListener` tracks whether there are local ops that have not been
acknowledged by the server. The UI shows an indicator (a spinner or warning icon)
while ops are in flight. This provides user feedback on sync state.

#### Reconnection

On WebSocket disconnect:
1. `WaveWebSocketClient` attempts to reconnect after 5 seconds.
2. On reconnect, re-authenticates and re-sends `ProtocolOpenRequest`.
3. `OperationChannel` replays any unacknowledged ops against the new snapshot.
   (See [03-concurrency-control](03-concurrency-control.md) §reconnection.)

### 10.19 Read/unread state and diff highlighting

`Reader` integrates focus movement with read-state:

1. When focus moves *to* a blip:
   - `supplement.startReading(blip)` marks the blip as being read.
   - `document.startDiffRetention()` holds diffs visible.
2. When focus moves *away* from a blip:
   - `supplement.stopReading(blip)` finalizes the read marker.
   - `document.stopDiffRetention()` releases diff retention.
   - `document.clearDiffs()` removes diff highlighting from the blip's DOM.

**Diff rendering**: New content since the user's last read is highlighted via
`diff/added` annotations (yellow background). Deleted content is shown inline
as `diff/deleted` strikethrough. These are inserted by `DiffAnnotationHandler`
based on the delta that introduced the changes.

`LocalSupplementedWave` overrides `isUnread(blip)` to return `false` for blips
being actively read, even before the server has acknowledged the read op, so the
UI immediately reflects the user's reading action.

### 10.20 Search panel

The search panel is independent of the wave panel:

1. `SearchPresenter` holds a `Search` model and a `SearchPanelView`.
2. On load, it immediately issues `search(query="in:inbox", pageSize=20)` via
   `RemoteSearchService` (HTTP GET `/search?q=...`).
3. Results arrive as `SearchResponse` (JSON); `SimpleSearch` converts them to
   `Digest` objects.
4. The presenter polls every 15 seconds for updates (`IncrementalTask`).
5. User can type a new query; results replace the current list.
6. Clicking a digest fires `WaveSelectionEvent(waveId)` which triggers wave
   loading (§10.10).

The search panel renders one `DigestView` per result: avatar, title, snippet,
unread count badge, timestamp.

---

## Wire / storage formats

The client uses no local storage (no IndexedDB, no localStorage for wave data).
All state is in memory and re-fetched on reload.

The WebSocket wire format is in [04-wire-protocol](04-wire-protocol.md). The
search HTTP endpoint returns:

```json
{
  "query": "in:inbox",
  "numResults": 42,
  "digests": [
    {
      "waveId": "example.com!w+abc",
      "title": "Meeting notes",
      "participants": ["alice@example.com", "bob@example.com"],
      "lastModified": 1672531200000,
      "unreadCount": 3,
      "blipCount": 12,
      "snippet": "Tomorrow at 10am..."
    }
  ]
}
```

URL scheme for wave references (the GWT History token, i.e. the URL fragment
after `#`; from `WaverefEncoder.encode`):
```
#<wave-domain>/<wave-local-id>[/(~|<wavelet-domain>)/<wavelet-local-id>[/<blip-id>]]
```
`~` is a shorthand emitted in place of the wavelet domain when it equals the wave
domain (decoded back to the wave domain); the full wavelet domain is written only
when the domains differ.

Examples:
- `#example.com/w+abc123` (wave only)
- `#example.com/w+abc123/~/conv+root/b+45kg` (down to a specific blip, wavelet
  domain == wave domain)

---

## Interfaces / APIs

### Editor interface (contract for a rebuild)

```
Editor {
    // Lifecycle
    init(registries, keyBindings, settings)
    reset()
    cleanup()

    // Document management
    setContent(doc: ContentDocument)
    setContent(op: DocInitialization, schema: DocumentSchema)
    removeContent() → ContentDocument
    removeContentAndUnrender() → ContentDocument
    getContent() → ContentDocument | null
    hasDocument() → bool

    // Mode
    setEditing(editing: bool)

    // Operation output
    setOutputSink(sink: SilentOperationSink<DocOp>)
    clearOutputSink()

    // Selection
    getSelectionHelper() → SelectionHelper
    getFocusedContentRange() → FocusedContentRange | null

    // Flush (synchronous completion of deferred work)
    flushUpdates()
    flushAnnotationPainting()
    flushSaveSelection()

    // Events
    addKeySignalListener(listener)
    removeKeySignalListener(listener)
}
```

### ContentDocument operations (model mutation API)

Mutations go through `CMutableDocument` (an `IndexedDocumentImpl` subtype):

```
// Text operations
insertText(point: Point, text: string)
deleteText(point: Point, length: int)

// Element operations
createElement(tag: string, attrs: map<string, string>, parent: ContentElement, before: ContentNode | null)
deleteNode(node: ContentNode)
updateElementAttributes(element: ContentElement, attrs: map<string, string>)

// Annotation operations
setAnnotation(start: int, end: int, key: string, value: string | null)
resetAnnotation(start: int, end: int, key: string, value: string | null)
```

All mutations are converted to `DocOp` via `Nindo` (a mutable builder) →
`IndexedDocumentImpl` → outgoing sink.

### InteractiveDocument interface

```
InteractiveDocument {
    getDocument() → ContentDocument
    startRendering(registries, panel)
    stopRendering()
    startDiffSuppression()   // editing: suppress diffs
    stopDiffSuppression()
    startDiffRetention()     // reading: hold diffs visible
    stopDiffRetention()
    clearDiffs()
    isCompleteDiff() → bool  // true if doc is entirely new since last read
}
```

### EditSession events

```
EditSession.Listener {
    onSessionStart(editor: Editor, blipUi: BlipView)
    onSessionEnd(editor: Editor, blipUi: BlipView)
}
```

---

## Edge cases & failure modes

1. **DOM inconsistency during remote op**: A remote op arrives while the user is
   typing. The `TypingExtractor` must be flushed first; then the remote op is
   applied to the model; then the `SelectionMaintainer` restores the selection.
   If the DOM becomes inconsistent during this, `Repairer` corrects it.

2. **Cursor in transparent node**: After a mutation, the browser may place the
   selection inside a transparent decoration. `AggressiveSelectionHelper` detects
   this and moves selection to the nearest valid content position.

3. **IME composition interrupted by remote op**: A remote op arrives during IME
   composition. The editor defers the remote op until composition ends (on
   `compositionEnd`), then applies both atomically. The IME container isolates
   the composing text from the surrounding model.

4. **Empty paragraph sentinel**: When a paragraph is emptied by a delete, the
   browser collapses it visually. The `ParagraphHelper` inserts a `<br>` sentinel
   to maintain height and allow cursor placement. This `<br>` is transparent.

5. **Large wave initial render**: For waves with many blips, `BlipQueueRenderer`
   renders only a few blips per tick to avoid blocking the UI. Blips not yet
   rendered show a placeholder. Scrolling to a blip forces its immediate render.

6. **Wave panel reset on navigation**: When the user opens a different wave, the
   existing stage two is torn down. All `ContentDocument` objects are released.
   The `OperationChannel` is closed. In-flight unacknowledged ops are discarded
   (the server will eventually apply or reject them).

7. **WebSocket disconnect during typing**: Unacknowledged ops accumulate in the
   client queue. On reconnect and resync, they are replayed. If the server
   rejects them (e.g., schema violation), they are dropped and the model is
   reset to the server's current state.

8. **Annotation painter interruption**: The painter runs incrementally. If the
   document changes while painting is in progress, the paint pass is restarted
   from the point of change.

9. **Doodad with nested editable**: Some doodads (image caption) contain their
   own editable region. The containing editor must delegate key events into the
   doodad's `NodeEventHandler` rather than processing them as top-level events.

---

## Open questions / ambiguities

### Q1: How much of the editor is irreducible?

The editor's core complexity comes from three sources:

1. **Dual representation** (ContentDocument ↔ DOM): Any rich-text browser editor
   must maintain a mapping between a logical document model and browser DOM. The
   ContentDocument/NodeManager design is one approach; a rebuild could use
   alternative designs (e.g., ProseMirror's approach, or CodeMirror 6's state
   model). The *contract* — that local ops are generated correctly and remote ops
   are applied correctly without DOM corruption — is irreducible.

2. **IME / composition**: Handling CJK input, voice input, and other composed
   input correctly is genuinely hard and requires browser-specific code.
   Irreducible for any production editor.

3. **Caret/selection management across mutations**: When remote ops arrive,
   preserving the user's cursor position requires translating between content
   positions and DOM positions. Irreducible.

The annotation painting system (transparent `<l:s>`/`<l:b>` elements) is a
specific implementation choice. A rebuild could instead use CSS Custom Properties
or `::before`/`::after` on data attributes, or a shadow DOM approach —
functionally equivalent but simpler than the current injection-based approach.

The paragraph/line model (`<body>/<line>` → `<p>`) is a fixed data format
constraint. Any editor rebuild must correctly render and edit this structure.

**Assessment**: Rebuilding a functionally equivalent editor from scratch is a
large, months-long effort (on par with building a collaborative editor like
ProseMirror or Quill from scratch). Porting the editor to TypeScript (stripping
GWT APIs and replacing with DOM APIs) would be faster than a ground-up rebuild.
A third option is adopting an existing rich-text framework (ProseMirror, Tiptap,
Quill) and implementing Wave's document model on top — this trades implementation
effort for integration complexity.

### Q2: GWT-specific vs. behavioral

GWT artifacts that a rebuild can skip:
- `AsyncHolder` / stage loading (use any async module loading approach).
- `GWT.create()` for deferred binding (use TypeScript generics / DI).
- `UiBinder` XML layouts (use React/Vue/Svelte templates or plain HTML).
- `SchedulerInstance` (use `requestAnimationFrame` / `setTimeout`).
- `JavaScriptException` wrapping (use native JS error handling).
- `StyleInjector` (use `<link>` tags or CSS modules).

GWT artifacts that reflect behavioral requirements:
- The annotation painter's "max 80 iterations per tick" budget (prevents UI jank).
- The `TypingExtractor` deferred flush pattern (correctness, not performance).
- Browser quirks handling in `ParagraphHelper` variants (correctness).

### Q3: Collaborative cursors (resolved)

The selection annotation mechanism (§10.14.7) writes selection data into the
document's **persistent** annotation set (`MutableAnnotationSet.Persistent` via
`context.getDocument()`), so each change produces a DocOp that travels over the
OT stream and is sent to the server like any other annotation op — they are NOT
kept purely local. This is why other clients render remote carets automatically
from the ops stream. The annotations are transient (cleared on session end) but
are not stripped server-side. A remaining design question for the Go rewrite is
whether to keep collaborative-cursor state on the persistent OT path (matching
the original) or move it to a separate presence/awareness channel; the original
behavior is the one described in §10.14.7.

### Q4: The supplement wavelet

`LocalSupplementedWave` writes read markers back to the server via the supplement
wavelet (UDW). The exact schema of ops emitted to the supplement wavelet is
inherited from the model layer. A rebuilt client must emit compatible ops or the
server-side supplement will be corrupted. The supplement schema is in
[01-data-model](01-data-model.md) §supplement.

### Q5: Blip ID vs. thread anchor position

Inline reply threads are anchored to a character offset within the parent blip
(the `addReplyThread(location)` call). When the parent blip is edited, the
anchor point may shift. The current implementation stores the anchor as a static
offset, which may be wrong after remote edits. A rebuild should decide whether
to implement offset-tracking (via OT) or accept drift.

### Q6: Gadget security boundary

Gadgets run in `<iframe>` with a sandboxed domain. The communication channel
(`postMessage`) is not described in detail in the client code; see
[09-robots-gadgets-api](09-robots-gadgets-api.md) for the gadget protocol. A
rebuilt client must replicate the iframe isolation or provide an alternative
sandbox.

### Q7: Profile images and participant rendering

`ProfileManager` fetches user profiles (display name, avatar URL) from the
server. The current implementation fetches lazily and updates views when profiles
arrive. A rebuild should clarify whether profiles are fetched on demand or
pre-fetched for all wave participants.

---

## Source references

| Path | Role |
|------|------|
| `wave/src/main/java/org/waveprotocol/wave/client/editor/EditorImpl.java` | Main editor implementation; event dispatch, DOM bridge |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/Editor.java` | Editor interface and static roots |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/ContentDocument.java` | The model/DOM dual document |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/ContentElement.java` | Wrapper for document elements |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/impl/NodeManager.java` | DOM ↔ ContentNode bidirectional map |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/extract/TypingExtractor.java` | Converts DOM text mutations to DocOps |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/extract/Repairer.java` | Corrects DOM/model inconsistencies |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/extract/ImeExtractor.java` | IME composition isolation |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/event/EditorEventHandler.java` | Central browser event handler |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/AnnotationPainter.java` | Deferred annotation-to-CSS renderer |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/misc/AnnotationPaint.java` | Paint element definitions (`l:s`, `l:b`) |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/misc/StyleAnnotationHandler.java` | Inline style annotations |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/paragraph/Paragraph.java` | Paragraph element schema |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/paragraph/Line.java` | Line metadata within paragraph model |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/ElementHandlerRegistry.java` | Registry for doodad renderers/handlers |
| `wave/src/main/java/org/waveprotocol/wave/client/editor/content/Registries.java` | Composed registry set |
| `wave/src/main/java/org/waveprotocol/wave/client/wave/LazyContentDocument.java` | On-demand ContentDocument loader |
| `wave/src/main/java/org/waveprotocol/wave/client/wave/InteractiveDocument.java` | Diff/render lifecycle interface |
| `wave/src/main/java/org/waveprotocol/wave/client/wave/LocalSupplementedWaveImpl.java` | Optimistic read-state supplement |
| `wave/src/main/java/org/waveprotocol/wave/client/StageZero.java` | Stage 0 bootstrap |
| `wave/src/main/java/org/waveprotocol/wave/client/StageOne.java` | Stage 1: wave panel shell |
| `wave/src/main/java/org/waveprotocol/wave/client/StageTwo.java` | Stage 2: wave model + rendering |
| `wave/src/main/java/org/waveprotocol/wave/client/StageThree.java` | Stage 3: editing features |
| `wave/src/main/java/org/waveprotocol/wave/client/Stages.java` | Stage orchestration |
| `wave/src/main/java/org/waveprotocol/wave/client/wavepanel/impl/edit/EditSession.java` | Edit session lifecycle |
| `wave/src/main/java/org/waveprotocol/wave/client/wavepanel/impl/edit/ActionsImpl.java` | Edit actions (reply, delete, etc.) |
| `wave/src/main/java/org/waveprotocol/wave/client/wavepanel/render/FullDomRenderer.java` | Initial HTML render of conversation |
| `wave/src/main/java/org/waveprotocol/wave/client/wavepanel/render/ShallowBlipRenderer.java` | Blip metadata rendering |
| `wave/src/main/java/org/waveprotocol/wave/client/wavepanel/impl/reader/Reader.java` | Focus → read state integration |
| `wave/src/main/java/org/waveprotocol/wave/client/wavepanel/impl/toolbar/EditToolbar.java` | Formatting toolbar |
| `wave/src/main/java/org/waveprotocol/wave/client/doodad/DoodadInstallers.java` | Doodad installer interfaces |
| `wave/src/main/java/org/waveprotocol/wave/client/doodad/link/LinkAnnotationHandler.java` | Link annotation doodad |
| `wave/src/main/java/org/waveprotocol/wave/client/doodad/attachment/ImageThumbnail.java` | Image/attachment doodad |
| `wave/src/main/java/org/waveprotocol/wave/client/doodad/selection/SelectionExtractor.java` | Collaborative cursor annotation writer |
| `wave/src/main/java/org/waveprotocol/wave/client/concurrencycontrol/WaveletOperationalizer.java` | Makes wavelets mutable via op sinks |
| `wave/src/main/java/org/waveprotocol/wave/client/concurrencycontrol/MuxConnector.java` | Wires wavelet op channels |
| `wave/src/main/java/org/waveprotocol/box/webclient/client/WebClient.java` | GWT entry point; top-level app wiring |
| `wave/src/main/java/org/waveprotocol/box/webclient/client/WaveWebSocketClient.java` | WebSocket wrapper; message framing |
| `wave/src/main/java/org/waveprotocol/box/webclient/client/RemoteViewServiceMultiplexer.java` | Demuxes wavelet updates by wave ID |
| `wave/src/main/java/org/waveprotocol/box/webclient/client/StageTwoProvider.java` | App-specific Stage 2 wiring |
| `wave/src/main/java/org/waveprotocol/box/webclient/search/SearchPresenter.java` | Search panel logic |
