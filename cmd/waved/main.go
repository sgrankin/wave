// Command waved is the Wave server: it serves the OT session protocol
// (internal/transport) over a socket (or stdio), backed by a SQLite delta store,
// with structured logging, an operability HTTP endpoint, and signal-driven
// graceful shutdown.
//
// A dev browser WebSocket transport is available behind -ws (unauthenticated,
// pinned identity — see startWebSocket); session-cookie authentication is a later
// phase (docs/architecture/02-porting-plan.md).
package main

import (
	"context"
	"errors"
	"expvar"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/search"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/transport"
)

// shutdownGrace bounds the operability server's drain during shutdown.
const shutdownGrace = 5 * time.Second

type config struct {
	network       string // unix | tcp | stdio
	addr          string // unix socket path or host:port
	dbPath        string
	httpAddr      string // operability HTTP address; "" disables
	wsAddr        string // browser WebSocket transport address; "" disables
	wsUser        string // dev identity pinned to every WebSocket connection
	webRoot       string // static web root served at / on the -ws server; "" disables
	logFormat     string // text | json
	logLevel      string // debug | info | warn | error
	snapshotEvery int    // write a snapshot every N ops (0 disables)
	index         bool   // maintain the derived read index (inbox/search)
	showVersion   bool
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "waved:", err)
		os.Exit(2)
	}
	if cfg.showVersion {
		fmt.Println(buildVersion())
		return
	}
	if err := runMain(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "waved:", err)
		os.Exit(1)
	}
}

// runMain owns the signal-cancelled context so its cleanup runs before any
// os.Exit in main (which would otherwise skip a deferred stop).
func runMain(cfg config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, cfg)
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("waved", flag.ContinueOnError)
	var c config
	fs.StringVar(&c.network, "net", "unix", "listener network: unix | tcp | stdio")
	fs.StringVar(&c.addr, "addr", "/tmp/waved.sock", "listen address (unix socket path or host:port)")
	fs.StringVar(&c.dbPath, "db", "waved.db", "sqlite database path (\":memory:\" for ephemeral)")
	fs.StringVar(&c.httpAddr, "http", "127.0.0.1:8099", "operability HTTP address (\"\" to disable)")
	fs.StringVar(&c.wsAddr, "ws", "", "browser WebSocket transport address, host:port (\"\" to disable)")
	fs.StringVar(&c.wsUser, "ws-user", "user@example.com", "DEV identity pinned to every WebSocket connection (no real auth yet)")
	fs.StringVar(&c.webRoot, "webroot", "", "static web root served at / on the -ws server (\"\" to disable)")
	fs.StringVar(&c.logFormat, "log", "text", "log format: text | json")
	fs.StringVar(&c.logLevel, "log-level", "info", "log level: debug | info | warn | error")
	fs.IntVar(&c.snapshotEvery, "snapshot-every", 0, "snapshot a wavelet every N ops (0 = disabled)")
	fs.BoolVar(&c.index, "index", true, "maintain the derived read index (inbox/search)")
	fs.BoolVar(&c.showVersion, "version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	return c, nil
}

func run(ctx context.Context, cfg config) error {
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	store, err := sqlite.Open(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open db %q: %w", cfg.dbPath, err)
	}
	defer store.Close()

	var opts []server.Option
	if cfg.snapshotEvery > 0 {
		opts = append(opts, server.WithSnapshots(store, cfg.snapshotEvery))
		logger.Info("snapshots enabled", "every", cfg.snapshotEvery)
	}
	if cfg.index {
		opts = append(opts, server.WithIndexer(search.New(store, logger)))
		logger.Info("index maintenance enabled")
	}
	wm := server.NewWaveMap(store, clock.System{}, opts...)
	srv := &transport.Server{WaveMap: wm, Logger: logger}

	// stdio mode serves exactly one session over stdin/stdout (for pipe pairing,
	// LSP-style). It ends when the peer closes stdin; there is no listener to
	// drain, no operability HTTP endpoint, and SIGTERM will NOT interrupt a
	// blocked stdin read — the peer must close the pipe. Other shutdown steps
	// still run on return.
	if cfg.network == "stdio" {
		logger.Info("serving one session over stdio")
		serveErr := srv.ServeConn(stdioConn{})
		return finishShutdown(store, srv, nil, nil, logger, serveErr)
	}

	httpSrv := startOperability(cfg, srv, wm, logger)

	wsSrv, err := startWebSocket(cfg, srv, logger)
	if err != nil {
		return finishShutdown(store, srv, httpSrv, nil, logger, err)
	}

	ln, err := listen(cfg)
	if err != nil {
		return finishShutdown(store, srv, httpSrv, wsSrv, logger, err)
	}
	logger.Info("listening", "net", cfg.network, "addr", cfg.addr, "db", cfg.dbPath)

	// Accept blocks until ctx is cancelled (signal), then stops accepting and
	// drains active sessions before returning.
	serveErr := srv.Accept(ctx, ln)
	return finishShutdown(store, srv, httpSrv, wsSrv, logger, serveErr)
}

// finishShutdown runs the ordered teardown after the socket transport has drained:
// drain live WebSocket sessions, stop the HTTP servers, checkpoint the WAL, then
// let the deferred store.Close run. It returns the original serve error (if any).
func finishShutdown(store *sqlite.Store, srv *transport.Server, httpSrv, wsSrv *http.Server, logger *slog.Logger, serveErr error) error {
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if wsSrv != nil {
		// http.Server.Shutdown does not close hijacked WebSocket connections, so
		// drain the live sessions via the transport server first, then stop the
		// HTTP server (whose handlers have now returned).
		if err := srv.Shutdown(sctx); err != nil {
			logger.Warn("websocket drain incomplete", "err", err)
		}
		_ = wsSrv.Shutdown(sctx)
	}
	if httpSrv != nil {
		_ = httpSrv.Shutdown(sctx)
	}
	if err := store.Checkpoint(); err != nil {
		logger.Warn("wal checkpoint failed", "err", err)
	}
	logger.Info("shutdown complete")
	return serveErr
}

