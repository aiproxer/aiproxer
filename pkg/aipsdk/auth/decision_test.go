package auth

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// TestAPIKeyPlusSSO_unmetSSOChallengesAfterDeviceRecognized models policy where the app key
// is valid but the required SSO step is not satisfied (challenge, not allow).
func TestAPIKeyPlusSSO_unmetSSOChallengesAfterDeviceRecognized(t *testing.T) {
	t.Parallel()
	d := Decision{
		Outcome: OutcomeChallenge,
		Principal: execview.PrincipalView{
			ID: "user-42",
		},
		Device: DeviceIdentity{
			ID:          "dev-1",
			KeyID:       "key-abc",
			Fingerprint: "fp:redacted",
		},
		SatisfiedLevel: LevelAPIKey, // key matched; SSO not yet satisfied
		Challenge: Challenge{
			Kind:       ChallengeSSORequired,
			ReasonCode: "sso_step_incomplete",
			Summary:    "SSO sign-in required to complete access",
		},
		ReasonCode: "api_key_recognized_sso_unmet",
	}
	if d.Outcome != OutcomeChallenge {
		t.Fatalf("Outcome: got %q", d.Outcome)
	}
	if d.Challenge.Kind != ChallengeSSORequired {
		t.Fatalf("Challenge kind: got %q", d.Challenge.Kind)
	}
	if d.Device.KeyID == "" {
		t.Fatal("expected device key id for API-key path")
	}
}
