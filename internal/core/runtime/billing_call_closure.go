package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// handoffBillingTurn owns the request-level BillingCallID closure claim. The
// terminal mutex remains held through the sink append so a failed append is
// retryable while concurrent terminal paths cannot publish duplicate closures.
func (t *turnTerminal) handoffBillingTurn(ctx context.Context, facts requestTerminalFacts, command sdkterminal.Command) error {
	if t == nil || t.appendBillingCall == nil || t.billingWorkload == nil {
		return nil
	}
	t.billingClosureMu.Lock()
	defer t.billingClosureMu.Unlock()
	if t.appendBillingCall == nil || t.billingClosureSuccess {
		return nil
	}
	if err := facts.billingCallID.Validate(); err != nil {
		return nil
	}
	if !facts.identityStamped {
		return nil
	}
	accountID := strings.TrimSpace(facts.accountID)
	if accountID == "" {
		return nil
	}
	ids := facts.billingState.freezeAllocatedBLegs()
	now := t.nowTime()
	started, finished := facts.billingState.timingBounds(now)
	workloadCtx := facts.toRecvTurnFacts(ctx).projectContext(ctx, nil)
	record := usageevidence.CallUsageRecord{
		SchemaVersion:      usageevidence.CurrentRecordSchemaVersion,
		CallID:             facts.billingCallID,
		SubmissionID:       strings.TrimSpace(facts.submissionID),
		AccountID:          accountID,
		ALegID:             strings.TrimSpace(facts.aLegID),
		SessionID:          strings.TrimSpace(facts.sessionID),
		StartedAt:          started,
		FinishedAt:         finished,
		Outcome:            turnOutcomeFromCommand(command),
		CustomerPricingRef: facts.pricing,
		ChargePolicyRef:    facts.chargePolicy,
		ExpectedBLegIDs:    ids,
		Workload:           t.billingWorkload(workloadCtx, facts.aLegID),
	}
	sealed, err := record.Seal()
	if err != nil {
		if t.log != nil {
			t.log.DebugContext(ctx, "billing call-closure seal failed", "error", err)
		}
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The frozen facts scope rides the closure even on detached contexts.
	ctx = withTerminalScope(ctx, facts.recvViews.Scope)
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	err = safety.Call(safety.BoundaryStream, "billing_call_closure", func() error {
		return t.appendBillingCall(persistCtx, sealed)
	})
	if err != nil {
		if t.logBillingAppendFailure != nil {
			t.logBillingAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing call-closure append failed", err)
		}
		return fmt.Errorf("billing call-closure append: %w", err)
	}
	t.billingClosureSuccess = true
	return nil
}

func callClosureTimes(legs []usageevidence.CallLegUsageRecord, now time.Time) (time.Time, time.Time) {
	var started, finished time.Time
	for _, leg := range legs {
		if !leg.StartedAt.IsZero() && (started.IsZero() || leg.StartedAt.Before(started)) {
			started = leg.StartedAt
		}
		if !leg.FinishedAt.IsZero() && (finished.IsZero() || leg.FinishedAt.After(finished)) {
			finished = leg.FinishedAt
		}
	}
	if started.IsZero() {
		started = now
	}
	if finished.IsZero() {
		finished = now
	}
	if finished.Before(started) {
		finished = started
	}
	return started, finished
}
