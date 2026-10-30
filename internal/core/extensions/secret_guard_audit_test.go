package extensions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestBuildSecretDecisionEvent_safeFieldsOnly(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}}
	ev := extensions.BuildSecretDecisionEvent(
		secretguard.Meta{
			TraceID: "tr",
			PeerIP:  "203.0.113.9",
			Principal: struct {
				ID          string
				DisplayName string
				Roles       []string
				Claims      map[string]string
			}{},
		},
		call,
		"secrets-guard",
		secretguard.Decision{
			Outcome: secretguard.OutcomeBlock,
			Findings: []secretguard.Finding{{
				SecretRefName: "OPENAI_API_KEY",
			}},
			FailureReason: secret,
		},
		secretguard.QuarantineResultCommitted,
		false,
		"single_user",
		"1",
		"turn-1",
		time.Unix(1, 0).UTC(),
	)
	if ev.RequestedRoute != "openai:gpt-4" || ev.RequestedModel != "gpt-4" {
		t.Fatalf("route/model: %q %q", ev.RequestedRoute, ev.RequestedModel)
	}
	if ev.Action != "block" || ev.BackendDispatched {
		t.Fatalf("action=%q dispatched=%v", ev.Action, ev.BackendDispatched)
	}
	if ev.Source != "remote_addr" {
		t.Fatalf("source=%q", ev.Source)
	}
	for _, s := range testkit.AllSyntheticSecretGuardValues() {
		if s != "" && (ev.Findings[0].SecretRefName == s || ev.Action == s) {
			t.Fatal("event must not embed synthetic secret values")
		}
	}
}

func TestBuildSecretDecisionEvent_clonesFindings(t *testing.T) {
	t.Parallel()
	findings := []secretguard.Finding{{
		SecretRefName:   "OPENAI_API_KEY",
		Aliases:         []string{"OPENAI_API_KEY_2"},
		SourceCategory:  secretguard.SourceCategoryProxyEnv,
		Location:        "messages[0].parts[0].text",
		OccurrenceCount: 1,
		DetectorID:      secretguard.DetectorIDBetterLeaks,
		RuleID:          "github-pat",
		Confidence:      secretguard.ConfidenceHigh,
	}}
	ev := extensions.BuildSecretDecisionEvent(
		secretguard.Meta{TraceID: "tr"},
		&aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}},
		"secrets-guard",
		secretguard.Decision{Outcome: secretguard.OutcomeBlock, Findings: findings},
		secretguard.QuarantineResultCommitted,
		false,
		"single_user",
		"1",
		"turn-1",
		time.Unix(1, 0).UTC(),
	)
	findings[0].SecretRefName = testkit.SyntheticOpenAIAPIKey
	findings[0].Aliases[0] = testkit.SyntheticOpenRouterAPIKey
	if ev.Findings[0].SecretRefName != "OPENAI_API_KEY" {
		t.Fatalf("secret_ref cloned incorrectly: %q", ev.Findings[0].SecretRefName)
	}
	if ev.Findings[0].Aliases[0] != "OPENAI_API_KEY_2" {
		t.Fatalf("aliases cloned incorrectly: %q", ev.Findings[0].Aliases[0])
	}
	if ev.Findings[0].DetectorID != secretguard.DetectorIDBetterLeaks {
		t.Fatalf("detector ID cloned incorrectly: %q", ev.Findings[0].DetectorID)
	}
	if ev.Findings[0].RuleID != "github-pat" || ev.Findings[0].Confidence != secretguard.ConfidenceHigh {
		t.Fatalf("provenance cloned incorrectly: rule=%q confidence=%q", ev.Findings[0].RuleID, ev.Findings[0].Confidence)
	}
}

func TestRunSecretGuardStage_auditObserverFailClosedWrapped(t *testing.T) {
	t.Parallel()
	call := validCall()
	boom := errors.New("audit sink down")
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{id: "ok", decision: secretguard.Decision{Outcome: secretguard.OutcomePass}},
	}, &call, secretguard.Meta{TraceID: "t1"}, secretguard.Services{}, &extensions.SecretGuardAudit{
		Observer: secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
			return boom
		}),
	}, nil)
	if err == nil {
		t.Fatal("expected audit delivery error")
	}
	if !errors.Is(err, extensions.ErrSecretAuditDelivery) {
		t.Fatalf("want ErrSecretAuditDelivery, got %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped boom, got %v", err)
	}
}
