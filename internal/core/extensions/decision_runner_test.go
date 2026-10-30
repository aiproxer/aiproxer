package extensions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func runnerTestCall(text string) *aipapi.Call {
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(text)},
		}},
	}
}

func runnerTestText(c *aipapi.Call) string {
	if c == nil || len(c.Messages) == 0 || len(c.Messages[0].Parts) == 0 {
		return ""
	}
	return c.Messages[0].Parts[0].Text
}

func setRunnerTestText(c *aipapi.Call, text string) {
	c.Messages[0].Parts[0].Text = text
}

// TestRunBoundedCall_CloneSnapshotSurvivesLateTimedOutProvider asserts the mutable
// helper snapshots the live call before launching the bounded goroutine. After the
// helper times out, the live call is changed and the still-running provider is released;
// it must observe the pre-timeout clone, not the later live value.
func TestRunBoundedCall_CloneSnapshotSurvivesLateTimedOutProvider(t *testing.T) {
	t.Parallel()
	ev := &DecisionEvidence{TimeoutBudget: StaticTimeoutBudgetSource{Budget: 30 * time.Millisecond}}
	call := runnerTestCall("before")
	release := make(chan struct{})
	observed := make(chan string, 1)

	result := make(chan boundedProviderResult[struct{}], 1)
	go func() {
		result <- runBoundedCall(t.Context(), ev, "stage", "prov", call, func(ctx context.Context, target *aipapi.Call) (struct{}, error) {
			<-release
			observed <- runnerTestText(target)
			return struct{}{}, ctx.Err()
		})
	}()

	res := <-result
	if !res.TimedOut {
		t.Fatalf("expected timeout, got %+v", res)
	}
	setRunnerTestText(call, "after")
	close(release)
	if got := <-observed; got != "before" {
		t.Fatalf("late provider must observe prepare-time clone, got %q want before", got)
	}
	if got := runnerTestText(call); got != "after" {
		t.Fatalf("live call should keep post-timeout value, got %q", got)
	}
}

// TestRunBoundedCall_CommitOnlyOnSuccessAndNotOnTimeout asserts clone commits happen
// only when the bounded provider returns before timeout/parent cancellation.
func TestRunBoundedCall_CommitOnlyOnSuccessAndNotOnTimeout(t *testing.T) {
	t.Parallel()

	t.Run("success commits mutated clone", func(t *testing.T) {
		t.Parallel()
		ev := &DecisionEvidence{TimeoutBudget: StaticTimeoutBudgetSource{Budget: 5 * time.Second}}
		call := runnerTestCall("before")
		res := runBoundedCall(t.Context(), ev, "stage", "prov", call, func(_ context.Context, target *aipapi.Call) (struct{}, error) {
			setRunnerTestText(target, "bounded")
			return struct{}{}, nil
		})
		if res.TimedOut || res.ParentCanceled {
			t.Fatalf("expected success, got %+v", res)
		}
		if got := runnerTestText(call); got != "bounded" {
			t.Fatalf("success must commit bounded mutation: got %q want bounded", got)
		}
	})

	t.Run("timeout does not commit", func(t *testing.T) {
		t.Parallel()
		ev := &DecisionEvidence{TimeoutBudget: StaticTimeoutBudgetSource{Budget: 20 * time.Millisecond}}
		call := runnerTestCall("before")
		release := make(chan struct{})
		res := runBoundedCall(t.Context(), ev, "stage", "prov", call, func(ctx context.Context, target *aipapi.Call) (struct{}, error) {
			<-release
			setRunnerTestText(target, "late")
			return struct{}{}, ctx.Err()
		})
		if !res.TimedOut {
			t.Fatalf("expected timeout, got %+v", res)
		}
		if got := runnerTestText(call); got != "before" {
			t.Fatalf("timeout must not commit clone: got %q want before", got)
		}
		close(release)
	})

	t.Run("parent cancel does not commit", func(t *testing.T) {
		t.Parallel()
		ev := &DecisionEvidence{TimeoutBudget: StaticTimeoutBudgetSource{Budget: 5 * time.Second}}
		parent, cancel := context.WithCancel(t.Context())
		cancel()
		call := runnerTestCall("before")
		res := runBoundedCall(parent, ev, "stage", "prov", call, func(ctx context.Context, target *aipapi.Call) (struct{}, error) {
			<-ctx.Done()
			setRunnerTestText(target, "canceled")
			return struct{}{}, ctx.Err()
		})
		if !res.ParentCanceled {
			t.Fatalf("expected parent cancellation, got %+v", res)
		}
		if got := runnerTestText(call); got != "before" {
			t.Fatalf("parent cancellation must not commit clone: got %q want before", got)
		}
	})
}

