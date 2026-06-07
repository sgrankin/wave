// Command waved is the Wave server: it serves the OT session protocol
// (internal/transport) over a socket (or stdio), backed by a SQLite delta store,
// with structured logging, an operability HTTP endpoint, and signal-driven
// graceful shutdown.
//
// A browser WebSocket transport is available behind -ws: it serves /socket,
// /login, and /whoami with session-cookie authentication (see startWebSocket and
// docs/architecture/04-auth-model.md). The -auth mode selects dev (trust-any
// login, permissive access) or proxy (a trusted-header provider, strict wavelet
// membership).
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

	"github.com/sgrankin/wave/internal/attachapi"
	"github.com/sgrankin/wave/internal/auth"
	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/profileapi"
	"github.com/sgrankin/wave/internal/queryapi"
	"github.com/sgrankin/wave/internal/search"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/attachments"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

// shutdownGrace bounds the operability server's drain during shutdown.
const shutdownGrace = 5 * time.Second

type config struct {
	network       string // unix | tcp | stdio
	addr          string // unix socket path or host:port
	dbPath        string
	httpAddr      string // operability HTTP address; "" disables
	wsAddr        string // browser WebSocket transport address; "" disables
	webRoot       string // static web root served at / on the -ws server; "" disables
	authMode      string // dev | proxy
	authHeader    string // proxy mode: header carrying the identity
	authDomain    string // default domain appended to a bare username
	sessionTTL    time.Duration
	seed          bool   // server-side-seed a new wavelet's conversation at first open
	attachRoot    string // filesystem root for attachment blobs; "" disables attachments
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
	fs.StringVar(&c.webRoot, "webroot", "", "static web root served at / on the -ws server (\"\" to disable)")
	fs.StringVar(&c.authMode, "auth", "dev", "auth mode for the -ws server: dev (trust-any login, permissive access) | proxy (trusted-header, strict membership)")
	fs.StringVar(&c.authHeader, "auth-header", "X-Authenticated-User", "proxy mode: request header carrying the verified identity")
	fs.StringVar(&c.authDomain, "auth-domain", "example.com", "default domain appended to a bare username at login")
	fs.DurationVar(&c.sessionTTL, "session-ttl", 24*time.Hour, "session cookie lifetime")
	fs.BoolVar(&c.seed, "seed-conversations", true, "server-side-seed a brand-new wavelet's conversation (manifest + root blip) at first open")
	fs.StringVar(&c.attachRoot, "attach-root", "", "filesystem root for attachment blobs on the -ws server (\"\" to disable attachments)")
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
	// The read index backs the browser inbox/search API; keep a reference so the
	// query endpoints can read it (nil disables both the maintenance and the API).
	var idx *search.Index
	if cfg.index {
		idx = search.New(store, logger)
		opts = append(opts, server.WithIndexer(idx))
		logger.Info("index maintenance enabled")
	}
	wm := server.NewWaveMap(store, clock.System{}, opts...)
	srv := &transport.Server{WaveMap: wm, Logger: logger}

	// Browser-path authorization: seed a new wavelet's conversation on first open
	// (open-or-create), and — in proxy/strict mode — gate opens by wavelet
	// membership. These apply to authenticated (WebSocket) connections only; the
	// trusted socket/stdio path is unaffected. Built even when -ws is disabled so
	// the wiring stays in one place; they cost nothing without WebSocket clients.
	authSvc, err := buildAuth(cfg, store)
	if err != nil {
		return finishShutdown(store, srv, nil, nil, logger, err)
	}
	if cfg.seed {
		srv.Seed = func(opener id.ParticipantID) ([]waveop.Operation, error) {
			return conv.SeedConversation(opener, clock.System{}.Now().UnixMilli())
		}
	}
	if cfg.authMode == "proxy" {
		srv.Access = transport.MembershipChecker{WaveMap: wm}
	}

	// Attachment blob store (filesystem); nil disables the attachment API.
	var attachStore *attachments.Store
	if cfg.attachRoot != "" {
		attachStore, err = attachments.New(cfg.attachRoot)
		if err != nil {
			return finishShutdown(store, srv, nil, nil, logger, fmt.Errorf("open attachment store %q: %w", cfg.attachRoot, err))
		}
		logger.Info("attachments enabled", "root", cfg.attachRoot)
	}

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

	wsSrv, err := startWebSocket(cfg, srv, authSvc, idx, store, attachStore, logger)
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
		// CAVEAT: the socket transports (-net unix/tcp/stdio) are intentionally
		// TRUSTED — they serve via ServeConn with no per-connection authentication,
		// for use behind a trust boundary. Authenticated access is the WebSocket
		// path (-ws), not this listener. Bind a tcp socket transport to loopback or
		// a trusted network; do not expose it publicly.
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

// requireSafeAuthBind rejects the trust-any dev login on a non-loopback bind.
// In dev mode the /login endpoint asserts any claimed identity with no proof
// (and sets a cookie on a GET), so exposing it beyond loopback is a complete
// authentication bypass — a safe-by-default guard rather than relying on the
// operator reading a comment. Proxy mode (a trusted-header provider behind an
// authenticating proxy) may bind anywhere. An empty/disabled -ws is fine.
func requireSafeAuthBind(authMode, wsAddr string) error {
	if authMode != "dev" || wsAddr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(wsAddr)
	if err != nil {
		return fmt.Errorf("invalid -ws address %q: %w", wsAddr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("-auth dev mounts a trust-any login and must bind -ws to a loopback "+
		"address (got %q); bind to 127.0.0.1/localhost, or use -auth proxy behind an "+
		"authenticating proxy to bind publicly", wsAddr)
}

// buildAuth constructs the authentication Service for the browser path: a session
// signer keyed by a persisted (restart-stable) signing key, register-on-first-use
// provisioning into the account store, and a provider chain selected by -auth.
// In dev mode the chain is empty (identity is asserted via the dev /login
// endpoint and then carried by the session cookie); in proxy mode it reads a
// trusted-header set by a fronting authenticating proxy.
func buildAuth(cfg config, store *sqlite.Store) (*auth.Service, error) {
	key, err := auth.SigningKey(store)
	if err != nil {
		return nil, err
	}
	sessions := auth.NewSessions(key, cfg.sessionTTL, clock.System{})
	prov := auth.Provisioner{Accounts: store, RegisterOnFirstUse: true}
	var providers []auth.Provider
	switch cfg.authMode {
	case "dev":
		// Cookie-only general chain; the dev /login endpoint asserts identity.
	case "proxy":
		providers = append(providers, auth.TrustedHeader{Header: cfg.authHeader, Domain: cfg.authDomain})
	default:
		return nil, fmt.Errorf("unknown -auth %q (want dev or proxy)", cfg.authMode)
	}
	svc := auth.NewService(sessions, prov, providers...)
	// Dev runs over plain HTTP, so the Secure cookie attribute would prevent the
	// browser from ever sending the cookie. Proxy deployments terminate TLS.
	svc.SecureCookies = cfg.authMode == "proxy"
	return svc, nil
}

// startWebSocket serves the browser transport on the configured address: the
// WebSocket session at /socket and the auth endpoints /login and /whoami, plus
// the static web root. Returns (nil, nil) when disabled (empty address), or an
// error if the bind fails.
//
// /socket and /whoami are mounted behind auth.Service.Middleware, so the session
// cookie is verified before the WebSocket upgrade (a 401 precedes Accept) and the
// authenticated participant is bound to the request (identify reads it from the
// context). The static web root is intentionally NOT authenticated — the app
// shell must load so its JS can call /whoami and redirect to /login when needed.
func startWebSocket(cfg config, srv *transport.Server, authSvc *auth.Service, idx *search.Index, store *sqlite.Store, attachStore *attachments.Store, logger *slog.Logger) (*http.Server, error) {
	if cfg.wsAddr == "" {
		return nil, nil
	}
	if err := requireSafeAuthBind(cfg.authMode, cfg.wsAddr); err != nil {
		return nil, err
	}
	identify := func(r *http.Request) (id.ParticipantID, bool) {
		return auth.ParticipantFrom(r.Context())
	}

	mux := http.NewServeMux()
	mux.Handle("/socket", authSvc.Middleware(srv.WebSocketHandler(identify)))
	mux.Handle("/whoami", authSvc.Middleware(authSvc.WhoAmIHandler()))
	switch cfg.authMode {
	case "dev":
		mux.Handle("/login", authSvc.DevLoginHandler(cfg.authDomain))
	case "proxy":
		mux.Handle("/login", authSvc.LoginHandler())
	}
	// The read-side wave query API (inbox/search) backs the app shell's wave list;
	// it is available only when the index is maintained (-index).
	if idx != nil {
		qh := queryapi.New(idx, queryapi.NewWaveMapReader(srv.WaveMap), store, identify, logger)
		mux.Handle("/api/", authSvc.Middleware(qh.Routes()))
	}
	// Participant profiles (display names) back the client's humanized rosters,
	// inbox, and identity widget. Always available (the account store always
	// exists); mounted at the specific /api/profile(s) paths so they win over the
	// queryapi "/api/" subtree above. Both delegate to the same handler.
	{
		ph := profileapi.New(store, identify, logger)
		routes := authSvc.Middleware(ph.Routes())
		mux.Handle("/api/profiles", routes)
		mux.Handle("/api/profile", routes)
	}
	// Attachment blobs (upload/download/thumbnail), gated by wavelet membership.
	// Both patterns are needed: "/attachments" matches the bare upload path and
	// "/attachments/" matches the {id} sub-paths; both delegate to the same handler.
	if attachStore != nil {
		ah := attachapi.New(attachStore, transport.MembershipChecker{WaveMap: srv.WaveMap}, identify)
		routes := authSvc.Middleware(ah.Routes())
		mux.Handle("/attachments", routes)
		mux.Handle("/attachments/", routes)
	}
	if cfg.webRoot != "" {
		// Serve the browser client from the same origin as the socket (so the page,
		// the WebSocket, and the auth cookie share host/port). The more specific
		// "/socket" etc. patterns still win over "/".
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
	logger.Info("websocket transport listening",
		"addr", cfg.wsAddr, "paths", "/socket /login /whoami", "auth", cfg.authMode,
		"access", map[bool]string{true: "strict", false: "permissive"}[cfg.authMode == "proxy"],
		"seed", cfg.seed)
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
