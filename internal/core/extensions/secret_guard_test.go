package extensions_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// D11: the secret-guard runner must not deep-copy / CloneCall the call for
// mutation detection (unlike request_transform). Guards mutate in place; evidence
// uses Decision.Outcome, not reflect.DeepEqual against a pre-Evaluate snapshot.

func TestRunSecretGuardStage_emptyGuardsNoOp(t *testing.T) {
	t.Parallel()
	call := validCall()
	if _, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, nil, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil); err != nil {
		t.Fatalf("nil guards: %v", err)
	}
	if _, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil); err != nil {
		t.Fatalf("empty guards: %v", err)
	}
}

func TestRunSecretGuardStage_blockReturnsPolicyDeniedSafeMessage(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	call := aipapi.Call{
		ID: "call",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("token=" + secret)},
		}},
	}
	block, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{
			id: "blocker",
			decision: secretguard.Decision{
				Outcome: secretguard.OutcomeBlock,
				Findings: []secretguard.Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					SourceCategory:  secretguard.SourceCategoryProxyEnv,
					Location:        "messages[0].parts[0].text",
					OccurrenceCount: 1,
				}},
			},
		},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err != nil {
		t.Fatalf("block decision must not return stage error: %v", err)
	}
	if block == nil {
		t.Fatal("expected block result")
	}
	err = block.DenialError()
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("want policy denied, got %v", err)
	}
	var pde *aipapi.PolicyDecisionError
	if !errors.As(err, &pde) {
		t.Fatalf("want *PolicyDecisionError, got %T", err)
	}
	if !strings.Contains(pde.ClientMessage, "start a new session") {
		t.Fatalf("client message must instruct new session, got %q", pde.ClientMessage)
	}
	if pde.ReasonCode != extensions.ReasonSecretGuardBlocked {
		t.Fatalf("reason: got %q want %q", pde.ReasonCode, extensions.ReasonSecretGuardBlocked)
	}
	if pde.Stage != feature.StageIDSecretGuard {
		t.Fatalf("stage: got %q", pde.Stage)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(pde.ClientMessage, secret) {
		t.Fatal("block error must not contain synthetic secret value")
	}
}

func TestRunSecretGuardStage_providerErrorIgnoresForgedDecisionData(t *testing.T) {
	t.Parallel()
	call := validCall()
	obs := &runnerEvidenceObserver{}
	ctx := withRunnerEvidence(t.Context(), obs)
	auditCalls := atomic.Int32{}
	audit := &extensions.SecretGuardAudit{
		Observer: secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
			auditCalls.Add(1)
			return nil
		}),
		AccessMode:    "single_user",
		ConfigVersion: "cfg-test",
		TurnID:        "turn-test",
		Now:           func() time.Time { return time.Unix(1234, 0).UTC() },
	}
	metrics := &recordingSecretGuardMetrics{}
	_, err := extensions.RunSecretGuardStage(ctx, nil, nil, []secretguard.Guard{
		sgGuard{
			id:   "forged-failure",
			mode: secretguard.FailClosed,
			decision: secretguard.Decision{
				Outcome:      secretguard.OutcomeBlock,
				ScanLimitHit: true,
				Findings: []secretguard.Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					SourceCategory:  secretguard.SourceCategoryProxyEnv,
					Location:        "messages[0].parts[0].text",
					OccurrenceCount: 1,
				}},
			},
			err: errors.New("provider exploded"),
		},
	}, &call, secretguard.Meta{}, secretguard.Services{}, audit, metrics)
	if err == nil {
		t.Fatal("expected provider failure")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("want policy failure, got %v", err)
	}
	if metrics.decisions != 0 || metrics.matches != 0 || metrics.quarantines != 0 || metrics.scanLimits != 0 {
		t.Fatalf("forged decision data must not drive decision metrics: %+v", metrics)
	}
	if metrics.failures != 1 {
		t.Fatalf("expected one generic failure metric, got %+v", metrics)
	}
	if got := auditCalls.Load(); got != 0 {
		t.Fatalf("forged failure must not reach audit, calls=%d", got)
	}
	recs := obs.snapshot()
	if len(recs) != 1 {
		t.Fatalf("forged failure must emit one generic failure record, got %+v", recs)
	}
	rec := recs[0]
	if rec.Provider.ID != "forged-failure" {
		t.Fatalf("provider id: got %q want %q", rec.Provider.ID, "forged-failure")
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("failure record must be error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ClientCategory != extensions.CategoryFailure {
		t.Fatalf("failure record category: got %q want %q", rec.ClientCategory, extensions.CategoryFailure)
	}
	if rec.ReasonCode != extensions.ReasonSecretGuardFailure {
		t.Fatalf("failure reason: got %q want %q", rec.ReasonCode, extensions.ReasonSecretGuardFailure)
	}
	dump := rec.Provider.ID + rec.ReasonCode + rec.ClientMessage + rec.ClientCategory + rec.Stage
	for _, needle := range []string{"OutcomeBlock", "scan_limit", "proxy_env", "OPENAI_API_KEY"} {
		if strings.Contains(dump, needle) {
			t.Fatalf("generic failure evidence leaked %q: %+v", needle, rec)
		}
	}
}

