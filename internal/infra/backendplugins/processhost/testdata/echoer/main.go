// Command echoer is a process-host test fixture: it writes one known marker
// to stdout and one to stderr, then exits.
//
// The fixture proves that the platform launcher hands the host the child's
// real standard streams. The host drains those streams under the diagnostic
// budget, so a launcher that returned empty readers would let a connector
// flood the host without ever being charged for it.
package main

import (
	"fmt"
	"os"
)

const (
	// StdoutMarker and StderrMarker are the markers the host test looks
	// for. Keep them in sync with drain_flood_test.go.
	StdoutMarker = "aip-processhost-echoer-stdout"
	StderrMarker = "aip-processhost-echoer-stderr"
)

func main() {
	// The markers are the whole output of the fixture, so a failed write
	// ends the fixture like any other write failure.
	_, _ = fmt.Fprintln(os.Stdout, StdoutMarker)
	_, _ = fmt.Fprintln(os.Stderr, StderrMarker)
}
