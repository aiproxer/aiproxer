package preflight_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCheck_strictRejectsOnCountFailure(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{err: errors.New("count unavailable")}
	checker := preflight.NewChecker(counter, preflight.Config{Enabled: true, Mode: preflight.ModeStrict})

	decision := checker.Check(context.Background(), preflight.Input{
		Backend: "openai",
		Model:   "gpt-test",
		CallID:  "call-1",
		Call:    testCall(),
		Facts:   modelcatalog.ModelFacts{},
	})

	if decision.Allowed {
		t.Fatalf("Allowed = true, want false")
	}
	if decision.Reason != preflight.ReasonCountUnavailable {
		t.Fatalf("Reason = %q, want %q", decision.Reason, preflight.ReasonCountUnavailable)
	}
	if counter.calls != 1 {
		t.Fatalf("counter calls = %d, want 1", counter.calls)
	}
}

func TestCheck_advisoryAllowsOnCountFailureWithWarning(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{err: errors.New("count unavailable")}
	checker := preflight.NewChecker(counter, preflight.Config{Enabled: true, Mode: preflight.ModeAdvisory})

	decision := checker.Check(context.Background(), preflight.Input{
		Backend: "openai",
		Model:   "gpt-test",
		CallID:  "call-1",
		Call:    testCall(),
		Facts:   modelcatalog.ModelFacts{},
	})

	if !decision.Allowed {
		t.Fatalf("Allowed = false, want true")
	}
	if len(decision.Warnings) != 1 || !strings.Contains(decision.Warnings[0], "count unavailable") {
		t.Fatalf("Warnings = %#v, want count unavailable warning", decision.Warnings)
	}
	if counter.calls != 1 {
		t.Fatalf("counter calls = %d, want 1", counter.calls)
	}
}

func TestCheck_contextLimitExceededRejectsBeforeBackendLikeCall(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{result: app.CountResult{InputTokens: 90}}
	checker := preflight.NewChecker(counter, preflight.Config{Enabled: true, Mode: preflight.ModeStrict})
	maxOut := 20

	decision := checker.Check(context.Background(), preflight.Input{
		Backend:                  "openai",
		Model:                    "gpt-test",
		CallID:                   "call-1",
		Call:                     testCall(),
		RequestedMaxOutputTokens: &maxOut,
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 100},
		},
	})

	if decision.Allowed {
		t.Fatalf("Allowed = true, want false")
	}
	if decision.Reason != preflight.ReasonContextLimitExceeded {
		t.Fatalf("Reason = %q, want %q", decision.Reason, preflight.ReasonContextLimitExceeded)
	}
	if counter.calls != 1 {
		t.Fatalf("counter calls = %d, want 1", counter.calls)
	}
}

func TestCheck_configuredLimitsRejectWithoutModelCatalogFacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfg        preflight.Config
		count      app.CountResult
		requested  *int
		wantReason preflight.Reason
	}{
		{
			name:       "input tokens",
			cfg:        preflight.Config{Enabled: true, Mode: preflight.ModeStrict, MaxInputTokens: 4},
			count:      app.CountResult{InputTokens: 5},
			wantReason: preflight.ReasonInputLimitExceeded,
		},
		{
			name:       "output tokens",
			cfg:        preflight.Config{Enabled: true, Mode: preflight.ModeStrict, MaxOutputTokens: 8},
			count:      app.CountResult{InputTokens: 1},
			requested:  func() *int { v := 9; return &v }(),
			wantReason: preflight.ReasonOutputLimitExceeded,
		},
		{
			name:       "context tokens",
			cfg:        preflight.Config{Enabled: true, Mode: preflight.ModeStrict, MaxContextTokens: 10},
			count:      app.CountResult{InputTokens: 7},
			requested:  func() *int { v := 4; return &v }(),
			wantReason: preflight.ReasonContextLimitExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := preflight.NewChecker(&fakeCounter{result: tt.count}, tt.cfg)

			decision := checker.Check(context.Background(), preflight.Input{
				Backend:                  "openai",
				Model:                    "gpt-test",
				CallID:                   "call-1",
				Call:                     testCall(),
				RequestedMaxOutputTokens: tt.requested,
				Facts:                    modelcatalog.ModelFacts{},
			})

			if decision.Allowed {
				t.Fatalf("Allowed = true, want false")
			}
			if decision.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", decision.Reason, tt.wantReason)
			}
		})
	}
}

func TestCheck_advisoryContextLimitExceededRejects(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{result: app.CountResult{InputTokens: 90}}
	checker := preflight.NewChecker(counter, preflight.Config{Enabled: true, Mode: preflight.ModeAdvisory})
	maxOut := 20

	decision := checker.Check(context.Background(), preflight.Input{
		Backend:                  "openai",
		Model:                    "gpt-test",
		CallID:                   "call-1",
		Call:                     testCall(),
		RequestedMaxOutputTokens: &maxOut,
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 100},
		},
	})

	if decision.Allowed {
		t.Fatalf("Allowed = true, want false")
	}
	if decision.Reason != preflight.ReasonContextLimitExceeded {
		t.Fatalf("Reason = %q, want %q", decision.Reason, preflight.ReasonContextLimitExceeded)
	}
}