func TestRunSecretGuardStage_failClosedEvaluateErrorIsPolicyFailure(t *testing.T) {
	t.Parallel()
	call := validCall()
	cause := errors.New("evaluate boom")
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{id: "bad", mode: secretguard.FailClosed, err: cause},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("want policy failure, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("must preserve cause, got %v", err)
	}
}

func TestRunSecretGuardStage_failOpenEvaluateErrorContinues(t *testing.T) {
	t.Parallel()
	call := validCall()
	var seen []string
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{id: "bad", order: 1, mode: secretguard.FailOpen, err: errors.New("boom"), seen: &seen},
		sgGuard{id: "next", order: 2, seen: &seen},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err != nil {
		t.Fatalf("fail-open must continue, got %v", err)
	}
	if len(seen) != 2 || seen[0] != "bad" || seen[1] != "next" {
		t.Fatalf("seen = %#v", seen)
	}
}

func TestRunSecretGuardStage_unknownOutcomeIsPolicyMalformed(t *testing.T) {
	t.Parallel()
	call := validCall()
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{id: "weird", decision: secretguard.Decision{Outcome: secretguard.Outcome("nope")}},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err == nil {
		t.Fatal("expected malformed")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("want policy malformed, got %v", err)
	}
}

func TestRunSecretGuardStage_postRedactionInvalidCallIsPolicyMalformed(t *testing.T) {
	t.Parallel()
	call := validCall()
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{
			id: "clear",
			decision: secretguard.Decision{
				Outcome:       secretguard.OutcomeRedacted,
				MutationCount: 1,
				Findings: []secretguard.Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					SourceCategory:  secretguard.SourceCategoryProxyEnv,
					Location:        "messages[0].parts[0].text",
					OccurrenceCount: 1,
				}},
			},
			mutate: func(c *aipapi.Call) { c.Messages = nil },
		},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err == nil {
		t.Fatal("expected malformed validation error")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("want policy malformed, got %v", err)
	}
}

