// Package codec is the canonical serialization of operations and deltas. It is
// the single encoding used for the wavelet history hash chain and for storage
// (and, later, the browser wire). Encoding is deterministic: everything is
// emitted as positional CBOR arrays and toarray structs (no maps), so byte
// output depends only on the data, and the frozen RFC 8949 Core Deterministic
// mode pins shortest-integer and definite-length encoding.
//
// FROZEN: the encoding feeds the hash chain. The wire shapes and the CoreDet
// mode must not change in a way that alters bytes for existing data — stored
// hashes are read back, never recomputed, but new deltas chain from them.
//
// Spec: docs/specs/04-wire-protocol.md, docs/specs/05-storage-persistence.md.
package codec

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// encMode is the frozen deterministic encoder shared by all encoding.
var encMode = mustEncMode()

func mustEncMode() cbor.EncMode {
	em, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic("codec: building CBOR encode mode: " + err.Error())
	}
	return em
}

func marshal(v any) []byte {
	b, err := encMode.Marshal(v)
	if err != nil {
		panic("codec: marshal: " + err.Error()) // our values are always encodable
	}
	return b
}

// need bounds-checks a decoded element array before indexing. Decode input comes
// from storage (potentially corrupt on disk), so a truncated array must surface
// as an error, not an index-out-of-range panic.
func need(raw []cbor.RawMessage, n int) error {
	if len(raw) < n {
		return fmt.Errorf("codec: truncated: have %d elements, need %d", len(raw), n)
	}
	return nil
}

// Determinism note: byte output must depend only on the data (it feeds the hash
// chain). This holds because (a) nothing here encodes a Go map — only positional
// arrays, toarray structs, and slices built with make; and (b) empty collections
// have a single representation upstream: op.New{Attributes,AttributesUpdate,
// AnnotationBoundaryMap} normalize their slices, and version.NewHashedVersion
// collapses an empty history hash to nil. Do not hand the encoder a raw,
// possibly-nil slice whose empty form would differ from the normalized one
// (see endKeysWire).

// endKeysWire returns a non-nil slice so empty end-key lists encode as an empty
// CBOR array, matching the other empty lists (which use make). Annotation end
// keys come from EndKeys(), which is nil when empty — left raw it would encode
// as CBOR null and diverge from the format's single empty-list representation.
func endKeysWire(ends []string) []string {
	out := make([]string, len(ends))
	copy(out, ends)
	return out
}

// --- component (DocOp) kinds ---

const (
	cRetain uint64 = iota
	cCharacters
	cElementStart
	cElementEnd
	cDeleteCharacters
	cDeleteElementStart
	cDeleteElementEnd
	cReplaceAttributes
	cUpdateAttributes
	cAnnotationBoundary
)

// --- wavelet operation kinds ---

const (
	oWaveletBlip uint64 = iota
	oAddParticipant
	oRemoveParticipant
	oNoOp
)

// --- leaf wire types (positional arrays for determinism) ---

type wireAttr struct {
	_     struct{} `cbor:",toarray"`
	Name  string
	Value string
}

type wireChange struct {
	_   struct{} `cbor:",toarray"`
	Key string
	Old *string
	New *string
}

type wireHV struct {
	_       struct{} `cbor:",toarray"`
	Version uint64
	Hash    []byte
}

type wireContext struct {
	_                struct{} `cbor:",toarray"`
	Creator          string
	Timestamp        int64
	VersionIncrement int64
	HV               *wireHV // nil when the context carries no hashed version
}

func wireAttrs(a op.Attributes) []wireAttr {
	all := a.All()
	out := make([]wireAttr, len(all))
	for i, at := range all {
		out[i] = wireAttr{Name: at.Name, Value: at.Value}
	}
	return out
}

func attrsFrom(w []wireAttr) (op.Attributes, error) {
	if len(w) == 0 {
		return op.NewAttributes(nil)
	}
	m := make(map[string]string, len(w))
	for _, a := range w {
		m[a.Name] = a.Value
	}
	return op.NewAttributes(m)
}

