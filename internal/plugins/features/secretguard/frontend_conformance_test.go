package secretguard_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	feanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	fegemini "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	feopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	feopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type exactStubMatcher struct {
	secret  []byte
	refName string
	cat     sdk.SourceCategory
}

func (m *exactStubMatcher) ScanBytes(_ context.Context, input []byte) ([]sdk.Finding, error) {
	n := countOccurrences(input, m.secret)
	if n == 0 {
		return nil, nil
	}
	return []sdk.Finding{{
		SecretRefName:   m.refName,
		SourceCategory:  m.cat,
		OccurrenceCount: n,
	}}, nil
}

func (m *exactStubMatcher) ScanString(ctx context.Context, input string) ([]sdk.Finding, error) {
	return m.ScanBytes(ctx, []byte(input))
}

func (m *exactStubMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	findings, err := m.ScanBytes(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	if len(findings) == 0 {
		return append([]byte(nil), input...), nil, nil
	}
	mask := bytes.Repeat([]byte("*"), len(m.secret))
	return bytes.ReplaceAll(input, m.secret, mask), findings, nil
}

func (m *exactStubMatcher) RedactString(ctx context.Context, input string) (string, []sdk.Finding, error) {
	out, findings, err := m.RedactBytes(ctx, []byte(input))
	return string(out), findings, err
}

type staticResolver struct{ m sdk.Matcher }

func (r staticResolver) Resolve(context.Context) (sdk.Matcher, error) { return r.m, nil }

func countOccurrences(haystack, needle []byte) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return 0
	}
	n := 0
	for i := 0; ; {
		j := bytes.Index(haystack[i:], needle)
		if j < 0 {
			return n
		}
		n++
		i += j + len(needle)
	}
}

type frontendConformanceOutcome struct {
	Outcome         sdk.Outcome
	SecretRefName   string
	OccurrenceCount int
}

func frontendConformanceResult(frontendID, location string) (frontendConformanceOutcome, error) {
	call, err := decodeFrontendCall(frontendID, location, testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		return frontendConformanceOutcome{}, err
	}
	if err := call.Validate(); err != nil {
		return frontendConformanceOutcome{}, fmt.Errorf("%s validate: %w", frontendID, err)
	}
	guard := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionBlock})
	decision, err := guard.Evaluate(context.Background(), call, sdk.Meta{}, sdk.Services{
		MatcherResolver: staticResolver{m: &exactStubMatcher{
			secret:  []byte(testkit.SyntheticOpenAIAPIKey),
			refName: "OPENAI_API_KEY",
			cat:     sdk.SourceCategoryProxyEnv,
		}},
	})
	if err != nil {
		return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: %w", frontendID, location, err)
	}
	if !frontendFieldEligible(location) {
		if decision.Outcome != sdk.OutcomePass || len(decision.Findings) != 0 {
			return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: excluded field produced outcome=%q findings=%d", frontendID, location, decision.Outcome, len(decision.Findings))
		}
		return frontendConformanceOutcome{Outcome: sdk.OutcomePass}, nil
	}
	if decision.Outcome != sdk.OutcomeBlock {
		return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: outcome=%q", frontendID, location, decision.Outcome)
	}
	if len(decision.Findings) != 1 {
		return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: findings=%d", frontendID, location, len(decision.Findings))
	}
	got := decision.Findings[0]
	if got.SecretRefName != "OPENAI_API_KEY" {
		return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: ref=%q", frontendID, location, got.SecretRefName)
	}
	if got.OccurrenceCount != 1 {
		return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: occurrences=%d", frontendID, location, got.OccurrenceCount)
	}
	if got.Location == "" {
		return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: empty finding location", frontendID, location)
	}
	if strings.Contains(got.SecretRefName, testkit.SyntheticOpenAIAPIKey) || strings.Contains(got.Location, testkit.SyntheticOpenAIAPIKey) {
		return frontendConformanceOutcome{}, fmt.Errorf("%s/%s: secret leaked into finding metadata", frontendID, location)
	}
	return frontendConformanceOutcome{
		Outcome:         decision.Outcome,
		SecretRefName:   got.SecretRefName,
		OccurrenceCount: got.OccurrenceCount,
	}, nil
}

