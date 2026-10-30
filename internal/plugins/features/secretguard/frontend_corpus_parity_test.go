package secretguard_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	feanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	feGemini "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	feopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	feopenai "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	feopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type corpusFrontendCase struct {
	name     string
	value    string
	material func(string) string
	wantRule string
	negative bool
}

func TestSyntheticSecretGuardCorpus_BetterLeaksAttribution(t *testing.T) {
	t.Parallel()
	policy := secretguard.DetectorPolicy{BetterLeaks: secretguard.BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: secretguard.DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    secretguard.DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		MaxFindings:       secretguard.DefaultBetterLeaksMaxFindings,
	}}
	services, err := secretguard.BuildGenerationServices(policy, engine.NewDisabledSource())
	if err != nil {
		failSafeError(t, "building detector services", err)
	}
	guard := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionBlock})
	for _, tc := range testkit.SyntheticSecretGuardCorpus() {
		t.Run(tc.Name, func(t *testing.T) {
			call := corpusCall(tc.Material)
			decision, err := guard.Evaluate(t.Context(), &call, sdk.Meta{}, sdk.Services{Capability: services})
			if err != nil {
				t.Fatalf("corpus case %s returned an unsafe error classification", tc.Name)
			}
			if decision.Outcome != sdk.OutcomeBlock {
				t.Fatalf("corpus case %s was not blocked", tc.Name)
			}
			assertFinding(t, decision, sdk.DetectorIDBetterLeaks, tc.RuleID)
			assertSafeDecision(t, decision)
		})
	}
}

func TestSyntheticSecretGuardCorpus_FrontendParityMatrix(t *testing.T) {
	t.Parallel()
	cases := []corpusFrontendCase{
		{name: "openai", value: testkit.SyntheticOpenAIDetectorKey, wantRule: "openai-api-key", material: textMaterial},
		{name: "anthropic", value: testkit.SyntheticAnthropicDetectorKey, wantRule: "anthropic-api-key", material: textMaterial},
		{name: "github", value: testkit.SyntheticGitHubPAT, wantRule: "github-pat", material: textMaterial},
		{name: "slack", value: testkit.SyntheticSlackBotToken, wantRule: "slack-bot-token", material: textMaterial},
		{name: "stripe", value: testkit.SyntheticStripeTestKey, wantRule: "stripe-access-token", material: textMaterial},
		{name: "aws-multipart", value: testkit.SyntheticAWSAccessKeyID, wantRule: "aws-access-token", material: func(value string) string {
			return "aws_token = \"" + value + "\" aws_secret_access_key = \"" + testkit.SyntheticAWSSecretAccessKey + "\""
		}},
		{name: "generic-api-key", value: testkit.SyntheticGenericDetectorAPIKey, wantRule: "generic-api-key", material: func(value string) string {
			return "api_token = \"" + value + "\""
		}},
		{name: "generic-password", value: testkit.SyntheticGenericPassword, wantRule: "generic-password", material: func(value string) string {
			return "login(user, \"" + value + "\")"
		}},
		{name: "credential-uri", value: testkit.SyntheticCredentialURISecret, wantRule: "generic-credential-uri", material: func(value string) string {
			return "DATABASE_URL=postgresql://app:" + value + "@db.internal/app"
		}},
		{name: "private-key", value: testkit.SyntheticPrivateKey, wantRule: "private-key", material: textMaterial},
		{name: "json-context", value: testkit.SyntheticGitHubPAT, wantRule: "github-pat", material: func(value string) string {
			return `{"credentials":{"github_token":` + mustJSONQuote(value) + `}}`
		}},
		{name: "tool-schema", value: testkit.SyntheticGitHubPAT, wantRule: "github-pat", material: func(value string) string {
			return value
		}},
		{name: "tool-result", value: testkit.SyntheticGitHubPAT, wantRule: "github-pat", material: textMaterial},
		{name: "repeated-overlap", value: testkit.SyntheticOverlapShorter, wantRule: "OVERLAP_TEST", material: func(value string) string {
			return value + " / " + testkit.SyntheticOverlapLonger + " / " + value
		}},
		{name: "allow-marker", value: testkit.SyntheticSlackBotToken, wantRule: "slack-bot-token", material: func(value string) string {
			return value + " // betterleaks:allow"
		}},
		{name: "decoded", value: testkit.SyntheticGitHubPAT, wantRule: "github-pat", material: func(value string) string {
			return "encoded=" + base64.StdEncoding.EncodeToString([]byte(value))
		}},
		{name: "public-negative", negative: true, material: func(string) string {
			return "public documentation: https://example.com/api and request-id=00000000-0000-0000-0000-000000000000"
		}},
	}

	frontends := []string{"openresponses", "openai-responses", "openai-legacy", "anthropic", "gemini"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, frontendID := range frontends {
				t.Run(frontendID, func(t *testing.T) {
					call, err := decodeCorpusFrontendCall(frontendID, tc, tc.material(tc.value))
					if err != nil {
						failSafeError(t, "frontend decode failed", err)
					}
					if err := call.Validate(); err != nil {
						failSafeError(t, "decoded canonical call invalid", err)
					}
					assertCanonicalCorpusRepresentation(t, tc, call)
					before, err := json.Marshal(call)
					if err != nil {
						failSafeError(t, "marshalling decoded canonical call", err)
					}
					decision := evaluateWithBetterLeaks(t, call)
					if tc.name == "repeated-overlap" {
						decision = evaluateWithExactMatcher(t, call, tc.value, tc.wantRule)
					}
					if tc.negative {
						if decision.Outcome != sdk.OutcomePass {
							t.Fatalf("public negative was blocked or logged")
						}
						return
					}
					after, err := json.Marshal(call)
					if err != nil {
						failSafeError(t, "marshalling post-decision canonical call", err)
					}
					if string(after) != string(before) {
						t.Fatalf("block action mutated the decoded canonical call")
					}
					if tc.name == "tool-schema" {
						if decision.Outcome != sdk.OutcomePass || len(decision.Findings) != 0 {
							t.Fatalf("excluded tool definition produced outcome=%q findings=%d", decision.Outcome, len(decision.Findings))
						}
						return
					}
					if decision.Outcome != sdk.OutcomeBlock {
						t.Fatalf("frontend %s did not block corpus case %s", frontendID, tc.name)
					}
					if tc.name == "repeated-overlap" {
						assertExactFinding(t, decision, tc.wantRule)
					} else {
						assertFinding(t, decision, sdk.DetectorIDBetterLeaks, tc.wantRule)
					}
					assertSafeDecision(t, decision)
				})
			}
		})
	}
}

