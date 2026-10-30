package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestExecute_ProviderFailureReachesTheHostTerminal proves the connector's
// classification is not swallowed by the generic terminal. A pre-output 503 is
// visible as a temporary provider condition and remains retryable.
func TestExecute_ProviderFailureReachesTheHostTerminal(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	provider.status = http.StatusServiceUnavailable
	provider.errorBody = `{"error":{"message":"temporarily unavailable","type":"server_error","code":"temporary_unavailable"}}`
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{"profile_store_path": storePath})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("a provider failure must be returned to the host")
	}
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != responses.ErrorClassTemporaryUnavailable {
		t.Fatalf("returned error=%v, want the temporary provider classification", err)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Error == nil || terminal.Error.Code != backendplugin.ErrorCodeProviderTransient || !terminal.Error.Retryable || terminal.Error.OutputCommitted {
		t.Fatalf("terminal error=%+v, want retryable provider_transient without output commit", terminal.Error)
	}
}

// TestExecute_UsageExhaustionReachesTheHostTerminal proves the documented plan
// limit is a distinct, non-retryable terminal so the host cannot rotate the
// selected registration.
func TestExecute_UsageExhaustionReachesTheHostTerminal(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	provider.status = http.StatusTooManyRequests
	provider.errorBody = `{"error":{"message":"limit","type":"invalid_request_error","code":"subscription_sharing_usage_limit_exceeded"}}`
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{"profile_store_path": storePath})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("usage exhaustion must be returned to the host")
	}
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != responses.ErrorClassUsageExhausted {
		t.Fatalf("returned error=%v, want usage exhaustion", err)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Error == nil || terminal.Error.Code != backendplugin.ErrorCodeResourceExhausted || terminal.Error.Retryable {
		t.Fatalf("terminal error=%+v, want non-retryable resource_exhausted", terminal.Error)
	}
}

// TestExecute_PostOutputStreamFailureReachesTheHostTerminal proves a stream
// failure after client-visible output is terminal and non-retryable, even
// though the connector also classifies it as a protocol violation.
func TestExecute_PostOutputStreamFailureReachesTheHostTerminal(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, midStreamFailureScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{"profile_store_path": storePath})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("a post-output stream failure must be returned to the host")
	}
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != responses.ErrorClassProtocolViolation {
		t.Fatalf("returned error=%v, want the protocol-violation classification", err)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Error == nil || terminal.Error.Code != backendplugin.ErrorCodeProtocolViolation {
		t.Fatalf("terminal error=%+v, want protocol_violation", terminal.Error)
	}
	if terminal.Error.Retryable || !terminal.Error.OutputCommitted {
		t.Fatalf("post-output terminal error=%+v, want non-retryable with output committed", terminal.Error)
	}
}

func terminalFrameOf(t *testing.T, frames []backendplugin.ServerFrame) *backendplugin.Terminal {
	t.Helper()
	for _, frame := range slices.Backward(frames) {
		if frame.Kind == backendplugin.ServerFrameTerminal {
			if frame.Terminal == nil {
				t.Fatal("terminal frame carries no terminal")
			}
			return frame.Terminal
		}
	}
	t.Fatal("no terminal frame")
	return nil
}
