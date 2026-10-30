package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
)

type exposureRuntimeStateAdmission struct{}

func (exposureRuntimeStateAdmission) Admit(context.Context, BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	return billingport.CallExposure{Status: billingport.ExposureOpen}, nil
}

func TestExposureGenerationUsesOnlyTerminalCallState(t *testing.T) {
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	state := newBillingCallState(callID)
	state.noteAllocatedBLeg("b-1", 1)
	got := state.freezeAllocatedBLegs()
	if len(got) != 1 || got[0] != "b-1" {
		t.Fatalf("frozen terminal leg set = %v", got)
	}
}