func TestSyntheticSecretGuardCorpus_HybridRepeatedMatchesDeduplicate(t *testing.T) {
	t.Parallel()
	frontends := []string{"openresponses", "openai-responses", "openai-legacy", "anthropic", "gemini"}
	material := testkit.SyntheticGitHubPAT + " / " + testkit.SyntheticGitHubPAT
	cases := []struct {
		name            string
		exactRule       string
		exactValue      string
		wantFindings    int
		wantExactCount  int
		wantBetterCount int
	}{
		{name: "repeated", exactRule: "GITHUB_TOKEN", exactValue: testkit.SyntheticGitHubPAT, wantFindings: 1, wantExactCount: 2},
		{name: "overlap", exactRule: "OVERLAP_TEST", exactValue: testkit.SyntheticGitHubPAT[:24], wantFindings: 2, wantExactCount: 2, wantBetterCount: 2},
	}
	for _, frontendID := range frontends {
		for _, tc := range cases {
			t.Run(frontendID+"/"+tc.name, func(t *testing.T) {
				catalog, err := engine.BuildCatalog([]engine.CatalogInput{{
					Name:           tc.exactRule,
					Value:          tc.exactValue,
					SourceCategory: sdk.SourceCategoryProxyEnv,
				}}, 8)
				if err != nil {
					failSafeError(t, "building hybrid matcher catalog", err)
				}
				call, err := decodeCorpusBody(frontendID, []byte(corpusBody(frontendID, material)))
				if err != nil {
					failSafeError(t, "frontend decode failed", err)
				}
				if err := call.Validate(); err != nil {
					failSafeError(t, "decoded canonical call invalid", err)
				}
				decision := evaluateWithHybrid(t, call, engine.NewStaticMatcherResolver(catalog, engine.MatcherOptions{}))
				if decision.Outcome != sdk.OutcomeBlock {
					t.Fatalf("frontend %s did not block repeated hybrid corpus", frontendID)
				}
				if len(decision.Findings) != tc.wantFindings {
					t.Fatalf("hybrid findings were not bounded/deduplicated (count=%d want=%d)", len(decision.Findings), tc.wantFindings)
				}
				var exactCount, betterCount int
				for _, finding := range decision.Findings {
					switch finding.DetectorID {
					case sdk.DetectorIDExact:
						if finding.OccurrenceCount != tc.wantExactCount {
							t.Fatalf("exact hybrid occurrence count=%d want=%d", finding.OccurrenceCount, tc.wantExactCount)
						}
						exactCount++
					case sdk.DetectorIDBetterLeaks:
						if finding.OccurrenceCount != tc.wantBetterCount {
							t.Fatalf("BetterLeaks hybrid occurrence count=%d want=%d", finding.OccurrenceCount, tc.wantBetterCount)
						}
						betterCount++
					default:
						t.Fatalf("unexpected hybrid detector attribution %q", finding.DetectorID)
					}
				}
				if exactCount != 1 || (tc.wantBetterCount > 0 && betterCount != 1) || (tc.wantBetterCount == 0 && betterCount != 0) {
					t.Fatalf("hybrid detector result counts unexpected (exact=%d betterleaks=%d)", exactCount, betterCount)
				}
				assertSafeDecision(t, decision)
			})
		}
	}
}

