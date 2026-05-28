// Command waved is the Wave server.
//
// This is the project skeleton. The server wiring — storage, the OT apply
// pipeline, transports, and auth — is built out across the phases in
// docs/architecture/02-porting-plan.md. For now it only reports its version.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildVersion())
		return
	}

	fmt.Fprintln(os.Stderr,
		"waved: not runnable yet — server wiring lands in later phases "+
			"(see docs/architecture/02-porting-plan.md)")
	os.Exit(1)
}

// buildVersion reports the module version embedded by the Go toolchain, or
// "devel" when built outside a release (e.g. via `go run`).
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}
