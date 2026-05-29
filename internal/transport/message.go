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

// --- open: [mOpen, waveletName] ---

func encodeOpen(waveletName string) []byte { return marshal([]any{mOpen, waveletName}) }

func decodeOpen(raw []cbor.RawMessage) (string, error) {
	if err := need(raw, 2); err != nil {
		return "", err
	}
	var name string
	err := cbor.Unmarshal(raw[1], &name)
	return name, err
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

// --- submit response: [mSubmitResponse, ok, code, msg, resultingVersionBytes] ---

func encodeSubmitResponse(ok bool, code uint64, msg string, resultingVersion []byte) []byte {
	return marshal([]any{mSubmitResponse, ok, code, msg, resultingVersion})
}

// submitResponse is the decoded ack/nack. ResultingVersion is the codec encoding
// of the post-apply hashed version (nil on a nack).
type submitResponse struct {
	OK               bool
	Code             uint64
	Msg              string
	ResultingVersion []byte
}

func decodeSubmitResponse(raw []cbor.RawMessage) (submitResponse, error) {
	var r submitResponse
	if err := need(raw, 5); err != nil {
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
