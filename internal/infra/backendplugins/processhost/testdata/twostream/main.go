// Command twostream is a process-host test fixture: it writes a bounded
// payload to stdout and to stderr, then stays alive.
//
// The host drains both streams under one diagnostic budget. The fixture
// exists to prove that the two streams are charged against the same
// allowance: the payload is larger than the budget on the two streams
// together and smaller than the budget on either stream alone. A host that
// charged the streams separately would never exhaust the allowance, and the
// connector would never be terminated.
//
// The fixture never stops on its own, so the connector can only have been
// terminated by the host.
package main

import (
	"os"
	"time"
)

const (
	// stdoutBytes and stderrBytes are the bounded payloads the fixture
	// writes to the two streams.
	stdoutBytes = 60 << 10
	stderrBytes = 60 << 10
)

func main() {
	stdout := make([]byte, stdoutBytes)
	stderr := make([]byte, stderrBytes)
	for i := range stdout {
		stdout[i] = 'o'
		stderr[i] = 'e'
	}
	// A failed write means the reader is gone. Keep the process alive: the
	// host, not a broken pipe, must end it.
	_, _ = os.Stdout.Write(stdout)
	_, _ = os.Stderr.Write(stderr)
	for {
		time.Sleep(time.Hour)
	}
}
