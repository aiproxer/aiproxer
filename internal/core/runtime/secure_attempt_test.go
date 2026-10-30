package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSecureAttemptOutcome_mapsOutcomes(t *testing.T) {
	t.Parallel()
	st := execctx.SecureSessionTurn{SessionID: domain.SessionID("s1"), TurnID: domain.TurnID("t1")}
	bleg := b2bua.BLegRecord{BLegID: "b1", Seq: 3}
	ended := time.Unix(3000, 0).UTC()

	tests := []struct {
		name   string
		p      recordAttemptParams
		assert func(t *testing.T, o domain.AttemptOutcome)
	}{
		{
			name: "success",
			p: recordAttemptParams{
				BLeg: bleg, Outcome: aipapi.AttemptSuccess,
			},
			assert: func(t *testing.T, o domain.AttemptOutcome) {
				t.Helper()
				if !o.Success || o.SurfaceState != domain.SurfaceSurfaced {
					t.Fatalf("%+v", o)
				}
			},
		},
		{
			name: "swallowed",
			p: recordAttemptParams{
				BLeg: bleg, Outcome: aipapi.AttemptSwallowedFailure, Reason: "sw",
				DetailErr: aipapi.RecoverablePreOutputError(errors.New("x")),
			},
			assert: func(t *testing.T, o domain.AttemptOutcome) {
				t.Helper()
				if o.Success || o.SurfaceState != domain.SurfaceSwallowed {
					t.Fatalf("%+v", o)
				}
			},
		},
		{
			name: "surfaced_upstream",
			p: recordAttemptParams{
				BLeg: bleg, Outcome: aipapi.AttemptSurfacedFailure, Reason: "boom",
				DetailErr: &aipapi.UpstreamFailureError{Phase: aipapi.PhasePreOutput, Recoverable: false, CandidateKey: "ck"},
			},
			assert: func(t *testing.T, o domain.AttemptOutcome) {
				t.Helper()
				if o.Success || o.SurfaceState != domain.SurfaceSurfaced || o.ErrorCode != "ck" {
					t.Fatalf("%+v", o)
				}
			},
		},
		{
			name: "cancelled",
			p: recordAttemptParams{
				BLeg: bleg, Outcome: aipapi.AttemptCancelled, DetailErr: context.Canceled,
			},
			assert: func(t *testing.T, o domain.AttemptOutcome) {
				t.Helper()
				if o.Success || o.TimeoutClass != "canceled" {
					t.Fatalf("%+v", o)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := secureAttemptOutcome(st, bleg, tc.p, ended)
			tc.assert(t, o)
			if o.SessionID != domain.SessionID("s1") || o.TurnID != domain.TurnID("t1") || o.BLegID != "b1" {
				t.Fatalf("ids: %+v", o)
			}
		})
	}
}

func TestBuildAttemptTrace_fillsRoutingFields(t *testing.T) {
	t.Parallel()
	st := execctx.SecureSessionTurn{SessionID: domain.SessionID("sid"), TurnID: domain.TurnID("tid")}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "be", Model: "mres"},
		Key:     "be:mres",
	}
	tmp := 0.5
	call := aipapi.Call{Route: aipapi.RouteIntent{Selector: "alias:here"}, Options: aipapi.GenerationOptions{
		Temperature:     &tmp,
		ReasoningEffort: "high",
		Verbosity:       aipapi.VerbosityMedium,
	}}
	tr := buildAttemptTrace(st, "a1", b2bua.BLegRecord{BLegID: "b9", Seq: 2}, cand, call, time.Unix(1, 0))
	if tr.ResolvedBackend != "be" || tr.ResolvedModel != "mres" || tr.RouteReason != "be:mres" {
		t.Fatalf("%+v", tr)
	}
	if tr.RequestedModel != "alias:here" {
		t.Fatalf("requested: %q", tr.RequestedModel)
	}
	if tr.Settings.ReasoningEffort != "high" || tr.Settings.Verbosity != "medium" || tr.Settings.Temperature == nil || *tr.Settings.Temperature != 0.5 {
		t.Fatalf("settings: %+v", tr.Settings)
	}
}
