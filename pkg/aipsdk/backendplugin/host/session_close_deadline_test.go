package host_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
)

// Close must not wedge behind a held Execute: it serializes with the active
// stream, then completes its own terminal work once the deadline-bound context
// expires. The connector TCK hit the reverse: a session whose teardown hung
// past the context expiry held the whole module suite until the go test
// timeout fired.
func TestSession_CloseDuringHeldExecuteRespectsDeadline(t *testing.T) {
	t.Parallel()
	plugin := &publicFake{executeStarted: make(chan struct{}), executeRelease: make(chan struct{})}
	conn := startPublicFake(t, plugin)
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "close-deadline", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}

	stream := &publicStream{ctx: context.Background(), frames: []backendplugin.ClientFrame{{Kind: backendplugin.ClientFrameStart, InstanceID: "close-deadline", Invocation: validInvocation()}}}
	executeDone := make(chan error, 1)
	go func() { executeDone <- sess.Execute(stream) }()
	select {
	case <-plugin.executeStarted:
	case <-time.After(time.Second):
		t.Fatal("execute did not reach the configured blocking point")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	closeErr := sess.Close(ctx)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("Close took %v with a 500ms deadline", elapsed)
	}
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want context.DeadlineExceeded", closeErr)
	}

	// The deadline-bound Close cancels the held Execute: it must terminate
	// promptly with a cancellation, not hang and not succeed.
	close(plugin.executeRelease)
	select {
	case err := <-executeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error = %v, want context.Canceled after deadline Close", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not finish after the deadline-bound Close")
	}
}