func frontendFieldEligible(location string) bool {
	switch location {
	case "message_text", "tool_role_text", "tool_result":
		return true
	default:
		return false
	}
}

func TestFrontendFieldCoverageMatrix_conformance(t *testing.T) {
	t.Parallel()
	frontends := []string{"openai-responses", "openai-legacy", "anthropic", "gemini"}
	locations := append([]string(nil), frontendFieldCoverageMatrix["openai-responses"]...)
	for _, location := range locations {
		t.Run(location, func(t *testing.T) {
			t.Parallel()
			var baseline frontendConformanceOutcome
			for i, frontendID := range frontends {
				got, err := frontendConformanceResult(frontendID, location)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					baseline = got
					continue
				}
				if got.Outcome != baseline.Outcome || got.SecretRefName != baseline.SecretRefName || got.OccurrenceCount != baseline.OccurrenceCount {
					t.Fatalf("%s/%s drift: got %#v want %#v", frontendID, location, got, baseline)
				}
			}
		})
	}
}

func TestFrontendFieldCoverageMatrix_repeatedHistory(t *testing.T) {
	t.Parallel()
	for frontendID := range frontendFieldCoverageMatrix {
		t.Run(frontendID, func(t *testing.T) {
			t.Parallel()
			call, err := decodeRepeatedHistoryCall(frontendID, testkit.SyntheticOpenAIAPIKey)
			if err != nil {
				t.Fatal(err)
			}
			guard := secretguard.NewGuard(secretguard.Config{Action: secretguard.ActionBlock})
			decision, err := guard.Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{
				MatcherResolver: staticResolver{m: &exactStubMatcher{
					secret:  []byte(testkit.SyntheticOpenAIAPIKey),
					refName: "OPENAI_API_KEY",
					cat:     sdk.SourceCategoryProxyEnv,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Outcome != sdk.OutcomeBlock {
				t.Fatalf("outcome=%q", decision.Outcome)
			}
			if len(decision.Findings) == 0 {
				t.Fatal("missing repeated history findings")
			}
			total := 0
			for _, finding := range decision.Findings {
				if finding.SecretRefName != "OPENAI_API_KEY" {
					t.Fatalf("unexpected ref name in repeated history: %#v", finding)
				}
				total += finding.OccurrenceCount
			}
			if total != 2 {
				t.Fatalf("repeated history occurrence count: %d findings=%#v", total, decision.Findings)
			}
		})
	}
}

func TestFrontendFieldCoverageMatrix_protocolNativeErrors(t *testing.T) {
	t.Parallel()
	bad := []byte(`{"`)
	tests := []struct {
		frontendID string
		decodeErr  func() error
	}{
		{
			frontendID: "openai-responses",
			decodeErr: func() error {
				_, err := feopenairesponses.DecodeCreateRequest(bad, feopenairesponses.DecodeOptions{RouteSelector: "openai-responses:gpt-4o-mini"})
				return err
			},
		},
		{
			frontendID: "openai-legacy",
			decodeErr: func() error {
				_, err := feopenailegacy.DecodeChatRequest(bad, feopenailegacy.DecodeOptions{RouteSelector: "openai-legacy:gpt-4o-mini"})
				return err
			},
		},
		{
			frontendID: "anthropic",
			decodeErr: func() error {
				_, err := feanthropic.DecodeMessageRequest(bad, feanthropic.DecodeOptions{RouteSelector: "anthropic:claude-3-5-haiku-20241022"})
				return err
			},
		},
		{
			frontendID: "gemini",
			decodeErr: func() error {
				_, err := fegemini.DecodeGenerateContentRequest(bad, fegemini.DecodeOptions{RouteSelector: "gemini:gemini-2.0-flash", Model: "gemini-2.0-flash"})
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.frontendID, func(t *testing.T) {
			t.Parallel()
			err := tc.decodeErr()
			if err == nil {
				t.Fatal("expected native decode error")
			}
			expectedPrefix := tc.frontendID + ":"
			if tc.frontendID == "openai-responses" {
				expectedPrefix = "openairesponses:"
			}
			if tc.frontendID == "openai-legacy" {
				expectedPrefix = "openailegacy:"
			}
			if !strings.HasPrefix(err.Error(), expectedPrefix) {
				t.Fatalf("error prefix=%q want %q", err.Error(), expectedPrefix)
			}
			for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
				if strings.Contains(err.Error(), needle) {
					t.Fatalf("decode error leaked synthetic secret substring %q", needle)
				}
			}
		})
	}
}

func decodeFrontendCall(frontendID, location, secret string) (*aipapi.Call, error) {
	switch frontendID {
	case "openai-responses":
		return decodeOpenAIResponsesCall(location, secret)
	case "openai-legacy":
		return decodeOpenAILegacyCall(location, secret)
	case "anthropic":
		return decodeAnthropicCall(location, secret)
	case "gemini":
		return decodeGeminiCall(location, secret)
	default:
		return nil, fmt.Errorf("unknown frontend %q", frontendID)
	}
}

func decodeRepeatedHistoryCall(frontendID, secret string) (*aipapi.Call, error) {
	switch frontendID {
	case "openai-responses":
		body := fmt.Sprintf(`{"model":"gpt-4o-mini","input":[{"role":"user","content":"%s"},{"role":"assistant","content":"ack"},{"role":"user","content":"again %s"}]}`, secret, secret)
		d, err := feopenairesponses.DecodeCreateRequest([]byte(body), feopenairesponses.DecodeOptions{RouteSelector: "openai-responses:gpt-4o-mini"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	case "openai-legacy":
		body := fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"%s"},{"role":"assistant","content":"ack"},{"role":"user","content":"again %s"}]}`, secret, secret)
		d, err := feopenailegacy.DecodeChatRequest([]byte(body), feopenailegacy.DecodeOptions{RouteSelector: "openai-legacy:gpt-4o-mini"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	case "anthropic":
		body := fmt.Sprintf(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"%s"},{"role":"assistant","content":"ack"},{"role":"user","content":"again %s"}]}`, secret, secret)
		d, err := feanthropic.DecodeMessageRequest([]byte(body), feanthropic.DecodeOptions{RouteSelector: "anthropic:claude-3-5-haiku-20241022"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	case "gemini":
		body := fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"%s"}]},{"role":"model","parts":[{"text":"ack"}]},{"role":"user","parts":[{"text":"again %s"}]}]}`, secret, secret)
		d, err := fegemini.DecodeGenerateContentRequest([]byte(body), fegemini.DecodeOptions{RouteSelector: "gemini:gemini-2.0-flash", Model: "gemini-2.0-flash"})
		if err != nil {
			return nil, err
		}
		return d.Call, nil
	default:
		return nil, fmt.Errorf("unknown frontend %q", frontendID)
	}
}

func decodeOpenAIResponsesCall(location, secret string) (*aipapi.Call, error) {
	var body string
	switch location {
	case "instructions":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","instructions":"%s","input":[{"role":"user","content":"ping"}]}`, secret)
	case "message_text":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","input":[{"role":"user","content":"%s"}]}`, secret)
	case "tool_call_arguments":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","input":[{"type":"function_call","call_id":"call_1","name":"fn","arguments":{"token":"%s"}}]}`, secret)
	case "tool_role_text":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","input":[{"role":"tool","content":"%s"}]}`, secret)
	case "tool_result":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","input":[{"type":"function_call_output","call_id":"call_1","output":{"token":"%s"}}]}`, secret)
	case "tool_description_schema":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","input":[{"role":"user","content":"ping"}],"tools":[{"type":"function","function":{"name":"fn","description":"uses secret","parameters":{"type":"object","properties":{"token":{"default":"%s"}}}}}]}`, secret)
	default:
		return nil, fmt.Errorf("openai-responses: unsupported location %q", location)
	}
	d, err := feopenairesponses.DecodeCreateRequest([]byte(body), feopenairesponses.DecodeOptions{RouteSelector: "openai-responses:gpt-4o-mini"})
	if err != nil {
		return nil, err
	}
	return d.Call, nil
}

