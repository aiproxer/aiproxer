package secretguard_test

import "testing"

// frontendFieldCoverageMatrix freezes the Phase 1 acceptance surface (reqs 1.2, 5.1).
var frontendFieldCoverageMatrix = map[string][]string{
	"openai-responses": {"instructions", "message_text", "tool_call_arguments", "tool_role_text", "tool_result", "tool_description_schema"},
	"openai-legacy":    {"instructions", "message_text", "tool_call_arguments", "tool_role_text", "tool_result", "tool_description_schema"},
	"anthropic":        {"instructions", "message_text", "tool_call_arguments", "tool_role_text", "tool_result", "tool_description_schema"},
	"gemini":           {"instructions", "message_text", "tool_call_arguments", "tool_role_text", "tool_result", "tool_description_schema"},
}

func TestFrontendFieldCoverageMatrix_shape(t *testing.T) {
	t.Parallel()
	wantLocs := 6
	if len(frontendFieldCoverageMatrix) != 4 {
		t.Fatalf("frontends: got %d want 4", len(frontendFieldCoverageMatrix))
	}
	for fe, locs := range frontendFieldCoverageMatrix {
		if len(locs) != wantLocs {
			t.Fatalf("%s locations: got %d want %d", fe, len(locs), wantLocs)
		}
	}
}