func TestRunDecisionProviderWithDeadlineGuarded_parentCanceledSkipsCall(t *testing.T) {
	t.Parallel()
	guard := NewProviderTimeoutGuard()
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	started := make(chan struct{}, 1)

	res := RunDecisionProviderWithDeadlineGuarded(parent, time.Now().Add(time.Second), guard, "stage", "prov", func(context.Context) (string, error) {
		started <- struct{}{}
		return "unexpected", nil
	})
	if !res.ParentCanceled {
		t.Fatalf("expected parent cancellation, got %#v", res)
	}
	if res.TimedOut || res.GuardRejected || res.ProviderStillRunning {
		t.Fatalf("parent cancellation must not look like timeout/guard rejection, got %#v", res)
	}
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("err must wrap context.Canceled, got %v", res.Err)
	}
	select {
	case <-started:
		t.Fatal("canceled parent must not launch provider goroutine")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRunBoundedCall_ZeroTimeoutSkipsCloneAndDeadline asserts the zero-timeout legacy
// path runs directly against the live call with no child context, goroutine, or deadline.
func TestRunBoundedCall_ZeroTimeoutSkipsCloneAndDeadline(t *testing.T) {
	t.Parallel()
	ev := &DecisionEvidence{TimeoutBudget: DefaultTimeoutBudgetSource{}}
	call := runnerTestCall("before")
	var directCtx context.Context
	res := runBoundedCall(t.Context(), ev, "stage", "prov", call, func(ctx context.Context, target *aipapi.Call) (struct{}, error) {
		directCtx = ctx
		if target != call {
			t.Fatal("zero-timeout path must run against live call")
		}
		setRunnerTestText(target, "direct")
		return struct{}{}, nil
	})
	if res.UsedTimeout {
		t.Fatal("zero timeout must not set UsedTimeout")
	}
	if !res.Deadline.IsZero() {
		t.Fatalf("zero timeout result Deadline must be zero, got %v", res.Deadline)
	}
	if res.IterCtx != directCtx {
		t.Fatal("zero timeout IterCtx must be the ctx passed to provider")
	}
	if _, hasDeadline := directCtx.Deadline(); hasDeadline {
		t.Fatal("zero timeout must not attach a deadline to the provider ctx")
	}
	if got := runnerTestText(call); got != "direct" {
		t.Fatalf("zero timeout must mutate live call directly: got %q", got)
	}
}

// TestRunBoundedProvider_DeadlineMatchesProviderCtxDeadline asserts immutable bounded
// results carry the exact deadline used to bound the provider child context.
func TestRunBoundedProvider_DeadlineMatchesProviderCtxDeadline(t *testing.T) {
	t.Parallel()
	ev := &DecisionEvidence{TimeoutBudget: StaticTimeoutBudgetSource{Budget: 5 * time.Second}}
	var captured time.Time
	var saw bool
	res := runBoundedProvider(t.Context(), ev, "stage", "prov", func(ctx context.Context) (struct{}, error) {
		d, ok := ctx.Deadline()
		if ok {
			saw = true
			captured = d
		}
		return struct{}{}, nil
	})
	if !saw {
		t.Fatal("bounded provider ctx must carry a deadline")
	}
	if res.Deadline.IsZero() {
		t.Fatal("bounded result Deadline must be non-zero on the bounded path")
	}
	if !res.Deadline.Equal(captured) {
		t.Fatalf("bounded result Deadline %v must equal provider ctx deadline %v exactly",
			res.Deadline, captured)
	}
}

func TestRunBoundedProvider_GuardRejectsWhileProviderStillRunning(t *testing.T) {
	t.Parallel()
	guard := NewProviderTimeoutGuard()
	ev := &DecisionEvidence{
		TimeoutBudget: StaticTimeoutBudgetSource{Budget: 20 * time.Millisecond},
		TimeoutGuard:  guard,
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	providerCalls := make(chan struct{}, 2)

	first := runBoundedProvider(t.Context(), ev, "stage", "prov", func(ctx context.Context) (struct{}, error) {
		providerCalls <- struct{}{}
		started <- struct{}{}
		<-release
		return struct{}{}, ctx.Err()
	})
	if !first.TimedOut || first.GuardRejected {
		t.Fatalf("first call should time out in provider, got %+v", first)
	}
	if !first.ProviderStillRunning {
		t.Fatalf("first call must report still-running provider, got %+v", first)
	}
	<-started

	second := runBoundedProvider(t.Context(), ev, "stage", "prov", func(context.Context) (struct{}, error) {
		providerCalls <- struct{}{}
		return struct{}{}, nil
	})
	if !second.TimedOut || !second.GuardRejected {
		t.Fatalf("second call should be rejected by guard, got %+v", second)
	}
	if len(providerCalls) != 1 {
		t.Fatalf("guard rejection must not launch provider again, calls=%d", len(providerCalls))
	}

	close(release)
	deadline := time.After(2 * time.Second)
	for {
		third := runBoundedProvider(t.Context(), ev, "stage", "prov", func(context.Context) (struct{}, error) {
			providerCalls <- struct{}{}
			return struct{}{}, nil
		})
		if !third.GuardRejected {
			if third.TimedOut || third.ParentCanceled {
				t.Fatalf("third call should run normally after prior provider returns, got %+v", third)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("guard did not release after provider returned")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(providerCalls) != 2 {
		t.Fatalf("expected exactly two launched providers, got %d", len(providerCalls))
	}
}

func TestHandleProviderTimeoutNilEmitSafe(t *testing.T) {
	t.Parallel()
	cfg := stageFailureConfig{Stage: "test_stage", EmitTimeout: nil}
	cont, err := handleProviderTimeout(t.Context(), nil, nil, nil, cfg, t.Context(), "p", time.Time{}, sdkhooks.FailOpen)
	if !cont || err != nil {
		t.Fatalf("nil EmitTimeout FailOpen = (%v,%v), want (true,nil)", cont, err)
	}
}

func TestHandleProviderFailureNilEmitSafe(t *testing.T) {
	t.Parallel()
	cfg := stageFailureConfig{Stage: "test_stage", EmitFailure: nil}
	cont, err := handleProviderFailure(t.Context(), nil, nil, nil, cfg, t.Context(), "p", time.Time{}, sdkhooks.FailOpen, errors.New("boom"))
	if !cont || err != nil {
		t.Fatalf("nil EmitFailure FailOpen = (%v,%v), want (true,nil)", cont, err)
	}
}