func wireUpdate(u op.AttributesUpdate) []wireChange {
	all := u.All()
	out := make([]wireChange, len(all))
	for i, c := range all {
		out[i] = wireChange{Key: c.Name, Old: c.OldValue, New: c.NewValue}
	}
	return out
}

func updateFrom(w []wireChange) (op.AttributesUpdate, error) {
	changes := make([]op.AttributeChange, len(w))
	for i, c := range w {
		changes[i] = op.AttributeChange{Name: c.Key, OldValue: c.Old, NewValue: c.New}
	}
	return op.NewAttributesUpdate(changes)
}

func wireChanges(m op.AnnotationBoundaryMap) []wireChange {
	chs := m.Changes()
	out := make([]wireChange, len(chs))
	for i, c := range chs {
		out[i] = wireChange{Key: c.Key, Old: c.OldValue, New: c.NewValue}
	}
	return out
}

func boundaryFrom(ends []string, w []wireChange) (op.AnnotationBoundaryMap, error) {
	changes := make([]op.AnnotationChange, len(w))
	for i, c := range w {
		changes[i] = op.AnnotationChange{Key: c.Key, OldValue: c.Old, NewValue: c.New}
	}
	return op.NewAnnotationBoundaryMap(ends, changes)
}

func wireCtx(c waveop.Context) wireContext {
	wc := wireContext{Creator: c.Creator.Address(), Timestamp: c.Timestamp, VersionIncrement: c.VersionIncrement}
	if c.HashedVersion != nil {
		wc.HV = &wireHV{Version: c.HashedVersion.Version(), Hash: c.HashedVersion.HistoryHash()}
	}
	return wc
}

func ctxFrom(wc wireContext) (waveop.Context, error) {
	creator, err := id.NewParticipantID(wc.Creator)
	if err != nil {
		return waveop.Context{}, err
	}
	c := waveop.Context{Creator: creator, Timestamp: wc.Timestamp, VersionIncrement: wc.VersionIncrement}
	if wc.HV != nil {
		hv := version.NewHashedVersion(wc.HV.Version, wc.HV.Hash)
		c.HashedVersion = &hv
	}
	return c, nil
}

// --- component encode/decode ---

func componentValue(c op.Component) []any {
	switch v := c.(type) {
	case op.Retain:
		return []any{cRetain, v.Count}
	case op.Characters:
		return []any{cCharacters, v.Text}
	case op.ElementStart:
		return []any{cElementStart, v.Type, wireAttrs(v.Attributes)}
	case op.ElementEnd:
		return []any{cElementEnd}
	case op.DeleteCharacters:
		return []any{cDeleteCharacters, v.Text}
	case op.DeleteElementStart:
		return []any{cDeleteElementStart, v.Type, wireAttrs(v.Attributes)}
	case op.DeleteElementEnd:
		return []any{cDeleteElementEnd}
	case op.ReplaceAttributes:
		return []any{cReplaceAttributes, wireAttrs(v.OldAttributes), wireAttrs(v.NewAttributes)}
	case op.UpdateAttributes:
		return []any{cUpdateAttributes, wireUpdate(v.Update)}
	case op.AnnotationBoundary:
		return []any{cAnnotationBoundary, endKeysWire(v.Boundary.EndKeys()), wireChanges(v.Boundary)}
	default:
		panic(fmt.Sprintf("codec: unknown component %T", c))
	}
}

