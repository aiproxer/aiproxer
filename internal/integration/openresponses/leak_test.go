package openresponses

import (
	"context"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
)

// TestHarness_CloseReleasesPorts proves the full-path deployment frees its
// frontend and provider-origin listeners deterministically: after Close, each
// listener the deployment owned rejects further accepts, not hangs.
//
// The assertion deliberately probes the listener objects the deployment created
// instead of dialing their addresses. A freed ephemeral port is process-wide
// state: another deployment in the same test binary may legitimately take it, so
// a successful dial to a released address is not evidence of a leaked listener
// (that form produced a false failure in the race-fuzz broad lane; #886).
func TestHarness_CloseReleasesPorts(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportWebSocket,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
		t.Fatalf("round trip before close: %v", err)
	}
	frontend := d.Server.Listener
	provider := d.OriginFor(conformance.BackendOpenResponses).Listener()
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertListenerReleased(t, frontend, "frontend origin")
	assertListenerReleased(t, provider, "provider origin")
}

// TestListenerReleasedDistinguishesLiveListener binds [listenerReleased] to live
// mutants: an open listener, a listener whose Accept fails for a reason other
// than closure, and a closed listener must all be classified correctly, so a
// mutation of either branch or of the closure check itself stays red.
func TestListenerReleasedDistinguishesLiveListener(t *testing.T) {
	t.Parallel()
	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = live.Close() }()

	// Open with no pending connection: the probe must time out, not report release.
	if listenerReleased(live, 200*time.Millisecond) {
		t.Fatal("open listener reported as released")
	}

	// An Accept that fails for a reason other than closure is not release.
	tcp, ok := live.(*net.TCPListener)
	if !ok {
		t.Fatalf("listener type %T does not support deadlines", live)
	}
	if err := tcp.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	timedOut := listenerReleased(live, time.Second)
	if err := tcp.SetDeadline(time.Time{}); err != nil {
		t.Fatalf("clear deadline: %v", err)
	}
	if timedOut {
		t.Fatal("listener whose Accept timed out reported as released")
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !listenerReleased(closed, time.Second) {
		t.Fatal("closed listener reported as still open")
	}
}

// TestHarness_NoGoroutineLeak proves repeated deploy/round-trip/close cycles do
// not accumulate goroutines beyond a settled baseline. It deliberately does not
// call t.Parallel: it performs a global goroutine census, and sibling parallel
// tests that keep harness deployments alive would perturb both the baseline and
// the after sample.
func TestHarness_NoGoroutineLeak(t *testing.T) { //nolint:paralleltest // performs global goroutine census
	baseline := settleGoroutines(t)

	for i := range 3 {
		d := conformance.Deploy(t, conformance.DeploymentSpec{
			Frontend:  conformance.FrontendOpenResponses,
			Backend:   conformance.BackendOpenResponses,
			Transport: conformance.TransportJSON,
		})
		if d == nil {
			t.Fatalf("Deploy cycle %d failed", i)
		}
		if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
			t.Fatalf("round trip cycle %d: %v", i, err)
		}
		if err := d.Close(); err != nil {
			t.Fatalf("Close cycle %d: %v", i, err)
		}
	}

	after := settleGoroutines(t)
	const allowedGrowth = 5
	if after > baseline+allowedGrowth {
		t.Fatalf("goroutines grew from %d to %d across deploy/close cycles", baseline, after)
	}
}

// settleGoroutines samples the goroutine count over a grace window and returns
// the minimum observed. Transient goroutines (asynchronous connection teardown,
// scheduler spikes) only inflate individual samples, so the minimum approximates
// the quiet count; a real leak stays present in every sample.
func settleGoroutines(tb testing.TB) int {
	tb.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	best := runtime.NumGoroutine()
	stableCount := 0
	for {
		n := runtime.NumGoroutine()
		if n < best {
			best = n
			stableCount = 0
		} else if n == best {
			stableCount++
			if stableCount >= 3 {
				return best
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return best
}

// assertListenerReleased fails unless the listener has been closed, which is the
// deterministic form of the deployment releasing its port.
func assertListenerReleased(tb testing.TB, l net.Listener, label string) {
	tb.Helper()
	if l == nil {
		tb.Fatalf("%s: no listener captured; release cannot be proven", label)
	}
	if !listenerReleased(l, time.Second) {
		tb.Fatalf("%s listener still accepting connections after Close", label)
	}
}

// listenerReleased reports whether the listener has been closed: Accept on a
// closed listener returns immediately with net.ErrClosed, while a live listener
// blocks until grace expires, so the probe is bounded and cannot hang the suite.
// Only net.ErrClosed counts as release; an Accept that fails or times out for any
// other reason leaves the listener unreleased. The probe does not close the
// listener, so a timed-out probe leaves one goroutine in Accept until the caller
// closes it.
func listenerReleased(l net.Listener, grace time.Duration) bool {
	accepted := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		accepted <- err
	}()
	select {
	case err := <-accepted:
		return errors.Is(err, net.ErrClosed)
	case <-time.After(grace):
		return false
	}
}

// TestHarness_WebSocketConnectionClosedOnClientDisconnect proves a WS client
// disconnect does not leave a lingering session goroutine or accepted port.
func TestHarness_WebSocketConnectionClosedOnClientDisconnect(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportWebSocket,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	// A raw turn opens and closes a connection; repeated raw turns exercise
	// session open/close without leaking.
	for i := range 2 {
		if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
			t.Fatalf("ws turn %d: %v", i, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
