// Package loopback reserves loopback addresses for tests whose server must be
// told its address before it binds.
//
// A plain net.Listen("127.0.0.1:0") followed by Close returns a port the kernel
// may hand to any other ":0" reservation before the caller's server binds it.
// Under parallel load that other reservation can hold the port, and the server
// fails with "bind: address already in use" for a reason the code under test
// never caused. Reserving from a harness-owned window below the kernel's
// ephemeral range removes that reassignment: a concurrent ":0" caller can never
// be handed one of these ports.
package loopback

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The harness owns a window of windowSize ports below the kernel ephemeral
// range, split into windowSlices equal slices. Each test binary takes one slice,
// selected by its process id, so two concurrently running binaries (for example
// two packages in one "go test ./..." invocation) do not walk the same ports.
const (
	windowSize   = 10000
	windowSlices = 32
	sliceSize    = windowSize / windowSlices
)

var (
	windowOnce sync.Once
	windowBase int
	windowSpan int
	cursor     atomic.Uint32
	start      uint32
)

// Reserve returns a loopback address from the harness-owned window and releases
// it, so the caller's server can bind it. The window sits outside the kernel's
// ephemeral range, so a concurrent net.Listen("127.0.0.1:0") in this or any
// other process can never be handed one of its ports between the reservation
// and the bind. Concurrent test binaries are separated by process id, so they
// do not compete for the same slice.
func Reserve(t *testing.T) string {
	t.Helper()
	addr, err := reserve()
	if err != nil {
		t.Fatalf("loopback: %v", err)
	}
	return addr
}

// reserve is the error-returning form, so concurrent callers can report a
// failure without calling t.Fatalf from a goroutine.
func reserve() (string, error) {
	base, span := window()
	first := int(cursor.Add(1)-1+start) % span
	for offset := range span {
		port := base + (first+offset)%span
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		addr := listener.Addr().String()
		if err := listener.Close(); err != nil {
			return "", fmt.Errorf("release %s: %w", addr, err)
		}
		return addr, nil
	}
	return "", fmt.Errorf("no free port in window %d-%d", base, base+span-1)
}

// window reports the port window harness-owned listeners bind from. It prefers
// the block immediately below the kernel's ephemeral range and falls back to a
// fixed window when the platform does not publish the range.
func window() (base, span int) {
	windowOnce.Do(func() {
		windowBase, windowSpan = 20000, windowSize
		low, high, ok := kernelEphemeralPortRange()
		if ok {
			switch {
			case low-windowSize >= 1024:
				windowBase = low - windowSize
			case 65535-high >= windowSize:
				windowBase = high + 1
			}
		}
		// Two concurrent test binaries draw from the same window unless their
		// slices differ; keying the slice by process id keeps them apart without
		// a shared lock. A collision now requires matching process-id residues,
		// and the ports still stay outside the kernel's ephemeral range.
		windowBase += (os.Getpid() % windowSlices) * sliceSize
		windowSpan = sliceSize
		start = uint32(time.Now().UnixNano() % int64(sliceSize))
	})
	return windowBase, windowSpan
}

// kernelEphemeralPortRange reports the ports the kernel hands to a
// net.Listen("tcp", "127.0.0.1:0") caller. Only Linux publishes the range.
func kernelEphemeralPortRange() (low, high int, ok bool) {
	if runtime.GOOS != "linux" {
		return 0, 0, false
	}
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return 0, 0, false
	}
	low, lowErr := strconv.Atoi(fields[0])
	high, highErr := strconv.Atoi(fields[1])
	if lowErr != nil || highErr != nil || low <= 1024 || low >= high || high > 65535 {
		return 0, 0, false
	}
	return low, high, true
}
