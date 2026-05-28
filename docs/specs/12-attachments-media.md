# 12 — Attachments & Media

## Purpose & scope

This spec covers file attachments in Apache Wave: how attachments are
identified and modelled, how they are uploaded and downloaded, how thumbnails
are generated and served, and how attachment bytes and metadata are persisted.
It also describes how an attachment appears inside a blip document as an
inline element.

Out of scope: operational-transform details of document mutations (see
[01-data-model](01-data-model.md) and [02-operational-transform](02-operational-transform.md)),
the generic wire-protocol framing (see [04-wire-protocol](04-wire-protocol.md)),
and the general storage layer (see [05-storage-persistence](05-storage-persistence.md)).

---

## Concepts & glossary

| Term | Definition |
|------|-----------|
| **AttachmentId** | A two-part identifier for an attachment: `domain` + `id`. Serialized as `"domain/id"`. |
| **AttachmentMetadata** | Server-side record of an attachment's properties: filename, MIME type, byte size, creator, waveRef, URLs, and optional image/thumbnail dimensions. Persisted as a protobuf binary blob. |
| **AttachmentData** | The raw byte stream of an attachment or its thumbnail, together with a known size. |
| **Attachment data document** | A per-wavelet document whose ID starts with `"attach+"` and stores client-visible attachment state as an XML key/value map. |
| **Image element** | An inline `<image>` element inside a blip document that links to an attachment by its serialized AttachmentId. |
| **Thumbnail pattern** | A static PNG image file served as the thumbnail for non-image attachments; files are named after the MIME type with `/` replaced by `_`. |
| **AttachmentStore** | The storage interface for raw bytes and metadata. Has two backend implementations: file-system and MongoDB. |

---

## Data structures

### AttachmentId

```
AttachmentId {
    domain  string   // the server domain, e.g. "example.com"; empty for legacy ids
    id      string   // unique opaque token; must not contain "/"
}
```

Serialized form: `domain + "/" + id`. For legacy (pre-domain) ids the serialized
form is just `id` (no slash). Neither component may contain `/`.

**Invariant:** `AttachmentId.deserialise(x.serialise()) == x`.

### AttachmentMetadata (server side)

```
AttachmentMetadata {
    attachmentId    string          // serialized AttachmentId
    waveRef         string          // URI-path-encoded WaveRef identifying the owning wavelet
    fileName        string          // original filename (basename only, path stripped)
    mimeType        string          // MIME type detected from filename; "application/octet-stream" if unknown
    size            int64           // byte count of the raw attachment; on the /attachmentsInfo JSON wire it is a two-element [lowWord, highWord] array (see Wire / storage formats)
    creator         string          // ParticipantId.address of uploader; empty string if unknown
    attachmentUrl   string          // relative URL to download the attachment: "/attachment/<id>"
    thumbnailUrl    string          // relative URL for the thumbnail: "/thumbnail/<id>"
    imageMetadata   ImageMetadata?  // only present if the attachment is a recognized image
    thumbnailMetadata ImageMetadata? // dimensions of the stored thumbnail; or pattern size (95×60) for non-images
    malware         bool?           // set if attachment is known/suspected malware
}

ImageMetadata {
    width   int32
    height  int32
}
```

### Client-side attachment state (attachment data document)

Each attachment associated with a wavelet has a corresponding **data document**
within that wavelet. The document ID is:

```
"attach+" + attachmentId.serialise()
```

(The prefix constant is `"attach"` — see `IdConstants.ATTACHMENT_METADATA_PREFIX`.)

The document body is an XML key/value map with element tag `"node"`, attribute
`"key"`, attribute `"value"`. Keys (lowercased) are:

| Key | Type | Description |
|-----|------|-------------|
| `attachment_id` | string | Serialized AttachmentId |
| `attachment_url` | string | Relative download URL |
| `thumbnail_url` | string | Relative thumbnail URL |
| `filename` | string | User-provided filename |
| `mime_type` | string | MIME type |
| `attachment_size` | int64 (as string) | Total byte size |
| `creator` | string | Uploader's participant address |
| `image_width` | int32 (as string) | Original image width (images only) |
| `image_height` | int32 (as string) | Original image height (images only) |
| `thumbnail_width` | int32 (as string) | Thumbnail width |
| `thumbnail_height` | int32 (as string) | Thumbnail height |
| `malware` | bool (as string) | Malware flag |
| `status` | enum string | Upload status (see below) |
| `upload_progress` | int64 (as string) | Bytes uploaded so far |
| `upload_retries` | int64 (as string) | Number of retry attempts |
| `download_token` | string | Reserved for future use |

