//go:build productsmoke

package productsmoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Fixed management paths from docs/runtime-config-reload.md. The journey talks
// to the management listener on the wire instead of importing the handler.
const (
	configReloadPath = "/admin/config/reload"
	configStatusPath = "/admin/config/status"
)

const (
	reloadBackendID = "reload"
	reloadCanonical = "reload/chat"
	reloadNative    = "reload-native"
	reloadRoute     = reloadBackendID + ":" + reloadCanonical

	reloadProviderAContent = "provider A answered before the reload."
	reloadProviderBContent = "provider B answered after the reload."

	// Reload categories the journeys assert, from the documented outcome table.
	reloadPublished = "published"
	reloadInvalid   = "invalid"
)

// reloadResultDTO is the secret-safe management reload result.
type reloadResultDTO struct {
	Category           string `json:"category"`
	AttemptID          int64  `json:"attempt_id"`
	ActiveGeneration   int64  `json:"active_generation"`
	PreviousGeneration int64  `json:"previous_generation,omitempty"`
	ReasonCategory     string `json:"reason_category,omitempty"`
}

// reloadStatusDTO mirrors the management status fields the journeys assert.
type reloadStatusDTO struct {
	ActiveGeneration int64           `json:"active_generation"`
	LastResult       reloadResultDTO `json:"last_result"`
	LastSuccess      reloadResultDTO `json:"last_success"`
	LastFailure      reloadResultDTO `json:"last_failure"`
	SourceIntegrity  string          `json:"source_integrity"`
}

// reloadScript is one provider's scripted answer. Both providers share the
// native model; only the content and usage differ, so the client wire alone
// identifies which provider served the request.
func reloadScript(content string, usage fakeUsage) fakeScript {
	return fakeScript{Model: reloadNative, Deltas: []string{content}, Usage: usage}
}