func TestSyntheticSecretGuardCorpus_RedactDecodedAndJSONLiterals(t *testing.T) {
	t.Parallel()
	frontends := []string{"openresponses", "openai-responses", "openai-legacy", "anthropic", "gemini"}
	for _, frontendID := range frontends {
		t.Run(frontendID, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				call func() (*aipapi.Call, error)
			}{
				{name: "literal", call: func() (*aipapi.Call, error) {
					return decodeCorpusBody(frontendID, []byte(corpusBody(frontendID, testkit.SyntheticGitHubPAT)))
				}},
				{name: "json-context", call: func() (*aipapi.Call, error) {
					return decodeCorpusJSONContext(frontendID, testkit.SyntheticGitHubPAT)
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					call, err := tc.call()
					if err != nil {
						failSafeError(t, "frontend decode failed", err)
					}
					if err := call.Validate(); err != nil {
						failSafeError(t, "decoded canonical call invalid", err)
					}
					if tc.name == "json-context" && !corpusCallHasJSON(call) {
						t.Fatal("JSON context did not survive frontend decoding as canonical JSON")
					}
					guard := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionRedact})
					services, err := corpusServices()
					if err != nil {
						failSafeError(t, "building detector services", err)
					}
					decision, err := guard.Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{Capability: services})
					if err != nil {
						failSafeError(t, "evaluating redaction decision", err)
					}
					if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount == 0 {
						t.Fatalf("redaction decision did not commit a safe mutation (outcome=%q failure=%q findings=%d)", decision.Outcome, decision.FailureKind, len(decision.Findings))
					}
					if err := call.Validate(); err != nil {
						failSafeError(t, "redacted canonical call invalid", err)
					}
					encoded, err := json.Marshal(call)
					if err != nil {
						failSafeError(t, "marshalling redacted canonical call", err)
					}
					if !json.Valid(encoded) {
						t.Fatal("redacted canonical call is not valid JSON")
					}
					for _, value := range testkit.AllSyntheticSecretGuardValues() {
						if strings.Contains(string(encoded), value) {
							t.Fatalf("redacted canonical JSON retained synthetic secret material")
						}
					}
				})
			}
		})
	}
}