Upload status values: `NOT_UPLOADING`, `SUCCEEDED`, `IN_PROGRESS`,
`FAILED_AND_RETRYABLE`, `FAILED_AND_NOT_RETRYABLE`.

### Inline image element (in a blip document)

An attachment that is displayed inline in a blip appears as an `<image>` element
in the document XML (see [01-data-model](01-data-model.md) §4 for the document
XML format). The element may optionally contain a `<caption>` child element
with text.

Attributes:

| Attribute | Required | Description |
|-----------|----------|-------------|
| `attachment` | yes | Serialized AttachmentId |
| `style` | no | `"full"` for full-size display; absent for thumbnail mode |

Example XML fragment:
```xml
<image attachment="example.com/abc123">
  <caption>My photo</caption>
</image>
```

**Invariant:** the `attachment` attribute value must be a valid serialized
AttachmentId (parseable by `AttachmentId.deserialise`).

---

## Algorithms & behavior

### ID generation

AttachmentIds are generated client-side before the upload starts, using the
session's `IdGenerator`:

```
attachmentId = AttachmentId(
    domain = idGenerator.getDefaultDomain(),
    id     = idGenerator.newUniqueToken()   // random base64url token
)
```

The client pre-allocates the ID so it can insert the `<image>` element into
the document and submit the delta concurrently with the upload.

### Upload flow

1. The user selects a file in the upload UI. The client calls
   `newAttachmentId()` to allocate an ID.
2. The client submits an HTTP `POST` to `/attachment/<attachmentId.id>` (the
   path component only uses the `id` part, not the full `domain/id`).
   - Content-type: `multipart/form-data`.
   - Form fields:
     - `attachmentId` — full serialized AttachmentId (`domain/id`).
     - `waveRef` — URI-path-encoded WaveRef of the target wavelet.
     - File field — the file bytes.
3. The server (`AttachmentServlet.doPost`) validates:
   - The request is `multipart/form-data`; otherwise → `415 Unsupported Media Type`.
   - `attachmentId` field is present; otherwise → `400 Bad Request`.
   - `waveRef` field is present; otherwise → `400 Bad Request`.
   - The authenticated user (from session) has access to the target wavelet via
     `WaveletProvider.checkAccessPermission`; otherwise → `403 Forbidden`.
4. On success, the server calls `AttachmentService.storeAttachment`:
   a. Stores raw bytes: `AttachmentStore.storeAttachment(id, stream)`.
   b. Builds metadata: detects MIME type from filename, reads byte size, tries
      to decode the content as an image (via `javax.imageio.ImageIO`).
   c. If the content is a recognized image: records `imageMetadata` (width ×
      height), generates a scaled thumbnail (see Thumbnail generation), stores
      the thumbnail, records `thumbnailMetadata`.
   d. If not an image: sets `thumbnailMetadata` to the pattern dimensions (95 × 60).
   e. Writes metadata: `AttachmentStore.storeMetadata(id, metadata)`.
5. Server responds `201 Created` with body `"OK"`.
6. On upload completion, the client inserts `<image attachment="...">` into the
   blip document via an OT delta.

**Size limits** (defined as constants in `AttachmentConstants`):
- Maximum attachment size: 20 MB (`MAX_BLOB_SIZE_BYTES = 20 * 1024 * 1024`).
  Note: the servlet does not explicitly enforce this limit — it is primarily a
  client-side guard. The server relies on the store silently accepting whatever
  is sent.
- Maximum thumbnail bytes: 64 KB (`MAX_THUMBNAIL_BYTES`).
- Maximum thumbnail display size: 120 px (`MAX_THUMBNAIL_SIZE`).

### Download / serving flow

`GET /attachment/<attachmentId>` — serves raw attachment bytes.

`GET /thumbnail/<attachmentId>` — serves a thumbnail.

Both endpoints share `AttachmentServlet.doGet`:

