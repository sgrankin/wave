package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sgrankin/wave/internal/auth"
)

func TestRequireSafeAuthBind(t *testing.T) {
	// A registry with a loopback-only method (dev) vs one without (proxy asserted
	// exclusive, or a real IdP). requireSafeAuthBind only blocks the former on a
	// public bind.
	devReg := auth.NewRegistry(auth.DevMethod{})                                  // RequireLoopback() == true
	proxyExclusiveReg := auth.NewRegistry(auth.ProxyMethod{ProxyExclusive: true}) // false
	proxyReg := auth.NewRegistry(auth.ProxyMethod{})                              // true (not exclusive)
	cases := []struct {
		name    string
		reg     *auth.Registry
		wsAddr  string
		wantErr bool
	}{
		{"dev disabled ws", devReg, "", false},
		{"dev loopback ip", devReg, "127.0.0.1:8131", false},
		{"dev loopback ipv6", devReg, "[::1]:8131", false},
		{"dev localhost", devReg, "localhost:8131", false},
		{"dev all interfaces", devReg, "0.0.0.0:8131", true},
		{"dev empty host", devReg, ":8131", true},
		{"dev lan ip", devReg, "192.168.1.10:8131", true},
		{"dev bad addr", devReg, "not-an-addr", true},
		{"proxy non-exclusive lan blocked", proxyReg, "192.168.1.10:8131", true},
		{"proxy exclusive all interfaces ok", proxyExclusiveReg, "0.0.0.0:8131", false},
		{"proxy exclusive lan ok", proxyExclusiveReg, "192.168.1.10:8131", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSafeAuthBind(tc.reg, tc.wsAddr)
			if (err != nil) != tc.wantErr {
				t.Errorf("requireSafeAuthBind(%q) err = %v, wantErr %v", tc.wsAddr, err, tc.wantErr)
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