func componentFrom(raw []cbor.RawMessage) (op.Component, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("codec: empty component")
	}
	var kind uint64
	if err := cbor.Unmarshal(raw[0], &kind); err != nil {
		return nil, err
	}
	switch kind {
	case cRetain:
		if err := need(raw, 2); err != nil {
			return nil, err
		}
		// Decode via int64 then range-check: a negative or (on 32-bit) overflowing
		// count is malformed, not a valid retain.
		var n int64
		if err := cbor.Unmarshal(raw[1], &n); err != nil {
			return nil, err
		}
		if n < 0 || int64(int(n)) != n {
			return nil, fmt.Errorf("codec: retain count %d out of range", n)
		}
		return op.Retain{Count: int(n)}, nil
	case cCharacters:
		if err := need(raw, 2); err != nil {
			return nil, err
		}
		var s string
		err := cbor.Unmarshal(raw[1], &s)
		return op.Characters{Text: s}, err
	case cElementStart:
		typ, attrs, err := decodeTypedElement(raw)
		return op.ElementStart{Type: typ, Attributes: attrs}, err
	case cElementEnd:
		return op.ElementEnd{}, nil
	case cDeleteCharacters:
		if err := need(raw, 2); err != nil {
			return nil, err
		}
		var s string
		err := cbor.Unmarshal(raw[1], &s)
		return op.DeleteCharacters{Text: s}, err
	case cDeleteElementStart:
		typ, attrs, err := decodeTypedElement(raw)
		return op.DeleteElementStart{Type: typ, Attributes: attrs}, err
	case cDeleteElementEnd:
		return op.DeleteElementEnd{}, nil
	case cReplaceAttributes:
		if err := need(raw, 3); err != nil {
			return nil, err
		}
		var wo, wn []wireAttr
		if err := cbor.Unmarshal(raw[1], &wo); err != nil {
			return nil, err
		}
		if err := cbor.Unmarshal(raw[2], &wn); err != nil {
			return nil, err
		}
		oldA, err := attrsFrom(wo)
		if err != nil {
			return nil, err
		}
		newA, err := attrsFrom(wn)
		return op.ReplaceAttributes{OldAttributes: oldA, NewAttributes: newA}, err
	case cUpdateAttributes:
		if err := need(raw, 2); err != nil {
			return nil, err
		}
		var wu []wireChange
		if err := cbor.Unmarshal(raw[1], &wu); err != nil {
			return nil, err
		}
		u, err := updateFrom(wu)
		return op.UpdateAttributes{Update: u}, err
	case cAnnotationBoundary:
		if err := need(raw, 3); err != nil {
			return nil, err
		}
		var ends []string
		if err := cbor.Unmarshal(raw[1], &ends); err != nil {
			return nil, err
		}
		var wc []wireChange
		if err := cbor.Unmarshal(raw[2], &wc); err != nil {
			return nil, err
		}
		m, err := boundaryFrom(ends, wc)
		return op.AnnotationBoundary{Boundary: m}, err
	default:
		return nil, fmt.Errorf("codec: unknown component kind %d", kind)
	}
}

func decodeTypedElement(raw []cbor.RawMessage) (string, op.Attributes, error) {
	if err := need(raw, 3); err != nil {
		return "", op.Attributes{}, err
	}
	var typ string
	if err := cbor.Unmarshal(raw[1], &typ); err != nil {
		return "", op.Attributes{}, err
	}
	var wa []wireAttr
	if err := cbor.Unmarshal(raw[2], &wa); err != nil {
		return "", op.Attributes{}, err
	}
	a, err := attrsFrom(wa)
	return typ, a, err
}

func docOpValue(d op.DocOp) []any {
	comps := d.Components()
	out := make([]any, len(comps))
	for i, c := range comps {
		out[i] = componentValue(c)
	}
	return out
}

func docOpFrom(raw []cbor.RawMessage) (op.DocOp, error) {
	comps := make([]op.Component, len(raw))
	for i, r := range raw {
		var inner []cbor.RawMessage
		if err := cbor.Unmarshal(r, &inner); err != nil {
			return op.DocOp{}, err
		}
		c, err := componentFrom(inner)
		if err != nil {
			return op.DocOp{}, err
		}
		comps[i] = c
	}
	return op.NewDocOp(comps), nil
}