1. Parse the AttachmentId from the path segment after the leading `/`.
2. Fetch metadata from `AttachmentService.getMetadata(id)`.
3. If metadata is absent (legacy attachment without stored metadata), fall back
   to query params `fileName` and `waveRef` to reconstruct metadata on the fly
   (calls `buildAndStoreMetadataWithThumbnail(id, waveletName, fileName, creator=null)`).
   Note: the rebuild passes a null/absent creator — **not** the requesting session
   user — so the reconstructed metadata records `creator = ""` (empty string). This
   differs from the upload path, which passes the authenticated user. A Go
   reimplementation of this legacy fallback must not substitute the session user.
4. If metadata is still absent → `404 Not Found`.
5. Resolve the owning wavelet from `metadata.waveRef` (decoded via
   `JavaWaverefEncoder.decodeWaveRefFromPath`; if no waveletId present, defaults
   to `conv+root`).
6. Check that the session user is a participant in that wavelet via
   `WaveletProvider.checkAccessPermission`. If not → `403 Forbidden`.
7. Serve the requested data:
   - For `/attachment/…`: content-type = `metadata.mimeType`; body = raw bytes.
   - For `/thumbnail/…`:
     - If `metadata.imageMetadata` is present (i.e. original is an image):
       content-type = `image/jpeg`; body = stored JPEG thumbnail.
     - If not an image: look up the pattern file (see Thumbnail patterns);
       content-type = `"png"` (literally the extension string); body = pattern bytes.
8. Set `Content-Disposition: attachment; filename="<metadata.fileName>"`.
9. Set `Content-Length` to the data size.
10. Respond `200 OK`.

### Attachment info endpoint

`GET /attachmentsInfo?attachmentIds=<id1>,<id2>,...`

Returns a JSON-serialized `AttachmentsResponse` protobuf containing metadata
for each requested attachment ID that the calling user is authorized to see.
Unauthorized attachments are silently excluded from the response. Response
content-type is `application/json; charset=utf8`; `Cache-Control: no-store`.

The JSON is produced by `ProtoSerializer.toJson` (PST-Gson), so the int64
`size` field appears as a two-element `[lowWord, highWord]` array rather than a
plain number (see §Wire / storage formats and [04-wire-protocol](04-wire-protocol.md)).

### Thumbnail generation (images)

For recognized image attachments, a thumbnail is generated server-side during
upload:

```
MAX_THUMBNAIL_WIDTH  = 200
MAX_THUMBNAIL_HEIGHT = 200

thumbnailWidth  = min(imageWidth,  MAX_THUMBNAIL_WIDTH)
thumbnailHeight = min(imageHeight, MAX_THUMBNAIL_HEIGHT)

// Maintain aspect ratio (fit within the bounding box):
if imageWidth * thumbnailHeight < imageHeight * thumbnailWidth:
    thumbnailWidth  = imageWidth  * thumbnailHeight / imageHeight
else:
    thumbnailHeight = imageHeight * thumbnailWidth  / imageWidth

// Render with bicubic interpolation onto a black RGB background.
// Encoded as JPEG (format name "jpeg").
```

**Invariant:** the thumbnail is always at most 200 × 200 pixels. Both
dimensions are positive.

### Thumbnail patterns (non-image attachments)

When an attachment is not a recognized image, the thumbnail served is a static
PNG icon file selected by MIME type. The server reads from the configured
`thumbnail_patterns_directory`:

1. Compute filename: replace `/` with `_` in the MIME type string.
   E.g., `application/pdf` → `application_pdf`.
2. If a file with that name exists in the directory, serve it.
3. Otherwise, serve the file named `default`.

Pattern files are PNG images, nominally 95 × 60 pixels (the dimensions recorded
in `thumbnailMetadata` for non-image attachments).

The `thumbnail_patterns` directory ships with the Wave server distribution and
includes patterns for common MIME types (PDF, ZIP, Office formats, audio, video,
spreadsheet, presentation, etc.) and a `default` fallback.

---

## Wire / storage formats

### attachment.proto

```protobuf
syntax = "proto2";
package attachment;

message AttachmentsResponse {
  repeated AttachmentMetadata attachment = 1;
}

message AttachmentMetadata {
  required string attachmentId    = 1;
  required string waveRef         = 2;   // URI-path WaveRef, e.g. "example.com/w+abc/~/conv+root"
  required string fileName        = 3;
  required string mimeType        = 4;
  required int64  size            = 5;   // byte count; see int64 wire-encoding note below
  required string creator         = 6;   // ParticipantId address; empty if unknown
  required string attachmentUrl   = 7;   // "/attachment/<serialized-id>"
  required string thumbnailUrl    = 8;   // "/thumbnail/<serialized-id>"
  optional ImageMetadata imageMetadata     = 9;   // present iff attachment is an image
  optional ImageMetadata thumbnailMetadata = 10;  // always present after metadata is built
  optional bool   malware         = 11;
}

message ImageMetadata {
  required int32 width  = 1;
  required int32 height = 2;
}
```

