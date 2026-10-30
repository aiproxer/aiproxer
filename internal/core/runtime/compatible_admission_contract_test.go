package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	compatibleadmission "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/compatible"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestCompatibleAdmission_PermitOverloadMapsToConcurrencyLimit binds compatible
// admission saturation to the stable runtime concurrency_limit policy surface.
func TestCompatibleAdmission_PermitOverloadMapsToConcurrencyLimit(t *testing.T) {
	t.Parallel()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"be": 1}, leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "compatible-admission-test"}))
	if err != nil {
		t.Fatal(err)
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: compatibleadmission.ProviderID, Provider: reg.Provider,
			Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
		}},
	}
	d, err := coord.Admit(context.Background(), authority.AttemptAdmission{
		RequestID: "req", AttemptID: "b1", BLegID: "b1", BackendID: "be",
		Lifecycle: metering.LifecycleBackendAttempt, Perspective: metering.PerspectiveOperator,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	})
	if err != nil || d.Kind != authority.DecisionAllow {
		t.Fatalf("first admit err=%v", err)
	}
	_, err = coord.Admit(context.Background(), authority.AttemptAdmission{
		RequestID: "req", AttemptID: "b2", BLegID: "b2", BackendID: "be",
		Lifecycle: metering.LifecycleBackendAttempt, Perspective: metering.PerspectiveOperator,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	})
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("err=%v want policy denied", err)
	}
	var denied *authoritycoord.DeniedError
	if !errors.As(err, &denied) || denied.ProviderID != compatibleadmission.ProviderID {
		t.Fatalf("err=%T", err)
	}
	mapped := mapAttemptAuthorityCoordinatorErrorForTest(err)
	if !aipapi.IsPolicyDenied(mapped) {
		t.Fatalf("mapped=%v", mapped)
	}
	var pol *aipapi.PolicyDecisionError
	if !errors.As(mapped, &pol) {
		t.Fatal("expected policy error")
	}
	if pol.ReasonCode != "concurrency_limit" || pol.ClientCategory != "concurrency_limit" {
		t.Fatalf("reason/category=%q/%q", pol.ReasonCode, pol.ClientCategory)
	}
	_ = coord.Release(context.Background(), d.Stack)
}

func mapAttemptAuthorityCoordinatorErrorForTest(err error) error {
	var denied *authoritycoord.DeniedError
	if errors.As(err, &denied) && denied != nil && denied.ProviderID == compatibleadmission.ProviderID {
		return aipapi.NewPolicyDeniedError(
			"compatible_backend_attempt", "", "concurrency_limit", "concurrency_limit",
			"compatible backend concurrent request limit reached", nil,
		)
	}
	return err
}
