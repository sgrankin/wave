package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestEnvDefaultsApplyWhenFlagAbsent(t *testing.T) {
	t.Setenv("WAVED_DB", "/from/env.db")
	t.Setenv("WAVED_AUTH_DOMAIN", "env.example.com")
	cfg, err := parseFlags(nil) // no command-line flags
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dbPath != "/from/env.db" {
		t.Errorf("dbPath = %q, want the env value", cfg.dbPath)
	}
	if cfg.authDomain != "env.example.com" {
		t.Errorf("authDomain = %q, want the env value", cfg.authDomain)
	}
}

func TestExplicitFlagBeatsEnv(t *testing.T) {
	t.Setenv("WAVED_DB", "/from/env.db")
	cfg, err := parseFlags([]string{"-db", "/from/flag.db"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dbPath != "/from/flag.db" {
		t.Errorf("dbPath = %q, want the explicit flag to win over env", cfg.dbPath)
	}
}

// pingFunc adapts a func to the pinger interface.
type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestReadyHandler(t *testing.T) {
	ok := httptest.NewRecorder()
	readyHandler(pingFunc(func(context.Context) error { return nil }))(ok, httptest.NewRequest("GET", "/readyz", nil))
	if ok.Code != http.StatusOK {
		t.Errorf("healthy ping: status = %d, want 200", ok.Code)
	}

	bad := httptest.NewRecorder()
	readyHandler(pingFunc(func(context.Context) error { return errors.New("db down") }))(bad, httptest.NewRequest("GET", "/readyz", nil))
	if bad.Code != http.StatusServiceUnavailable {
		t.Errorf("failed ping: status = %d, want 503", bad.Code)
	}
}
