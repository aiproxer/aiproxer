package auth

import "testing"

func TestHandlerKind_stringValues(t *testing.T) {
	t.Parallel()
	cases := map[HandlerKind]string{
		HandlerLocalNoop:   "local_noop",
		HandlerLocalAPIKey: "local_api_key",
		HandlerRemote:      "remote",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Fatalf("HandlerKind %v: want %q", k, want)
		}
	}
}

func TestRequiredLevel_stringValues(t *testing.T) {
	t.Parallel()
	cases := map[RequiredLevel]string{
		LevelNone:      "none",
		LevelAPIKey:    "api_key",
		LevelAPIKeySSO: "api_key_sso",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Fatalf("RequiredLevel %v: want %q", k, want)
		}
	}
}

func TestDecisionOutcome_stringValues(t *testing.T) {
	t.Parallel()
	cases := map[DecisionOutcome]string{
		OutcomeAllow:     "allow",
		OutcomeDeny:      "deny",
		OutcomeChallenge: "challenge",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Fatalf("DecisionOutcome %v: want %q", k, want)
		}
	}
}
