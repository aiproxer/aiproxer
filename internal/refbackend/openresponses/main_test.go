package openresponses

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks across the emulator test suite (server
// handlers, WebSocket sessions, and capture concurrency all close cleanly).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