This proto is used for both the `/attachmentsInfo` JSON response and as the
binary persistence format for per-attachment metadata.

**int64 wire encoding.** The `size` field is a plain `int64` (no `[(int52)=true]`
annotation). When the `/attachmentsInfo` response is serialized to JSON (via
`ProtoSerializer.toJson` / PST-Gson), `size` is encoded as a two-element
`[lowWord, highWord]` JSON array — **not** a plain number — consistent with the
int64 encoding rule in [04-wire-protocol](04-wire-protocol.md). Decode with
`toLong(highWord=arr[1], lowWord=arr[0])`. The binary protobuf persistence form is
unaffected (standard varint `int64`); this two-element-array encoding applies only
to the JSON wire form.

### File store layout

Config key: `core.attachment_store_directory` (default: `_attachments`).

The filename for each attachment is the Base64 encoding of the serialized
AttachmentId (UTF-8 bytes), using the standard Java Base64 alphabet. Three
files per attachment:

| Suffix | Contents |
|--------|----------|
| _(none)_ | Raw attachment bytes |
| `.meta` | Binary-serialized `AttachmentMetadata` protobuf |
| `.thumbnail` | JPEG thumbnail bytes (images) or absent (non-images) |

**Invariant:** `storeAttachment` / `storeMetadata` / `storeThumbnail` all
throw `IOException("Attachment already exist")` if the file already exists —
uploads are write-once.

**`deleteAttachment` leak.** `deleteAttachment` deletes only the raw-bytes file
(the path with no suffix). The `.meta` and `.thumbnail` files are **not** deleted
and remain on disk. (Contrast with the MongoDB backend, which removes all three.)

### MongoDB store layout

Three GridFS namespaces in the Wave MongoDB database:

| Namespace | Filename key | Contents |
|-----------|-------------|----------|
| `attachments` | `attachmentId.serialise()` | Raw attachment bytes |
| `thumbnails` | `attachmentId.serialise()` | Thumbnail bytes |
| `metadata` | `attachmentId.serialise()` | Binary-serialized `AttachmentMetadata` protobuf |

`deleteAttachment` removes all three entries (bytes, thumbnail, metadata) — note
this differs from the file backend, which removes only the raw-bytes file. Reads
return `null` if not found. There is no existence check on writes, so a second
`storeAttachment` / `storeMetadata` / `storeThumbnail` to the same ID silently
creates a duplicate GridFS entry (see §Edge cases — Duplicate upload).

---

## Interfaces / APIs

### AttachmentStore (storage backend)

```
interface AttachmentStore {
    getMetadata(id AttachmentId) -> (AttachmentMetadata | null, error)
    getAttachment(id AttachmentId) -> (AttachmentData | null, error)
    getThumbnail(id AttachmentId) -> (AttachmentData | null, error)

    storeMetadata(id AttachmentId, meta AttachmentMetadata) -> error
    storeAttachment(id AttachmentId, data io.Reader) -> error
    storeThumbnail(id AttachmentId, data io.Reader) -> error

    deleteAttachment(id AttachmentId)
}
```

**`deleteAttachment` semantics differ by backend.** The MongoDB backend
(`MongoDbStore.deleteAttachment`) removes all three artifacts — raw bytes,
thumbnail, and metadata. The file backend (`FileAttachmentStore.deleteAttachment`)
removes **only** the raw-bytes file, leaving the `.meta` and `.thumbnail` files
orphaned on disk. A Go reimplementation should decide deliberately whether to
preserve this file-store leak or fix it (deleting all three).

```

interface AttachmentData {
    getInputStream() -> (io.Reader, error)
    getSize() -> int64
}
```

### AttachmentService (server logic layer)

