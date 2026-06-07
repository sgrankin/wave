package main

import "testing"

func TestRequireSafeAuthBind(t *testing.T) {
	cases := []struct {
		name     string
		authMode string
		wsAddr   string
		wantErr  bool
	}{
		{"dev disabled ws", "dev", "", false},
		{"dev loopback ip", "dev", "127.0.0.1:8131", false},
		{"dev loopback ipv6", "dev", "[::1]:8131", false},
		{"dev localhost", "dev", "localhost:8131", false},
		{"dev all interfaces", "dev", "0.0.0.0:8131", true},
		{"dev empty host", "dev", ":8131", true},
		{"dev lan ip", "dev", "192.168.1.10:8131", true},
		{"dev bad addr", "dev", "not-an-addr", true},
		{"proxy all interfaces ok", "proxy", "0.0.0.0:8131", false},
		{"proxy lan ok", "proxy", "192.168.1.10:8131", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSafeAuthBind(tc.authMode, tc.wsAddr)
			if (err != nil) != tc.wantErr {
				t.Errorf("requireSafeAuthBind(%q, %q) err = %v, wantErr %v", tc.authMode, tc.wsAddr, err, tc.wantErr)
			}
		})
	}
}
