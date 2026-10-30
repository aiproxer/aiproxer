package reasoningpreservation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/require"
)

// wrappedBudgetStore is a CompressionStore decorator that returns a wrapped
// *BudgetError from one or both budget-checked mutations. A durable or
// remote store implementation may add context with %w; classification by
// errors.Is(err, ErrCompressionBudgetExceeded) must still hold, and the
// telemetry mapping must still recover the specific BudgetKind.
type wrappedBudgetStore struct {
	reasoningpreservation.CompressionStore
	reserveErr error
	attachErr  error
}

func (s wrappedBudgetStore) ReserveCompression(context.Context, reasoningpreservation.SessionPartition, string, [32]byte, string, [32]byte, [32]byte) (reasoningpreservation.CompressionClaim, error) {
	if s.reserveErr != nil {
		return reasoningpreservation.CompressionClaim{}, s.reserveErr
	}
	return reasoningpreservation.CompressionClaim{}, fmt.Errorf("wrappedBudgetStore: reserve not configured")
}

func (s wrappedBudgetStore) AttachSurrogate(context.Context, reasoningpreservation.CompressionClaim, auxiliary.JobID, reasoningpreservation.ReasoningSurrogate) error {
	if s.attachErr != nil {
		return s.attachErr
	}
	return fmt.Errorf("wrappedBudgetStore: attach not configured")
}

func wrappedBudgetError(kind reasoningpreservation.BudgetKind) error {
	return fmt.Errorf("compression store: %w", &reasoningpreservation.BudgetError{Kind: kind, Limit: 1, Current: 2})
}

func TestReservation_TelemetryClassifiesWrappedBudgetError(t *testing.T) {
	t.Parallel()
	cfg := compressionObserverConfig(t)
	tel := reasoningpreservation.NewTelemetry()
	store := wrappedBudgetStore{reserveErr: wrappedBudgetError(reasoningpreservation.BudgetPendingPerSession)}
	hook := reasoningpreservation.NewCompressionReservationHookWithTelemetry(cfg, store, nil, tel)
	// Source bytes clear MinSourceBytes so the store's budget verdict is reached.
	corr := reasoningpreservation.PostAppendCorrelation{
		Partition:           reasoningpreservation.NewSessionPartition("sess-wrapped-budget"),
		ArtifactID:          "art-wrapped-budget",
		Anchor:              [32]byte{1},
		OriginalDigest:      [32]byte{2},
		SemanticDigest:      [32]byte{3},
		EgressPolicyRefHash: [32]byte{4},
		SourceBytes:         8192,
		PolicyRevision:      cfg.Compression.EgressPolicyRef,
	}
	require.NoError(t, hook(context.Background(), corr))
	snapshot := tel.Snapshot()
	require.Equal(t, int64(1), snapshot[reasoningpreservation.OutcomeBudgetPendingPerSession],
		"a wrapped budget error must still map to its specific pending-budget outcome")
	require.Zero(t, snapshot[reasoningpreservation.OutcomeReservationBudgetExceeded])
}

func TestAdoption_TelemetryClassifiesWrappedBudgetError(t *testing.T) {
	t.Parallel()
	cfg := pollTestConfig(t)
	cs := storeForSubmit(t, cfg)
	tel := reasoningpreservation.NewTelemetry()
	p := reasoningpreservation.NewSessionPartition("sess-adopt-wrapped-budget")
	art := longArtifact("art-adopt-wrapped-budget", strings.Repeat("a", 100))
	setupAdoptionPendingCorrect(t, cs, p, art, cfg)
	rawObj := map[string]any{"schema_version": 1, "segments": []map[string]any{{"index": 0, "text": strings.Repeat("b", 10)}}}
	raw, err := json.Marshal(rawObj)
	require.NoError(t, err)
	var collected aipapi.Collected
	collected.Text.WriteString(string(raw))
	collected.FinishReceived = true
	poller := &pollTestPoller{result: auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: collected}}
	svc := reasoningpreservation.CompressionServices{Client: poller, Poller: poller, EgressPolicy: pollTestEgress{}, Sanitizer: pollTestSan{}}
	store := wrappedBudgetStore{
		CompressionStore: cs,
		attachErr:        wrappedBudgetError(reasoningpreservation.BudgetSurrogatePerSession),
	}
	stage := reasoningpreservation.NewDecoderAdoptionStage(cfg, store, svc, tel)
	xform := reasoningpreservation.NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg, store, svc, reasoningpreservation.CompanionPolicy{}, stage, tel)
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("visible answer")}}}}
	_, err = xform.HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "be", Model: "m", ReplaySupport: pollTestSupport,
		Session: session.SessionView{AuthoritativeSessionID: "sess-adopt-wrapped-budget"},
	}, request.Services{})
	require.NoError(t, err, "adoption failures are fail-open for the original turn")
	snapshot := tel.Snapshot()
	require.Equal(t, int64(1), snapshot[reasoningpreservation.OutcomeBudgetSurrogatePerSession],
		"a wrapped budget error must still map to its specific surrogate-budget outcome")
	require.Zero(t, snapshot[reasoningpreservation.OutcomeReservationBudgetExceeded])
}
