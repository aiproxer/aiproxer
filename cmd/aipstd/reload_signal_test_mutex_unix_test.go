//go:build unix

package main

import (
	"sync"
	"testing"
)

// sighupTestMu serializes tests that send process-directed SIGHUP (os.FindProcess).
var sighupTestMu sync.Mutex

func withExclusiveSIGHUP(t *testing.T) {
	t.Helper()
	sighupTestMu.Lock()
	t.Cleanup(sighupTestMu.Unlock)
}