// EncodeDocOp returns the canonical CBOR encoding of a DocOp.
func EncodeDocOp(d op.DocOp) []byte { return marshal(docOpValue(d)) }

// DecodeDocOp parses a canonical CBOR DocOp encoding.
func DecodeDocOp(data []byte) (op.DocOp, error) {
	var raw []cbor.RawMessage
	if err := cbor.Unmarshal(data, &raw); err != nil {
		return op.DocOp{}, err
	}
	return docOpFrom(raw)
}

// --- wavelet operation encode/decode ---

func operationValue(o waveop.Operation) []any {
	switch v := o.(type) {
	case waveop.WaveletBlipOperation:
		bc, ok := v.BlipOp.(waveop.BlipContentOperation)
		if !ok {
			panic(fmt.Sprintf("codec: unsupported blip operation %T", v.BlipOp))
		}
		return []any{oWaveletBlip, v.BlipID, wireCtx(bc.Ctx), docOpValue(bc.ContentOp), uint64(bc.Method)}
	case waveop.AddParticipant:
		return []any{oAddParticipant, wireCtx(v.Ctx), v.Participant.Address()}
	case waveop.RemoveParticipant:
		return []any{oRemoveParticipant, wireCtx(v.Ctx), v.Participant.Address()}
	case waveop.NoOp:
		return []any{oNoOp, wireCtx(v.Ctx)}
	default:
		panic(fmt.Sprintf("codec: unknown operation %T", o))
	}
}

func operationFrom(raw []cbor.RawMessage) (waveop.Operation, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("codec: empty operation")
	}
	var kind uint64
	if err := cbor.Unmarshal(raw[0], &kind); err != nil {
		return nil, err
	}
	switch kind {
	case oWaveletBlip:
		if err := need(raw, 5); err != nil {
			return nil, err
		}
		var blipID string
		if err := cbor.Unmarshal(raw[1], &blipID); err != nil {
			return nil, err
		}
		ctx, err := decodeCtx(raw[2])
		if err != nil {
			return nil, err
		}
		var docRaw []cbor.RawMessage
		if err := cbor.Unmarshal(raw[3], &docRaw); err != nil {
			return nil, err
		}
		content, err := docOpFrom(docRaw)
		if err != nil {
			return nil, err
		}
		var method uint64
		if err := cbor.Unmarshal(raw[4], &method); err != nil {
			return nil, err
		}
		return waveop.WaveletBlipOperation{BlipID: blipID, BlipOp: waveop.BlipContentOperation{
			Ctx: ctx, ContentOp: content, Method: waveop.UpdateContributorMethod(method)}}, nil
	case oAddParticipant:
		ctx, p, err := decodeCtxParticipant(raw)
		return waveop.AddParticipant{Ctx: ctx, Participant: p}, err
	case oRemoveParticipant:
		ctx, p, err := decodeCtxParticipant(raw)
		return waveop.RemoveParticipant{Ctx: ctx, Participant: p}, err
	case oNoOp:
		if err := need(raw, 2); err != nil {
			return nil, err
		}
		ctx, err := decodeCtx(raw[1])
		return waveop.NoOp{Ctx: ctx}, err
	default:
		return nil, fmt.Errorf("codec: unknown operation kind %d", kind)
	}
}

func decodeCtx(r cbor.RawMessage) (waveop.Context, error) {
	var wc wireContext
	if err := cbor.Unmarshal(r, &wc); err != nil {
		return waveop.Context{}, err
	}
	return ctxFrom(wc)
}

func decodeCtxParticipant(raw []cbor.RawMessage) (waveop.Context, id.ParticipantID, error) {
	if err := need(raw, 3); err != nil {
		return waveop.Context{}, id.ParticipantID{}, err
	}
	ctx, err := decodeCtx(raw[1])
	if err != nil {
		return waveop.Context{}, id.ParticipantID{}, err
	}
	var addr string
	if err := cbor.Unmarshal(raw[2], &addr); err != nil {
		return waveop.Context{}, id.ParticipantID{}, err
	}
	p, err := id.NewParticipantID(addr)
	return ctx, p, err
}

