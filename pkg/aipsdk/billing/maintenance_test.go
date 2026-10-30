package billing_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
)

// validMaintenanceUsage returns the D2 maintenance DTO whose durable fingerprint
// is pinned below. Its identity is the operation ID; cost stays absent (the
// keep-warm hook never invents provider cost).
func validMaintenanceUsage() billing.MaintenanceUsage {
	now := time.Unix(1_700_000_000, 0).UTC()
	return billing.MaintenanceUsage{
		OperationID: "op-maint-1",
		ALegID:      "a-leg-1",
		TargetID:    "target-1",
		BackendID:   "backend-1",
		ModelID:     "model-1",
		RecordedAt:  now,
		Evidence: billing.MaintenanceEvidence{
			InputTokens: billing.MaintenanceQuantity{Value: 23, Present: true},
			Cost:        billing.MaintenanceMoney{},
			Source:      billing.MaintenanceSourceProviderReported,
			Authority:   billing.MaintenanceAuthorityAuthoritative,
			DedupeKey:   "op-maint-1",
		},
	}
}

func TestMaintenanceUsageValidateAcceptsValidUsage(t *testing.T) {
	t.Parallel()
	usage := validMaintenanceUsage()
	if err := usage.Validate(); err != nil {
		t.Fatalf("valid usage: %v", err)
	}
	// An explicit provider zero is valid and distinct from absence.
	zero := usage
	zero.Evidence.OutputTokens = billing.MaintenanceQuantity{Value: 0, Present: true}
	if err := zero.Validate(); err != nil {
		t.Fatalf("explicit zero must validate: %v", err)
	}
	// A present cost requires a currency; when present it validates.
	withCost := usage
	withCost.Evidence.Cost = billing.MaintenanceMoney{NanoUnits: 5, Currency: "USD", Present: true}
	if err := withCost.Validate(); err != nil {
		t.Fatalf("present cost must validate: %v", err)
	}
}

func TestMaintenanceUsageValidateMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*billing.MaintenanceUsage)
	}{
		{"missing operation id", func(u *billing.MaintenanceUsage) { u.OperationID = "  " }},
		{"missing a-leg", func(u *billing.MaintenanceUsage) { u.ALegID = "" }},
		{"missing target", func(u *billing.MaintenanceUsage) { u.TargetID = "" }},
		{"missing backend", func(u *billing.MaintenanceUsage) { u.BackendID = "" }},
		{"missing model", func(u *billing.MaintenanceUsage) { u.ModelID = "" }},
		{"zero recorded at", func(u *billing.MaintenanceUsage) { u.RecordedAt = time.Time{} }},
		{"negative quantity", func(u *billing.MaintenanceUsage) {
			u.Evidence.InputTokens = billing.MaintenanceQuantity{Value: -1, Present: true}
		}},
		{"absent nonzero quantity", func(u *billing.MaintenanceUsage) {
			u.Evidence.TotalTokens = billing.MaintenanceQuantity{Value: 3, Present: false}
		}},
		{"negative cost", func(u *billing.MaintenanceUsage) {
			u.Evidence.Cost = billing.MaintenanceMoney{NanoUnits: -1, Present: true, Currency: "USD"}
		}},
		{"absent nonzero cost", func(u *billing.MaintenanceUsage) {
			u.Evidence.Cost = billing.MaintenanceMoney{NanoUnits: 4, Present: false}
		}},
		{"present cost without currency", func(u *billing.MaintenanceUsage) {
			u.Evidence.Cost = billing.MaintenanceMoney{NanoUnits: 4, Present: true, Currency: "  "}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			usage := validMaintenanceUsage()
			tc.mutate(&usage)
			err := usage.Validate()
			if !errors.Is(err, billing.ErrInvalidMaintenance) {
				t.Fatalf("err=%v, want ErrInvalidMaintenance", err)
			}
			if _, fpErr := usage.Fingerprint(); !errors.Is(fpErr, billing.ErrInvalidMaintenance) {
				t.Fatalf("fingerprint err=%v, want ErrInvalidMaintenance (validation first)", fpErr)
			}
		})
	}
}

// TestMaintenanceUsageFingerprintGolden pins the durable identity preimage with
// a literal digest computed independently of this package (stdlib JSON shape +
// SHA-256), so the persisted fingerprint cannot drift silently through the
// carve. RecordedAt is excluded: two usages differing only in timestamps share
// one fingerprint, while a changed amount changes it.
func TestMaintenanceUsageFingerprintGolden(t *testing.T) {
	t.Parallel()
	const golden = "provider-maintenance:v1:a25a1cda9f24b10dd4fa5eea89b527e379dfb618d07eb18024ed3671c5151cfd"
	usage := validMaintenanceUsage()
	got, err := usage.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if got != golden {
		t.Fatalf("fingerprint = %q, want pinned golden %q", got, golden)
	}
	if !strings.HasPrefix(got, "provider-maintenance:v1:") {
		t.Fatalf("fingerprint %q must keep the frozen version prefix", got)
	}

	replayed := usage
	replayed.RecordedAt = usage.RecordedAt.Add(2 * time.Hour)
	replayFP, err := replayed.Fingerprint()
	if err != nil {
		t.Fatalf("replay Fingerprint: %v", err)
	}
	if replayFP != golden {
		t.Fatalf("RecordedAt must be excluded from the fingerprint: %q vs %q", replayFP, golden)
	}

	changed := usage
	changed.Evidence.InputTokens = billing.MaintenanceQuantity{Value: 24, Present: true}
	changedFP, err := changed.Fingerprint()
	if err != nil {
		t.Fatalf("changed Fingerprint: %v", err)
	}
	if changedFP == golden {
		t.Fatal("a changed amount must change the fingerprint (changed payloads conflict at the durable owner)")
	}
}

func TestMaintenanceObserverFuncNilSafe(t *testing.T) {
	t.Parallel()
	var nilFunc billing.MaintenanceObserverFunc
	if err := nilFunc.ObserveProviderMaintenance(context.Background(), validMaintenanceUsage()); err != nil {
		t.Fatalf("nil observer func must be a no-op: %v", err)
	}
	calls := 0
	var observer billing.MaintenanceObserver = billing.MaintenanceObserverFunc(func(_ context.Context, usage billing.MaintenanceUsage) error {
		calls++
		if usage.OperationID != "op-maint-1" {
			t.Errorf("observed operation = %q", usage.OperationID)
		}
		return nil
	})
	if err := observer.ObserveProviderMaintenance(context.Background(), validMaintenanceUsage()); err != nil {
		t.Fatalf("observer dispatch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("observer calls = %d, want 1", calls)
	}
}