func TestSyntheticSecretGuardCorpus_DecodedRedactionFailsClosed(t *testing.T) {
	t.Parallel()
	frontends := []string{"openresponses", "openai-responses", "openai-legacy", "anthropic", "gemini"}
	encoded := base64.StdEncoding.EncodeToString([]byte(testkit.SyntheticGitHubPAT))
	for _, frontendID := range frontends {
		t.Run(frontendID, func(t *testing.T) {
			call, err := decodeCorpusBody(frontendID, []byte(corpusBody(frontendID, "encoded="+encoded)))
			if err != nil {
				failSafeError(t, "frontend decode failed", err)
			}
			services, err := corpusServices()
			if err != nil {
				failSafeError(t, "building detector services", err)
			}
			before, err := json.Marshal(call)
			if err != nil {
				failSafeError(t, "marshalling pre-decision canonical call", err)
			}
			decision, err := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionRedact}).Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{Capability: services})
			if err != nil {
				failSafeError(t, "evaluating decoded redaction decision", err)
			}
			if decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != secretguard.FailureKindUnrewritableDetectedSecret {
				t.Fatalf("decoded redaction did not fail closed (outcome=%q failure=%q)", decision.Outcome, decision.FailureKind)
			}
			after, err := json.Marshal(call)
			if err != nil {
				failSafeError(t, "marshalling post-decision canonical call", err)
			}
			if string(after) != string(before) {
				t.Fatal("decoded-only redaction mutated the live canonical call")
			}
		})
	}
}

func evaluateWithBetterLeaks(t *testing.T, call *aipapi.Call) sdk.Decision {
	t.Helper()
	services, err := corpusServices()
	if err != nil {
		failSafeError(t, "building detector services", err)
	}
	decision, err := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionBlock}).Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{Capability: services})
	if err != nil {
		failSafeError(t, "evaluating BetterLeaks decision", err)
	}
	return decision
}

func evaluateWithExactMatcher(t *testing.T, call *aipapi.Call, value, ruleID string) sdk.Decision {
	t.Helper()
	decision, err := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionBlock}).Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{
		MatcherResolver: staticResolver{m: &exactStubMatcher{
			secret:  []byte(value),
			refName: ruleID,
			cat:     sdk.SourceCategoryUnknown,
		}},
	})
	if err != nil {
		failSafeError(t, "evaluating exact matcher decision", err)
	}
	return decision
}

func evaluateWithHybrid(t *testing.T, call *aipapi.Call, resolver sdk.MatcherResolver) sdk.Decision {
	t.Helper()
	services, err := corpusServices()
	if err != nil {
		failSafeError(t, "building detector services", err)
	}
	decision, err := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionBlock}).Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{
		Capability:      services,
		MatcherResolver: resolver,
	})
	if err != nil {
		failSafeError(t, "evaluating hybrid decision", err)
	}
	return decision
}

func corpusServices() (*secretguard.GenerationServices, error) {
	return secretguard.BuildGenerationServices(secretguard.DetectorPolicy{BetterLeaks: secretguard.BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: secretguard.DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    secretguard.DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		MaxFindings:       secretguard.DefaultBetterLeaksMaxFindings,
	}}, engine.NewDisabledSource())
}

func corpusCall(material string) aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: material}}}}}
}

func decodeCorpusFrontendCall(frontendID string, tc corpusFrontendCase, material string) (*aipapi.Call, error) {
	var body string
	switch tc.name {
	case "json-context":
		return decodeCorpusJSONContext(frontendID, tc.value)
	case "tool-schema":
		body = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}],"tools":[{"type":"function","function":{"name":"lookup","description":"` + material + `","parameters":{"type":"object","properties":{"token":{"default":"` + tc.value + `"}}}}}]} `
		if frontendID == "openai-responses" {
			body = `{"model":"gpt-4o-mini","input":[{"role":"user","content":"ping"}],"tools":[{"type":"function","function":{"name":"lookup","description":"` + material + `","parameters":{"type":"object","properties":{"token":{"default":"` + tc.value + `"}}}}}]} `
		}
		if frontendID == "openresponses" {
			body = `{"model":"gpt-4o-mini","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}],"tools":[{"type":"function","name":"lookup","description":"` + material + `","parameters":{"type":"object","properties":{"token":{"default":"` + tc.value + `"}}}}]} `
		}
		if frontendID == "anthropic" {
			body = `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}],"tools":[{"name":"lookup","description":"` + material + `","input_schema":{"type":"object","properties":{"token":{"default":"` + tc.value + `"}}}}]}`
		}
		if frontendID == "gemini" {
			body = `{"contents":[{"role":"user","parts":[{"text":"ping"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","description":"` + material + `","parameters":{"type":"object","properties":{"token":{"default":"` + tc.value + `"}}}}]}]}`
		}
	case "tool-result":
		return decodeCorpusToolResult(frontendID, tc.value)
	default:
		body = corpusBody(frontendID, material)
	}
	return decodeCorpusBody(frontendID, []byte(strings.TrimSpace(body)))
}

