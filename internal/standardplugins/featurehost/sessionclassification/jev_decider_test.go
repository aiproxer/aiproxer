package sessionclassification

import (
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
)

// TestNewRemoteDeciderBuildsOnlyForRemoteCapableModes pins requirements 6.1 and
// 6.2 at the composition seam: a heuristic generation constructs no decider at all,
// while jev and hybrid construct the adapter from their own validated posture.
//
// The heuristic half is the load-bearing one for requirement 6.1: a nil decider
// with no error is what lets the composed generation publish a classifier that
// structurally cannot reach the network, and it is what distinguishes this gate
// from a mode check the caller could bypass.
func TestNewRemoteDeciderBuildsOnlyForRemoteCapableModes(t *testing.T) {
	// Requirement 6.10 now refuses a candidate whose credential does not resolve,
	// so the cases that must publish need a resolvable reference in the environment.
	t.Setenv(jevTestCredentialEnv, jevTestToken)

	validRemote := func() *featurestate.RemoteConfig {
		timeout := 750 * time.Millisecond
		return &featurestate.RemoteConfig{
			Provider:              "jev",
			APIKeyEnv:             jevTestCredentialEnv,
			Timeout:               timeout,
			MaxAttemptsPerSession: 1,
			LeaseTTL:              timeout + featurestate.RemoteLeaseSafetyMargin + time.Second,
			RetryBackoff:          0,
			PositiveThreshold:     0.9,
		}
	}

	cases := []struct {
		name        string
		cfg         featurestate.Config
		wantDecider bool
		wantErr     bool
	}{
		{
			// Requirement 6.2: heuristic mode is the documented V1 default and
			// must never construct an adapter, so no endpoint, credential
			// reference, or client exists for it to call.
			name: "heuristic mode constructs nothing",
			cfg:  featurestate.Config{Mode: featurestate.ModeHeuristic},
		},
		{
			name: "omitted mode defaults to heuristic and constructs nothing",
			cfg:  featurestate.Config{},
		},
		{
			name:        "jev mode constructs the adapter",
			cfg:         featurestate.Config{Mode: featurestate.ModeJev, Remote: validRemote()},
			wantDecider: true,
		},
		{
			name:        "hybrid mode constructs the adapter",
			cfg:         featurestate.Config{Mode: featurestate.ModeHybrid, Remote: validRemote()},
			wantDecider: true,
		},
		{
			// Requirement 6.10: a remote mode whose settings are unusable fails
			// the candidate here rather than publishing a partially configured
			// remote mode.
			name: "jev mode without remote settings is rejected",
			cfg:  featurestate.Config{Mode: featurestate.ModeJev},
			// The posture validation inside this gate is what rejects it.
			wantErr: true,
		},
		{
			name:    "jev mode with a timeout outside its bounds is rejected",
			cfg:     featurestate.Config{Mode: featurestate.ModeJev, Remote: &featurestate.RemoteConfig{Provider: "jev", APIKeyEnv: jevTestCredentialEnv, MaxAttemptsPerSession: 1, PositiveThreshold: 0.9}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decider, err := NewRemoteDecider(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewRemoteDecider(%+v) accepted an unusable posture", tc.cfg)
				}
				if decider != nil {
					t.Fatal("NewRemoteDecider returned a decider together with an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRemoteDecider: %v", err)
			}
			if tc.wantDecider != (decider != nil) {
				t.Fatalf("decider constructed = %t, want %t for mode %q", decider != nil, tc.wantDecider, tc.cfg.Mode)
			}
		})
	}
}

// TestJevErrorOutcomeMapsEveryBoundedFailure pins the adapter's whole
// contribution to the provider-neutral failure contract: every bounded JevFailure
// kind maps to a member of the closed RemoteOutcome vocabulary, and no kind maps
// to an out-of-vocabulary value (requirements 6.9, 9.4).
//
// The mapping is load-bearing twice over. The retry decision reads it to decide
// whether the finite budget permits another attempt, and the bounded observation
// exports it as a metric label, so a missing or wrong member would either spin on
// a determined failure or export an unusable label.
func TestJevErrorOutcomeMapsEveryBoundedFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind JevFailure
		want featurestate.RemoteOutcome
		// retryable records whether the bounded failure is transient enough that
		// repeating the attempt inside the finite budget can plausibly succeed
		// (requirement 6.7). The adapter does not own this decision, but a wrong
		// mapping would make the generation's decision wrong.
		retryable bool
	}{
		{kind: JevFailureRateLimited, want: featurestate.RemoteRateLimited, retryable: true},
		{kind: JevFailureServerUnavailable, want: featurestate.RemoteServerError, retryable: true},
		{kind: JevFailureTimeout, want: featurestate.RemoteTimeout, retryable: true},
		{kind: JevFailureTransport, want: featurestate.RemoteNetworkError, retryable: true},
		{kind: JevFailureMalformedResponse, want: featurestate.RemoteMalformed},
		{kind: JevFailureResponseOversized, want: featurestate.RemoteMalformed},
		{kind: JevFailureCredentialMissing, want: featurestate.RemoteSkipped},
		{kind: JevFailureInputRefused, want: featurestate.RemoteSkipped},
		{kind: JevFailureRequestRefused, want: featurestate.RemoteSkipped},
		{kind: JevFailureRedirectRefused, want: featurestate.RemoteSkipped},
		{kind: JevFailureRequestRejected, want: featurestate.RemoteSkipped},
		{kind: JevFailureCanceled, want: featurestate.RemoteSkipped},
	}

	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()

			err := &JevError{Kind: tc.kind, Err: errForTest}
			if got := JevFailureKindOf(err); got != tc.kind {
				t.Fatalf("JevFailureKindOf = %q, want %q", got, tc.kind)
			}
			got := err.Outcome()
			if got != tc.want {
				t.Fatalf("Outcome(%q) = %q, want %q", tc.kind, got, tc.want)
			}
			if !featurestate.RemoteOutcomeAllowed(got) {
				t.Fatalf("Outcome(%q) = %q, which is outside the closed vocabulary", tc.kind, got)
			}
			if !featurestate.ValidRemoteObservation(featurestate.RemoteObservation{Outcome: got}) {
				t.Fatalf("Outcome(%q) cannot be exported as a bounded observation", tc.kind)
			}
			retryable := got == featurestate.RemoteNetworkError || got == featurestate.RemoteRateLimited ||
				got == featurestate.RemoteServerError || got == featurestate.RemoteTimeout
			if retryable != tc.retryable {
				t.Fatalf("Outcome(%q) = %q is retryable=%t, want %t", tc.kind, got, retryable, tc.retryable)
			}
		})
	}

	t.Run("a nil error still classifies", func(t *testing.T) {
		t.Parallel()

		var absent *JevError
		if got := absent.Outcome(); !featurestate.RemoteOutcomeAllowed(got) {
			t.Fatalf("a nil JevError classified as %q, which is outside the closed vocabulary", got)
		}
	})
}

var errForTest = &testCause{}

// testCause is a stand-in transport cause. It exists so the failure-mapping test
// does not depend on a real network error's text (requirements 7.5, 12.9).
type testCause struct{}

func (*testCause) Error() string { return "scripted transport cause" }
