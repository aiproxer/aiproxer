package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// failingRefresher makes credential acquisition return one chosen typed
// refresh outcome without provider I/O.
type failingRefresher struct {
	err error
}

func (r failingRefresher) Refresh(context.Context, string) (siwc.ProfileRecord, error) {
	return siwc.ProfileRecord{}, r.err
}

func configureForRefreshFailure(t *testing.T, provider *planReference, refreshErr error) *instance {
	t.Helper()
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{"profile_store_path": storePath})
	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	manager, err := siwc.NewManager(siwc.ManagerConfig{
		Profiles:  store,
		Refresher: failingRefresher{err: refreshErr},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	inst.execution.authority = &selectedProfiles{store: store, manager: manager, pinned: record.ID}
	return inst
}

// TestExecute_TransientRefreshFailurePublishesItsClassification covers the
// nested-deadline case: the refresh client's own timeout wraps
// context.DeadlineExceeded while the caller context is still active. The host
// must receive the typed transient terminal, not a silent cancellation.
func TestExecute_TransientRefreshFailurePublishesItsClassification(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	refreshErr := siwc.NewRefreshError(siwc.RefreshClassTransient, 0, "", "", context.DeadlineExceeded)
	inst := configureForRefreshFailure(t, provider, refreshErr)

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error=%v, want the original nested deadline", err)
	}
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != responses.ErrorClassRefreshTransient || !classified.Retryable {
		t.Fatalf("Execute error=%v, want a retryable refresh-transient classification", err)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Error == nil || terminal.Error.Code != backendplugin.ErrorCodeProviderTransient || !terminal.Error.Retryable || terminal.Error.OutputCommitted {
		t.Fatalf("terminal error=%+v, want retryable provider_transient without output commit", terminal.Error)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("a credential failure reached the provider: %d request(s)", len(requests))
	}
}

// TestExecute_TerminalRefreshFailurePublishesReauthorization covers the
// terminal refresh outcome: it must be a non-retryable unauthenticated
// terminal, not the generic internal failure.
func TestExecute_TerminalRefreshFailurePublishesReauthorization(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	refreshErr := siwc.NewRefreshError(siwc.RefreshClassTerminal, http.StatusUnauthorized, "invalid_grant", "", nil)
	inst := configureForRefreshFailure(t, provider, refreshErr)

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	var classified *responses.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != responses.ErrorClassRefreshTerminal || classified.Retryable {
		t.Fatalf("Execute error=%v, want a non-retryable refresh-terminal classification", err)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Error == nil || terminal.Error.Code != backendplugin.ErrorCodeUnauthenticated || terminal.Error.Retryable || terminal.Error.OutputCommitted {
		t.Fatalf("terminal error=%+v, want non-retryable unauthenticated without output commit", terminal.Error)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("a credential failure reached the provider: %d request(s)", len(requests))
	}
}