func TestRunSecretGuardStage_suppressedPluginSkipped(t *testing.T) {
	t.Parallel()
	call := validCall()
	ctx := execctx.WithSuppressedPluginIDs(t.Context(), []string{"skip-me"})
	var seen []string
	_, err := extensions.RunSecretGuardStage(ctx, nil, nil, []secretguard.Guard{
		sgGuard{id: "skip-me", order: 1, seen: &seen, decision: secretguard.Decision{Outcome: secretguard.OutcomeBlock}},
		sgGuard{id: "run-me", order: 2, seen: &seen},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "run-me" {
		t.Fatalf("seen = %#v", seen)
	}
}

func TestRunSecretGuardStage_orderByOrderThenID(t *testing.T) {
	t.Parallel()
	call := validCall()
	var seen []string
	guards := secretguard.MaterializeSorted([]secretguard.Guard{
		sgGuard{id: "b", order: 10, seen: &seen},
		sgGuard{id: "a", order: 10, seen: &seen},
		sgGuard{id: "z", order: 1, seen: &seen},
	})
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, guards, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[0] != "z" || seen[1] != "a" || seen[2] != "b" {
		t.Fatalf("seen = %#v want [z a b]", seen)
	}
}

func TestRunSecretGuardStage_redactMutatesInPlaceWithoutRunnerClone(t *testing.T) {
	t.Parallel()
	// D11: runner must not CloneCall for mutation detection; redact mutates Text in place.
	const original = "hello"
	const redacted = "*****"
	call := aipapi.Call{
		ID: "call",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(original)},
		}},
	}
	textPtr := &call.Messages[0].Parts[0].Text
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{
		sgGuard{
			id: "redactor",
			decision: secretguard.Decision{
				Outcome:       secretguard.OutcomeRedacted,
				MutationCount: 1,
				Findings: []secretguard.Finding{{
					SecretRefName:   "OPENAI_API_KEY",
					Location:        "messages[0].parts[0].text",
					OccurrenceCount: 1,
				}},
			},
			mutate: func(c *aipapi.Call) {
				c.Messages[0].Parts[0].Text = redacted
			},
		},
	}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != redacted {
		t.Fatalf("want redacted text %q got %q", redacted, call.Messages[0].Parts[0].Text)
	}
	if textPtr != &call.Messages[0].Parts[0].Text {
		t.Fatal("runner must not replace call message parts via CloneCall for redact path")
	}
	if vErr := call.Validate(); vErr != nil {
		t.Fatalf("call must remain valid after redact: %v", vErr)
	}
}

func TestRunSecretGuardStage_nilCallAndNilContext(t *testing.T) {
	t.Parallel()
	_, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []secretguard.Guard{sgGuard{id: "g"}}, nil, secretguard.Meta{}, secretguard.Services{}, nil, nil)
	if err == nil || !errors.Is(err, aipapi.ErrInvalidCall) {
		t.Fatalf("nil call: got %v", err)
	}
	call := validCall()
	_, err = extensions.RunSecretGuardStage(nil, nil, nil, []secretguard.Guard{sgGuard{id: "g"}}, &call, secretguard.Meta{}, secretguard.Services{}, nil, nil) //nolint:staticcheck // intentionally nil ctx
	if err == nil || !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("nil ctx: got %v", err)
	}
}

func TestProjectSecretGuardDecision_safeAndLegal(t *testing.T) {
	t.Parallel()
	ctx := preBackendContext(feature.StageIDSecretGuard)
	secret := testkit.SyntheticOpenAIAPIKey

	block := extensions.ProjectSecretGuardDecision(ctx, "g1", secretguard.Decision{
		Outcome: secretguard.OutcomeBlock,
		Findings: []secretguard.Finding{{
			SecretRefName: "OPENAI_API_KEY",
			Location:      "messages[0]",
		}},
		FailureReason: secret,
	})
	if block.Outcome != policydecision.OutcomeDeny || block.Effect != policydecision.EffectNone {
		t.Fatalf("block: outcome=%q effect=%q", block.Outcome, block.Effect)
	}
	if block.BackendAttempted {
		t.Fatal("secret_guard must set BackendAttempted=false")
	}
	if block.ReasonCode != extensions.ReasonSecretGuardBlocked {
		t.Fatalf("block reason %q", block.ReasonCode)
	}
	if !strings.Contains(block.ClientMessage, "start a new session") {
		t.Fatalf("block client message %q", block.ClientMessage)
	}
	if strings.Contains(block.ClientMessage, secret) || strings.Contains(block.ReasonCode, secret) {
		t.Fatal("projection must not leak synthetic secret into ClientMessage/ReasonCode")
	}
	assertLegal(t, block)

	redacted := extensions.ProjectSecretGuardDecision(ctx, "g1", secretguard.Decision{Outcome: secretguard.OutcomeRedacted})
	if redacted.Outcome != policydecision.OutcomeAllow || redacted.Effect != policydecision.EffectMutate {
		t.Fatalf("redacted: outcome=%q effect=%q", redacted.Outcome, redacted.Effect)
	}
	if redacted.ReasonCode != extensions.ReasonSecretGuardRedacted {
		t.Fatalf("redacted reason %q", redacted.ReasonCode)
	}
	assertLegal(t, redacted)

	pass := extensions.ProjectSecretGuardDecision(ctx, "g1", secretguard.Decision{Outcome: secretguard.OutcomePass})
	if pass.Outcome != policydecision.OutcomeAllow || pass.Effect != policydecision.EffectNone {
		t.Fatalf("pass: outcome=%q effect=%q", pass.Outcome, pass.Effect)
	}
	assertLegal(t, pass)

	logDec := extensions.ProjectSecretGuardDecision(ctx, "g1", secretguard.Decision{Outcome: secretguard.OutcomeLog})
	if logDec.Outcome != policydecision.OutcomeAllow || logDec.Effect != policydecision.EffectNone {
		t.Fatalf("log: outcome=%q effect=%q", logDec.Outcome, logDec.Effect)
	}
	if logDec.ReasonCode != extensions.ReasonSecretGuardLog {
		t.Fatalf("log reason %q", logDec.ReasonCode)
	}
	assertLegal(t, logDec)

	unknown := extensions.ProjectSecretGuardDecision(ctx, "g1", secretguard.Decision{Outcome: "???"})
	if unknown.Outcome != policydecision.OutcomeError || unknown.Effect != policydecision.EffectNone {
		t.Fatalf("unknown: outcome=%q effect=%q", unknown.Outcome, unknown.Effect)
	}
	if unknown.ReasonCode != extensions.ReasonSecretGuardMalformed {
		t.Fatalf("unknown reason %q", unknown.ReasonCode)
	}
	assertLegal(t, unknown)
}

