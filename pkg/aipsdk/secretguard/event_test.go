package secretguard_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestDecisionEvent_shapeHasNoSecretFields(t *testing.T) {
	t.Parallel()
	ev := secretguard.DecisionEvent{
		Timestamp:           time.Unix(1, 0).UTC(),
		EventID:             "evt-1",
		TraceID:             "tr",
		SessionID:           "sess",
		ALegID:              "aleg",
		TurnID:              "turn",
		PrincipalID:         "u1",
		TenantID:            "t1",
		OrgID:               "o1",
		WorkspaceID:         "w1",
		PeerIP:              "203.0.113.9",
		Source:              "remote_addr",
		FrontendID:          "openai-responses",
		Operation:           "chat",
		AgentIdentityDigest: "digest",
		RequestedRoute:      "openai:gpt-4",
		RequestedModel:      "gpt-4",
		Findings: []secretguard.Finding{{
			SecretRefName:   "OPENAI_API_KEY",
			Aliases:         []string{"OPENAI_API_KEY_2"},
			SourceCategory:  secretguard.SourceCategoryProxyEnv,
			Location:        "messages[0].parts[0]",
			OccurrenceCount: 1,
		}},
		Action:            "block",
		Outcome:           secretguard.OutcomeBlock,
		AccessMode:        "single_user",
		ConfigVersion:     "1",
		QuarantineResult:  secretguard.QuarantineResultCommitted,
		BackendDispatched: false,
		GuardID:           "secrets-guard",
	}
	hay := strings.Join([]string{
		ev.EventID, ev.TraceID, ev.SessionID, ev.PeerIP, ev.RequestedRoute, ev.RequestedModel,
		ev.Findings[0].SecretRefName, ev.Findings[0].Location, string(ev.Outcome), ev.Action,
	}, " ")
	for _, s := range testkit.AllSyntheticSecretGuardValues() {
		if s != "" && strings.Contains(hay, s) {
			t.Fatalf("decision event must not embed synthetic secret values")
		}
	}
}

func TestChainObservers_failClosedStops(t *testing.T) {
	t.Parallel()
	var n int
	boom := errors.New("sink down")
	obs := secretguard.ChainObservers(
		secretguard.AuditFailClosed,
		secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
			n++
			return boom
		}),
		secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
			n++
			return nil
		}),
	)
	err := obs.OnSecretDecision(t.Context(), secretguard.DecisionEvent{})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if n != 1 {
		t.Fatalf("observers invoked=%d want 1", n)
	}
}

func TestChainObservers_bestEffortContinues(t *testing.T) {
	t.Parallel()
	var n int
	obs := secretguard.ChainObservers(
		secretguard.AuditBestEffort,
		secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
			n++
			return errors.New("ignored")
		}),
		secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
			n++
			return nil
		}),
	)
	if err := obs.OnSecretDecision(t.Context(), secretguard.DecisionEvent{}); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("observers invoked=%d want 2", n)
	}
}

type ptrObserver struct {
	n *int
}

func (o *ptrObserver) OnSecretDecision(context.Context, secretguard.DecisionEvent) error {
	if o != nil && o.n != nil {
		(*o.n)++
	}
	return nil
}

func TestChainObservers_skipsTypedNil(t *testing.T) {
	t.Parallel()
	var n int
	var typedNil *ptrObserver
	obs := secretguard.ChainObservers(
		secretguard.AuditFailClosed,
		typedNil,
		secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
			n++
			return nil
		}),
	)
	if err := obs.OnSecretDecision(t.Context(), secretguard.DecisionEvent{}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("observers invoked=%d want 1", n)
	}
}
