// Package transport is the logical session protocol over a byte stream: the
// client-facing wire on top of the wavelet-serving core (package server). It is
// transport-agnostic — it speaks over any io.ReadWriter (an in-process net.Pipe
// in tests, stdin/stdout for a paired process, a socket for many clients) — so
// the browser transport in Phase 8 plugs in here without touching the protocol.
//
// Framing is a 4-byte big-endian length prefix followed by a CBOR message
// envelope (see message.go). Delta payloads inside the envelope use the frozen
// canonical encoding (package codec); the envelope itself is NOT frozen and may
// evolve. One connection serves one wavelet (bound by the opening message),
// matching "one client delta in flight per wavelet channel" (invariant #4).
//
// Spec: docs/specs/04-wire-protocol.md; docs/architecture/01-target-architecture.md
// (Wire & transport).
package transport

import (
	"encoding/binary"
	"fmt"
	"io"
)

// maxFrameSize bounds a single frame's payload, guarding against a corrupt or
// hostile length prefix triggering an unbounded allocation.
const maxFrameSize = 64 << 20 // 64 MiB

// writeFrame writes one length-prefixed frame in a single Write (header +
// payload), so a frame is never split across writes by this function. Callers
// must still serialize writeFrame on a given writer (one writer goroutine per
// connection) — concurrent calls would interleave frames.
func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > maxFrameSize {
		return fmt.Errorf("transport: frame too large to send: %d bytes", len(payload))
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	_, err := w.Write(buf)
	return err
}

// readFrame reads one length-prefixed frame. It returns io.EOF if the stream
// ends cleanly at a frame boundary, and io.ErrUnexpectedEOF if it ends mid-frame.
func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameSize {
		return nil, fmt.Errorf("transport: frame too large to read: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
