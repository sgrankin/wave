package transport

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Message kinds. Each message is a CBOR array [kind, fields...]. This envelope
// is the evolvable wire layer — not the frozen hash-feeding encoding (codec).
const (
	mOpen           uint64 = iota // client→server: bind the connection to a wavelet
	mOpenResponse                 // server→client: starting view (state snapshot or delta history)
	mSubmit                       // client→server: a client delta to apply
	mSubmitResponse               // server→client: ack/nack for a submit
	mUpdate                       // server→client: a newly applied delta (live stream)
	mError                        // server→client: a protocol/processing error
	mResync                       // client→server: reconnect/resync at a known version
	mResyncResponse               // server→client: incremental tail, or a full-view reset
	mResyncRequired               // server→client: the live stream gapped; client must Resync
)

// Resync response modes.
const (
	resyncTail  uint64 = 0 // tail holds the stored deltas after the client's known version
	resyncReset uint64 = 1 // known point unusable (fork/pruned): full view follows; discard local state
)

// msgEnc is the envelope encoder. CoreDeterministic is used for consistency with
// codec, though the envelope is not hashed and determinism is not required here.
var msgEnc = mustMsgEnc()

func mustMsgEnc() cbor.EncMode {
	em, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic("transport: building CBOR encode mode: " + err.Error())
	}
	return em
}

// marshal encodes a message envelope. Our message values are always encodable,
// so an error is a programming bug.
func marshal(v []any) []byte {
	b, err := msgEnc.Marshal(v)
	if err != nil {
		panic("transport: marshal message: " + err.Error())
	}
	return b
}

// need bounds-checks a decoded envelope before indexing its fields.
func need(raw []cbor.RawMessage, n int) error {
	if len(raw) < n {
		return fmt.Errorf("transport: truncated message: have %d fields, need %d", len(raw), n)
	}
	return nil
}

// messageKind decodes the envelope and returns the message kind plus the raw
// field list (raw[0] is the kind).
func messageKind(data []byte) (uint64, []cbor.RawMessage, error) {
	var raw []cbor.RawMessage
	if err := cbor.Unmarshal(data, &raw); err != nil {
		return 0, nil, err
	}
	if len(raw) == 0 {
		return 0, nil, fmt.Errorf("transport: empty message")
	}
	var kind uint64
	if err := cbor.Unmarshal(raw[0], &kind); err != nil {
		return 0, nil, err
	}
	return kind, raw, nil
}

// --- open: [mOpen, waveletName, suppressEcho] ---
// suppressEcho: when true the server omits this connection's own applied deltas
// from its update stream (the submitter learns each outcome from the submit ack
// only). Optimistic clients set it — they apply locally and transform incoming
// deltas themselves, so an echo of their own delta would be a duplicate they
// cannot reliably recognize (the server's transformed delta and the client's
// locally-transformed in-flight can differ syntactically while still converging).
// The pessimistic replica client leaves it false and advances by applying its
// own echoed delta.

func encodeOpen(waveletName string, suppressEcho bool) []byte {
	return marshal([]any{mOpen, waveletName, suppressEcho})
}

func decodeOpen(raw []cbor.RawMessage) (name string, suppressEcho bool, err error) {
	if err := need(raw, 3); err != nil {
		return "", false, err
	}
	if err := cbor.Unmarshal(raw[1], &name); err != nil {
		return "", false, err
	}
	if err := cbor.Unmarshal(raw[2], &suppressEcho); err != nil {
		return "", false, err
	}
	return name, suppressEcho, nil
}

// --- open response: [mOpenResponse, snapshotBlob, [storedDeltaBytes...]] ---
// snapshotBlob is empty for a history-based join (history is the full log from
// version 0); non-empty for a snapshot-based join (history is then empty).

func encodeOpenResponse(snapshotBlob []byte, history [][]byte) []byte {
	return marshal([]any{mOpenResponse, snapshotBlob, history})
}

func decodeOpenResponse(raw []cbor.RawMessage) (snapshotBlob []byte, history [][]byte, err error) {
	if err := need(raw, 3); err != nil {
		return nil, nil, err
	}
	if err := cbor.Unmarshal(raw[1], &snapshotBlob); err != nil {
		return nil, nil, err
	}
	if err := cbor.Unmarshal(raw[2], &history); err != nil {
		return nil, nil, err
	}
	return snapshotBlob, history, nil
}

// --- submit: [mSubmit, clientDeltaBytes] ---

func encodeSubmit(deltaBytes []byte) []byte { return marshal([]any{mSubmit, deltaBytes}) }

func decodeSubmit(raw []cbor.RawMessage) ([]byte, error) {
	if err := need(raw, 2); err != nil {
		return nil, err
	}
	var b []byte
	err := cbor.Unmarshal(raw[1], &b)
	return b, err
}

// --- submit response: [mSubmitResponse, ok, code, msg, resultingVersionBytes, opsApplied] ---
// opsApplied is the number of operations the server actually applied (the
// authoritative version span of the delta): equal to the submitted op count
// normally, but zero for a deduped resend or a fully transformed-away delta.
// Client concurrency control needs it to settle the in-flight delta correctly.