// reloadRequestBody is one normal client chat request on the reloaded route.
func reloadRequestBody() []byte {
	raw, err := json.Marshal(chatBody{
		Model:    reloadCanonical,
		Stream:   false,
		Messages: smokeMessages(),
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// startReloadSUT starts the proxy with the management API enabled and one
// backend row pointed at provider A. The readiness backend is appended by
// startSUT and never reaches either journey provider.
func startReloadSUT(t *testing.T, canary string, providerA *fakeProvider) *sut {
	t.Helper()
	return startSUT(t, sutConfig{
		Canary:     canary,
		Frontends:  []string{"openai-legacy"},
		Management: true,
		Backends: []backendWiring{{
			ID:      reloadBackendID,
			Kind:    kindOpenAILegacyCompatible,
			BaseURL: providerA.OpenAILegacyBaseURL(),
			Models:  []modelWiring{{Canonical: reloadCanonical, Native: reloadNative}},
		}},
		DefaultRoute: reloadRoute,
	})
}

// readReloadStatus reads and decodes the management status snapshot. A status
// read that is not a 200 cannot witness the active generation.
func readReloadStatus(ctx context.Context, h *sut) (reloadStatusDTO, *clientWire, error) {
	wire := doRequest(ctx, http.MethodGet, h.ManagementURL()+configStatusPath, nil)
	if wire.Err != nil {
		return reloadStatusDTO{}, wire, fmt.Errorf("status exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return reloadStatusDTO{}, wire, fmt.Errorf("status returned %d, want 200; body: %s", wire.Status, excerpt(h.evidence.redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	var status reloadStatusDTO
	if err := json.Unmarshal(wire.Body, &status); err != nil {
		return reloadStatusDTO{}, wire, fmt.Errorf("decode reload status: %v; body: %s", err, excerpt(h.evidence.redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	return status, wire, nil
}

// decodeReloadResult decodes a management reload response body. Status and
// category assertions stay in the journey because a valid and an invalid reload
// expect different terminals.
func decodeReloadResult(wire *clientWire, redact redactor) (reloadResultDTO, error) {
	if wire == nil {
		return reloadResultDTO{}, errors.New("no reload exchange was captured")
	}
	if wire.Err != nil {
		return reloadResultDTO{}, fmt.Errorf("reload exchange failed: %w", wire.Err)
	}
	var result reloadResultDTO
	if err := json.Unmarshal(wire.Body, &result); err != nil {
		return reloadResultDTO{}, fmt.Errorf("status %d: decode reload result: %v; body: %s", wire.Status, err, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	return result, nil
}

// writeReloadProviderLogs stores both providers' request logs so a failure and
// a pass carry the same witnesses.
func writeReloadProviderLogs(h *sut, providerA, providerB *fakeProvider) {
	h.evidence.write("provider-a-requests.txt", providerA.RequestLog())
	if providerB != nil {
		h.evidence.write("provider-b-requests.txt", providerB.RequestLog())
	}
}

// testReloadValid proves a published management-API reload changes where later
// requests go: provider A serves before the atomic replacement, provider B
// serves after it, and provider A receives nothing for the post-reload request.
func testReloadValid(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	scriptA := reloadScript(reloadProviderAContent, fakeUsage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8})
	scriptB := reloadScript(reloadProviderBContent, fakeUsage{PromptTokens: 7, CompletionTokens: 9, TotalTokens: 16})
	providerA := newOpenAILegacyProvider(t, canary, scriptA)
	providerB := newOpenAILegacyProvider(t, canary, scriptB)
	h := startReloadSUT(t, canary, providerA)
	failures := failureContext{sut: h, provider: providerA}

	before, statusWire, err := readReloadStatus(t.Context(), h)
	if err != nil {
		failf(t, failureContext{sut: h, provider: providerA, wire: statusWire}, "reload_valid: read the startup reload status: %v", err)
	}
	if before.ActiveGeneration < 1 {
		failf(t, failureContext{sut: h, provider: providerA, wire: statusWire},
			"reload_valid: startup reported active generation %d, want the published startup generation", before.ActiveGeneration)
	}
	if before.LastResult.Category != reloadPublished {
		failf(t, failureContext{sut: h, provider: providerA, wire: statusWire},
			"reload_valid: startup last result category %q, want %q", before.LastResult.Category, reloadPublished)
	}

	// The pre-reload request must be served by provider A, so the post-reload
	// provider change is an observed difference rather than a first-ever request.
	preWire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, reloadRequestBody())
	if err := checkOpenAICollect(preWire, scriptA, h.evidence.redact); err != nil {
		failf(t, failureContext{sut: h, provider: providerA, wire: preWire},
			"reload_valid: the request before the reload: %v", err)
	}
	if got := providerA.RequestCount(); got != 1 {
		failf(t, failureContext{sut: h, provider: providerA, wire: preWire},
			"reload_valid: provider A received %d request(s) before the reload, want exactly 1", got)
	}
	if got := providerB.RequestCount(); got != 0 {
		failf(t, failureContext{sut: h, provider: providerB, wire: preWire},
			"reload_valid: provider B received %d request(s) before the reload, want 0", got)
	}

	candidate, err := h.CandidateConfig(func(render *sutRender) error {
		return render.setBackendBaseURL(reloadBackendID, providerB.OpenAILegacyBaseURL())
	})
	if err != nil {
		failf(t, failures, "reload_valid: render the candidate configuration: %v", err)
	}
	// server.address is startup-only, so the candidate must keep it fixed or the
	// reload would be judged restart-required instead of published.
	if want := fmt.Sprintf("address: %q", h.addr); !strings.Contains(candidate, want) {
		failf(t, failures, "reload_valid: the candidate configuration does not keep the started server address %s", want)
	}
	if err := h.ReplaceConfig("reload-valid-candidate.yaml", candidate); err != nil {
		failf(t, failures, "reload_valid: atomically replace the fixed configuration path: %v", err)
	}

	reloadWire := doJSON(t.Context(), h.ManagementURL()+configReloadPath, nil)
	failures = failureContext{sut: h, provider: providerB, wire: reloadWire}
	h.evidence.write("reload-valid-exchange.txt", reloadWire.render(h.evidence.redact))
	writeReloadProviderLogs(h, providerA, providerB)
	published, err := decodeReloadResult(reloadWire, h.evidence.redact)
	if err != nil {
		failf(t, failures, "reload_valid: management reload: %v", err)
	}
	if reloadWire.Status != http.StatusOK {
		failf(t, failures, "reload_valid: management reload returned status %d, want 200; category %q, reason %q",
			reloadWire.Status, published.Category, published.ReasonCategory)
	}
	if published.Category != reloadPublished {
		failf(t, failures, "reload_valid: reload category %q, want %q", published.Category, reloadPublished)
	}
	if published.PreviousGeneration != before.ActiveGeneration {
		failf(t, failures, "reload_valid: reload reported previous generation %d, want the started generation %d",
			published.PreviousGeneration, before.ActiveGeneration)
	}
	if published.ActiveGeneration != before.ActiveGeneration+1 {
		failf(t, failures, "reload_valid: reload reported active generation %d, want the next monotonic generation %d",
			published.ActiveGeneration, before.ActiveGeneration+1)
	}
	after, afterWire, err := readReloadStatus(t.Context(), h)
	if err != nil {
		failf(t, failureContext{sut: h, provider: providerB, wire: afterWire}, "reload_valid: read the status after the reload: %v", err)
	}
	if after.ActiveGeneration != published.ActiveGeneration {
		failf(t, failureContext{sut: h, provider: providerB, wire: afterWire},
			"reload_valid: status reports active generation %d after the reload, want the published generation %d",
			after.ActiveGeneration, published.ActiveGeneration)
	}
	if after.SourceIntegrity != "ok" {
		failf(t, failureContext{sut: h, provider: providerB, wire: afterWire},
			"reload_valid: source integrity is %q after the published reload, want ok", after.SourceIntegrity)
	}

	// A new request must reach provider B and must not reach provider A again.
	providerABefore := providerA.RequestCount()
	postWire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, reloadRequestBody())
	failures = failureContext{sut: h, provider: providerB, wire: postWire}
	writeReloadProviderLogs(h, providerA, providerB)
	if err := checkOpenAICollect(postWire, scriptB, h.evidence.redact); err != nil {
		failf(t, failures, "reload_valid: the request after the reload: %v", err)
	}
	if got := providerB.RequestCount(); got != 1 {
		failf(t, failures, "reload_valid: provider B received %d request(s) after the reload, want exactly the new request", got)
	}
	if got := providerA.RequestCount(); got != providerABefore {
		failf(t, failures, "reload_valid: provider A received %d request(s) after the reload, want the %d it had before: the route must use the reloaded provider",
			got, providerABefore)
	}
	requestB := singleProviderRequest(t, failures, providerB)
	if err := checkTranslatedOpenAIChat(requestB, reloadNative, openAIChatPath, smokeMessages(), nil, h.evidence.redact); err != nil {
		failf(t, failures, "reload_valid: provider B did not receive the translated client request: %v", err)
	}
	if !requestB.KeyMatched {
		failf(t, failures, "reload_valid: the post-reload request did not carry the canary credential")
	}

	summary := fmt.Sprintf(
		"generation_before=%d generation_after=%d provider_a_requests=%d provider_b_requests=%d category=%s source_integrity=%s\n",
		before.ActiveGeneration, published.ActiveGeneration, providerA.RequestCount(), providerB.RequestCount(), published.Category, after.SourceIntegrity)
	h.evidence.write("reload-valid.txt", summary)
	h.evidence.write("reload-valid-client-wire.txt", postWire.render(h.evidence.redact))
	t.Logf("reload valid passed: generation %d -> %d, provider B served the new request; evidence %s",
		before.ActiveGeneration, published.ActiveGeneration, h.evidence.dir)
}

// testReloadInvalid proves a rejected reload leaves the last good configuration
// serving. The candidate sets an unknown logging level, which is a startup-only
// setting that fails validation: the coordinator rejects the whole transaction
// as invalid (422) before preparing a generation, so this asserts the decode and
// validation rejection path rather than the restart-required path.
func testReloadInvalid(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	scriptA := reloadScript(reloadProviderAContent, fakeUsage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8})
	providerA := newOpenAILegacyProvider(t, canary, scriptA)
	h := startReloadSUT(t, canary, providerA)
	failures := failureContext{sut: h, provider: providerA}

	before, statusWire, err := readReloadStatus(t.Context(), h)
	if err != nil {
		failf(t, failureContext{sut: h, provider: providerA, wire: statusWire}, "reload_invalid: read the startup reload status: %v", err)
	}
	if before.LastResult.Category != reloadPublished {
		failf(t, failureContext{sut: h, provider: providerA, wire: statusWire},
			"reload_invalid: startup last result category %q, want %q", before.LastResult.Category, reloadPublished)
	}

	// A request before the rejected reload proves the last good configuration
	// already serves from provider A.
	preWire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, reloadRequestBody())
	if err := checkOpenAICollect(preWire, scriptA, h.evidence.redact); err != nil {
		failf(t, failureContext{sut: h, provider: providerA, wire: preWire},
			"reload_invalid: the request before the reload: %v", err)
	}
	if got := providerA.RequestCount(); got != 1 {
		failf(t, failureContext{sut: h, provider: providerA, wire: preWire},
			"reload_invalid: provider A received %d request(s) before the invalid reload, want exactly 1", got)
	}

	candidate, err := h.CandidateConfig(func(render *sutRender) error {
		render.loggingLevel = "verbose"
		return nil
	})
	if err != nil {
		failf(t, failures, "reload_invalid: render the invalid candidate configuration: %v", err)
	}
	if !strings.Contains(candidate, "level: verbose") {
		failf(t, failures, "reload_invalid: the candidate configuration does not carry the invalid logging level")
	}
	if want := fmt.Sprintf("address: %q", h.addr); !strings.Contains(candidate, want) {
		failf(t, failures, "reload_invalid: the candidate configuration does not keep the started server address %s", want)
	}
	if err := h.ReplaceConfig("reload-invalid-candidate.yaml", candidate); err != nil {
		failf(t, failures, "reload_invalid: atomically replace the fixed configuration path: %v", err)
	}

	reloadWire := doJSON(t.Context(), h.ManagementURL()+configReloadPath, nil)
	failures = failureContext{sut: h, provider: providerA, wire: reloadWire}
	h.evidence.write("reload-invalid-exchange.txt", reloadWire.render(h.evidence.redact))
	writeReloadProviderLogs(h, providerA, nil)
	rejected, err := decodeReloadResult(reloadWire, h.evidence.redact)
	if err != nil {
		failf(t, failures, "reload_invalid: management reload: %v", err)
	}
	if reloadWire.Status != http.StatusUnprocessableEntity {
		failf(t, failures, "reload_invalid: management reload returned status %d, want 422; category %q, reason %q",
			reloadWire.Status, rejected.Category, rejected.ReasonCategory)
	}
	if rejected.Category != reloadInvalid {
		failf(t, failures, "reload_invalid: reload category %q, want %q", rejected.Category, reloadInvalid)
	}
	if rejected.ActiveGeneration != before.ActiveGeneration {
		failf(t, failures, "reload_invalid: the rejected reload reports active generation %d, want the unchanged last good generation %d",
			rejected.ActiveGeneration, before.ActiveGeneration)
	}
	after, afterWire, err := readReloadStatus(t.Context(), h)
	if err != nil {
		failf(t, failureContext{sut: h, provider: providerA, wire: afterWire}, "reload_invalid: read the status after the rejection: %v", err)
	}
	if after.ActiveGeneration != before.ActiveGeneration {
		failf(t, failureContext{sut: h, provider: providerA, wire: afterWire},
			"reload_invalid: status reports active generation %d after the rejection, want the unchanged last good generation %d",
			after.ActiveGeneration, before.ActiveGeneration)
	}
	if after.LastFailure.Category != reloadInvalid {
		failf(t, failureContext{sut: h, provider: providerA, wire: afterWire},
			"reload_invalid: status last failure category %q, want %q", after.LastFailure.Category, reloadInvalid)
	}
	if after.LastSuccess.Category != reloadPublished {
		failf(t, failureContext{sut: h, provider: providerA, wire: afterWire},
			"reload_invalid: status last success category %q, want the unchanged last good %q", after.LastSuccess.Category, reloadPublished)
	}

	// The last good configuration must keep serving from provider A.
	postWire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, reloadRequestBody())
	failures = failureContext{sut: h, provider: providerA, wire: postWire}
	writeReloadProviderLogs(h, providerA, nil)
	if err := checkOpenAICollect(postWire, scriptA, h.evidence.redact); err != nil {
		failf(t, failures, "reload_invalid: the request after the rejection: %v", err)
	}
	if got := providerA.RequestCount(); got != 2 {
		failf(t, failures, "reload_invalid: provider A received %d request(s), want the request before and after the rejection", got)
	}
	requests := providerA.Requests()
	request := requests[len(requests)-1]
	if err := checkTranslatedOpenAIChat(request, reloadNative, openAIChatPath, smokeMessages(), nil, h.evidence.redact); err != nil {
		failf(t, failures, "reload_invalid: provider A did not receive the translated client request after the rejection: %v", err)
	}

	summary := fmt.Sprintf(
		"generation_before=%d generation_after=%d category=%s reason=%s last_success=%s provider_a_requests=%d source_integrity=%s\n",
		before.ActiveGeneration, after.ActiveGeneration, rejected.Category, rejected.ReasonCategory,
		after.LastSuccess.Category, providerA.RequestCount(), after.SourceIntegrity)
	h.evidence.write("reload-invalid.txt", summary)
	h.evidence.write("reload-invalid-client-wire.txt", postWire.render(h.evidence.redact))
	t.Logf("reload invalid passed: category %s, generation stayed %d and provider A kept serving; evidence %s",
		rejected.Category, after.ActiveGeneration, h.evidence.dir)
}
