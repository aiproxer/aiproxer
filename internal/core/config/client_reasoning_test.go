package config

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim: routing.attempt_policy defaults to log_skip, accepts reject, fails
// closed on unknown values, and validation rejects unknown keys.
// Source: req 1.3, 1.4. Mutant: default reject / accept unknown -> FAIL.
func TestRoutingConfig_AttemptPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value string
		want  aipapi.AttemptPolicy
		valid bool
	}{
		{name: "empty defaults to log_skip", value: "", want: aipapi.AttemptLogSkip, valid: true},
		{name: "explicit log_skip", value: "log_skip", want: aipapi.AttemptLogSkip, valid: true},
		{name: "explicit reject", value: "reject", want: aipapi.AttemptReject, valid: true},
		{name: "whitespace trimmed", value: " reject ", want: aipapi.AttemptReject, valid: true},
		{name: "unknown fails closed", value: "omit", want: aipapi.AttemptReject, valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := RoutingConfig{AttemptPolicy: tc.value}
			if got := cfg.EffectiveAttemptPolicy(); got != tc.want {
				t.Fatalf("EffectiveAttemptPolicy() = %q, want %q", got, tc.want)
			}
			err := validateRoutingAttemptPolicy(&Config{Routing: cfg})
			if tc.valid && err != nil {
				t.Fatalf("policy %q must validate: %v", tc.value, err)
			}
			if !tc.valid {
				if err == nil {
					t.Fatal("unknown policy must be rejected")
				}
				if !strings.Contains(err.Error(), "routing.attempt_policy") {
					t.Fatalf("error must name the key, got %q", err.Error())
				}
			}
		})
	}
}