func decodeOpenAILegacyCall(location, secret string) (*aipapi.Call, error) {
	var body string
	switch location {
	case "instructions":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"system","content":"%s"},{"role":"user","content":"ping"}]}`, secret)
	case "message_text":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"%s"}]}`, secret)
	case "tool_call_arguments":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"assistant","function_call":{"name":"fn","arguments":{"token":"%s"}}}]}`, secret)
	case "tool_role_text":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"tool","tool_call_id":"call_1","content":"%s"}]}`, secret)
	case "tool_result":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"tool","tool_call_id":"call_1","content":{"token":"%s"}}]}`, secret)
	case "tool_description_schema":
		body = fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}],"tools":[{"function":{"name":"fn","description":"uses secret","parameters":{"type":"object","properties":{"token":{"default":"%s"}}}}}]}`, secret)
	default:
		return nil, fmt.Errorf("openailegacy: unsupported location %q", location)
	}
	d, err := feopenailegacy.DecodeChatRequest([]byte(body), feopenailegacy.DecodeOptions{RouteSelector: "openai-legacy:gpt-4o-mini"})
	if err != nil {
		return nil, err
	}
	return d.Call, nil
}

func decodeAnthropicCall(location, secret string) (*aipapi.Call, error) {
	var body string
	switch location {
	case "instructions":
		body = fmt.Sprintf(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"system":"%s","messages":[{"role":"user","content":"ping"}]}`, secret)
	case "message_text":
		body = fmt.Sprintf(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"%s"}]}`, secret)
	case "tool_call_arguments":
		body = fmt.Sprintf(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"fn","input":{"token":"%s"}}]}]}`, secret)
	case "tool_role_text":
		body = fmt.Sprintf(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"fn","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"%s"}]}]}`, secret)
	case "tool_result":
		body = fmt.Sprintf(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"fn","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"%s"}]}]}]}`, secret)
	case "tool_description_schema":
		body = fmt.Sprintf(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}],"tools":[{"name":"fn","description":"uses secret","input_schema":{"type":"object","properties":{"token":{"default":"%s"}}}}]}`, secret)
	default:
		return nil, fmt.Errorf("anthropic: unsupported location %q", location)
	}
	d, err := feanthropic.DecodeMessageRequest([]byte(body), feanthropic.DecodeOptions{RouteSelector: "anthropic:claude-3-5-haiku-20241022"})
	if err != nil {
		return nil, err
	}
	return d.Call, nil
}

func decodeGeminiCall(location, secret string) (*aipapi.Call, error) {
	var body string
	switch location {
	case "instructions":
		body = fmt.Sprintf(`{"systemInstruction":{"role":"user","parts":[{"text":"%s"}]},"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`, secret)
	case "message_text":
		body = fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"%s"}]}]}`, secret)
	case "tool_call_arguments":
		body = fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"ping"}]},{"role":"model","parts":[{"functionCall":{"name":"fn","args":{"token":"%s"}}}]}]}`, secret)
	case "tool_role_text":
		body = fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"ping"}]},{"role":"user","parts":[{"functionResponse":{"name":"fn","response":"%s"}}]}]}`, secret)
	case "tool_result":
		body = fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"ping"}]},{"role":"user","parts":[{"functionResponse":{"name":"fn","response":{"token":"%s"}}}]}]}`, secret)
	case "tool_description_schema":
		body = fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"ping"}]}],"tools":[{"functionDeclarations":[{"name":"fn","description":"uses secret","parameters":{"type":"object","properties":{"token":{"default":"%s"}}}}]}]}`, secret)
	default:
		return nil, fmt.Errorf("gemini: unsupported location %q", location)
	}
	d, err := fegemini.DecodeGenerateContentRequest([]byte(body), fegemini.DecodeOptions{RouteSelector: "gemini:gemini-2.0-flash", Model: "gemini-2.0-flash"})
	if err != nil {
		return nil, err
	}
	return d.Call, nil
}
