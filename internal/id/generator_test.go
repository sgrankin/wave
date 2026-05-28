package id

// White-box tests for the unexported counter encoding and token builder; the
// public generator surface is exercised below via the exported API.

import (
	"sync"
	"testing"
)

func TestWeb64Encode(t *testing.T) {
	// Golden vectors locking the minimum-length, big-endian, web-safe alphabet
	// against IdGeneratorImpl.base64Encode.
	tests := map[uint32]string{
		0:      "A",
		1:      "B",
		25:     "Z",
		26:     "a",
		51:     "z",
		52:     "0",
		61:     "9",
		62:     "-",
		63:     "_",
		64:     "BA",
		4095:   "__",
		4096:   "BAA",
		262143: "___",  // 3-byte boundary (0x3FFFF)
		262144: "BAAA", // 4-byte boundary (0x40000)
	}
	for in, want := range tests {
		if got := web64Encode(in); got != want {
			t.Errorf("web64Encode(%d) = %q, want %q", in, got, want)
		}
	}
	// Top of the uint32 range encodes to 6 web64 chars (36 bits cover 32).
	if got := web64Encode(0xFFFFFFFF); len(got) != 6 {
		t.Errorf("web64Encode(0xFFFFFFFF) = %q (len %d), want len 6", got, len(got))
	}
}

func TestEscapeToken(t *testing.T) {
	tests := map[string]string{
		"abc":  "abc",
		"a+b":  "a~+b",
		"a!b":  "a~!b",
		"a~b":  "a~~b",
		"~+!":  "~~~+~!",
		"none": "none",
	}
	for in, want := range tests {
		if got := escapeToken(in); got != want {
			t.Errorf("escapeToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildID(t *testing.T) {
	if got, want := buildID("b", "abcA"), "b+abcA"; got != want {
		t.Errorf("buildID(b, abcA) = %q, want %q", got, want)
	}
	if got, want := buildID("user", "alice@example.com"), "user+alice@example.com"; got != want {
		t.Errorf("buildID(user, addr) = %q, want %q", got, want)
	}
}

func TestGeneratorDeterministicWithFixedSeed(t *testing.T) {
	g := NewGenerator("example.com", func() string { return "seed" })
	// Counter starts at 0: web64(0)="A", web64(1)="B", web64(2)="C".
	if got := g.NewBlipID(); got != "b+seedA" {
		t.Errorf("first blip id = %q, want b+seedA", got)
	}
	if got := g.NewBlipID(); got != "b+seedB" {
		t.Errorf("second blip id = %q, want b+seedB", got)
	}
	w, err := g.NewWaveID()
	if err != nil {
		t.Fatalf("NewWaveID: %v", err)
	}
	if w.ID() != "w+seedC" {
		t.Errorf("wave id local = %q, want w+seedC", w.ID())
	}
}

func TestGeneratorConcurrentUniqueTokens(t *testing.T) {
	g := NewGenerator("example.com", func() string { return "s" })
	const n = 1000
	var mu sync.Mutex
	seen := make(map[string]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := g.NewUniqueToken()
			mu.Lock()
			seen[tok] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("got %d unique tokens, want %d (collision)", len(seen), n)
	}
}

func TestUserDataWaveletID(t *testing.T) {
	g := NewGenerator("server.example", func() string { return "s" })
	p, err := NewParticipantID("alice@other.com")
	if err != nil {
		t.Fatal(err)
	}
	w, err := g.NewUserDataWaveletID(p)
	if err != nil {
		t.Fatalf("NewUserDataWaveletID: %v", err)
	}
	// Hosted on the participant's domain, id "user+<address>".
	if w.Domain() != "other.com" {
		t.Errorf("UDW domain = %q, want other.com", w.Domain())
	}
	if w.ID() != "user+alice@other.com" {
		t.Errorf("UDW id = %q, want user+alice@other.com", w.ID())
	}
}
