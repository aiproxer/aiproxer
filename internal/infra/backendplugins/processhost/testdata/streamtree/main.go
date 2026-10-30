// Command streamtree is a process-host test fixture: it spawns a descendant
// that inherits the connector's standard streams and keeps them open.
//
// A connector that leaves a descendant holding its output handles open must
// not survive the host's termination: the descendant keeps the host's drain
// read open, and the host would wait for it forever.
//
// The fixture records the descendant's process id in "descendant.pid" in its
// working directory, so the host test can observe the descendant itself.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// descendantPIDFile is the file the descendant's process id is written
	// to, in the fixture's working directory.
	descendantPIDFile = "descendant.pid"
	// chunkBytes is the size of one write.
	chunkBytes = 32 << 10
	// chunkInterval is the pause between two writes.
	chunkInterval = 50 * time.Millisecond
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "descendant" {
		// The descendant holds the inherited streams open and does nothing
		// else. Only the host's termination may end it.
		time.Sleep(10 * time.Minute)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "executable:", err)
		os.Exit(1)
	}
	cmd := exec.Command(exe, "descendant")
	// The descendant inherits the connector's output handles, so the host's
	// drain stays open even after the root process is terminated.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "descendant:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(
		filepath.Join(".", descendantPIDFile),
		[]byte(strconv.Itoa(cmd.Process.Pid)),
		0o600,
	); err != nil {
		fmt.Fprintln(os.Stderr, "pid file:", err)
		os.Exit(1)
	}

	chunk := make([]byte, chunkBytes)
	for i := range chunk {
		chunk[i] = 't'
	}
	for {
		_, _ = os.Stdout.Write(chunk)
		time.Sleep(chunkInterval)
	}
}