func TestRunSecretGuardStage_rejectsMalformedDecisionBeforeAuditAndContinuation(t *testing.T) {
	t.Parallel()

	secret := testkit.SyntheticOpenAIAPIKey
	cases := []struct {
		name     string
		decision secretguard.Decision
	}{
		{
			name: "pass_with_findings",
			decision: secretguard.Decision{
				Outcome:  secretguard.OutcomePass,
				Findings: []secretguard.Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
		},
		{
			name: "pass_with_scan_limit_metadata",
			decision: secretguard.Decision{
				Outcome:       secretguard.OutcomePass,
				ScanLimitHit:  true,
				FailureKind:   "scan_limit",
				FailureReason: "scan_max_bytes exceeded",
			},
		},
		{
			name: "log_with_mutation",
			decision: secretguard.Decision{
				Outcome:       secretguard.OutcomeLog,
				MutationCount: 1,
				Findings:      []secretguard.Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
		},
		{
			name: "redacted_without_mutation",
			decision: secretguard.Decision{
				Outcome:  secretguard.OutcomeRedacted,
				Findings: []secretguard.Finding{{SecretRefName: "OPENAI_API_KEY", Location: "messages[0].parts[0]", OccurrenceCount: 1}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := &runnerEvidenceObserver{}
			ctx := withRunnerEvidence(t.Context(), obs)
			var auditCalls atomic.Int32
			audit := &extensions.SecretGuardAudit{
				Observer: secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
					auditCalls.Add(1)
					return nil
				}),
				AccessMode:    "single_user",
				ConfigVersion: "cfg-malformed",
				TurnID:        "turn-malformed",
				Now:           func() time.Time { return time.Unix(2000, 0).UTC() },
			}
			var metrics malformedSecretGuardMetrics
			seen := make([]string, 0, 2)
			call := validCall()
			_, err := extensions.RunSecretGuardStage(ctx, nil, nil, []secretguard.Guard{
				sgGuard{
					id:       "bad-" + tc.name,
					decision: tc.decision,
					mutate: func(c *aipapi.Call) {
						c.Messages[0].Parts[0].Text = "mutated-" + secret
					},
				},
				sgGuard{
					id:    "next-" + tc.name,
					seen:  &seen,
					order: 10,
				},
			}, &call, secretguard.Meta{}, secretguard.Services{}, audit, &metrics)
			if err == nil {
				t.Fatal("expected malformed policy error")
			}
			if !aipapi.IsPolicyMalformed(err) {
				t.Fatalf("want policy malformed, got %v", err)
			}
			if !strings.Contains(err.Error(), "policy decision was malformed") {
				t.Fatalf("malformed error message must be generic, got %q", err.Error())
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("malformed error leaked synthetic secret: %q", err.Error())
			}
			for _, needle := range []string{"OutcomePass", "OutcomeLog", "OutcomeRedacted", "OutcomeBlock", "scan_limit", "scan_max_bytes exceeded"} {
				if strings.Contains(err.Error(), needle) {
					t.Fatalf("malformed error leaked decision value %q: %q", needle, err.Error())
				}
			}
			if len(seen) != 0 {
				t.Fatalf("malformed decision must stop the chain before continuation, seen=%#v", seen)
			}
			if got := auditCalls.Load(); got != 0 {
				t.Fatalf("malformed decision must not reach audit, calls=%d", got)
			}
			if got := metrics.total(); got != 0 {
				t.Fatalf("malformed decision must not emit metrics, calls=%d", got)
			}
			recs := obs.snapshot()
			if len(recs) != 1 {
				t.Fatalf("malformed decision must emit exactly one malformed evidence record, got %+v", recs)
			}
			rec := recs[0]
			if rec.Provider.ID != "secret_guard_chain" {
				t.Fatalf("malformed evidence provider: %q", rec.Provider.ID)
			}
			if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
				t.Fatalf("malformed evidence must be error/none, got %s/%s", rec.Outcome, rec.Effect)
			}
			if rec.ReasonCode != extensions.ReasonSecretGuardMalformed {
				t.Fatalf("malformed reason: got %q want %q", rec.ReasonCode, extensions.ReasonSecretGuardMalformed)
			}
			dump := rec.Provider.ID + rec.ReasonCode + rec.ClientMessage + rec.Stage + rec.ClientCategory
			if strings.Contains(dump, secret) {
				t.Fatalf("malformed evidence leaked synthetic secret: %+v", rec)
			}
		})
	}
}

