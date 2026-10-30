package terminal_test

import (
	"errors"
	"testing"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Exhaustive command×scope claim legality and dual-plane effect flags
// (requirements 7.1–7.2, design D1/D8).

func TestOwner_Claim_CommandScopeMatrix(t *testing.T) {
	t.Parallel()

	for _, scope := range []sdk.Scope{sdk.ScopeRequest, sdk.ScopeAttempt} {
		for _, cmd := range sdk.AllCommands() {
			name := string(scope) + "/" + string(cmd)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				o := coreterm.NewOwner(scope)
				snap := coreterm.NewAccumulatorSnapshot([]byte(name), false)
				r := o.Claim(cmd, snap)

				if !cmd.AllowsScope(scope) {
					if r.Won {
						t.Fatalf("illegal scope claim won: %+v", r)
					}
					if !errors.Is(r.Err, sdk.ErrScopeMismatch) {
						t.Fatalf("Err=%v want ErrScopeMismatch", r.Err)
					}
					if o.State() != sdk.StateOpen {
						t.Fatalf("state=%q want open", o.State())
					}
					if _, ok := o.Outcome(); ok {
						t.Fatal("illegal claim must not publish outcome")
					}
					return
				}

				if !r.Won || r.Err != nil || r.State != sdk.StateTerminalizing {
					t.Fatalf("legal claim failed: %+v", r)
				}
				assertPlaneEffects(t, scope, r.Outcome)
			})
		}
	}
}

func TestOwner_Claim_PlaneEffects_NoWrongPlane(t *testing.T) {
	t.Parallel()

	t.Run("request settles customer only", func(t *testing.T) {
		t.Parallel()
		o := coreterm.NewOwner(sdk.ScopeRequest)
		r := o.Claim(sdk.CommandNormalFinish, coreterm.NewAccumulatorSnapshot([]byte("r"), false))
		if !r.Won {
			t.Fatalf("%+v", r)
		}
		assertPlaneEffects(t, sdk.ScopeRequest, r.Outcome)
		if r.Outcome.SettleOperator || r.Outcome.ReleaseAttempt {
			t.Fatal("request owner must not set attempt-plane effects")
		}
	})

	t.Run("attempt settles operator only", func(t *testing.T) {
		t.Parallel()
		o := coreterm.NewOwner(sdk.ScopeAttempt)
		r := o.Claim(sdk.CommandNormalFinish, coreterm.NewAccumulatorSnapshot([]byte("a"), false))
		if !r.Won {
			t.Fatalf("%+v", r)
		}
		assertPlaneEffects(t, sdk.ScopeAttempt, r.Outcome)
		if r.Outcome.SettleCustomer || r.Outcome.ReleaseConcurrency {
			t.Fatal("attempt owner must not set request-plane effects")
		}
	})

	t.Run("attempt-only commands rejected on request", func(t *testing.T) {
		t.Parallel()
		for _, cmd := range []sdk.Command{
			sdk.CommandParallelLoser,
			sdk.CommandSwallowedAttempt,
			sdk.CommandPreBackendDenial,
			sdk.CommandBackendOpenFailure,
		} {
			o := coreterm.NewOwner(sdk.ScopeRequest)
			r := o.Claim(cmd, coreterm.NewAccumulatorSnapshot(nil, false))
			if r.Won || !errors.Is(r.Err, sdk.ErrScopeMismatch) {
				t.Fatalf("%s: %+v", cmd, r)
			}
		}
	})

	t.Run("request-only commands rejected on attempt", func(t *testing.T) {
		t.Parallel()
		for _, cmd := range []sdk.Command{
			sdk.CommandFrontendEncoderFailure,
			sdk.CommandGateReplacement,
		} {
			o := coreterm.NewOwner(sdk.ScopeAttempt)
			r := o.Claim(cmd, coreterm.NewAccumulatorSnapshot(nil, false))
			if r.Won || !errors.Is(r.Err, sdk.ErrScopeMismatch) {
				t.Fatalf("%s: %+v", cmd, r)
			}
		}
	})
}

func assertPlaneEffects(t *testing.T, scope sdk.Scope, out coreterm.Outcome) {
	t.Helper()
	if out.Scope != scope {
		t.Fatalf("outcome scope=%q want %q", out.Scope, scope)
	}
	switch scope {
	case sdk.ScopeRequest:
		if !out.SettleCustomer || !out.ReleaseConcurrency {
			t.Fatalf("request plane flags unset: settle=%v release=%v", out.SettleCustomer, out.ReleaseConcurrency)
		}
		if out.SettleOperator || out.ReleaseAttempt {
			t.Fatalf("request leaked attempt flags: settleOp=%v releaseAtt=%v", out.SettleOperator, out.ReleaseAttempt)
		}
	case sdk.ScopeAttempt:
		if !out.SettleOperator || !out.ReleaseAttempt {
			t.Fatalf("attempt plane flags unset: settle=%v release=%v", out.SettleOperator, out.ReleaseAttempt)
		}
		if out.SettleCustomer || out.ReleaseConcurrency {
			t.Fatalf("attempt leaked request flags: settleCust=%v releaseConc=%v", out.SettleCustomer, out.ReleaseConcurrency)
		}
	default:
		t.Fatalf("unknown scope %q", scope)
	}
}