func TestCheck_outputClampAdjustsMaxOutputWithinLimit(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{result: app.CountResult{InputTokens: 10}}
	checker := preflight.NewChecker(counter, preflight.Config{
		Enabled:              true,
		Mode:                 preflight.ModeStrict,
		ClampMaxOutputTokens: true,
	})
	maxOut := 200

	decision := checker.Check(context.Background(), preflight.Input{
		Backend:                  "openai",
		Model:                    "gpt-test",
		CallID:                   "call-1",
		Call:                     testCall(),
		RequestedMaxOutputTokens: &maxOut,
		Facts: modelcatalog.ModelFacts{
			OutputLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 64},
		},
	})

	if !decision.Allowed {
		t.Fatalf("Allowed = false, want true: %s", decision.Reason)
	}
	if decision.AdjustedMaxOutputTokens == nil || *decision.AdjustedMaxOutputTokens != 64 {
		t.Fatalf("AdjustedMaxOutputTokens = %v, want 64", decision.AdjustedMaxOutputTokens)
	}
}

func TestCheck_outputClampRunsBeforeContextLimit(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{result: app.CountResult{InputTokens: 90}}
	checker := preflight.NewChecker(counter, preflight.Config{
		Enabled:              true,
		Mode:                 preflight.ModeStrict,
		ClampMaxOutputTokens: true,
	})
	maxOut := 50

	decision := checker.Check(context.Background(), preflight.Input{
		Backend:                  "openai",
		Model:                    "gpt-test",
		CallID:                   "call-1",
		Call:                     testCall(),
		RequestedMaxOutputTokens: &maxOut,
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 100},
			OutputLimit:  modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 10},
		},
	})

	if !decision.Allowed {
		t.Fatalf("Allowed = false, want true: %s", decision.Reason)
	}
	if decision.AdjustedMaxOutputTokens == nil || *decision.AdjustedMaxOutputTokens != 10 {
		t.Fatalf("AdjustedMaxOutputTokens = %v, want 10", decision.AdjustedMaxOutputTokens)
	}
}

func TestCheck_strictRejectsNegativeCountAsUnavailable(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{result: app.CountResult{InputTokens: -1}}
	checker := preflight.NewChecker(counter, preflight.Config{Enabled: true, Mode: preflight.ModeStrict})

	decision := checker.Check(context.Background(), preflight.Input{
		Backend: "openai",
		Model:   "gpt-test",
		CallID:  "call-1",
		Call:    testCall(),
		Facts:   modelcatalog.ModelFacts{},
	})

	if decision.Allowed {
		t.Fatalf("Allowed = true, want false")
	}
	if decision.Reason != preflight.ReasonCountUnavailable {
		t.Fatalf("Reason = %q, want %q", decision.Reason, preflight.ReasonCountUnavailable)
	}
}

func TestCheck_contextCancellationRejectsInStrictAndAdvisory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode preflight.Mode
	}{
		{name: "strict", mode: preflight.ModeStrict},
		{name: "advisory", mode: preflight.ModeAdvisory},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			counter := &fakeCounter{err: context.Canceled}
			checker := preflight.NewChecker(counter, preflight.Config{Enabled: true, Mode: tt.mode})

			decision := checker.Check(context.Background(), preflight.Input{
				Backend: "openai",
				Model:   "gpt-test",
				CallID:  "call-1",
				Call:    testCall(),
				Facts:   modelcatalog.ModelFacts{},
			})

			if decision.Allowed {
				t.Fatalf("Allowed = true, want false")
			}
			if decision.Reason != preflight.ReasonCountUnavailable {
				t.Fatalf("Reason = %q, want %q", decision.Reason, preflight.ReasonCountUnavailable)
			}
			if !errors.Is(decision.Err, context.Canceled) {
				t.Fatalf("Err = %v, want context.Canceled", decision.Err)
			}
		})
	}
}

func TestCheck_unknownLimitsAllowWhenCountSucceeds(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{result: app.CountResult{InputTokens: 999}}
	checker := preflight.NewChecker(counter, preflight.Config{Enabled: true, Mode: preflight.ModeStrict})
	maxOut := 999

	decision := checker.Check(context.Background(), preflight.Input{
		Backend:                  "openai",
		Model:                    "gpt-test",
		CallID:                   "call-1",
		Call:                     testCall(),
		RequestedMaxOutputTokens: &maxOut,
		Facts:                    modelcatalog.ModelFacts{},
	})

	if !decision.Allowed {
		t.Fatalf("Allowed = false, want true: %s", decision.Reason)
	}
	if counter.calls != 1 {
		t.Fatalf("counter calls = %d, want 1", counter.calls)
	}
}

func TestCheck_disabledDoesNotCallCounter(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{err: errors.New("must not be called")}
	checker := preflight.NewChecker(counter, preflight.Config{Enabled: false, Mode: preflight.ModeStrict})

	decision := checker.Check(context.Background(), preflight.Input{
		Backend: "openai",
		Model:   "gpt-test",
		CallID:  "call-1",
		Call:    testCall(),
		Facts:   modelcatalog.ModelFacts{},
	})

	if !decision.Allowed {
		t.Fatalf("Allowed = false, want true: %s", decision.Reason)
	}
	if counter.calls != 0 {
		t.Fatalf("counter calls = %d, want 0", counter.calls)
	}
}

type fakeCounter struct {
	calls  int
	result app.CountResult
	err    error
}

func (f *fakeCounter) CountCall(ctx context.Context, input app.CountCallInput) (app.CountResult, error) {
	_ = ctx
	_ = input
	f.calls++
	return f.result, f.err
}

func testCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
		},
	}
}
