package codexclientcompat

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAgentMatchersUseSharedIdentityRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		match func(compatInput) bool
		value string
	}{
		{name: "OpenCode", match: openCodeAgentMatch, value: "opencode/1.2.26"},
		{name: "Pi", match: piAgentMatch, value: "@mariozechner/pi-coding-agent/0.55.3"},
		{name: "Factory Droid", match: droidAgentMatch, value: "factory-cli/1.2.3"},
		{name: "Hermes", match: hermesAgentMatch, value: "hermes-agent/1.0 NousResearch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !tc.match(compatInput{agents: []string{tc.value}}) {
				t.Fatalf("agent matcher rejected shared identity %q", tc.value)
			}
		})
	}
}

func TestAgentMatchersPreserveLegacySubstringFallbacks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		match func(compatInput) bool
		value string
	}{
		{name: "OpenCode", match: openCodeAgentMatch, value: "vendor-opencode-wrapper"},
		{name: "Pi", match: piAgentMatch, value: "vendor-pi-coding-agent-wrapper"},
		{name: "Factory Droid", match: droidAgentMatch, value: "Android/15"},
		{name: "Hermes", match: hermesAgentMatch, value: "vendor-hermes-agent-wrapper"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !tc.match(compatInput{agents: []string{tc.value}}) {
				t.Fatalf("legacy compatibility matcher rejected historical substring %q", tc.value)
			}
		})
	}
}

func TestPiAgentMatchRejectsSharedOnlyVersionPrefixes(t *testing.T) {
	t.Parallel()

	for _, candidate := range []string{"pi/1.2", "client (pi/1.2)"} {
		t.Run(candidate, func(t *testing.T) {
			t.Parallel()
			if piAgentMatch(compatInput{agents: []string{candidate}}) {
				t.Fatalf("Pi compatibility matcher accepted non-legacy identity %q", candidate)
			}
		})
	}
}

func TestApplyCompatLeavesCallsWithSharedOnlyPiVersionPrefixesUnchanged(t *testing.T) {
	t.Parallel()

	for _, candidate := range []string{"pi/1.2", "client (pi/1.2)"} {
		t.Run(candidate, func(t *testing.T) {
			t.Parallel()
			identity, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			call := &aipapi.Call{
				Instructions: []aipapi.Message{{
					Role:  aipapi.RoleSystem,
					Parts: []aipapi.Part{aipapi.TextPart("Ordinary base instructions")},
				}},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("Read the project status")},
				}},
				Tools: []aipapi.ToolDef{{Name: "read_file"}},
				Extensions: map[string]json.RawMessage{
					extUserAgentKey: identity,
				},
			}
			before, err := json.Marshal(call)
			if err != nil {
				t.Fatal(err)
			}

			ApplyCompat(call)

			after, err := json.Marshal(call)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("ApplyCompat mutated call for non-legacy Pi identity %q: before=%s after=%s", candidate, before, after)
			}
		})
	}
}
