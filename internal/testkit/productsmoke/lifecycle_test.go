//go:build productsmoke

package productsmoke

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestProduct_Lifecycle covers operating the running process.
func TestProduct_Lifecycle(t *testing.T) {
	t.Run("canary_credential", testCanaryCredential)
	t.Run("reload_valid", testReloadValid)
	t.Run("reload_invalid", testReloadInvalid)
}

// testCanaryCredential proves the configured backend credential reaches the
// provider and never appears in a client body or header, the config file or the
// proxy log.
func testCanaryCredential(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	provider := newOpenAILegacyProvider(t, canary, smokeScript())
	h := startSUT(t, sutConfig{
		Canary:    canary,
		Frontends: []string{"openai-legacy"},
		Backends: []backendWiring{{
			ID:      smokeBackendID,
			Kind:    kindOpenAILegacyCompatible,
			BaseURL: provider.OpenAILegacyBaseURL(),
			Models:  []modelWiring{{Canonical: smokeCanonical, Native: smokeNative}},
		}},
	})
	wire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, smokeRequestBody(true))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkOpenAIStream(wire, smokeScript(), h.evidence.redact); err != nil {
		failf(t, failures, "canary credential: %v", err)
	}

	requests := provider.Requests()
	if len(requests) != 1 {
		failf(t, failures, "provider received %d requests, want exactly 1", len(requests))
	}
	request := requests[0]
	if !request.KeyMatched {
		failf(t, failures, "the canary did not reach the provider in %s", request.KeyHeader)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+canary {
		failf(t, failures, "the provider Authorization header did not carry the canary (value withheld)")
	}
	if err := wire.canaryAbsenceError(canary); err != nil {
		failf(t, failures, "canary credential: %v", err)
	}
	configBytes, err := os.ReadFile(h.configPath)
	if err != nil {
		failf(t, failures, "read proxy config: %v", err)
	}
	if strings.Contains(string(configBytes), canary) {
		failf(t, failures, "the canary appeared in the proxy config file instead of only in the backend credential environment")
	}

	// Stop before inspecting the log so shutdown lines are included, and prove
	// the bounded stop contract on the way.
	if err := h.Stop(); err != nil {
		failf(t, failures, "stop the proxy: %v", err)
	}
	if h.Alive() {
		failf(t, failures, "the proxy process was still alive after Stop returned")
	}
	h.logs.Flush()
	if leaks := h.logs.Redactions(); leaks != 0 {
		failf(t, failures, "the proxy log contained the canary %d time(s); first redacted region: %s", leaks, h.logs.FirstLeak())
	}
	logBytes, err := os.ReadFile(h.evidence.path("proxy.log"))
	if err != nil {
		failf(t, failures, "read captured proxy log: %v", err)
	}
	if strings.Contains(string(logBytes), canary) {
		failf(t, failures, "the canary appeared in the captured proxy log")
	}

	summary := fmt.Sprintf(
		"provider_header=%s matched=true client_body_contains_canary=false client_headers_contain_canary=false config_contains_canary=false log_contains_canary=false\n",
		request.KeyHeader)
	h.evidence.write("canary-check.txt", summary)
	t.Logf("canary credential passed: provider saw the key in %s; evidence %s", request.KeyHeader, h.evidence.dir)
}