```
AttachmentService.storeAttachment(
    id          AttachmentId,
    data        io.Reader,
    waveletName WaveletName,
    fileName    string,
    creator     ParticipantId,
) -> error

AttachmentService.buildAndStoreMetadataWithThumbnail(
    id          AttachmentId,
    waveletName WaveletName,
    fileName    string,
    creator     ParticipantId,
) -> (AttachmentMetadata, error)   // used for legacy attachments missing metadata

AttachmentService.getMetadata(id AttachmentId) -> (AttachmentMetadata | null, error)
AttachmentService.getAttachment(id AttachmentId) -> (AttachmentData | null, error)
AttachmentService.getThumbnail(id AttachmentId) -> (AttachmentData | null, error)
```

### HTTP endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/attachment/<id-token>` | Upload a file (multipart/form-data) |
| `GET` | `/attachment/<serialized-id>` | Download raw attachment bytes |
| `GET` | `/thumbnail/<serialized-id>` | Download thumbnail |
| `GET` | `/attachmentsInfo?attachmentIds=<id1>,<id2>,...` | Fetch metadata for multiple attachments |

Note: the POST path uses only the `id` token; the GET paths use the full
serialized `domain/id`.

### Data document lookup

```
// Returns the data document for an attachment within a wavelet.
// Creates the document if it does not yet exist.
getAttachmentDataDoc(wavelet Wavelet, attachmentIdString string) -> Document

// Document ID formula:
docId = "attach+" + attachmentId.serialise()
```

The lookup has a fallback for legacy (pre-domain) IDs: if the full ID is not
found, it checks whether any existing `attach+` document's `id` component
matches, ignoring the domain component.

---

## Edge cases & failure modes

**Metadata absent at download time.** Legacy attachments may have been stored
without metadata. The download servlet accepts optional `fileName` and `waveRef`
query parameters; if provided and the attachment bytes exist, metadata is built
and stored on first access. If neither metadata nor these params are present →
`404`.

**Non-image attachment thumbnail.** If `ImageIO.read` cannot decode the content
as an image, no JPEG thumbnail is stored. The thumbnail endpoint falls back to
the pattern file. `thumbnailMetadata` is set to 95 × 60 regardless.

**Image decode failure during upload.** `ImageIO.read` failure is logged at
SEVERE but does not abort the upload. The attachment bytes are stored
successfully; the attachment is treated as a non-image.

**Thumbnail generation failure.** Logged at SEVERE; the attachment metadata is
still written (with no `thumbnailMetadata` from image dims, and no stored
thumbnail blob). Subsequent thumbnail requests fall back to the pattern.

**Duplicate upload.** The *file* store (`FileAttachmentStore`) is write-once:
`storeAttachment` / `storeMetadata` / `storeThumbnail` each check `file.exists()`
and throw `IOException("Attachment already exist")` on a second write to the same
ID, which the server surfaces as `500 Internal Server Error`. The *MongoDB* store
(`MongoDbStore`) has **no** existence check — `storeAttachment` / `storeMetadata`
/ `storeThumbnail` call `GridFSInputFile.save()` via a helper that only unwraps
wrapped `MongoException`s; GridFS does not enforce filename uniqueness, so a
second write silently succeeds and creates a duplicate GridFS entry with the same
filename (reads via `findOne(serialise())` then return an arbitrary one of the
duplicates). There is no idempotent re-upload path. A Go reimplementation should
decide and document one consistent duplicate-write policy across backends rather
than mirroring this divergence.

**Access control.** All download and upload paths require an active session.
Access is gated on `WaveletProvider.checkAccessPermission` against the wavelet
named in the metadata (or the `waveRef` param). Unauthenticated users receive
`403 Forbidden`. The `/attachmentsInfo` endpoint silently excludes unauthorized
entries rather than returning an error.

**AttachmentId with domain component containing `/`.** Rejected at construction
time with `IllegalArgumentException`. Same for the `id` component.

**Pattern file missing for MIME type and no `default`.** `getThumbnailByContentType`
constructs a file path but does not check for existence before building the
`AttachmentData` wrapper. A missing `default` file would cause an
`IOException` when the stream is opened.

---

## Open questions / ambiguities

1. **Server-side size enforcement.** The 20 MB limit is defined in
   `AttachmentConstants` (originally a GWT/App Engine client constant) but is
   not checked in `AttachmentServlet`. A Go reimplementation should decide
   whether to enforce it server-side (recommended) and at what layer.

2. **Upload path: `id` vs. `domain/id`.** The client posts to
   `/attachment/<id>` (token only), but the `attachmentId` form field carries
   `domain/id`. The URL path component is unused by the servlet (the ID is read
   from the form field). The Go rewrite can simplify by using the full ID in the
   path.