type sgGuard struct {
	id       string
	order    int
	mode     secretguard.FailureMode
	decision secretguard.Decision
	err      error
	seen     *[]string
	mutate   func(*aipapi.Call)
}

func (g sgGuard) ID() string { return g.id }
func (g sgGuard) Order() int { return g.order }
func (g sgGuard) FailureMode() secretguard.FailureMode {
	return g.mode
}

func (g sgGuard) Evaluate(_ context.Context, call *aipapi.Call, _ secretguard.Meta, _ secretguard.Services) (secretguard.Decision, error) {
	if g.seen != nil {
		*g.seen = append(*g.seen, g.id)
	}
	if g.mutate != nil {
		g.mutate(call)
	}
	if g.err != nil {
		return secretguard.Decision{}, g.err
	}
	d := g.decision
	if d.Outcome == "" && g.err == nil {
		d.Outcome = secretguard.OutcomePass
	}
	return d, nil
}

type malformedSecretGuardMetrics struct {
	decision atomic.Int32
	match    atomic.Int32
	quar     atomic.Int32
	failure  atomic.Int32
	scan     atomic.Int32
}

func (m *malformedSecretGuardMetrics) IncDecision(_, _, _ string) { m.decision.Add(1) }
func (m *malformedSecretGuardMetrics) IncMatch(_, _, _ string)    { m.match.Add(1) }
func (m *malformedSecretGuardMetrics) IncQuarantine(_, _, _ string) {
	m.quar.Add(1)
}
func (m *malformedSecretGuardMetrics) IncFailure(_, _, _ string) { m.failure.Add(1) }
func (m *malformedSecretGuardMetrics) IncScanLimit(_, _, _ string) {
	m.scan.Add(1)
}

func (m *malformedSecretGuardMetrics) reset() {
	m.decision.Store(0)
	m.match.Store(0)
	m.quar.Store(0)
	m.failure.Store(0)
	m.scan.Store(0)
}

func (m *malformedSecretGuardMetrics) total() int32 {
	return m.decision.Load() + m.match.Load() + m.quar.Load() + m.failure.Load() + m.scan.Load()
}
