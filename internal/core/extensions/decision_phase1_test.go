package extensions_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

func TestValidateDecisionRecordAcceptsLegal(t *testing.T) {
	t.Parallel()
	cases := []policydecision.Record{
		{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectNone},
		{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectNone},
		{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeSkip, Effect: policydecision.EffectNone},
		{Stage: feature.StageIDToolEventReaction, Outcome: policydecision.OutcomeSkip, Effect: policydecision.EffectSwallow},
		{Stage: feature.StageIDCompletionGating, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectReplay},
	}
	for _, r := range cases {
		if err := extensions.ValidateDecisionRecord(r); err != nil {
			t.Fatalf("legal record rejected: %#v -> %v", r, err)
		}
	}
}

func TestValidateDecisionRecordRejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		record policydecision.Record
		want   string
	}{
		{
			name:   "unknown_stage",
			record: policydecision.Record{Stage: "nope", Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectNone},
			want:   "unknown stage",
		},
		{
			name:   "outcome_unknown",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeUnknown, Effect: policydecision.EffectNone},
			want:   "unknown outcome",
		},
		{
			name:   "unknown_effect",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.Effect("bogus")},
			want:   "unknown effect",
		},
		{
			name:   "deny_with_mutate",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectMutate},
			want:   "illegal pair",
		},
		{
			name:   "replay_at_pre_request",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectReplay},
			want:   "illegal pair",
		},
		{
			name:   "swallow_at_completion",
			record: policydecision.Record{Stage: feature.StageIDCompletionGating, Outcome: policydecision.OutcomeSkip, Effect: policydecision.EffectSwallow},
			want:   "illegal pair",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := extensions.ValidateDecisionRecord(c.record)
			if err == nil {
				t.Fatalf("malformed record accepted: %#v", c.record)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q must contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestValidateDecisionRecordEveryLegalStageHasAllowedPair(t *testing.T) {
	t.Parallel()
	for _, stage := range policydecision.LegalDecisionStages() {
		allowed := policydecision.AllowedDecisionsForStage(stage)
		if len(allowed) == 0 {
			t.Fatalf("stage %q has no allowed decisions", stage)
		}
		for _, a := range allowed {
			if err := extensions.ValidateDecisionRecord(policydecision.Record{
				Stage: a.Stage, Outcome: a.Outcome, Effect: a.Effects[0],
			}); err != nil {
				t.Fatalf("stage %q allowed pair (%q,%q) rejected: %v", stage, a.Outcome, a.Effects[0], err)
			}
		}
	}
}

func TestPolicyErrorFromMalformedClassifies(t *testing.T) {
	t.Parallel()
	cause := errors.New("validation")
	err := extensions.PolicyErrorFromMalformed(feature.StageIDPreRequest, "p1", cause)
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("must be malformed policy error")
	}
	if !errors.Is(err, aipapi.ErrPolicyMalformed) {
		t.Fatalf("must wrap ErrPolicyMalformed")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("must preserve cause")
	}
}

func TestPolicyErrorFromProviderFailureFailClosed(t *testing.T) {
	t.Parallel()
	cause := errors.New("boom")
	err := extensions.PolicyErrorFromProviderFailure(feature.StageIDPreRequest, "p1", policydecision.FailureBehaviorFailClosed, cause)
	if err == nil {
		t.Fatalf("fail-closed must return an error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("must be policy failure")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("must preserve cause")
	}
}

func TestPolicyErrorFromProviderFailureFailOpenReturnsNil(t *testing.T) {
	t.Parallel()
	if got := extensions.PolicyErrorFromProviderFailure(feature.StageIDPreRequest, "p1", policydecision.FailureBehaviorFailOpen, errors.New("boom")); got != nil {
		t.Fatalf("fail-open must not surface a failure error, got %v", got)
	}
}

func TestPolicyErrorFromTimeoutFailClosed(t *testing.T) {
	t.Parallel()
	err := extensions.PolicyErrorFromTimeout(feature.StageIDPreRequest, "p1", policydecision.FailureBehaviorFailClosed)
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("fail-closed timeout must be policy failure")
	}
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout message missing: %v", err)
	}
}

func TestPolicyErrorFromTimeoutFailOpenReturnsNil(t *testing.T) {
	t.Parallel()
	if got := extensions.PolicyErrorFromTimeout(feature.StageIDPreRequest, "p1", policydecision.FailureBehaviorFailOpen); got != nil {
		t.Fatalf("fail-open timeout must not surface a failure error, got %v", got)
	}
}

func TestPolicyErrorFromFailClosedClassifies(t *testing.T) {
	t.Parallel()
	err := extensions.PolicyErrorFromFailClosed(feature.StageIDPreRequest, "p1", "policy_denied", "request denied by policy")
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("must be policy denied")
	}
}

func TestIsContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !extensions.IsContextCancellation(ctx, ctx.Err()) {
		t.Fatalf("canceled context must be detected")
	}
	if !extensions.IsContextCancellation(context.Background(), context.Canceled) {
		t.Fatalf("context.Canceled must be detected")
	}
	if extensions.IsContextCancellation(context.Background(), context.DeadlineExceeded) {
		t.Fatalf("DeadlineExceeded with active parent must not be parent cancellation")
	}
	pctx, pcancel := context.WithTimeout(context.Background(), 0)
	defer pcancel()
	<-pctx.Done()
	if !extensions.IsContextCancellation(pctx, context.DeadlineExceeded) {
		t.Fatalf("DeadlineExceeded with expired parent must be parent cancellation")
	}
	if extensions.IsContextCancellation(context.Background(), errors.New("other")) {
		t.Fatalf("unrelated error must not be cancellation")
	}
	if extensions.IsContextCancellation(context.Background(), nil) {
		t.Fatalf("nil must not be cancellation")
	}
}

func TestDecisionProviderTimeoutChildDeadlineErrorClassifiesAsTimedOut(t *testing.T) {
	t.Parallel()
	for i := range 50 {
		res := extensions.RunDecisionProviderWithTimeout(context.Background(), 20*time.Millisecond, func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		if !res.TimedOut {
			t.Fatalf("iteration %d: child deadline error must classify as TimedOut, got %#v", i, res)
		}
		if res.ParentCanceled {
			t.Fatalf("iteration %d: must not be parent canceled when parent active, got %#v", i, res)
		}
		if res.Err != nil {
			t.Fatalf("iteration %d: TimedOut must not carry err, got %v", i, res.Err)
		}
	}
}

func TestTimeoutBudgetSourceDefaults(t *testing.T) {
	t.Parallel()
	var s extensions.DefaultTimeoutBudgetSource
	if got := s.TimeoutFor(feature.StageIDPreRequest, "p1"); got != 0 {
		t.Fatalf("default source must return 0, got %v", got)
	}
	static := extensions.StaticTimeoutBudgetSource{Budget: 50 * time.Millisecond}
	if got := static.TimeoutFor(feature.StageIDPreRequest, "p1"); got != 50*time.Millisecond {
		t.Fatalf("static source must return configured budget, got %v", got)
	}
}

func TestDecisionProviderTimeoutZeroIsSynchronous(t *testing.T) {
	t.Parallel()
	called := false
	res := extensions.RunDecisionProviderWithTimeout(context.Background(), 0, func(ctx context.Context) (string, error) {
		called = true
		return "ok", nil
	})
	if !called {
		t.Fatalf("zero timeout must still call provider")
	}
	if res.Value != "ok" || res.Err != nil || res.TimedOut || res.ParentCanceled {
		t.Fatalf("zero timeout result mismatch: %#v", res)
	}
}

func TestDecisionProviderTimeoutSucceedsBeforeDeadline(t *testing.T) {
	t.Parallel()
	res := extensions.RunDecisionProviderWithTimeout(context.Background(), 200*time.Millisecond, func(ctx context.Context) (string, error) {
		return "ok", nil
	})
	if res.Value != "ok" || res.TimedOut || res.ParentCanceled {
		t.Fatalf("fast provider result mismatch: %#v", res)
	}
}

func TestDecisionProviderTimeoutTimesOut(t *testing.T) {
	t.Parallel()
	res := extensions.RunDecisionProviderWithTimeout(context.Background(), 20*time.Millisecond, func(ctx context.Context) (string, error) {
		select {
		case <-time.After(500 * time.Millisecond):
			return "late", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if !res.TimedOut {
		t.Fatalf("expected timeout, got %#v", res)
	}
	if res.Value != "" {
		t.Fatalf("late value must not be returned, got %q", res.Value)
	}
	if res.ParentCanceled {
		t.Fatalf("parent cancellation must not be set on timeout")
	}
}

func TestDecisionProviderTimeoutParentCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := extensions.RunDecisionProviderWithTimeout(ctx, 200*time.Millisecond, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if !res.ParentCanceled {
		t.Fatalf("parent cancellation must be reported, got %#v", res)
	}
	if res.TimedOut {
		t.Fatalf("parent cancel must not be reported as timeout")
	}
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("err must wrap context.Canceled, got %v", res.Err)
	}
}

func TestDecisionProviderTimeoutParentDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	res := extensions.RunDecisionProviderWithTimeout(ctx, 5*time.Second, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if !res.ParentCanceled {
		t.Fatalf("parent deadline must be reported as parent canceled, got %#v", res)
	}
	if res.TimedOut {
		t.Fatalf("parent deadline must not be reported as provider timeout")
	}
}