3. **Thumbnail max size discrepancy.** `AttachmentConstants.MAX_THUMBNAIL_SIZE`
   is 120 px (a client constant), while `AttachmentService` uses 200 px for the
   server-generated thumbnail. The spec preserves the server behavior (200 px).
   Clarify which should be authoritative.

4. **`Content-type` for pattern thumbnails.** The servlet sets content-type to
   the literal string `"png"` (the format name string) rather than
   `"image/png"`. This is likely a bug. A Go rewrite should use `"image/png"`.

5. **`storeThumbnail` for non-images.** For non-image uploads, no thumbnail
   blob is stored. If a client requests `/thumbnail/<id>` for a non-image
   attachment that has `imageMetadata = null`, the servlet serves the pattern
   file. But if, for some reason, `imageMetadata` is present but the JPEG blob
   is absent, the servlet returns `404`. This edge case should be documented and
   handled gracefully.

6. **No streaming / range requests.** The servlet reads the entire attachment
   into a response at once. For large files, the Go rewrite should consider
   `Content-Range` support.

7. **Malware field.** The `malware` flag exists in both the proto and the data
   document but nothing in the open-source code sets it. It appears to be a stub
   for an anti-malware scanning hook. A Go rewrite can include the field but
   leave the scanning integration as a future extension point.

---

## Source references

| Path | Role |
|------|------|
| `wave/src/proto/proto/org/waveprotocol/box/attachment/attachment.proto` | Protobuf definitions for `AttachmentMetadata`, `ImageMetadata`, `AttachmentsResponse` |
| `wave/src/main/java/org/waveprotocol/box/server/attachment/AttachmentService.java` | Core upload logic, thumbnail generation, metadata construction |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/AttachmentServlet.java` | HTTP upload (`POST`) and download (`GET`) endpoints |
| `wave/src/main/java/org/waveprotocol/box/server/rpc/AttachmentInfoServlet.java` | `/attachmentsInfo` metadata query endpoint |
| `wave/src/main/java/org/waveprotocol/box/server/persistence/AttachmentStore.java` | Storage interface |
| `wave/src/main/java/org/waveprotocol/box/server/persistence/file/FileAttachmentStore.java` | File-system backend (Base64 filenames, `.meta` / `.thumbnail` suffixes) |
| `wave/src/main/java/org/waveprotocol/box/server/persistence/mongodb/MongoDbStore.java` | MongoDB GridFS backend (`attachments`, `thumbnails`, `metadata` collections) |
| `wave/src/main/java/org/waveprotocol/box/server/persistence/AttachmentUtil.java` | `waveRef2WaveletName` helper, stream-copy utility |
| `wave/src/main/java/org/waveprotocol/wave/media/model/AttachmentId.java` | ID structure, serialization, `deserialise` |
| `wave/src/main/java/org/waveprotocol/wave/media/model/AttachmentIdGeneratorImpl.java` | Client-side ID generation |
| `wave/src/main/java/org/waveprotocol/wave/media/model/AttachmentDataDocHelper.java` | Data document ID formula and lookup |
| `wave/src/main/java/org/waveprotocol/wave/media/model/AttachmentDocumentWrapper.java` | Client-side data document key/value schema |
| `wave/src/main/java/org/waveprotocol/wave/media/model/Attachment.java` | `Attachment` interface — status enum, field accessors |
| `wave/src/main/java/org/waveprotocol/wave/client/doodad/attachment/AttachmentConstants.java` | Size limits: 20 MB max blob, 120 px thumbnail, 64 KB thumbnail bytes |
| `wave/src/main/java/org/waveprotocol/wave/client/doodad/attachment/ImageThumbnail.java` | `<image>` element tag/attribute names, XML construction |
| `wave/src/main/java/org/waveprotocol/wave/model/image/ImageConstants.java` | `TAGNAME="image"`, `ATTACHMENT_ATTRIBUTE="attachment"` |
| `wave/src/main/java/org/waveprotocol/wave/model/id/IdConstants.java` | `ATTACHMENT_METADATA_PREFIX = "attach"` |
| `wave/src/dist/thumbnail_patterns/` | Shipped set of per-MIME-type PNG icon files |
| `wave/config/reference.conf` | Config keys: `core.attachment_store_directory`, `core.thumbnail_patterns_directory` |
