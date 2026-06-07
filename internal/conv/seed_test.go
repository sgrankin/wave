package conv_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/wavelet"
	"github.com/sgrankin/wave/internal/waveop"
)

// TestSeedConversation: the seed ops, applied to a fresh wavelet, produce a
// well-formed one-blip conversation with the author as participant — exactly the
// state the client used to bootstrap.
func TestSeedConversation(t *testing.T) {
	alice, err := id.NewParticipantID("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ops, err := conv.SeedConversation(alice, 1000)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("seed produced %d ops, want 3 (addParticipant, manifest, root blip)", len(ops))
	}

	waveID, _ := id.NewWaveID("example.com", "w+x")
	waveletID, _ := id.NewWaveletID("example.com", "conv+root")
	name := id.NewWaveletName(waveID, waveletID)
	w := wavelet.New(waveID, waveletID, alice, 1000, version.Zero(name))
	delta := waveop.NewWaveletDelta(alice, version.Zero(name), ops)
	if err := w.ApplyDelta(delta, []byte("seed")); err != nil {
		t.Fatalf("apply seed delta: %v", err)
	}

	if !w.HasParticipant(alice) {
		t.Error("author is not a participant after seeding")
	}
	manBlip, ok := w.Blip(conv.ManifestDocumentID)
	if !ok {
		t.Fatal("manifest document was not created")
	}
	m, err := conv.ReadManifest(manBlip.Content())
	if err != nil {
		t.Fatalf("read seeded manifest: %v", err)
	}
	if len(m.RootThread.Blips) != 1 || m.RootThread.Blips[0].ID != conv.RootBlipID {
		t.Errorf("root thread = %+v, want exactly one blip %q", m.RootThread.Blips, conv.RootBlipID)
	}
	if _, ok := w.Blip(conv.RootBlipID); !ok {
		t.Error("root blip document was not created")
	}
}
