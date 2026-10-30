package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/logging"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// logSafetySecrets are synthetic, assembly-built credential shapes covering
// common auth forms plus a storage DSN and a control-character injection. They
// are deliberately not real credential formats.
func logSafetySecrets() []struct{ name, text, marker string } {
	return []struct{ name, text, marker string }{
		{"bearer_header", "Authorization: Bearer r3synthetic-bearer-value", "r3synthetic-bearer-value"},
		{"basic_header", "Authorization: Basic r3synthetic-basic-value", "r3synthetic-basic-value"},
		{"api_key_query", "https://backend.invalid/v1?api_key=r3synthetic-query-value", "r3synthetic-query-value"},
		{"x_api_key_header", "X-Api-Key: r3synthetic-header-value", "r3synthetic-header-value"},
		{"password_dsn", "postgres://proxy:r3synthetic-dsn-value@db.invalid:5432/billing", "r3synthetic-dsn-value"},
		{"plain_secret_assignment", "secret=r3synthetic-assignment-value", "r3synthetic-assignment-value"},
		{"control_injection", "upstream failed\nlevel=ERROR r3synthetic-injected-value", "r3synthetic-injected-value"},
	}
}

func newLogSafetyLogger(t *testing.T) (*bytes.Buffer, *slog.Logger) {
	t.Helper()
	return newLogSafetyLoggerAt(t, "info")
}

func newLogSafetyLoggerAt(t *testing.T, level string) (*bytes.Buffer, *slog.Logger) {
	t.Helper()
	var out bytes.Buffer
	logger, err := logging.NewLogger(config.LoggingConfig{Level: level, Format: "json"}, &out)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return &out, logger
}

// TestRecoverablePreOutputLogIsExplicitAndSecretSafe proves the recv-phase
// swallow diagnostic stays explicit (message + stable reason) while never
// echoing untrusted producer error text, including credentials that a
// pattern redactor would not recognize.
func TestRecoverablePreOutputLogIsExplicitAndSecretSafe(t *testing.T) {
	t.Parallel()
	for _, tc := range logSafetySecrets() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, logger := newLogSafetyLogger(t)
			recvTurnFacts{}.logRecoverablePreOutput(context.Background(), logger, "candidate", aipapi.RecoverablePreOutputError(errors.New(tc.text)))
			got := out.String()
			if got == "" || !strings.Contains(got, "recoverable_pre_output_swallowed") || !strings.Contains(got, "\"reason\":\"recoverable_pre_output\"") {
				t.Fatalf("diagnostic lost its explicit message/reason: %q", got)
			}
			if strings.Contains(got, tc.marker) {
				t.Fatalf("diagnostic exposed untrusted credential text %q", tc.marker)
			}
		})
	}
}

// newRecvLogSafetyStream builds the direct retry-stream fixture the recv-phase
// diagnostics run through: a live attempt slot, the resume recovery controller,
// and a real production logger on the response pipeline.
func newRecvLogSafetyStream(t *testing.T, logger *slog.Logger) *retryRecvStream {
	t.Helper()
	sel, err := routing.Parse("openai:gpt-4")
	if err != nil {
		t.Fatalf("routing.Parse: %v", err)
	}
	s := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
			},
			aLegID:  "a-recv-log-safety",
			traceID: "t-recv-log-safety",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 3}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)},
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-recv-log-safety", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: newResponsePipeline(),
	}
	s.responsePipeline.log = logger
	installTestTurnTerminal(s)
	return s
}

// TestRetryRecvCancellationLogIsExplicitAndSecretSafe proves the recv-phase
// cancellation diagnostic stays explicit (message + stable reason) while never
// echoing the untrusted receive error text, including credentials a pattern
// redactor would not recognize. The pre-patch diagnostic carried the truncated
// receive error detail of the same error.
func TestRetryRecvCancellationLogIsExplicitAndSecretSafe(t *testing.T) {
	t.Parallel()
	for _, tc := range logSafetySecrets() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, logger := newLogSafetyLoggerAt(t, "debug")
			recvErr := fmt.Errorf("backend recv failed while dialing: %s: %w", tc.text, context.Canceled)
			if _, err := testRecvError(context.Background(), newRecvLogSafetyStream(t, logger), recvErr); !errors.Is(err, context.Canceled) {
				t.Fatalf("Recv err = %v, want the cancellation returned", err)
			}
			got := out.String()
			if got == "" || !strings.Contains(got, "retry_recv context cancellation") || !strings.Contains(got, "\"reason\":\"context canceled\"") {
				t.Fatalf("diagnostic lost its explicit message/reason: %q", got)
			}
			if strings.Contains(got, tc.marker) {
				t.Fatalf("diagnostic exposed untrusted credential text %q", tc.marker)
			}
		})
	}
}

