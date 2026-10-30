package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

func TestParallelRaceWinnerPropagatesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-parallel",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventTextDelta, Delta: "winner"},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}
	ex.Backends["backend-2"] = execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
		}),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: " "},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	progress := &recoveryController{
		budget:   &attemptBudget{max: 10},
		ttft:     &ttftBudget{},
		excluded: map[string]struct{}{},
	}
	progress.failures = progress.budget.getFailures()
	progress.budget.failures = progress.failures

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID: "trace-parallel",
				aLegID:  aLegID,
				baseline: aipapi.Call{
					ID:    "request-parallel",
					Route: aipapi.RouteIntent{Selector: "backend-1:model-1!backend-2:model-2"},
					Invocation: aipapi.Invocation{
						Operation:    aipapi.OperationOpenAIChatCompletions,
						DeliveryMode: aipapi.DeliveryModeStreaming,
					},
					Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
				},
			},
			bus: hooks.New(hooks.Config{}),
		},
		routeFacts: routeFacts{
			sel: &routing.Selector{},
			rng: routing.NewSeededRng(1),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{},
	}

	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(context.Background(), req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected parallel race to open a backend")
	}
	if out.ready.Candidate().Primary.Backend != "backend-1" {
		t.Fatalf("winner backend = %q, want backend-1", out.ready.Candidate().Primary.Backend)
	}
	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	if authState.admissionInput.Correlation.TraceID == "" {
		t.Fatal("expected winner authority to have a trace ID")
	}
	if authState.admissionResult.ReservationID != "reservation-parallel" {
		t.Fatalf("winner authority reservation ID = %q, want reservation-parallel", authState.admissionResult.ReservationID)
	}
}