func decodeCorpusJSONContext(frontendID, value string) (*aipapi.Call, error) {
	quoted := mustJSONQuote(value)
	switch frontendID {
	case "openresponses":
		return decodeCorpusBody(frontendID, []byte(`{"model":"gpt-4o-mini","input":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":`+mustJSONQuote(`{"github_token":`+quoted+`}`)+`}]}`))
	case "openai-responses":
		return decodeCorpusBody(frontendID, []byte(`{"model":"gpt-4o-mini","input":[{"type":"function_call_output","call_id":"call_1","output":{"github_token":`+quoted+`}}]}`))
	case "openai-legacy":
		return decodeCorpusBody(frontendID, []byte(`{"model":"gpt-4o-mini","messages":[{"role":"tool","tool_call_id":"call_1","content":{"github_token":`+quoted+`}}]}`))
	case "anthropic":
		return decodeCorpusBody(frontendID, []byte(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":`+mustJSONQuote(`{"github_token":`+quoted+`}`)+`}]}]}]}`))
	case "gemini":
		return decodeCorpusBody(frontendID, []byte(`{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"github_token":`+quoted+`}}}]}]}`))
	default:
		return nil, fmt.Errorf("unknown frontend %s", frontendID)
	}
}

func corpusBody(frontendID, material string) string {
	quoted := mustJSONQuote(material)
	switch frontendID {
	case "openresponses":
		return `{"model":"gpt-4o-mini","input":[{"type":"message","role":"user","content":` + quoted + `}]}`
	case "openai-responses":
		return `{"model":"gpt-4o-mini","input":[{"role":"user","content":` + quoted + `}]}`
	case "openai-legacy":
		return `{"model":"gpt-4o-mini","messages":[{"role":"user","content":` + quoted + `}]}`
	case "anthropic":
		return `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":` + quoted + `}]}`
	case "gemini":
		return `{"contents":[{"role":"user","parts":[{"text":` + quoted + `}]}]}`
	default:
		return ""
	}
}

func decodeCorpusToolResult(frontendID, value string) (*aipapi.Call, error) {
	quoted := mustJSONQuote(value)
	switch frontendID {
	case "openresponses":
		return decodeCorpusBody(frontendID, []byte(`{"model":"gpt-4o-mini","input":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":`+quoted+`}]}`))
	case "openai-responses":
		return decodeCorpusBody(frontendID, []byte(`{"model":"gpt-4o-mini","input":[{"type":"function_call_output","call_id":"call_1","output":`+quoted+`}]}`))
	case "openai-legacy":
		return decodeCorpusBody(frontendID, []byte(`{"model":"gpt-4o-mini","messages":[{"role":"tool","tool_call_id":"call_1","content":`+quoted+`}]}`))
	case "anthropic":
		return decodeCorpusBody(frontendID, []byte(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":`+quoted+`}]}]}`))
	case "gemini":
		return decodeCorpusBody(frontendID, []byte(`{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":`+quoted+`}}]}]}`))
	default:
		return nil, fmt.Errorf("unknown frontend %s", frontendID)
	}
}

func decodeCorpusBody(frontendID string, body []byte) (*aipapi.Call, error) {
	switch frontendID {
	case "openresponses":
		d, err := feopenresponses.AuthenticateAndDecodeCreate(context.Background(), body, feopenresponses.DecodeCreateOptions{RouteSelector: "openresponses:gpt-4o-mini"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	case "openai-responses":
		d, err := feopenai.DecodeCreateRequest(body, feopenai.DecodeOptions{RouteSelector: "openai-responses:gpt-4o-mini"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	case "openai-legacy":
		d, err := feopenailegacy.DecodeChatRequest(body, feopenailegacy.DecodeOptions{RouteSelector: "openai-legacy:gpt-4o-mini"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	case "anthropic":
		d, err := feanthropic.DecodeMessageRequest(body, feanthropic.DecodeOptions{RouteSelector: "anthropic:claude-3-5-haiku-20241022"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	case "gemini":
		d, err := feGemini.DecodeGenerateContentRequest(body, feGemini.DecodeOptions{RouteSelector: "gemini:gemini-2.0-flash", Model: "gemini-2.0-flash"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	default:
		return nil, fmt.Errorf("unknown frontend %s", frontendID)
	}
}

func textMaterial(value string) string { return "credential=" + value }

func mustJSONQuote(value string) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func assertFinding(t *testing.T, decision sdk.Decision, detectorID, ruleID string) {
	t.Helper()
	for _, finding := range decision.Findings {
		if finding.DetectorID == detectorID && finding.RuleID == ruleID {
			return
		}
	}
	t.Fatalf("missing detector attribution (detector=%q rule=%q finding_count=%d)", detectorID, ruleID, len(decision.Findings))
}

func assertExactFinding(t *testing.T, decision sdk.Decision, ruleID string) {
	t.Helper()
	for _, finding := range decision.Findings {
		if finding.RuleID == ruleID || finding.SecretRefName == ruleID {
			if finding.DetectorID != "" && finding.DetectorID != sdk.DetectorIDExact {
				t.Fatalf("unexpected detector attribution for explicit exact scenario (detector=%q rule=%q)", finding.DetectorID, ruleID)
			}
			return
		}
	}
	t.Fatalf("missing exact attribution (rule=%q finding_count=%d)", ruleID, len(decision.Findings))
}

func assertCanonicalCorpusRepresentation(t *testing.T, tc corpusFrontendCase, call *aipapi.Call) {
	t.Helper()
	switch tc.name {
	case "json-context":
		if !corpusCallHasJSON(call) {
			t.Fatal("JSON context case did not survive frontend decoding as canonical JSON")
		}
	case "tool-schema":
		if len(call.Tools) == 0 {
			t.Fatal("tool schema case did not survive frontend decoding")
		}
	case "tool-result":
		if corpusCallHasItem(call, aipapi.ItemKindToolResult) {
			return
		}
		if !corpusCallHasPart(call, aipapi.PartToolResult) {
			t.Fatal("tool result case did not survive frontend decoding")
		}
	}
}

func corpusCallHasPart(call *aipapi.Call, kind aipapi.PartKind) bool {
	if call == nil {
		return false
	}
	for _, message := range append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...) {
		for _, part := range message.Parts {
			if part.Kind == kind {
				return true
			}
		}
	}
	return false
}

func corpusCallHasItem(call *aipapi.Call, kind aipapi.ItemKind) bool {
	if call == nil {
		return false
	}
	for _, item := range call.Items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func corpusCallHasJSON(call *aipapi.Call) bool {
	if call == nil {
		return false
	}
	for _, item := range call.Items {
		switch item.Kind {
		case aipapi.ItemKindMessage:
			for _, part := range item.Content {
				if part.Kind == aipapi.ContentPartJSON && json.Valid([]byte(part.Text)) {
					return true
				}
			}
		case aipapi.ItemKindToolCall:
			if item.ToolCall != nil && json.Valid(item.ToolCall.Arguments) {
				return true
			}
		case aipapi.ItemKindToolResult:
			if item.ToolResult == nil {
				continue
			}
			if json.Valid([]byte(item.ToolResult.Output)) {
				return true
			}
			for _, part := range item.ToolResult.Parts {
				if part.Kind == aipapi.ContentPartJSON && json.Valid([]byte(part.Text)) {
					return true
				}
			}
		}
	}
	for _, message := range append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...) {
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartJSON && json.Valid(part.Content) {
				return true
			}
			if part.Kind == aipapi.PartToolResult && json.Valid(part.Content) {
				return true
			}
			if part.Kind == aipapi.PartToolResult && json.Valid([]byte(part.Text)) {
				return true
			}
		}
	}
	return false
}

func assertSafeDecision(t *testing.T, decision sdk.Decision) {
	t.Helper()
	encoded, err := json.Marshal(decision)
	if err != nil {
		failSafeError(t, "marshalling decision metadata", err)
	}
	for _, value := range testkit.AllSyntheticSecretGuardValues() {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("decision metadata retained synthetic secret material")
		}
	}
}

func failSafeError(t *testing.T, context string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s", context)
	}
	// Keep failure output bounded and independent of decoder/scanner error text.
	t.Fatalf("%s (error_type=%T)", context, err)
}
