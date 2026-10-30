package preflight_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
)

func TestCheck_unknownOutputPolicies(t *testing.T) {
	t.Parallel()

	modelLimit := modelcatalog.ModelFacts{
		OutputLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 2048},
	}

	tests := []struct {
		name          string
		cfg           preflight.Config
		facts         modelcatalog.ModelFacts
		wantAllowed   bool
		wantReason    preflight.Reason
		wantAdjusted  int
		wantCountOut  int
		wantNilAdjust bool
	}{
		{
			name: "require_client_limit denies",
			cfg: preflight.Config{
				Enabled:             true,
				Mode:                preflight.ModeStrict,
				UnknownOutputPolicy: preflight.UnknownOutputRequireClientLimit,
			},
			wantAllowed: false,
			wantReason:  preflight.ReasonUnknownOutputDenied,
		},
		{
			name: "deny denies",
			cfg: preflight.Config{
				Enabled:             true,
				Mode:                preflight.ModeStrict,
				UnknownOutputPolicy: preflight.UnknownOutputDeny,
			},
			wantAllowed: false,
			wantReason:  preflight.ReasonUnknownOutputDenied,
		},
		{
			name: "configured_default applies max_output_tokens",
			cfg: preflight.Config{
				Enabled:             true,
				Mode:                preflight.ModeStrict,
				MaxOutputTokens:     512,
				UnknownOutputPolicy: preflight.UnknownOutputConfiguredDefault,
			},
			wantAllowed:  true,
			wantReason:   preflight.ReasonAllowed,
			wantAdjusted: 512,
			wantCountOut: 512,
		},
		{
			name: "model_backend_maximum applies fact limit",
			cfg: preflight.Config{
				Enabled:             true,
				Mode:                preflight.ModeStrict,
				UnknownOutputPolicy: preflight.UnknownOutputModelBackendMaximum,
			},
			facts:        modelLimit,
			wantAllowed:  true,
			wantReason:   preflight.ReasonAllowed,
			wantAdjusted: 2048,
			wantCountOut: 2048,
		},
		{
			name: "clamp falls back to configured when model limit absent",
			cfg: preflight.Config{
				Enabled:             true,
				Mode:                preflight.ModeStrict,
				MaxOutputTokens:     256,
				UnknownOutputPolicy: preflight.UnknownOutputClamp,
			},
			wantAllowed:  true,
			wantReason:   preflight.ReasonAllowed,
			wantAdjusted: 256,
			wantCountOut: 256,
		},
		{
			name: "default prefers model limit when present",
			cfg: preflight.Config{
				Enabled:         true,
				Mode:            preflight.ModeStrict,
				MaxOutputTokens: 99,
			},
			facts:        modelLimit,
			wantAllowed:  true,
			wantReason:   preflight.ReasonAllowed,
			wantAdjusted: 2048,
			wantCountOut: 2048,
		},
		{
			name: "default uses configured when model limit absent",
			cfg: preflight.Config{
				Enabled:         true,
				Mode:            preflight.ModeStrict,
				MaxOutputTokens: 128,
			},
			wantAllowed:  true,
			wantReason:   preflight.ReasonAllowed,
			wantAdjusted: 128,
			wantCountOut: 128,
		},
		{
			name: "default allows unbound when no limit configured",
			cfg: preflight.Config{
				Enabled: true,
				Mode:    preflight.ModeStrict,
			},
			wantAllowed:   true,
			wantReason:    preflight.ReasonAllowed,
			wantNilAdjust: true,
		},
		{
			name: "explicit deny rejects unbound",
			cfg: preflight.Config{
				Enabled:             true,
				Mode:                preflight.ModeStrict,
				UnknownOutputPolicy: preflight.UnknownOutputDeny,
			},
			wantAllowed: false,
			wantReason:  preflight.ReasonUnknownOutputDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := preflight.NewChecker(&fakeCounter{result: app.CountResult{InputTokens: 10}}, tt.cfg)
			decision := checker.Check(context.Background(), preflight.Input{
				Backend: "openai",
				Model:   "gpt-test",
				CallID:  "call-1",
				Call:    testCall(),
				Facts:   tt.facts,
				// RequestedMaxOutputTokens omitted → unknown-output path
			})

			if decision.Allowed != tt.wantAllowed {
				t.Fatalf("Allowed=%v want %v (reason=%s err=%v)", decision.Allowed, tt.wantAllowed, decision.Reason, decision.Err)
			}
			if decision.Reason != tt.wantReason {
				t.Fatalf("Reason=%q want %q", decision.Reason, tt.wantReason)
			}
			if !tt.wantAllowed {
				return
			}
			if tt.wantNilAdjust {
				if decision.AdjustedMaxOutputTokens != nil {
					t.Fatalf("AdjustedMaxOutputTokens=%v want nil", decision.AdjustedMaxOutputTokens)
				}
				return
			}
			if decision.AdjustedMaxOutputTokens == nil || *decision.AdjustedMaxOutputTokens != tt.wantAdjusted {
				t.Fatalf("AdjustedMaxOutputTokens=%v want %d", decision.AdjustedMaxOutputTokens, tt.wantAdjusted)
			}
			if decision.Count.OutputTokens != tt.wantCountOut {
				t.Fatalf("Count.OutputTokens=%d want %d", decision.Count.OutputTokens, tt.wantCountOut)
			}
		})
	}
}