func encodeSubmitResponse(ok bool, code uint64, msg string, resultingVersion []byte, opsApplied uint64) []byte {
	return marshal([]any{mSubmitResponse, ok, code, msg, resultingVersion, opsApplied})
}

// submitResponse is the decoded ack/nack. ResultingVersion is the codec encoding
// of the post-apply hashed version (nil on a nack); OpsApplied is the server's
// applied op count (zero on a nack).
type submitResponse struct {
	OK               bool
	Code             uint64
	Msg              string
	ResultingVersion []byte
	OpsApplied       uint64
}

func decodeSubmitResponse(raw []cbor.RawMessage) (submitResponse, error) {
	var r submitResponse
	if err := need(raw, 6); err != nil {
		return r, err
	}
	if err := cbor.Unmarshal(raw[1], &r.OK); err != nil {
		return r, err
	}
	if err := cbor.Unmarshal(raw[2], &r.Code); err != nil {
		return r, err
	}
	if err := cbor.Unmarshal(raw[3], &r.Msg); err != nil {
		return r, err
	}
	if err := cbor.Unmarshal(raw[5], &r.OpsApplied); err != nil {
		return r, err
	}
	if err := cbor.Unmarshal(raw[4], &r.ResultingVersion); err != nil {
		return r, err
	}
	return r, nil
}

// --- update: [mUpdate, storedDeltaBytes] ---

func encodeUpdate(deltaBytes []byte) []byte { return marshal([]any{mUpdate, deltaBytes}) }

func decodeUpdate(raw []cbor.RawMessage) ([]byte, error) {
	if err := need(raw, 2); err != nil {
		return nil, err
	}
	var b []byte
	err := cbor.Unmarshal(raw[1], &b)
	return b, err
}

// --- error: [mError, msg] ---

func encodeError(msg string) []byte { return marshal([]any{mError, msg}) }

func decodeError(raw []cbor.RawMessage) (string, error) {
	if err := need(raw, 2); err != nil {
		return "", err
	}
	var msg string
	err := cbor.Unmarshal(raw[1], &msg)
	return msg, err
}

// --- resync: [mResync, waveletName, knownVersion, knownHash, suppressEcho] ---
// A reconnecting client states the (version, history-hash) it already holds; the
// server replies with the tail since then (or a reset). suppressEcho carries the
// same meaning as in Open and applies to the continuation stream.

func encodeResync(waveletName string, knownVersion uint64, knownHash []byte, suppressEcho bool) []byte {
	return marshal([]any{mResync, waveletName, knownVersion, knownHash, suppressEcho})
}

func decodeResync(raw []cbor.RawMessage) (name string, knownVersion uint64, knownHash []byte, suppressEcho bool, err error) {
	if err := need(raw, 5); err != nil {
		return "", 0, nil, false, err
	}
	if err := cbor.Unmarshal(raw[1], &name); err != nil {
		return "", 0, nil, false, err
	}
	if err := cbor.Unmarshal(raw[2], &knownVersion); err != nil {
		return "", 0, nil, false, err
	}
	if err := cbor.Unmarshal(raw[3], &knownHash); err != nil {
		return "", 0, nil, false, err
	}
	if err := cbor.Unmarshal(raw[4], &suppressEcho); err != nil {
		return "", 0, nil, false, err
	}
	return name, knownVersion, knownHash, suppressEcho, nil
}

// --- resync response: [mResyncResponse, mode, tail, snapshotBlob, history] ---
// mode resyncTail: tail holds the stored deltas after knownVersion; snapshotBlob
// and history are empty. mode resyncReset: the full view is in snapshotBlob (or
// history), exactly as an open response, and tail is empty.

func encodeResyncResponse(mode uint64, tail [][]byte, snapshotBlob []byte, history [][]byte) []byte {
	return marshal([]any{mResyncResponse, mode, tail, snapshotBlob, history})
}

func decodeResyncResponse(raw []cbor.RawMessage) (mode uint64, tail [][]byte, snapshotBlob []byte, history [][]byte, err error) {
	if err := need(raw, 5); err != nil {
		return 0, nil, nil, nil, err
	}
	if err := cbor.Unmarshal(raw[1], &mode); err != nil {
		return 0, nil, nil, nil, err
	}
	if err := cbor.Unmarshal(raw[2], &tail); err != nil {
		return 0, nil, nil, nil, err
	}
	if err := cbor.Unmarshal(raw[3], &snapshotBlob); err != nil {
		return 0, nil, nil, nil, err
	}
	if err := cbor.Unmarshal(raw[4], &history); err != nil {
		return 0, nil, nil, nil, err
	}
	return mode, tail, snapshotBlob, history, nil
}

// --- resync required: [mResyncRequired] ---
// The server dropped this connection's live stream (it fell too far behind); the
// client must reconnect and Resync at its last known version.

func encodeResyncRequired() []byte { return marshal([]any{mResyncRequired}) }
