package usagelimit_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Claim: optional enforcement never denies or fabricates accounting work,
// including invalid quantities and canceled requests (requirements 1.1, 2.2).
// Mutants: deny admission, return ctx.Err(), or mark any completion Applied.
func TestNoOp_AdmitsWithoutReservationsAndLeavesCompletionEmpty(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"active", context.Background()},
		{"canceled", canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var authority usagelimit.NoOp
			invalid := usagelimit.Amount{Unit: "unknown", Value: -1}
			admission := usagelimit.AdmissionInput{
				Dimensions:     usagelimit.Dimensions{PolicyLabels: map[string]scope.Value{"team": scope.Known("original")}},
				Request:        invalid,
				ReservationKey: usagelimit.ReservationKey{LogicalRequestID: "request", RuleID: "rule"},
			}
			t.Run("admit", func(t *testing.T) {
				got, err := authority.Admit(tc.ctx, admission)
				want := usagelimit.AdmissionResult{Allowed: true, Outcome: usagelimit.DecisionOutcomeAllow}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("Admit = %#v, %v; want only allow, no accounting evidence", got, err)
				}
			})
			t.Run("settle", func(t *testing.T) {
				got, err := authority.Settle(tc.ctx, usagelimit.SettleInput{
					ReservationID: "reservation", FinalUsage: invalid, FinalUsagePresent: true,
					Reservations: []usagelimit.SettlementDescriptor{{Reservation: usagelimit.ReservationDescriptor{RuleID: "rule"}}},
				})
				if err != nil || !reflect.DeepEqual(got, usagelimit.SettleResult{}) {
					t.Fatalf("Settle = %#v, %v; want empty result, nil", got, err)
				}
			})
			t.Run("release", func(t *testing.T) {
				got, err := authority.Release(tc.ctx, usagelimit.ReleaseInput{
					ReservationID: "reservation", Amount: invalid,
					Reservations: []usagelimit.ReleaseDescriptor{{Reservation: usagelimit.ReservationDescriptor{RuleID: "rule"}}},
				})
				if err != nil || !reflect.DeepEqual(got, usagelimit.ReleaseResult{}) {
					t.Fatalf("Release = %#v, %v; want empty result, nil", got, err)
				}
			})
			ruleIDs := []string{"rule"}
			t.Run("apply_usage", func(t *testing.T) {
				got, err := authority.ApplyUsage(tc.ctx, usagelimit.ApplyUsageCommand{
					RuleIDs: ruleIDs, RequestCount: invalid, UsagePresent: true,
					Usage: usagelimit.PreflightUsage{InputTokens: 42},
				})
				if err != nil || !reflect.DeepEqual(got, usagelimit.ApplyUsageResult{}) {
					t.Fatalf("ApplyUsage = %#v, %v; want empty result, nil", got, err)
				}
			})
			if !reflect.DeepEqual(ruleIDs, []string{"rule"}) ||
				!reflect.DeepEqual(admission.Dimensions.PolicyLabels, map[string]scope.Value{"team": scope.Known("original")}) {
				t.Fatal("NoOp changed caller-owned rule IDs or dimensions")
			}
		})
	}
}
