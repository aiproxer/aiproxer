package backendplugin_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// nestedDeadlineTerminalError is a classified connector failure whose cause is
// a nested operation deadline. The caller's own context is still active.
type nestedDeadlineTerminalError struct {
	cause error
}

func (e nestedDeadlineTerminalError) Error() string { return "nested deadline terminal failure" }
func (e nestedDeadlineTerminalError) Unwrap() error { return e.cause }

func (e nestedDeadlineTerminalError) TerminalPluginError() *backendplugin.PluginError {
	return &backendplugin.PluginError{
		Code:      backendplugin.ErrorCodeProviderTransient,
		Message:   "nested deadline",
		Retryable: true,
	}
}

func sentTerminal(stream *fakeExecuteStream) *backendplugin.Terminal {
	for _, v := range slices.Backward(stream.sent) {
		if v.Kind == backendplugin.ServerFrameTerminal {
			return v.Terminal
		}
	}
	return nil
}

// TestForwardExecute_ClassifiedNestedDeadlineEmitsTerminal proves a nested
// operation deadline on an already-classified connector failure is not
// mistaken for caller withdrawal. The host must receive the recovery terminal.
func TestForwardExecute_ClassifiedNestedDeadlineEmitsTerminal(t *testing.T) {
	t.Parallel()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return nil, nestedDeadlineTerminalError{cause: context.DeadlineExceeded}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("returned error=%v, want the original nested deadline", err)
	}
	terminal := sentTerminal(stream)
	if terminal == nil || terminal.Error == nil {
		t.Fatalf("terminal=%+v, want a mapped failure terminal", terminal)
	}
	if terminal.Error.Code != backendplugin.ErrorCodeProviderTransient || !terminal.Error.Retryable || terminal.Error.OutputCommitted {
		t.Fatalf("terminal error=%+v, want retryable provider_transient without output commit", terminal.Error)
	}
}

// TestForwardExecute_LegacyClassifiedNestedDeadlineEmitsTerminal covers the
// stream path after a successful open, where the same cancellation filter
// previously suppressed the terminal.
func TestForwardExecute_LegacyClassifiedNestedDeadlineEmitsTerminal(t *testing.T) {
	t.Parallel()
	ms := &scriptedManaged{err: nestedDeadlineTerminalError{cause: context.DeadlineExceeded}}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("returned error=%v, want the original nested deadline", err)
	}
	terminal := sentTerminal(stream)
	if terminal == nil || terminal.Error == nil {
		t.Fatalf("terminal=%+v, want a mapped failure terminal", terminal)
	}
	if terminal.Error.Code != backendplugin.ErrorCodeProviderTransient || !terminal.Error.Retryable {
		t.Fatalf("terminal error=%+v, want retryable provider_transient", terminal.Error)
	}
}

// TestForwardExecute_ActualCallerCancellationStaysTerminalFree proves a real
// caller withdrawal is still not converted into a connector failure terminal.
func TestForwardExecute_ActualCallerCancellationStaysTerminalFree(t *testing.T) {
	t.Parallel()
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "canceled", ctx: canceledCtx},
		{name: "deadline", ctx: deadlineCtx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stream := newFakeExecuteStream(tc.ctx, validStartFrame(t))
			err := backendplugin.ForwardExecute(stream, func(ctx context.Context, _ backendplugin.Invocation, _ aipapi.Call) (aipapi.ManagedEventStream, error) {
				return nil, ctx.Err()
			})
			if !errors.Is(err, tc.ctx.Err()) {
				t.Fatalf("returned error=%v, want the caller context error %v", err, tc.ctx.Err())
			}
			if terminal := sentTerminal(stream); terminal != nil {
				t.Fatalf("caller withdrawal must not publish a failure terminal: %+v", terminal)
			}
		})
	}
}