func listen(cfg config) (net.Listener, error) {
	switch cfg.network {
	case "unix":
		_ = os.Remove(cfg.addr) // clear a stale socket from a previous run
		ln, err := net.Listen("unix", cfg.addr)
		if err != nil {
			return nil, fmt.Errorf("listen unix %q: %w", cfg.addr, err)
		}
		return ln, nil
	case "tcp":
		// CAVEAT: the transport has no authentication yet (auth is a later phase),
		// so a tcp listener is UNAUTHENTICATED. Bind it to loopback (or a trusted
		// network) until auth lands; do not expose it publicly.
		ln, err := net.Listen("tcp", cfg.addr)
		if err != nil {
			return nil, fmt.Errorf("listen tcp %q: %w", cfg.addr, err)
		}
		return ln, nil
	default:
		return nil, fmt.Errorf("unknown -net %q (want unix, tcp, or stdio)", cfg.network)
	}
}

// startOperability publishes the server's metrics via expvar and serves
// /healthz and /debug/vars on the configured HTTP address. Returns nil if
// disabled (empty address).
func startOperability(cfg config, srv *transport.Server, wm *server.WaveMap, logger *slog.Logger) *http.Server {
	if cfg.httpAddr == "" {
		return nil
	}
	// NOTE: expvar.Publish panics on a duplicate name; this is safe only because
	// run() is called exactly once per process. A second in-process Server (e.g.
	// a run()-level test) would need a guard.
	m := srv.Metrics()
	expvar.Publish("wave_connections_total", expvar.Func(func() any { return m.ConnectionsTotal.Load() }))
	expvar.Publish("wave_active_sessions", expvar.Func(func() any { return m.ActiveSessions.Load() }))
	expvar.Publish("wave_submits_total", expvar.Func(func() any { return m.SubmitsTotal.Load() }))
	expvar.Publish("wave_submit_errors", expvar.Func(func() any { return m.SubmitErrors.Load() }))
	expvar.Publish("wave_loaded_wavelets", expvar.Func(func() any { return wm.Count() }))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.Handle("/debug/vars", expvar.Handler())

	httpSrv := &http.Server{Addr: cfg.httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		// A bind failure (e.g. metrics port in use) is logged but NOT fatal: an
		// operability problem must not take down the real-time wave server.
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("operability http server error", "err", err)
		}
	}()
	logger.Info("operability http listening", "addr", cfg.httpAddr, "paths", "/healthz /debug/vars")
	return httpSrv
}

// startWebSocket serves the browser WebSocket transport at /socket on the
// configured address. Returns (nil, nil) when disabled (empty address), or an
// error if the identity or bind is invalid.
//
// CAVEAT: this is DEV wiring. Every connection is pinned to -ws-user with NO real
// authentication — session-cookie login (auth.Service) is a later phase. Bind -ws
// to loopback, or run it behind a trusted authenticating proxy, until auth lands.
//
// DEV override: a ?user=<address> query parameter overrides the pinned identity,
// allowing integration tests and demos to connect distinct identities (e.g. alice,
// bob) to one server without real auth. Invalid addresses are rejected (the
// connection is refused). This override is intentionally dev-only.
func startWebSocket(cfg config, srv *transport.Server, logger *slog.Logger) (*http.Server, error) {
	if cfg.wsAddr == "" {
		return nil, nil
	}
	devUser, err := id.NewParticipantID(cfg.wsUser)
	if err != nil {
		return nil, fmt.Errorf("invalid -ws-user %q: %w", cfg.wsUser, err)
	}
	identify := func(r *http.Request) (id.ParticipantID, bool) {
		if addr := r.URL.Query().Get("user"); addr != "" {
			p, err := id.NewParticipantID(addr)
			if err != nil {
				return id.ParticipantID{}, false
			}
			return p, true
		}
		return devUser, true
	}

	mux := http.NewServeMux()
	mux.Handle("/socket", srv.WebSocketHandler(identify))
	if cfg.webRoot != "" {
		// Serve the browser client from the same origin as the socket (so the page
		// and the WebSocket share host/port, and later the auth cookie). The more
		// specific "/socket" pattern still wins for the WebSocket upgrade.
		mux.Handle("/", http.FileServer(http.Dir(cfg.webRoot)))
		logger.Info("serving web root", "dir", cfg.webRoot)
	}

	// Bind synchronously so a failure is reported here rather than lost in a
	// goroutine; the WebSocket transport is the browser-facing service.
	ln, err := net.Listen("tcp", cfg.wsAddr)
	if err != nil {
		return nil, fmt.Errorf("listen ws %q: %w", cfg.wsAddr, err)
	}
	httpSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("websocket server error", "err", err)
		}
	}()
	logger.Warn("websocket transport listening (DEV: unauthenticated, pinned identity)",
		"addr", cfg.wsAddr, "path", "/socket", "dev_user", cfg.wsUser)
	return httpSrv, nil
}

func newLogger(cfg config) *slog.Logger {
	var level slog.Level
	switch cfg.logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.logFormat == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// stdioConn adapts the process's stdin/stdout to an io.ReadWriter connection. It
// is intentionally not an io.Closer: the session ends when the peer closes
// stdin (EOF), not by us closing the streams.
type stdioConn struct{}

func (stdioConn) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdioConn) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

// buildVersion reports the module version embedded by the Go toolchain, or
// "devel" when built outside a release (e.g. via `go run`).
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}
