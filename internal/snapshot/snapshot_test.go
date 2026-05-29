package snapshot_test

import (
	"reflect"
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/snapshot"
	"github.com/sgrankin/wave/internal/wavelet"
)

func sample() wavelet.SnapshotState {
	return wavelet.SnapshotState{
		WaveID:           "example.com/w+x",
		WaveletID:        "example.com!conv+root",
		Creator:          "alice@example.com",
		CreationTime:     100,
		LastModifiedTime: 250,
		Version:          4,
		HistoryHash:      []byte{0xde, 0xad, 0xbe, 0xef},
		Participants:     []string{"alice@example.com", "bob@example.com"},
		Blips: []wavelet.BlipSnapshot{{
			ID:                  "b",
			Author:              "alice@example.com",
			Contributors:        []string{"alice@example.com", "bob@example.com"},
			LastModifiedTime:    250,
			LastModifiedVersion: 4,
			Content:             op.NewDocOp([]op.Component{op.Characters{Text: "hello"}}),
		}},
	}
}

func stateEqual(a, b wavelet.SnapshotState) bool {
	if a.WaveID != b.WaveID || a.WaveletID != b.WaveletID || a.Creator != b.Creator ||
		a.CreationTime != b.CreationTime || a.LastModifiedTime != b.LastModifiedTime ||
		a.Version != b.Version || string(a.HistoryHash) != string(b.HistoryHash) ||
		!reflect.DeepEqual(a.Participants, b.Participants) || len(a.Blips) != len(b.Blips) {
		return false
	}
	for i := range a.Blips {
		x, y := a.Blips[i], b.Blips[i]
		if x.ID != y.ID || x.Author != y.Author || !reflect.DeepEqual(x.Contributors, y.Contributors) ||
			x.LastModifiedTime != y.LastModifiedTime || x.LastModifiedVersion != y.LastModifiedVersion ||
			!x.Content.Equal(y.Content) {
			return false
		}
	}
	return true
}

// FromState then State must reproduce the input — the wavelet reconstruction
// round-trips.
func TestStateReconstruction(t *testing.T) {
	orig := sample()
	w, err := wavelet.FromState(orig)
	if err != nil {
		t.Fatalf("FromState: %v", err)
	}
	if got := w.State(); !stateEqual(got, orig) {
		t.Errorf("FromState→State mismatch:\n got %+v\nwant %+v", got, orig)
	}
}

// Encode then Decode must round-trip the snapshot bytes.
func TestEncodeDecode(t *testing.T) {
	orig := sample()
	dec, err := snapshot.Decode(snapshot.Encode(orig))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !stateEqual(dec, orig) {
		t.Errorf("Encode→Decode mismatch:\n got %+v\nwant %+v", dec, orig)
	}
}

func TestDecodeGarbage(t *testing.T) {
	if _, err := snapshot.Decode([]byte("not cbor")); err == nil {
		t.Error("Decode of garbage should error")
	}
}