// classifiedLogSafetyError stands in for the connector adapter's sanitized
// failure: core consumes only the stable machine-readable classification code,
// never the producer message.
type classifiedLogSafetyError struct {
	code    string
	message string
}

func (e classifiedLogSafetyError) Error() string              { return e.code + ": " + e.message }
func (e classifiedLogSafetyError) ClassificationCode() string { return e.code }

// TestRecoverablePreOutputLogReportsStableReasonClass proves the real recv
// swallow path maps the receive error to the stable reason category core logs
// instead of the untrusted message: a lineage conflict and each sanitized
// connector class keep distinct categories, and an unknown machine code stays
// generic. A call site that passed no error would log an empty reason.
func TestRecoverablePreOutputLogReportsStableReasonClass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"lineage_conflict", &execbackend.PromptCacheLineageConflictError{Class: execbackend.PromptCacheLineageConflictALeg}, "lineage_conflict"},
		{"protocol", classifiedLogSafetyError{code: "protocol", message: "unexpected frame"}, "protocol_violation"},
		{"transport", classifiedLogSafetyError{code: "transport", message: "connection reset"}, "transport_error"},
		{"provider", classifiedLogSafetyError{code: "provider", message: "overloaded"}, "provider_error"},
		{"classified_canceled", classifiedLogSafetyError{code: "canceled", message: "stopped"}, "canceled"},
		{"unknown_class", classifiedLogSafetyError{code: "internal_widget", message: "widget"}, "backend_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, logger := newLogSafetyLogger(t)
			_, recvErr := testRecvError(context.Background(), newRecvLogSafetyStream(t, logger), aipapi.RecoverablePreOutputError(tc.err))
			if recvErr == nil {
				t.Fatal("a recoverable pre-output error must be swallowed for a retry, not returned")
			}
			got := out.String()
			if !strings.Contains(got, "recoverable_pre_output_swallowed") || !strings.Contains(got, "\"reason\":\""+tc.want+"\"") {
				t.Fatalf("diagnostic = %q, want the stable reason %q", got, tc.want)
			}
			if raw := tc.err.Error(); strings.Contains(got, raw) {
				t.Fatalf("diagnostic echoed the untrusted error text %q", raw)
			}
		})
	}
}

// TestLineageRejectionLogIsExplicitAndSecretSafe proves the lineage conflict
// rejection stays observable (message + constant conflict class) while never
// echoing the producer-declared identity strings, which can carry injected
// credential text.
func TestLineageRejectionLogIsExplicitAndSecretSafe(t *testing.T) {
	t.Parallel()
	for _, tc := range logSafetySecrets() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			expires := now.Add(time.Hour)
			observation := promptcache.Observation{
				ALegID: "authorization: " + tc.text, BLegID: "b-leg", BackendInstanceID: "connector",
				TargetID: "target", GenerationID: "generation",
				Lifecycle: promptcache.LifecycleSlidingExpiry,
				Timing:    promptcache.Timing{ObservedAt: now, ExpiresAt: &expires},
				Renewable: true, Handle: promptcache.Handle("handle"),
			}
			_, err := execbackend.ReconcilePromptCacheObservationLineage(observation, execbackend.PromptCacheObservationLineage{
				ALegID: "a-leg", BLegID: "b-leg", BackendInstanceID: "connector",
			})
			if !errors.Is(err, execbackend.ErrPromptCacheLineageConflict) {
				t.Fatalf("conflict identity lost: %v", err)
			}
			if strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("conflict error embeds untrusted identity %q", tc.marker)
			}
			if _, ok := execbackend.PromptCacheLineageConflictClassOf(err); !ok {
				t.Fatalf("conflict class missing: %v", err)
			}

			out, logger := newLogSafetyLogger(t)
			pipeline := newResponsePipeline()
			pipeline.log = logger
			pipeline.promptCacheMaintenance = &lineageSpyMaintenance{}
			attempt := newAttemptSession(attemptSessionInput{})
			attempt.bleg.BLegID = "b-leg"
			attempt.cand.Key = "connector:model"
			attempt.cand.Primary.Backend = "connector"
			attempt.cand.Primary.Model = "model"
			attempt.promptCacheSource = &staticObservationSource{observations: []promptcache.Observation{observation}}
			attempt.promptCacheController = &fixedResultController{}
			attempt.discardSidebandState()
			pipeline.commitSuccessfulTurn(recvTurnFacts{aLegID: "a-leg"}, attempt, true)

			got := out.String()
			if got == "" || !strings.Contains(got, "prompt cache observation lineage rejected") || !strings.Contains(got, "\"reason\":\"a_leg_conflict\"") {
				t.Fatalf("lineage rejection lost its explicit message/reason: %q", got)
			}
			if strings.Contains(got, tc.marker) {
				t.Fatalf("lineage rejection exposed untrusted identity %q", tc.marker)
			}
		})
	}
}
