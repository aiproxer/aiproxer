package agentfacts

import "testing"

func TestMatchIdentityEvidenceCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		candidate string
		want      Family
	}{
		{name: "codex versioned identity", candidate: "codex_cli_rs/1.2.3", want: FamilyCodex},
		{name: "trimmed and case folded codex", candidate: "  CODEX_CLI_RS/1.2.3  ", want: FamilyCodex},
		{name: "codex as delimited identity token", candidate: "proxy/2 (codex_cli_rs/1.2.3)", want: FamilyCodex},
		{name: "roo versioned identity", candidate: "roo-code/1.2.3", want: FamilyRoo},
		{name: "opencode versioned identity", candidate: "opencode/1.2.26", want: FamilyOpenCode},
		{name: "pi package identity", candidate: "@mariozechner/pi-coding-agent/0.55.3", want: FamilyPi},
		{name: "pi version token", candidate: "client pi/1.2", want: FamilyPi},
		{name: "factory cli identity", candidate: "factory-cli/1.2.3", want: FamilyDroid},
		{name: "factory cli underscore identity", candidate: "factory_cli/1.2.3", want: FamilyDroid},
		{name: "factory droid identity", candidate: "factorydroid/1.2.3", want: FamilyDroid},
		{name: "droid identity token", candidate: "droid/1.2.3", want: FamilyDroid},
		{name: "hermes agent identity", candidate: "hermes-agent/1.0 NousResearch", want: FamilyHermes},
		{name: "hermes case folded", candidate: "NousResearch/HERMES-AGENT/1.0", want: FamilyHermes},
		{name: "hermes version prefix", candidate: "hermes/1.0", want: FamilyHermes},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := MatchIdentity(tc.candidate)
			if !ok {
				t.Fatalf("MatchIdentity(%q) did not match", tc.candidate)
			}
			if got.Family != tc.want {
				t.Fatalf("MatchIdentity(%q).Family = %q, want %q", tc.candidate, got.Family, tc.want)
			}
			if got.Confidence != ConfidenceHigh {
				t.Fatalf("MatchIdentity(%q).Confidence = %q, want %q", tc.candidate, got.Confidence, ConfidenceHigh)
			}
		})
	}
}

func TestMatchIdentityRejectsGenericSDKAndNearMissValues(t *testing.T) {
	t.Parallel()

	cases := []string{
		"Anthropic/JS",
		"OpenAI/JS",
		"Anthropic/JS (not-opencode)",
		"OpenAI/JS (roo-code-helper)",
		"Android/15",
		"opencode/1.2.26 roo-code/1.2.3",
		"Go-http-client/1.1",
		"Aider",
		"random/not-opencode",
		"prefixopencode/1.2.3",
		"roo-code-helper/1.2.3",
		"factory-cli-wrapper/1.2.3",
		"prefixhermes-agent/1.0",
		"",
		" \t ",
	}
	for _, candidate := range cases {
		t.Run(candidate, func(t *testing.T) {
			t.Parallel()
			if got, ok := MatchIdentity(candidate); ok {
				t.Fatalf("MatchIdentity(%q) = %+v, true; want no match", candidate, got)
			}
		})
	}
}
