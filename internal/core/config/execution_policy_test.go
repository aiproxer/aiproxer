package config

import (
	"strings"
	"testing"
)

func TestRoutingConfig_EffectiveExecutionCompositionPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  RoutingConfig
		want ExecutionCompositionPolicy
	}{
		{
			name: "empty defaults to safe",
			cfg:  RoutingConfig{},
			want: ExecutionCompositionSafe,
		},
		{
			name: "explicit safe",
			cfg:  RoutingConfig{ExecutionCompositionPolicy: ExecutionCompositionSafe},
			want: ExecutionCompositionSafe,
		},
		{
			name: "explicit unrestricted",
			cfg:  RoutingConfig{ExecutionCompositionPolicy: ExecutionCompositionUnrestricted},
			want: ExecutionCompositionUnrestricted,
		},
		{
			name: "whitespace padded safe",
			cfg:  RoutingConfig{ExecutionCompositionPolicy: " safe "},
			want: ExecutionCompositionSafe,
		},
		{
			name: "whitespace padded unrestricted",
			cfg:  RoutingConfig{ExecutionCompositionPolicy: " unrestricted "},
			want: ExecutionCompositionUnrestricted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.cfg.EffectiveExecutionCompositionPolicy()
			if got != tc.want {
				t.Fatalf("EffectiveExecutionCompositionPolicy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidate_RoutingExecutionCompositionPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		policy     ExecutionCompositionPolicy
		wantErrSub string
	}{
		{
			name:   "empty is valid",
			policy: "",
		},
		{
			name:   "safe is valid",
			policy: ExecutionCompositionSafe,
		},
		{
			name:   "unrestricted is valid",
			policy: ExecutionCompositionUnrestricted,
		},
		{
			name:       "invalid strict is rejected",
			policy:     "strict",
			wantErrSub: "invalid routing.execution_composition_policy",
		},
		{
			name:       "invalid boolean string is rejected",
			policy:     "true",
			wantErrSub: "invalid routing.execution_composition_policy",
		},
		{
			name:       "invalid disabled is rejected",
			policy:     "disabled",
			wantErrSub: "invalid routing.execution_composition_policy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Routing: RoutingConfig{
					ExecutionCompositionPolicy: tc.policy,
				},
			}
			err := validateRoutingExecutionComposition(cfg)
			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrSub)
				}
			} else if err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}
