package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
)

// FeatureOwner records the post-migration authority for one release-critical
// obligation. Matrix evidence is deliberately not an owner kind.
type FeatureOwner struct {
	Feature         string
	Frontend        []string
	Core            []string
	Backend         []string
	Profile         []string
	Protocol        []string
	Sentinel        []string
	ScenarioIDs     []string
	ExecutableTests []string
}

var releaseCriticalFeatureOwners = []FeatureOwner{
	{Feature: "json_text", Frontend: []string{"frontend.text-baseline"}, Core: []string{"core.requirement-derivation"}, Backend: []string{"backend.text-baseline"}, Sentinel: []string{"builtin-openresponses-sse"}, ScenarioIDs: []string{"text-baseline"}, ExecutableTests: []string{"internal/testkit/contract/frontend/standard_frontends_test.go::TestBundledFrontends_CertifyIndependentlyWithCapturingExecutor"}},
	{Feature: "sse_text", Frontend: []string{"frontend.text-streaming"}, Core: []string{"core.stream-validation"}, Backend: []string{"backend.text-streaming"}, Sentinel: []string{"builtin-openresponses-sse"}, ScenarioIDs: []string{"text-streaming"}, ExecutableTests: []string{"internal/testkit/contract/frontend/standard_frontends_test.go::TestBundledFrontends_CertifyIndependentlyWithCapturingExecutor"}},
	{Feature: "instructions_roles", Frontend: []string{"frontend.text-baseline"}, Core: []string{"core.requirement-derivation"}, Backend: []string{"backend.text-baseline"}, ScenarioIDs: []string{"text-baseline"}, ExecutableTests: []string{"internal/plugins/frontends/openairesponses/decode_test.go::TestDecodeCreate_emptyInstructionsYieldsEmptySlice"}},
	{Feature: "history", Frontend: []string{"frontend.text-baseline"}, Core: []string{"core.projection"}, Backend: []string{"backend.text-baseline"}, ScenarioIDs: []string{"text-baseline"}, ExecutableTests: []string{"internal/testkit/contract/frontend/standard_frontends_test.go::TestBundledFrontends_CertifyIndependentlyWithCapturingExecutor"}},
	{Feature: "tools", Frontend: []string{"frontend.tools-execution"}, Core: []string{"core.requirement-derivation"}, Backend: []string{"backend.tools-execution"}, ScenarioIDs: []string{"tools-execution", "tool-call-replay", "tool-result-replay"}, ExecutableTests: []string{"internal/testkit/contract/backend/standard_composition_tck_test.go::TestStandardComposition_CertifiesEveryInProcessFamily"}},
	{Feature: "multimodal", Frontend: []string{"frontend.vision-input"}, Core: []string{"core.admission"}, Backend: []string{"backend.vision-input"}, Profile: []string{"profile.family-capability-bound"}, ScenarioIDs: []string{"vision-input", "documents-input"}, ExecutableTests: []string{"internal/testkit/contract/backend/standard_composition_tck_test.go::TestStandardComposition_CertifiesEveryInProcessFamily"}},
	{Feature: "assistant_media", Frontend: []string{"frontend.text-streaming"}, Core: []string{"core.stream-validation"}, Backend: []string{"backend.text-streaming"}, Protocol: []string{"protocol.parity"}, ScenarioIDs: []string{"text-streaming"}, ExecutableTests: []string{"internal/testkit/conformance/parity_openai_test.go::TestParity_OpenAI_canonicalAssistantMediaCollects"}},
	{Feature: "usage_errors", Frontend: []string{"frontend.usage-present"}, Core: []string{"core.terminal-validation"}, Backend: []string{"backend.usage-present", "backend.terminal-error"}, ScenarioIDs: []string{"usage-present", "usage-zero", "recoverable-error", "terminal-error"}, ExecutableTests: []string{"internal/testkit/contract/backend/standard_composition_tck_test.go::TestStandardComposition_CertifiesEveryInProcessFamily"}},
	{Feature: "reasoning_replay", Frontend: []string{"frontend.reasoning-replay-dialect"}, Core: []string{"core.projection"}, Backend: []string{"backend.reasoning-replay-dialect"}, Protocol: []string{"openresponses.continuation"}, ScenarioIDs: []string{"reasoning-output", "reasoning-replay-dialect"}, ExecutableTests: []string{"internal/testkit/contract/frontend/standard_frontends_test.go::TestBundledFrontends_CertifyIndependentlyWithCapturingExecutor"}},
	{Feature: "assistant_phase", Frontend: []string{"frontend.text-baseline"}, Core: []string{"core.admission"}, Backend: []string{"backend.text-baseline"}, ScenarioIDs: []string{"text-baseline"}, ExecutableTests: []string{"internal/plugins/frontends/openresponses/unsupported_controls_test.go::TestWebSocketTurn_UnsupportedControlsRejectBeforeExecutor"}},
	{Feature: "item_references", Frontend: []string{"frontend.item-reference-dialect"}, Core: []string{"core.projection"}, Backend: []string{"backend.ordered-items"}, Protocol: []string{"openresponses.continuation"}, ScenarioIDs: []string{"item-reference-dialect"}, ExecutableTests: []string{"internal/plugins/frontends/openresponses/websocket_continuation_test.go::TestWebSocketContinuation_MaterializesLocalParentAndNewInput"}},
	{Feature: "continuation", Frontend: []string{"frontend.compaction-lifecycle"}, Core: []string{"core.materialize"}, Backend: []string{"backend.ordered-items"}, Protocol: []string{"openresponses.continuation"}, Sentinel: []string{"stateful-openresponses-websocket"}, ScenarioIDs: []string{"ordered-items", "item-reference-dialect"}, ExecutableTests: []string{"internal/plugins/frontends/openresponses/websocket_continuation_test.go::TestWebSocketContinuation_SuccessChainStaysContinuable"}},
	{Feature: "compaction", Frontend: []string{"frontend.compaction-lifecycle"}, Core: []string{"core.materialize"}, Backend: []string{"backend.compaction-lifecycle"}, Protocol: []string{"openresponses.compaction"}, ScenarioIDs: []string{"compaction-lifecycle"}, ExecutableTests: []string{"internal/plugins/frontends/openresponses/compact_test.go::TestCompact_RoutesCanonicalOperationToNormalExecutor"}},
	{Feature: "extensions", Frontend: []string{"frontend.opaque-extension-type"}, Core: []string{"core.admission"}, Backend: []string{"backend.opaque-extension-type"}, ScenarioIDs: []string{"opaque-extension-type"}, ExecutableTests: []string{"internal/plugins/frontends/openresponses/integration_test.go::TestIntegration_unknownPrefixedExtensionRejected"}},
	{Feature: "cancellation", Frontend: []string{"frontend.cancellation"}, Core: []string{"core.output-commitment"}, Backend: []string{"backend.cancellation"}, Sentinel: []string{"builtin-openresponses-sse"}, ScenarioIDs: []string{"cancellation"}, ExecutableTests: []string{"internal/testkit/contract/backend/standard_composition_tck_test.go::TestStandardComposition_CertifiesEveryInProcessFamily"}},
	{Feature: "failover", Frontend: []string{"frontend.text-streaming"}, Core: []string{"core.frozen-failover"}, Backend: []string{"backend.text-streaming"}, Sentinel: []string{"builtin-openresponses-sse"}, ScenarioIDs: []string{"text-streaming"}, ExecutableTests: []string{"internal/testkit/conformance/deployment_test.go::TestFailureInjection_MultipleCandidatesFailOver"}},
	{Feature: "no_retry_visible_output", Frontend: []string{"frontend.text-streaming"}, Core: []string{"core.output-commitment"}, Backend: []string{"backend.recoverable-error"}, Sentinel: []string{"builtin-openresponses-sse"}, ScenarioIDs: []string{"recoverable-error"}, ExecutableTests: []string{"internal/testkit/conformance/deployment_test.go::TestFailureInjection_MultipleCandidatesFailOver"}},
	{Feature: "connector_host", Backend: []string{"connector.negotiate-execute-close"}, Profile: []string{"profile.family-binding"}, Protocol: []string{"backendplugin.v1"}, Sentinel: []string{"connector-openresponses-sse"}, ScenarioIDs: []string{"lifecycle-close", "close-idempotent"}, ExecutableTests: []string{"internal/testkit/conformance/sentinel_test.go::TestBoundedSentinelComposition"}},
}

