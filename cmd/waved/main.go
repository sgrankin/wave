// Command waved is the Wave server: it serves the OT session protocol
// (internal/transport) over a socket (or stdio), backed by a SQLite delta store,
// with structured logging, an operability HTTP endpoint, and signal-driven
// graceful shutdown.
//
// Authentication, search, and the browser transport are added in later phases
// (docs/architecture/02-porting-plan.md); this is the headless real-time server.
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
	logFormat     string // text | json
	logLevel      string // debug | info | warn | error
	snapshotEvery int    // write a snapshot every N ops (0 disables)
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
	fs.StringVar(&c.logFormat, "log", "text", "log format: text | json")
	fs.StringVar(&c.logLevel, "log-level", "info", "log level: debug | info | warn | error")
	fs.IntVar(&c.snapshotEvery, "snapshot-every", 0, "snapshot a wavelet every N ops (0 = disabled)")
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
		return finishShutdown(store, nil, logger, serveErr)
	}

	httpSrv := startOperability(cfg, srv, wm, logger)

	ln, err := listen(cfg)
	if err != nil {
		return finishShutdown(store, httpSrv, logger, err)
	}
	logger.Info("listening", "net", cfg.network, "addr", cfg.addr, "db", cfg.dbPath)

	// Accept blocks until ctx is cancelled (signal), then stops accepting and
	// drains active sessions before returning.
	serveErr := srv.Accept(ctx, ln)
	return finishShutdown(store, httpSrv, logger, serveErr)
}

// finishShutdown runs the ordered teardown after the transport has drained: stop
// the operability server, checkpoint the WAL, then let the deferred store.Close
// run. It returns the original serve error (if any).
func finishShutdown(store *sqlite.Store, httpSrv *http.Server, logger *slog.Logger, serveErr error) error {
	if httpSrv != nil {
		sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		_ = httpSrv.Shutdown(sctx)
		cancel()
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
