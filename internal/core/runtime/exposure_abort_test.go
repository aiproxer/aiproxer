package runtime

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
)

// The admitted-exposure exit records one closure, listing the B-legs allocated
// by the time the call failed, no matter how many failure paths reach it.
func TestExposureAbort_RecordsOnceWithLegsAllocatedAtAbort(t *testing.T) {
	t.Parallel()
	sink := &capturingTerminalUsageSink{}
	ex := &Executor{}
	ex.TerminalUsageSink = sink
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	state := newBillingCallState(callID)
	abort := ex.wireExposureAbort(billingport.CallExposure{AccountID: "acct-1"}, callID, "a-1", "s-1", state)
	state.noteAllocatedBLeg("b-1", 1)

	abort.Abort(context.Background())
	abort.Abort(context.Background())

	if len(sink.calls) != 1 {
		t.Fatalf("closures=%d want 1", len(sink.calls))
	}
	rec := sink.calls[0]
	if rec.Outcome != usageevidence.TurnOutcomeFailed || rec.AccountID != "acct-1" || rec.ALegID != "a-1" {
		t.Fatalf("closure = %+v", rec)
	}
	if !slices.Equal(rec.ExpectedBLegIDs, []string{"b-1"}) {
		t.Fatalf("ExpectedBLegIDs=%v want [b-1]", rec.ExpectedBLegIDs)
	}
	if ex.wireExposureAbort(billingport.CallExposure{}, callID, "a-1", "s-1", state) != nil {
		t.Fatal("no admitted account must mean no abort exit")
	}
}