func ReleaseCriticalFeatureOwners() []FeatureOwner {
	out := make([]FeatureOwner, len(releaseCriticalFeatureOwners))
	copy(out, releaseCriticalFeatureOwners)
	return out
}

func ValidateFeatureOwnership() error {
	corpus := semantic.BaselineScenarioCorpus()
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	var inventory struct {
		RequiredFeatureIDs []string `json:"required_feature_ids"`
	}
	data, err := os.ReadFile(filepath.Join(root, "internal", "testkit", "conformance", "testdata", "baseline_cartesian_inventory.json"))
	if err != nil {
		return fmt.Errorf("read baseline feature inventory: %w", err)
	}
	if err := json.Unmarshal(data, &inventory); err != nil {
		return fmt.Errorf("decode baseline feature inventory: %w", err)
	}
	owned := make(map[string]bool, len(releaseCriticalFeatureOwners))
	sentinelIDs := make(map[string]bool)
	for _, tc := range conformance.BoundedSentinelCases() {
		sentinelIDs[tc.ID] = true
	}
	for _, owner := range releaseCriticalFeatureOwners {
		owned[owner.Feature] = true
		if strings.TrimSpace(owner.Feature) == "" {
			return fmt.Errorf("feature owner has empty feature")
		}
		if len(owner.Frontend)+len(owner.Core)+len(owner.Backend)+len(owner.Profile)+len(owner.Protocol)+len(owner.Sentinel) == 0 {
			return fmt.Errorf("feature %q has no owner", owner.Feature)
		}
		if len(owner.ScenarioIDs) == 0 {
			return fmt.Errorf("feature %q has no executable scenario ownership", owner.Feature)
		}
		for _, ref := range owner.ExecutableTests {
			parts := strings.SplitN(ref, "::", 2)
			if len(parts) != 2 {
				return fmt.Errorf("feature %q has malformed executable owner %q", owner.Feature, ref)
			}
			source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(parts[0])))
			if err != nil || !strings.Contains(string(source), "func "+parts[1]+"(") {
				return fmt.Errorf("feature %q executable owner is not present: %q", owner.Feature, ref)
			}
		}
		if owner.Feature == "connector_host" && len(owner.Sentinel) == 0 {
			return fmt.Errorf("connector host feature has no real-stack owner")
		}
		for _, id := range owner.Sentinel {
			if !sentinelIDs[id] {
				return fmt.Errorf("feature %q links unknown sentinel %q", owner.Feature, id)
			}
		}
		for _, id := range append(append([]string{}, owner.Frontend...), owner.Backend...) {
			if strings.HasPrefix(id, "frontend.") || strings.HasPrefix(id, "backend.") {
				if !slices.ContainsFunc(corpus, func(sc semantic.ScenarioDescriptor) bool {
					return string(sc.ID) == strings.TrimPrefix(strings.TrimPrefix(id, "frontend."), "backend.")
				}) {
					return fmt.Errorf("feature %q links unknown scenario owner %q", owner.Feature, id)
				}
			}
		}
	}
	for _, feature := range inventory.RequiredFeatureIDs {
		if !owned[feature] {
			return fmt.Errorf("baseline feature %q has no executable owner", feature)
		}
	}
	return nil
}
