package backendplugin_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// mappedTerminalError is a connector failure that supplies its own safe
// host-visible terminal classification.
type mappedTerminalError struct {
	code      backendplugin.ErrorCode
	retryable bool
}

func (e mappedTerminalError) Error() string { return "mapped terminal failure" }

func (e mappedTerminalError) TerminalPluginError() *backendplugin.PluginError {
	return &backendplugin.PluginError{
		Code:      e.code,
		Message:   "mapped terminal",
		Retryable: e.retryable,
	}
}

func terminalFrame(t *testing.T, stream *fakeExecuteStream) *backendplugin.Terminal {
	t.Helper()
	for _, v := range slices.Backward(stream.sent) {
		if v.Kind == backendplugin.ServerFrameTerminal {
			return v.Terminal
		}
	}
	t.Fatal("no terminal frame was sent")
	return nil
}

// TestForwardExecute_OpenFailureUsesTerminalErrorMapping proves a connector's
// pre-output classification reaches the terminal contract instead of the
// generic internal error.
func TestForwardExecute_OpenFailureUsesTerminalErrorMapping(t *testing.T) {
	t.Parallel()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return nil, mappedTerminalError{code: backendplugin.ErrorCodeResourceExhausted, retryable: false}
	})
	if err == nil {
		t.Fatal("the original failure must still be returned")
	}
	terminal := terminalFrame(t, stream)
	if terminal == nil || terminal.Error == nil {
		t.Fatalf("terminal=%+v, want a mapped error", terminal)
	}
	if terminal.Error.Code != backendplugin.ErrorCodeResourceExhausted || terminal.Error.Retryable {
		t.Fatalf("terminal error=%+v, want resource_exhausted non-retryable", terminal.Error)
	}
}

// TestForwardExecute_StreamFailureBeforeOutputKeepsSafeRetryability proves a
// temporary provider failure remains retryable only before output.
func TestForwardExecute_StreamFailureBeforeOutputKeepsSafeRetryability(t *testing.T) {
	t.Parallel()
	ms := &scriptedManaged{err: mappedTerminalError{code: backendplugin.ErrorCodeProviderTransient, retryable: true}}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	_ = backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	})
	terminal := terminalFrame(t, stream)
	if terminal == nil || terminal.Error == nil {
		t.Fatalf("terminal=%+v, want a mapped error", terminal)
	}
	if terminal.Error.Code != backendplugin.ErrorCodeProviderTransient || !terminal.Error.Retryable || terminal.Error.OutputCommitted {
		t.Fatalf("pre-output terminal error=%+v, want retryable provider_transient without output commit", terminal.Error)
	}
}

// TestForwardExecute_CommittedOutputForcesNonRetryableTerminal proves the
// shared helper never publishes a retryable terminal after client-visible
// output, even when the connector classifier says the condition is temporary.
func TestForwardExecute_CommittedOutputForcesNonRetryableTerminal(t *testing.T) {
	t.Parallel()
	ms := &scriptedManaged{
		events: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "visible"}},
		err:    mappedTerminalError{code: backendplugin.ErrorCodeProviderTransient, retryable: true},
	}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	_ = backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	})
	terminal := terminalFrame(t, stream)
	if terminal == nil || terminal.Error == nil {
		t.Fatalf("terminal=%+v, want a mapped error", terminal)
	}
	if terminal.Error.Code != backendplugin.ErrorCodeProviderTransient {
		t.Fatalf("terminal code=%q, want the connector's failure identity", terminal.Error.Code)
	}
	if terminal.Error.Retryable || !terminal.Error.OutputCommitted {
		t.Fatalf("committed terminal error=%+v, want non-retryable with output committed", terminal.Error)
	}
}