func opsValue(ops []waveop.Operation) []any {
	out := make([]any, len(ops))
	for i, o := range ops {
		out[i] = operationValue(o)
	}
	return out
}

func opsFrom(raw []cbor.RawMessage) ([]waveop.Operation, error) {
	ops := make([]waveop.Operation, len(raw))
	for i, r := range raw {
		var inner []cbor.RawMessage
		if err := cbor.Unmarshal(r, &inner); err != nil {
			return nil, err
		}
		o, err := operationFrom(inner)
		if err != nil {
			return nil, err
		}
		ops[i] = o
	}
	return ops, nil
}

// --- delta hashing and storage ---

// HashBytes returns the canonical bytes hashed into the wavelet version chain
// for a delta applied at appliedAtVersion: author, applied-at version,
// timestamp, and the applied operations. It deliberately excludes the resulting
// hash (which is derived from these bytes).
func HashBytes(author id.ParticipantID, appliedAtVersion uint64, timestamp int64, ops []waveop.Operation) []byte {
	return marshal([]any{author.Address(), appliedAtVersion, timestamp, opsValue(ops)})
}

// StoredDelta is the persisted form of an applied delta (spec §5.1
// TransformedWaveletDelta + applied-at version, which is derivable). The
// resulting hashed version is stored so reload reads it rather than recomputing.
type StoredDelta struct {
	Author           id.ParticipantID
	ResultingVersion version.HashedVersion
	Timestamp        int64
	Ops              []waveop.Operation
	// Nonce is the submitting client's per-submission tag, carried through so a
	// resync tail lets that client recognize its own delta. Opaque to the server,
	// not part of the hash chain, empty if unused.
	Nonce string
}

// EncodeStoredDelta returns the canonical CBOR encoding of a stored delta.
func EncodeStoredDelta(d StoredDelta) []byte {
	return marshal([]any{
		d.Author.Address(),
		wireHV{Version: d.ResultingVersion.Version(), Hash: d.ResultingVersion.HistoryHash()},
		d.Timestamp,
		opsValue(d.Ops),
		d.Nonce,
	})
}

// DecodeStoredDelta parses a stored delta encoding.
func DecodeStoredDelta(data []byte) (StoredDelta, error) {
	var raw []cbor.RawMessage
	if err := cbor.Unmarshal(data, &raw); err != nil {
		return StoredDelta{}, err
	}
	if len(raw) != 5 {
		return StoredDelta{}, fmt.Errorf("codec: stored delta has %d fields, want 5", len(raw))
	}
	var addr string
	if err := cbor.Unmarshal(raw[0], &addr); err != nil {
		return StoredDelta{}, err
	}
	author, err := id.NewParticipantID(addr)
	if err != nil {
		return StoredDelta{}, err
	}
	var hv wireHV
	if err := cbor.Unmarshal(raw[1], &hv); err != nil {
		return StoredDelta{}, err
	}
	var ts int64
	if err := cbor.Unmarshal(raw[2], &ts); err != nil {
		return StoredDelta{}, err
	}
	var opsRaw []cbor.RawMessage
	if err := cbor.Unmarshal(raw[3], &opsRaw); err != nil {
		return StoredDelta{}, err
	}
	ops, err := opsFrom(opsRaw)
	if err != nil {
		return StoredDelta{}, err
	}
	var nonce string
	if err := cbor.Unmarshal(raw[4], &nonce); err != nil {
		return StoredDelta{}, err
	}
	return StoredDelta{
		Author:           author,
		ResultingVersion: version.NewHashedVersion(hv.Version, hv.Hash),
		Timestamp:        ts,
		Ops:              ops,
		Nonce:            nonce,
	}, nil
}
