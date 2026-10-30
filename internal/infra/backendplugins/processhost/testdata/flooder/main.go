// Command flooder is a process-host test fixture: it floods stdout at a
// known byte rate so a host test can prove that a connector which floods the
// host is terminated and reaped.
//
// The fixture exists because the flood rate is part of the test contract. A
// host that produces output as fast as the pipe accepts makes the moment the
// diagnostic budget is exhausted depend on the machine, so the outcome of an
// activation would depend on a race between the flood and the activation.
//
// The fixture never stops on its own. A closed output pipe must not end it,
// so only an external termination can: Unix reports a broken pipe as a signal
// and Windows reports it as a write error, and the fixture survives both.
//
// The fixture is a plain Go program, so the flood rate is identical on every
// platform and the test does not depend on a platform-specific binary.
package main

import (
	"os"
	"time"
)

const (
	// chunkBytes is the size of one write.
	chunkBytes = 32 << 10
	// chunkInterval is the pause between two writes. Together with
	// chunkBytes it fixes the flood rate at 640 KiB/s.
	chunkInterval = 50 * time.Millisecond
)

func main() {
	// A writer to a closed pipe may be killed by a signal. Ignoring it keeps
	// a closed output pipe from ending this process, so only an external
	// termination can.
	ignoreBrokenPipeSignal()
	chunk := make([]byte, chunkBytes)
	for i := range chunk {
		chunk[i] = 'f'
	}
	for {
		// A failed write means the reader is gone. Keep the process alive:
		// the host, not a broken pipe, must end it.
		_, _ = os.Stdout.Write(chunk)
		time.Sleep(chunkInterval)
	}
}
