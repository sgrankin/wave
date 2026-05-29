// Command wavectl is the headless client for the Wave server: the test harness
// and demo client used to drive the OT server end to end and watch convergence
// without a browser (docs/architecture/02-porting-plan.md, Phase 5).
//
// It has two modes over the length-prefixed CBOR session protocol
// (internal/transport):
//
//	wavectl serve   --addr <sock> [--db <path>]   # a dev server (many clients)
//	wavectl connect --addr <sock> --as <id> ...   # a client REPL on one wave
//
// "serve" is a thin developer harness over transport.ListenAndServe; the
// production server (config, graceful drain, operability) is cmd/waved. Run two
// "connect" clients against one "serve" and watch their edits converge.
//
// The blip model here is deliberately simplified to flat text (no <body>/<line>
// structure) so the demo stays about the transport and OT, not the conversation
// schema (package conv).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/sgrankin/wave/internal/clock"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/server"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/transport"
	"github.com/sgrankin/wave/internal/waveop"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "connect":
		err = runConnect(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "wavectl: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `wavectl — headless Wave client/harness

usage:
  wavectl serve   --addr <socket> [--db <path>]
  wavectl connect --addr <socket> --as <participant> [--domain d] [--wave w] [--wavelet wl]

connect REPL commands:
  text <blip> <text...>   append text to a blip (creating it if new)
  show                    print the current document
  quit                    disconnect
`)
}

// runServe listens on a unix socket and serves the OT session protocol to each
// connection, sharing one WaveMap, until interrupted.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "/tmp/wave.sock", "unix socket path to listen on")
	dbPath := fs.String("db", ":memory:", "sqlite path (\":memory:\" = ephemeral, non-persistent)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := sqlite.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	wm := server.NewWaveMap(store, clock.System{})

	_ = os.Remove(*addr) // clear a stale socket from a previous run
	ln, err := net.Listen("unix", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer os.Remove(*addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "wavectl serve: listening on %s (db: %s)\n", *addr, *dbPath)
	return transport.ListenAndServe(ctx, ln, wm)
}

// runConnect dials a server, opens one wavelet, renders it on every change, and
// runs a line-oriented command REPL on stdin.
func runConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	addr := fs.String("addr", "/tmp/wave.sock", "unix socket path to dial")
	as := fs.String("as", "", "participant address, e.g. alice@example.com (required)")
	domain := fs.String("domain", "example.com", "wave/wavelet domain")
	wave := fs.String("wave", "w+demo", "wave local id")
	waveletLocal := fs.String("wavelet", "conv+root", "wavelet local id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *as == "" {
		return fmt.Errorf("--as is required")
	}
	author, err := id.NewParticipantID(*as)
	if err != nil {
		return err
	}
	waveID, err := id.NewWaveID(*domain, *wave)
	if err != nil {
		return err
	}
	waveletID, err := id.NewWaveletID(*domain, *waveletLocal)
	if err != nil {
		return err
	}
	name := id.NewWaveletName(waveID, waveletID)

	conn, err := net.Dial("unix", *addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	cl := transport.NewClient(conn, name, author)
	defer cl.Close()
	if err := cl.Open(); err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}

	r := &renderer{cl: cl}
	go func() {
		for range cl.Updates() {
			r.render()
		}
	}()
	r.render()

	fmt.Fprintln(os.Stderr, "connected. commands: text <blip> <text...> | show | quit")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "quit", "q", "exit":
			return nil
		case "show":
			r.render()
		case "text":
			if len(fields) < 3 {
				fmt.Fprintln(os.Stderr, "usage: text <blip> <text...>")
				continue
			}
			if err := appendToBlip(cl, author, fields[1], strings.Join(fields[2:], " ")); err != nil {
				fmt.Fprintf(os.Stderr, "submit failed: %v\n", err)
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n", fields[0])
		}
	}
	return sc.Err()
}

// appendToBlip submits an edit appending text to a blip, creating the blip (as a
// flat-text document) if it does not yet exist. The retain count is computed
// against the same locked snapshot SubmitWith uses to pick the target version,
// so the op is always well-formed for the version it targets; if other clients
// have advanced head, the server transforms the op to head from there.
func appendToBlip(cl *transport.Client, author id.ParticipantID, blipID, text string) error {
	_, err := cl.SubmitWith(func(blip func(string) (op.DocOp, bool)) []waveop.Operation {
		var content op.DocOp
		if cur, ok := blip(blipID); ok {
			content = op.NewDocOp([]op.Component{op.Retain{Count: cur.DocumentLength()}, op.Characters{Text: text}})
		} else {
			content = op.NewDocOp([]op.Component{op.Characters{Text: text}})
		}
		ctx := waveop.Context{Creator: author, Timestamp: 0, VersionIncrement: 1}
		return []waveop.Operation{waveop.WaveletBlipOperation{
			BlipID: blipID,
			BlipOp: waveop.BlipContentOperation{Ctx: ctx, ContentOp: content},
		}}
	})
	return err
}

// renderer prints the wavelet state on demand, serializing output so the
// background update goroutine and the REPL do not garble each other.
type renderer struct {
	cl *transport.Client
	mu sync.Mutex
}

func (r *renderer) render() {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Printf("\n--- %s @ v%d ---\n", r.cl.Version().String(), r.cl.Version().Version())
	ids := r.cl.BlipIDs()
	if len(ids) == 0 {
		fmt.Println("  (empty)")
	}
	for _, blipID := range ids {
		content, _ := r.cl.BlipContent(blipID)
		fmt.Printf("  %s: %q\n", blipID, plainText(content))
	}
}

// plainText extracts the character content of a flat-text blip document.
func plainText(d op.DocOp) string {
	var b strings.Builder
	for _, c := range d.Components() {
		if ch, ok := c.(op.Characters); ok {
			b.WriteString(ch.Text)
		}
	}
	return b.String()
}
