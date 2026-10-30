package loopback

import (
	"fmt"
	"net"
	"sync"
	"testing"
)

// TestReserveStaysOutsideTheEphemeralRange proves the harness never competes
// for a port the kernel hands out with ":0". A concurrent reservation that is
// handed one of these ports and keeps it would otherwise make the caller's
// server fail to bind.
func TestReserveStaysOutsideTheEphemeralRange(t *testing.T) {
	low, high, ok := kernelEphemeralPortRange()
	if !ok {
		t.Skip("kernel ephemeral port range is not published on this platform")
	}
	for range 16 {
		addr := Reserve(t)
		_, portText, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("split %q: %v", addr, err)
		}
		port := 0
		if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
			t.Fatalf("parse port %q: %v", portText, err)
		}
		if port >= low && port <= high {
			t.Fatalf("reserved %s inside the kernel ephemeral range %d-%d", addr, low, high)
		}
	}
}

// TestReserveIsDistinctAndSimultaneouslyBindable proves the cursor is atomic and
// the window is large enough for concurrent callers: every reservation is
// distinct and all of them can be bound at the same time.
func TestReserveIsDistinctAndSimultaneouslyBindable(t *testing.T) {
	// Stay well inside one process slice (sliceSize) across repeated -count runs.
	const callers = 16
	addrs := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			addr, err := reserve()
			addrs[i], errs[i] = addr, err
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, callers)
	listeners := make([]net.Listener, 0, callers)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for i, addr := range addrs {
		if errs[i] != nil {
			t.Fatalf("reserve %d: %v", i, errs[i])
		}
		if seen[addr] {
			t.Fatalf("reserved %s twice", addr)
		}
		seen[addr] = true
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("bind reserved %s: %v", addr, err)
		}
		listeners = append(listeners, listener)
	}
}
