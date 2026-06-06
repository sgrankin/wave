package transport

import (
	"bytes"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgrankin/wave/internal/cc"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payloads := [][]byte{[]byte("hello"), {}, bytes.Repeat([]byte{0xAB}, 5000)}
	for _, p := range payloads {
		if err := writeFrame(&buf, p); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	for i, want := range payloads {
		got, err := readFrame(&buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d = %x, want %x", i, got, want)
		}
	}
}

func TestMessageRoundTrips(t *testing.T) {
	// open
	if kind, raw, err := messageKind(encodeOpen("example.com/w+a/~/conv+root", true)); err != nil || kind != mOpen {
		t.Fatalf("open kind=%d err=%v", kind, err)
	} else if name, suppress, err := decodeOpen(raw); err != nil || name != "example.com/w+a/~/conv+root" || !suppress {
		t.Errorf("decodeOpen = %q, suppress=%v, %v", name, suppress, err)
	}

	// resync request
	if _, raw, err := messageKind(encodeResync("example.com/w+a/~/conv+root", 7, []byte{1, 2, 3}, true)); err != nil {
		t.Fatal(err)
	} else if name, v, hash, suppress, err := decodeResync(raw); err != nil ||
		name != "example.com/w+a/~/conv+root" || v != 7 || string(hash) != "\x01\x02\x03" || !suppress {
		t.Errorf("decodeResync = %q v=%d hash=%x suppress=%v err=%v", name, v, hash, suppress, err)
	}

	// resync response (tail mode)
	if _, raw, err := messageKind(encodeResyncResponse(resyncTail, [][]byte{[]byte("d0"), []byte("d1")}, nil, nil)); err != nil {
		t.Fatal(err)
	} else if mode, tail, snap, hist, err := decodeResyncResponse(raw); err != nil ||
		mode != resyncTail || len(tail) != 2 || len(snap) != 0 || len(hist) != 0 {
		t.Errorf("decodeResyncResponse(tail) = mode=%d tail=%d snap=%d hist=%d err=%v", mode, len(tail), len(snap), len(hist), err)
	}

	// resync required (no payload)
	if kind, _, err := messageKind(encodeResyncRequired()); err != nil || kind != mResyncRequired {
		t.Errorf("resyncRequired kind=%d err=%v", kind, err)
	}

	// submit response (nack carries code + message, nil version)
	_, raw, err := messageKind(encodeSubmitResponse(false, 4, "stale", nil))
	if err != nil {
		t.Fatal(err)
	}
	r, err := decodeSubmitResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || r.Code != 4 || r.Msg != "stale" || r.ResultingVersion != nil {
		t.Errorf("submitResponse round trip = %+v", r)
	}

	// error
	_, raw, _ = messageKind(encodeError("boom"))
	if msg, err := decodeError(raw); err != nil || msg != "boom" {
		t.Errorf("decodeError = %q, %v", msg, err)
	}
}

func TestTruncatedMessageRejected(t *testing.T) {
	// A bare kind with no fields must not panic the field decoders.
	_, raw, err := messageKind(marshal([]any{mSubmitResponse}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSubmitResponse(raw); err == nil {
		t.Error("decodeSubmitResponse should reject a truncated message")
	}
}

// TestSubmitBadVersionNacks drives the wire directly: it opens a wavelet, then
// submits a delta targeting a version/hash the history does not know, and
// expects a VersionError nack over the wire.
func TestSubmitBadVersionNacks(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wm := server.NewWaveMap(store, clock.NewFixed(time.UnixMilli(1000)))

	w, _ := id.NewWaveID("example.com", "w+abc")
	wl, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(w, wl)
	alice, _ := id.NewParticipantID("alice@example.com")

	cConn, sConn := net.Pipe()
	go func() { _ = Serve(sConn, wm) }()
	defer cConn.Close()

	// Open and apply a first delta so the wavelet exists at v1.
	mustWrite(t, cConn, encodeOpen(name.Serialize(), false))
	mustRead(t, cConn) // open response (empty history)
	good := codec.EncodeClientDelta(codec.ClientDelta{
		Author: alice, TargetVersion: version.Zero(name),
		Ops: []waveop.Operation{blipOp(alice, "b", "hi")},
	})
	mustWrite(t, cConn, encodeSubmit(good))
	if r := readSubmitResponse(t, cConn); !r.OK {
		t.Fatalf("good submit nacked: code=%d %s", r.Code, r.Msg)
	}

	// Submit targeting version 0 with the WRONG hash → VersionError.
	bad := codec.EncodeClientDelta(codec.ClientDelta{
		Author: alice, TargetVersion: version.NewHashedVersion(0, []byte("not-the-zero-hash")),
		Ops: []waveop.Operation{blipOp(alice, "b", "z")},
	})
	mustWrite(t, cConn, encodeSubmit(bad))
	r := readSubmitResponse(t, cConn)
	if r.OK {
		t.Fatal("bad-version submit was accepted")
	}
	if r.Code != uint64(cc.VersionError) {
		t.Errorf("nack code = %d, want VersionError (%d)", r.Code, uint64(cc.VersionError))
	}
}

func blipOp(author id.ParticipantID, blipID, text string) waveop.Operation {
	c := waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}
	return waveop.WaveletBlipOperation{
		BlipID: blipID,
		BlipOp: waveop.BlipContentOperation{Ctx: c, ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: text}})},
	}
}

func mustWrite(t *testing.T, conn net.Conn, f []byte) {
	t.Helper()
	if err := writeFrame(conn, f); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func mustRead(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	f, err := readFrame(conn)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return f
}

// readSubmitResponse reads frames until it finds the submit ack/nack, skipping
// any live updates (a successful submit also broadcasts an mUpdate, which races
// the ack on the wire).
func readSubmitResponse(t *testing.T, conn net.Conn) submitResponse {
	t.Helper()
	for {
		kind, raw, err := messageKind(mustRead(t, conn))
		if err != nil {
			t.Fatalf("decode response: %v", err)
		}
		switch kind {
		case mUpdate:
			continue
		case mSubmitResponse:
			r, err := decodeSubmitResponse(raw)
			if err != nil {
				t.Fatalf("decode submit response: %v", err)
			}
			return r
		default:
			t.Fatalf("got message kind %d, want submitResponse or update", kind)
		}
	}
}
