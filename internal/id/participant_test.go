package id_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/id"
)

func TestNewParticipantID(t *testing.T) {
	tests := []struct {
		in         string
		wantAddr   string
		wantName   string
		wantDomain string
	}{
		{"alice@example.com", "alice@example.com", "alice", "example.com"},
		{"Alice@Example.COM", "alice@example.com", "alice", "example.com"}, // normalized to lowercase
		{"@example.com", "@example.com", "", "example.com"},                // shared-domain participant
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			p, err := id.NewParticipantID(tc.in)
			if err != nil {
				t.Fatalf("NewParticipantID(%q): %v", tc.in, err)
			}
			if p.Address() != tc.wantAddr {
				t.Errorf("Address() = %q, want %q", p.Address(), tc.wantAddr)
			}
			if p.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", p.Name(), tc.wantName)
			}
			if p.Domain() != tc.wantDomain {
				t.Errorf("Domain() = %q, want %q", p.Domain(), tc.wantDomain)
			}
		})
	}
}

func TestNewParticipantIDInvalid(t *testing.T) {
	for _, in := range []string{"noatsign", "trailing@", "a@b@c"} {
		if _, err := id.NewParticipantID(in); err == nil {
			t.Errorf("NewParticipantID(%q) = nil error, want error", in)
		}
	}
}
