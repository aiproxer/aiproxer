package conformance

import "slices"

// ExpectedMigrationGoldenJSON lists migration parity JSON under testdata/migration/
// (Req. 15.13). Keep in sync with docs/release-gates.md.
var ExpectedMigrationGoldenJSON = []string{
	"anthropic_messages_nonstream.json",
	"openai_responses_http_nonstream.json",
	"openai_responses_http_streaming.json",
}

// ParitySuiteGoFiles lists parity suite sources (.kiro/specs/archive/llm-api-parity/tasks.md Phase 5).
// Keep in sync with testdata/migration/README.md and docs/release-gates.md.
var ParitySuiteGoFiles = []string{
	"parity_openai_test.go",
	"parity_anthropic_test.go",
	"parity_gemini_test.go",
	"parity_bedrock_test.go",
	"connector_host_test.go",
}

// ParityProtocolEvidence maps every bundled frontend/backend protocol id to at least one
// parity suite source file in this package (llm-api-parity P5.1). Shared families may
// list the same file for multiple ids. OpenResponses evidence spans the explicit
// frontend row and backend column conformance suites; the OpenRouter/NVIDIA
// compatibility identities reuse the configured provider-mode row proof.
var ParityProtocolEvidence = map[string][]string{
	"openai-responses": {"parity_openai_test.go"},
	"openai-legacy":    {"parity_openai_test.go"},
	"anthropic":        {"parity_anthropic_test.go"},
	"gemini":           {"parity_gemini_test.go"},
	"bedrock":          {"parity_bedrock_test.go"},
	"acp":              {"connector_host_test.go"},
	"openresponses":    {"sentinel_test.go"},
	"openrouter":       {"sentinel_test.go"},
	"nvidia":           {"sentinel_test.go"},
}

// AllBundledProtocolIDs returns the sorted union of bundled frontend and backend protocol ids.
func AllBundledProtocolIDs() []string {
	m := map[string]struct{}{}
	for _, id := range BundledFrontendIDs() {
		m[id] = struct{}{}
	}
	for _, id := range BundledBackendIDs() {
		m[id] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
