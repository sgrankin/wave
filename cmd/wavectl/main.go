// Command wavectl is the headless stdio client for the Wave server: the test
// harness and demo client used to drive the OT server end-to-end and validate
// convergence without a browser (docs/architecture/02-porting-plan.md, Phase 5).
//
// This is the project skeleton; the stdio session protocol is implemented in a
// later phase.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr,
		"wavectl: not implemented yet — stdio client lands in Phase 5 "+
			"(see docs/architecture/02-porting-plan.md)")
	os.Exit(1)
}
