package standardplugins

import (
	"slices"
	"testing"
)

// TestSpecBundle_standardBundleIDInventory locks the standard distribution plugin IDs.
// When adding a bundled plugin, update StandardBundle / StandardBackendBundle and this expectation.
func TestSpecBundle_standardBundleIDInventory(t *testing.T) {
	t.Parallel()
	b := StandardBundle()
	be := StandardBackendBundle(UpstreamAPIKeys{})

	wantFE := []string{
		"anthropic",
		"gemini",
		"openai-legacy",
		"openai-responses",
		"openresponses",
		"systemone",
	}
	var gotFE []string
	for _, e := range b.Frontends {
		gotFE = append(gotFE, e.ID)
	}
	slices.Sort(gotFE)
	if !slices.Equal(gotFE, wantFE) {
		t.Fatalf("frontend IDs\ngot  %#v\nwant %#v", gotFE, wantFE)
	}

	wantBE := []string{
		"alibaba-token-plan-intl",
		"anthropic",
		"bedrock",
		"custom-anthropic-compatible",
		"custom-openai-legacy-compatible",
		"custom-openai-responses-compatible",
		"custom-openresponses-compatible",
		"custom-systemone-compatible",
		"gemini",
		"openai-legacy",
		"openai-responses",
	}
	var gotBE []string
	for _, e := range be.Backends {
		gotBE = append(gotBE, e.ID)
	}
	slices.Sort(gotBE)
	if !slices.Equal(gotBE, wantBE) {
		t.Fatalf("backend IDs\ngot  %#v\nwant %#v", gotBE, wantBE)
	}

	wantFeat := []string{
		"agent-loop-guard",
		"codex-client-compat",
		"compaction-continuity",
		"downstream-cache-affinity",
		"interleaved-thinking",
		"keepwarm",
		"model-system-prompt",
		"parts-noop",
		"path_virtualization",
		"pre-request-policy",
		"proxy-memory",
		"reasoning-output-preservation",
		"ref-autoappend-file",
		"ref-request-suffix",
		"ref-submit-annotate",
		"ref-tool-policy",
		"ref-tool-prefix",
		"ref-traffic-transcript",
		"ref-verifier-stub",
		"ref-workspace-guard",
		"secrets-guard",
		"session-classification",
		"submit-noop",
		"tool-call-repair",
		"tool-name-correction",
		"tool-reactor-noop",
	}
	var gotFeat []string
	for _, e := range b.Features {
		gotFeat = append(gotFeat, e.ID)
	}
	slices.Sort(gotFeat)
	if !slices.Equal(gotFeat, wantFeat) {
		t.Fatalf("feature IDs\ngot  %#v\nwant %#v", gotFeat, wantFeat)
	}
}
