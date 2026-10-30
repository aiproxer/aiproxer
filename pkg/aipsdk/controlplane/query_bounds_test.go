package controlplane_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestValidateUsageQueryRejectsTooBroadAndWrongClass(t *testing.T) {
	t.Parallel()
	err := controlplane.ValidateUsageQuery(controlplane.UsageQuery{Limit: 10})
	if !errors.Is(err, controlplane.ErrQueryTooBroad) {
		t.Fatalf("got %v", err)
	}
	err = controlplane.ValidateUsageQuery(controlplane.UsageQuery{
		Common: controlplane.CommonFilters{TraceID: "trace-1"},
		Class:  controlplane.QueryClassFinancialProjection,
		Limit:  10,
	})
	if !errors.Is(err, controlplane.ErrQueryUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateUsageQueryAcceptsSelectiveDualPlaneFilters(t *testing.T) {
	t.Parallel()
	err := controlplane.ValidateUsageQuery(controlplane.UsageQuery{
		Common:         controlplane.CommonFilters{Scope: controlplane.ScopeFilters{PrincipalID: scope.Known("p")}},
		Perspective:    controlplane.UsagePerspectiveCustomer,
		Boundary:       controlplane.UsageBoundaryFrontendIngress,
		LifecycleScope: controlplane.UsageLifecycleLogicalRequest,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestValidateAccountingLimitQueryRequiresRemainingAuthorityClass(t *testing.T) {
	t.Parallel()
	err := controlplane.ValidateAccountingLimitStatusQuery(controlplane.AccountingLimitStatusQuery{
		RuleID: "rule-1",
		Class:  controlplane.QueryClassHistoricalMetering,
		Limit:  10,
	})
	if !errors.Is(err, controlplane.ErrQueryUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateAccountingDecisionQueryRejectsTooBroad(t *testing.T) {
	t.Parallel()
	err := controlplane.ValidateAccountingDecisionQuery(controlplane.AccountingDecisionQuery{Limit: 10})
	if !errors.Is(err, controlplane.ErrQueryTooBroad) {
		t.Fatalf("got %v", err)
	}
}
