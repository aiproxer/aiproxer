package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// exactStubMatcher is a test-only Matcher: exact match + redact with '*'.
type exactStubMatcher struct {
	secret  []byte
	refName string
	cat     sdk.SourceCategory
}

func newExactStub(secret, refName string, cat sdk.SourceCategory) *exactStubMatcher {
	return &exactStubMatcher{
		secret:  []byte(secret),
		refName: refName,
		cat:     cat,
	}
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

type nilResolver struct{}

func (nilResolver) Resolve(context.Context) (sdk.Matcher, error) { return nil, nil }

type failingRedactMatcher struct {
	match string
	err   error
}

func (m *failingRedactMatcher) ScanBytes(ctx context.Context, input []byte) ([]sdk.Finding, error) {
	return m.ScanString(ctx, string(input))
}

func (m *failingRedactMatcher) ScanString(_ context.Context, input string) ([]sdk.Finding, error) {
	if !strings.Contains(input, m.match) {
		return nil, nil
	}
	return []sdk.Finding{{
		SecretRefName:   "MATCHED_SECRET",
		SourceCategory:  sdk.SourceCategoryProxyEnv,
		OccurrenceCount: 1,
	}}, nil
}

func (m *failingRedactMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	redacted, findings, err := m.RedactString(ctx, string(input))
	return []byte(redacted), findings, err
}

func (m *failingRedactMatcher) RedactString(_ context.Context, input string) (string, []sdk.Finding, error) {
	if !strings.Contains(input, m.match) {
		return input, nil, nil
	}
	return input, nil, m.err
}

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

func assertNoSyntheticSecrets(t *testing.T, s string) {
	t.Helper()
	for _, v := range testkit.AllSyntheticSecretGuardValues() {
		if v != "" && strings.Contains(s, v) {
			t.Fatalf("secret material leaked into diagnostic string (len=%d)", len(s))
		}
	}
}

func baseCall() aipapi.Call {
	return aipapi.Call{
		ID:      "call-1",
		Session: aipapi.SessionRef{ClientSessionID: "sess-keep"},
		Route:   aipapi.RouteIntent{Selector: "model/keep"},
		Instructions: []aipapi.Message{{
			Role: aipapi.RoleSystem,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartText, Text: "instr clean"},
			},
		}},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartText, Text: "msg clean"},
			},
		}},
		Options: aipapi.GenerationOptions{ReasoningEffort: "keep"},
		Extensions: map[string]json.RawMessage{
			"keep": json.RawMessage(`{"secret_field":"ignored"}`),
		},
	}
}

func cleanStressCall() aipapi.Call {
	return aipapi.Call{
		ID:      "call-clean",
		Session: aipapi.SessionRef{ClientSessionID: "sess-clean"},
		Route:   aipapi.RouteIntent{Selector: "model/clean"},
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("alpha"), aipapi.TextPart("beta")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("gamma")}},
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("delta"), aipapi.TextPart("epsilon")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("zeta")}},
		},
		Tools: []aipapi.ToolDef{
			{Name: "tool_alpha", Description: "clean alpha", Parameters: json.RawMessage(`{"a":1,"b":["x","y"]}`)},
			{Name: "tool_beta", Description: "clean beta", Parameters: json.RawMessage(`{"nested":{"ok":true}}`)},
		},
		Options: aipapi.GenerationOptions{ReasoningEffort: "keep"},
		Extensions: map[string]json.RawMessage{
			"a": json.RawMessage(`{"x":1,"y":2}`),
			"b": json.RawMessage(`["one","two","three"]`),
		},
	}
}

func servicesWith(m sdk.Matcher) sdk.Services {
	return sdk.Services{MatcherResolver: staticResolver{m: m}}
}

func newProvenanceBetterLeaksServices(t *testing.T) *GenerationServices {
	t.Helper()
	services, err := BuildGenerationServices(DetectorPolicy{BetterLeaks: BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           1,
		MaxFindings:       DefaultBetterLeaksMaxFindings,
	}}, engine.NewDisabledSource())
	if err != nil {
		t.Fatalf("build BetterLeaks services: error type %T", err)
	}
	return services
}

func provenanceServices(mode string, exact sdk.Matcher, betterLeaks *GenerationServices) sdk.Services {
	switch mode {
	case "exact":
		return servicesWith(exact)
	case "betterleaks":
		return sdk.Services{Capability: betterLeaks}
	case "hybrid":
		return sdk.Services{MatcherResolver: staticResolver{m: exact}, Capability: betterLeaks}
	default:
		panic("unknown provenance detector mode")
	}
}

func validateProvenanceCall(t *testing.T, call *aipapi.Call) {
	t.Helper()
	if err := call.Validate(); err != nil {
		assertNoSyntheticSecrets(t, err.Error())
		t.Fatalf("invalid provenance fixture: error type %T", err)
	}
}

func provenanceMessageCall(secret string) aipapi.Call {
	return aipapi.Call{
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "instruction " + secret}},
		}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				{Kind: aipapi.PartText, Text: "prompt " + secret},
				{Kind: aipapi.PartJSON, Content: json.RawMessage(fmt.Sprintf(`{"api_key":%q}`, secret))},
				// Tool metadata cannot override trusted user provenance.
				{Kind: aipapi.PartJSON, ToolCallID: "model-call", ToolName: "lookup", Content: json.RawMessage(fmt.Sprintf(`{"argument":%q}`, secret))},
			}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "assistant " + secret}, {Kind: aipapi.PartJSON, ToolCallID: "assistant-call", ToolName: "lookup", Content: json.RawMessage(fmt.Sprintf(`{"argument":%q}`, secret))}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{
				{Kind: aipapi.PartText, Text: "tool output " + secret},
				{Kind: aipapi.PartJSON, ToolCallID: "model-call", ToolName: "lookup", Content: json.RawMessage(fmt.Sprintf(`{"result":%q}`, secret))},
			}},
		},
		Tools: []aipapi.ToolDef{{
			Name:        "lookup-" + secret,
			Description: "tool definition " + secret,
			Parameters:  json.RawMessage(fmt.Sprintf(`{"default":%q}`, secret)),
		}},
	}
}

func provenanceItemCall(secret string) aipapi.Call {
	return aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "assistant history " + secret}}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "item prompt " + secret},
			{Kind: aipapi.ContentPartJSON, Text: fmt.Sprintf(`{"api_key":%q}`, secret)},
		}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleTool, Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: "item tool message " + secret},
			{Kind: aipapi.ContentPartJSON, Text: fmt.Sprintf(`{"result":%q}`, secret)},
		}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{
			CallID: "model-call", Name: "lookup", Arguments: json.RawMessage(fmt.Sprintf(`{"argument":%q}`, secret)),
		}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
			CallID: "model-call", Name: "lookup", Output: fmt.Sprintf(`{"output":%q}`, secret),
		}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{
			CallID: "result-call", Name: "lookup", Arguments: json.RawMessage(`{"request":"result"}`),
		}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{
			CallID: "result-call", Name: "lookup", Parts: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "item tool output " + secret},
				{Kind: aipapi.ContentPartJSON, Text: fmt.Sprintf(`{"result":%q}`, secret)},
			},
		}},
	}, Tools: []aipapi.ToolDef{{Name: "lookup-" + secret, Description: "tool definition " + secret, Parameters: json.RawMessage(fmt.Sprintf(`{"default":%q}`, secret))}}}
}

func provenanceExcludedOnlyCall(authority string, secret string) aipapi.Call {
	if authority == "items" {
		return aipapi.Call{Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "assistant " + secret}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartJSON, Text: fmt.Sprintf(`{"system":%q}`, secret)}}},
			{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "model-call", Name: "lookup", Arguments: json.RawMessage(fmt.Sprintf(`{"argument":%q}`, secret))}},
		}, Tools: []aipapi.ToolDef{{Name: "lookup-" + secret, Description: "tool definition " + secret, Parameters: json.RawMessage(fmt.Sprintf(`{"default":%q}`, secret))}}}
	}
	return aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "instruction " + secret}}}},
		Messages:     []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "model-call", ToolName: "lookup", Content: json.RawMessage(fmt.Sprintf(`{"argument":%q}`, secret))}}}, {Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "assistant " + secret}}}},
		Tools:        []aipapi.ToolDef{{Name: "lookup-" + secret, Description: "tool definition " + secret, Parameters: json.RawMessage(fmt.Sprintf(`{"default":%q}`, secret))}},
	}
}

func assertProvenanceExcludedFieldsUnchanged(t *testing.T, before, after aipapi.Call, authority string) {
	t.Helper()
	if !reflect.DeepEqual(before.Instructions, after.Instructions) || !reflect.DeepEqual(before.Tools, after.Tools) {
		t.Fatal("instructions or tool definitions changed")
	}
	if authority == "items" {
		if len(before.Items) != len(after.Items) || !reflect.DeepEqual(before.Items[0], after.Items[0]) || !reflect.DeepEqual(before.Items[3], after.Items[3]) || !reflect.DeepEqual(before.Items[5], after.Items[5]) {
			t.Fatal("excluded item content changed")
		}
		return
	}
	if len(before.Messages) != len(after.Messages) || !reflect.DeepEqual(before.Messages[1], after.Messages[1]) {
		t.Fatal("excluded message content changed")
	}
}

func assertProvenanceEligibleRedacted(t *testing.T, call aipapi.Call, secret, authority string) {
	t.Helper()
	if authority == "items" {
		parts := call.Items[1].Content
		if strings.Contains(parts[0].Text, secret) || strings.Contains(parts[1].Text, secret) || strings.Contains(call.Items[2].Content[0].Text, secret) || strings.Contains(call.Items[2].Content[1].Text, secret) || strings.Contains(call.Items[4].ToolResult.Output, secret) || strings.Contains(call.Items[6].ToolResult.Parts[0].Text, secret) || strings.Contains(call.Items[6].ToolResult.Parts[1].Text, secret) {
			t.Fatal("eligible item content retained the secret")
		}
		return
	}
	if strings.Contains(call.Messages[0].Parts[0].Text, secret) || bytes.Contains(call.Messages[0].Parts[1].Content, []byte(secret)) || bytes.Contains(call.Messages[0].Parts[2].Content, []byte(secret)) || strings.Contains(call.Messages[2].Parts[0].Text, secret) || bytes.Contains(call.Messages[2].Parts[1].Content, []byte(secret)) {
		t.Fatal("eligible message content retained the secret")
	}
}

func TestGuard_ProvenanceMatrixPreservesExcludedContentAcrossActions(t *testing.T) {
	secret := testkit.SyntheticGitHubPAT
	exact := newExactStub(secret, "GITHUB_TOKEN", sdk.SourceCategoryProxyEnv)
	betterLeaks := newProvenanceBetterLeaksServices(t)
	for _, detector := range []string{"exact", "betterleaks", "hybrid"} {
		for _, authority := range []string{"messages", "items"} {
			for _, action := range []string{ActionBlock, ActionLog, ActionRedact} {
				name := detector + "/" + authority + "/" + action
				t.Run(name, func(t *testing.T) {
					var call aipapi.Call
					if authority == "items" {
						call = provenanceItemCall(secret)
					} else {
						call = provenanceMessageCall(secret)
					}
					validateProvenanceCall(t, &call)
					before := aipapi.CloneCall(call)
					decision, err := NewGuard(Config{Action: action}).Evaluate(t.Context(), &call, sdk.Meta{}, provenanceServices(detector, exact, betterLeaks))
					if err != nil {
						assertNoSyntheticSecrets(t, err.Error())
						t.Fatalf("evaluate provenance call: error type %T", err)
					}
					if len(decision.Findings) == 0 {
						t.Fatal("eligible secret produced no findings")
					}
					allowed := map[string]bool{}
					if authority == "items" {
						for _, location := range []string{"items[1].content[0]", "items[1].content[1]", "items[2].content[0]", "items[2].content[1]", "items[4].tool_result.output", "items[6].tool_result.parts[0]", "items[6].tool_result.parts[1]"} {
							allowed[location] = true
						}
					} else {
						for _, location := range []string{"messages[0].parts[0]", "messages[0].parts[1]", "messages[0].parts[2]", "messages[2].parts[0]", "messages[2].parts[1]"} {
							allowed[location] = true
						}
					}
					for _, finding := range decision.Findings {
						if !allowed[finding.Location] {
							t.Fatalf("finding from excluded location %q", finding.Location)
						}
					}
					switch action {
					case ActionBlock:
						if decision.Outcome != sdk.OutcomeBlock || decision.MutationCount != 0 || !reflect.DeepEqual(call, before) {
							t.Fatal("block changed provenance call")
						}
					case ActionLog:
						if decision.Outcome != sdk.OutcomeLog || decision.MutationCount != 0 || !reflect.DeepEqual(call, before) {
							t.Fatal("log changed provenance call")
						}
					case ActionRedact:
						if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount == 0 {
							t.Fatalf("redaction outcome=%q mutations=%d", decision.Outcome, decision.MutationCount)
						}
						validateProvenanceCall(t, &call)
						assertProvenanceExcludedFieldsUnchanged(t, before, call, authority)
						assertProvenanceEligibleRedacted(t, call, secret, authority)
					}
				})
			}
		}
	}
}

func TestGuard_ExcludedOnlyProvenancePassesWithoutBudgetAdmission(t *testing.T) {
	secret := testkit.SyntheticGitHubPAT
	exact := newExactStub(secret, "GITHUB_TOKEN", sdk.SourceCategoryProxyEnv)
	betterLeaks := newProvenanceBetterLeaksServices(t)
	for _, detector := range []string{"exact", "betterleaks", "hybrid"} {
		for _, authority := range []string{"messages", "items"} {
			for _, action := range []string{ActionBlock, ActionLog, ActionRedact} {
				name := detector + "/" + authority + "/" + action
				t.Run(name, func(t *testing.T) {
					call := provenanceExcludedOnlyCall(authority, secret)
					validateProvenanceCall(t, &call)
					before := aipapi.CloneCall(call)
					decision, err := NewGuard(Config{Action: action, ScanMaxBytes: 1}).Evaluate(t.Context(), &call, sdk.Meta{}, provenanceServices(detector, exact, betterLeaks))
					if err != nil {
						assertNoSyntheticSecrets(t, err.Error())
						t.Fatalf("evaluate excluded-only call: error type %T", err)
					}
					if decision.Outcome != sdk.OutcomePass || len(decision.Findings) != 0 || decision.ScanLimitHit || decision.MutationCount != 0 {
						t.Fatalf("excluded-only decision outcome=%q findings=%d scan_limit=%t mutations=%d", decision.Outcome, len(decision.Findings), decision.ScanLimitHit, decision.MutationCount)
					}
					if !reflect.DeepEqual(call, before) {
						t.Fatal("excluded-only call changed")
					}
				})
			}
		}
	}
}

func TestGuard_nilMatcherPasses(t *testing.T) {
	t.Parallel()
	g := NewGuard(mustCfg(t, ActionBlock))
	call := baseCall()
	call.Messages[0].Parts[0].Text = "has " + testkit.SyntheticOpenAIAPIKey
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, sdk.Services{
		MatcherResolver: nilResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
}

func TestGuard_nilResolverPasses(t *testing.T) {
	t.Parallel()
	g := NewGuard(mustCfg(t, ActionBlock))
	call := baseCall()
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, sdk.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
}

func TestGuard_block_allLocations_noMutation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionBlock))

	call := aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "text " + secret},
					{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"k":"` + secret + `","n":1}`)},
					{
						Kind:       aipapi.PartToolResult,
						ToolCallID: "c1",
						Text:       "tr-text " + secret,
						Content:    json.RawMessage(`{"out":"` + secret + `"}`),
					},
					{Kind: aipapi.PartImageRef, ImageRef: "img://" + secret},
				},
			},
		},
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "sys " + secret}},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "tool_" + secret,
			Description: "desc " + secret,
			Parameters:  json.RawMessage(`{"type":"object","properties":{"p":{"default":"` + secret + `"}}}`),
		}},
		Session: aipapi.SessionRef{ClientSessionID: secret},
		Route:   aipapi.RouteIntent{Selector: secret},
		ID:      secret,
		Extensions: map[string]json.RawMessage{
			"x": json.RawMessage(`"` + secret + `"`),
		},
	}
	before := aipapi.CloneCall(call)

	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		assertNoSyntheticSecrets(t, err.Error())
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("block must not mutate call")
	}
	wantLocs := map[string]bool{
		"messages[0].parts[0]": false,
		"messages[0].parts[1]": false,
		"messages[0].parts[2]": false,
	}
	for _, f := range d.Findings {
		assertNoSyntheticSecrets(t, f.SecretRefName)
		assertNoSyntheticSecrets(t, f.Location)
		if _, ok := wantLocs[f.Location]; !ok {
			t.Fatalf("unexpected location %q", f.Location)
		}
		wantLocs[f.Location] = true
		if f.SecretRefName != "OPENAI_API_KEY" {
			t.Fatalf("ref: %q", f.SecretRefName)
		}
	}
	for loc, seen := range wantLocs {
		if !seen {
			t.Fatalf("missing finding at %s", loc)
		}
	}
	// Image ref / id / route / session / extensions must not contribute findings.
	for _, f := range d.Findings {
		if strings.Contains(f.Location, "image") || f.Location == "" {
			t.Fatalf("bad location %q", f.Location)
		}
	}
}

func TestGuard_block_toolResultMergesSameLocation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenRouterAPIKey
	m := newExactStub(secret, "OPENROUTER_API_KEY", sdk.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionBlock))
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleTool,
			Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "c1",
				Text:       secret,
				Content:    json.RawMessage(`"` + secret + `"`),
			}},
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("want 1 merged finding, got %d", len(d.Findings))
	}
	f := d.Findings[0]
	if f.Location != "messages[0].parts[0]" {
		t.Fatalf("location: %q", f.Location)
	}
	if f.OccurrenceCount != 2 {
		t.Fatalf("occurrence_count: got %d want 2", f.OccurrenceCount)
	}
}

func TestGuard_log_deepEqualNoMutation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticAnthropicSecretGuardKey
	m := newExactStub(secret, "ANTHROPIC_API_KEY", sdk.SourceCategoryPopularEnv)
	g := NewGuard(mustCfg(t, ActionLog))
	call := baseCall()
	call.Messages[0].Parts[0].Text = "leak " + secret
	before := aipapi.CloneCall(call)

	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeLog {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must leave call deep-equal")
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
}

func TestGuard_log_passWhenClean(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticGeminiAPIKey, "GEMINI_API_KEY", sdk.SourceCategoryPopularEnv)
	g := NewGuard(mustCfg(t, ActionLog))
	call := baseCall()
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
}

func TestGuard_redact_validatesAndMutates(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionRedact))
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartText, Text: "hi " + secret},
				{Kind: aipapi.PartJSON, Content: json.RawMessage(fmt.Sprintf(`{"token":%q,"n":42,"ok":true}`, secret))},
			},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "safe_tool",
			Description: "uses " + secret,
			Parameters:  json.RawMessage(fmt.Sprintf(`{"const":%q,"count":3}`, secret)),
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		assertNoSyntheticSecrets(t, err.Error())
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeRedacted {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if d.MutationCount <= 0 {
		t.Fatal("expected mutations")
	}
	if err := call.Validate(); err != nil {
		assertNoSyntheticSecrets(t, err.Error())
		t.Fatalf("redacted call must Validate: %v", err)
	}
	if strings.Contains(call.Messages[0].Parts[0].Text, secret) {
		t.Fatal("text still contains secret")
	}
	if !strings.Contains(call.Messages[0].Parts[0].Text, strings.Repeat("*", len(secret))) {
		t.Fatal("text not redacted with mask")
	}
	var obj map[string]any
	if err := json.Unmarshal(call.Messages[0].Parts[1].Content, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["n"] != float64(42) {
		t.Fatalf("JSON number mutated: %#v", obj["n"])
	}
	if obj["ok"] != true {
		t.Fatalf("JSON bool mutated: %#v", obj["ok"])
	}
	tok, _ := obj["token"].(string)
	if tok == secret || strings.Contains(tok, secret) {
		t.Fatal("JSON string not redacted")
	}
	if call.Tools[0].Description != "uses "+secret {
		t.Fatal("tool definition description must remain unchanged")
	}
	if string(call.Tools[0].Parameters) != fmt.Sprintf(`{"const":%q,"count":3}`, secret) {
		t.Fatal("tool definition schema must remain unchanged")
	}
	if !json.Valid(call.Tools[0].Parameters) {
		t.Fatal("tool schema JSON invalid after redact")
	}
	assertNoSyntheticSecrets(t, string(call.Messages[0].Parts[1].Content))
}

func TestGuard_redact_noMatchLeavesCallUnchanged(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticOpenAIAPIKey, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	call := cleanStressCall()
	before := aipapi.CloneCall(call)

	redactGuard := NewGuard(mustCfg(t, ActionRedact))
	d, err := redactGuard.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomePass {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("clean redact must leave the call unchanged")
	}
}

func TestGuard_redactMatcherFailureLeavesCallUnchanged(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	call := baseCall()
	call.Messages[0].Parts[0].Text = "match " + secret
	before := aipapi.CloneCall(call)

	cases := []struct {
		name string
		err  error
	}{
		{name: "plain error", err: fmt.Errorf("redact failed")},
		{name: "context canceled", err: context.Canceled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &failingRedactMatcher{match: secret, err: tc.err}
			g := NewGuard(mustCfg(t, ActionRedact))
			c := call
			_, err := g.Evaluate(t.Context(), &c, sdk.Meta{}, servicesWith(m))
			if err == nil {
				t.Fatal("expected redaction error")
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("error: %v", err)
			}
			if !reflect.DeepEqual(c, before) {
				t.Fatal("redaction failure must leave the live call unchanged")
			}
		})
	}
}

func TestGuard_redact_invalidJSONOpaque(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	m := newExactStub(secret, "request_credential", sdk.SourceCategoryRequestCred)
	g := NewGuard(mustCfg(t, ActionRedact))
	opaque := []byte("not-json-" + secret)
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: opaque}},
		}},
	}
	// PartJSON requires valid JSON for Validate — after opaque RedactBytes content may still be invalid.
	// Guard should still redact bytes; Validate is runner-owned. Ensure secret gone from Content.
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeRedacted {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if bytes.Contains(call.Messages[0].Parts[0].Content, []byte(secret)) {
		t.Fatal("opaque JSON content still contains secret")
	}
}

func TestGuard_scanLimit_blockReturnsBlockWithoutError(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionBlock)
	first := "token=" + secret
	cfg.ScanMaxBytes = len(first) + 1
	g := NewGuard(cfg)
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(first)},
			},
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("z", 16))},
			},
		},
	}
	before := aipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ScanLimitHit {
		t.Fatal("ScanLimitHit want true")
	}
	if d.FailureKind != FailureKindScanLimit {
		t.Fatalf("FailureKind: %q", d.FailureKind)
	}
	if d.Outcome != sdk.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) == 0 {
		t.Fatal("expected findings before scan limit")
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("block must not mutate call")
	}
}

func TestGuard_scanLimit_blockIgnoresBestEffortAudit(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticOpenAIAPIKey, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionBlock)
	cfg.AuditFailurePolicy = AuditBestEffort
	cfg.ScanMaxBytes = 4
	g := NewGuard(cfg)
	call := baseCall()
	call.Messages[0].Parts[0].Text = "12345"
	before := aipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ScanLimitHit || d.Outcome != sdk.OutcomeBlock {
		t.Fatalf("decision: %#v", d)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("block scan-limit must not mutate call")
	}
}

func TestGuard_scanLimit_redactReturnsBlockWithoutMutatingLiveCall(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionRedact)
	first := "token=" + secret
	cfg.ScanMaxBytes = len(first) + 1
	g := NewGuard(cfg)
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(first)},
			},
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("z", 16))},
			},
		},
	}
	before := aipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("decision: %#v", d)
	}
	if d.Outcome != sdk.OutcomeBlock {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) == 0 {
		t.Fatal("expected findings before scan limit")
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("redact scan-limit must leave live call unchanged")
	}
	if !strings.Contains(call.Messages[0].Parts[0].Text, secret) {
		t.Fatal("redact scan-limit must not commit partial mutation")
	}
}

func TestGuard_scanLimit_logNoFindings(t *testing.T) {
	t.Parallel()
	m := newExactStub(testkit.SyntheticOpenAIAPIKey, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionLog)
	cfg.ScanMaxBytes = 4
	g := NewGuard(cfg)
	call := baseCall()
	call.Messages[0].Parts[0].Text = "12345"
	before := aipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeLog {
		t.Fatalf("log scan-limit outcome: %q want log", d.Outcome)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("decision: %#v", d)
	}
	if len(d.Findings) != 0 {
		t.Fatalf("want no findings, got %#v", d.Findings)
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must not mutate on scan limit")
	}
}

func TestGuard_scanLimit_logRetainsFindingsBeforeLimit(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionLog)
	cfg.ScanMaxBytes = len(secret) + 1
	g := NewGuard(cfg)
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(secret)},
			},
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("zz")},
			},
		},
	}
	before := aipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeLog {
		t.Fatalf("log scan-limit outcome: %q want log", d.Outcome)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("decision: %#v", d)
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("want 1 finding before limit, got %#v", d.Findings)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must not mutate when scan limit truncates")
	}
}

func TestGuard_scanLimit_logContinuesWithSecretPastBound(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	cfg := mustCfg(t, ActionLog)
	cfg.ScanMaxBytes = 8
	g := NewGuard(cfg)
	// Prefix fills the scan budget; secret lives past the bound and must not be detected.
	call := baseCall()
	call.Messages[0].Parts[0].Text = "AAAAAAAA" + secret
	before := aipapi.CloneCall(call)
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeLog {
		t.Fatalf("log must continue (log): got %q", d.Outcome)
	}
	if !d.ScanLimitHit || d.FailureKind != FailureKindScanLimit {
		t.Fatalf("want scan_limit hit, got %#v", d)
	}
	if len(d.Findings) != 0 {
		t.Fatalf("secret past bound must not be detected under scan limit: %#v", d.Findings)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("log must leave call byte-identical when scan limit truncates")
	}
	for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
		if strings.Contains(d.FailureReason, needle) {
			t.Fatalf("failure reason must not contain secret needle %q", needle)
		}
	}
}

func TestGuard_dedupeFindingsStaysWithinLocation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticDuplicateValueAliasA
	m := newExactStub(secret, "ALIAS_A", sdk.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionBlock))
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartText, Text: secret},
				{Kind: aipapi.PartText, Text: secret + " and again " + secret},
			},
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	// Dedupe is keyed by (location, secret reference): repeats inside one field
	// collapse to one finding with the full count, while the same value in a
	// second field must stay visible as its own finding.
	if len(d.Findings) != 2 {
		t.Fatalf("findings: got %d want 2: %#v", len(d.Findings), d.Findings)
	}
	counts := map[string]int{}
	for _, finding := range d.Findings {
		counts[finding.Location] = finding.OccurrenceCount
	}
	if got := counts["messages[0].parts[0]"]; got != 1 {
		t.Fatalf("messages[0].parts[0] occurrences: got %d want 1", got)
	}
	if got := counts["messages[0].parts[1]"]; got != 2 {
		t.Fatalf("messages[0].parts[1] occurrences: got %d want 2", got)
	}
}

func TestGuard_failureModeFailClosed(t *testing.T) {
	t.Parallel()
	g := NewGuard(mustCfg(t, ActionLog))
	if g.FailureMode() != sdk.FailClosed {
		t.Fatalf("FailureMode: %v", g.FailureMode())
	}
	if g.ID() != ID {
		t.Fatalf("ID: %q", g.ID())
	}
}

func TestFeatureBundle(t *testing.T) {
	t.Parallel()
	cfg := mustCfg(t, ActionBlock)
	b := FeatureBundle(cfg)
	if b.SchemaVersion != aipfeature.SchemaVersionV1 {
		t.Fatalf("schema: %d", b.SchemaVersion)
	}
	if guards := aipfeature.Get(b.PlaneSet, aipfeature.PlaneSecretGuards); len(guards) != 1 {
		t.Fatalf("guards: %d", len(guards))
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestJSONRedact_preservesNumbers(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticGeminiAPIKey
	m := newExactStub(secret, "GEMINI_API_KEY", sdk.SourceCategoryPopularEnv)
	in := json.RawMessage(fmt.Sprintf(`{"a":%q,"b":7,"c":[1,%q,true],"big":9007199254740993}`, secret, secret))
	out, findings, err := redactJSONPayload(t.Context(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("expected findings")
	}
	if !json.Valid(out) {
		t.Fatal("output not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	root, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("root type: %T", v)
	}
	if root["b"] != json.Number("7") {
		t.Fatalf("number: %#v", root["b"])
	}
	if root["big"] != json.Number("9007199254740993") {
		t.Fatalf("large integer mutated: %#v", root["big"])
	}
	arr, ok := root["c"].([]any)
	if !ok {
		t.Fatalf("array type: %T", root["c"])
	}
	if arr[0] != json.Number("1") || arr[2] != true {
		t.Fatalf("array: %#v", arr)
	}
	tok, _ := root["a"].(string)
	if len(tok) != len(secret) {
		t.Fatalf("decoded string byte length: got %d want %d", len(tok), len(secret))
	}
	assertNoSyntheticSecrets(t, string(out))
}

func TestGuard_redactJSONStringCountsMatchOnce(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	g := NewGuard(mustCfg(t, ActionRedact))
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"token":"` + secret + `"}`)}},
		}},
	}
	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeRedacted {
		t.Fatalf("outcome: %q", d.Outcome)
	}
	if len(d.Findings) != 1 {
		t.Fatalf("findings: %#v", d.Findings)
	}
	if d.Findings[0].OccurrenceCount != 1 {
		t.Fatalf("occurrence_count: got %d want 1", d.Findings[0].OccurrenceCount)
	}
	if d.MutationCount != 1 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if !json.Valid(call.Messages[0].Parts[0].Content) {
		t.Fatal("redacted JSON must remain valid")
	}
	if bytes.Contains(call.Messages[0].Parts[0].Content, []byte(secret)) {
		t.Fatal("redacted JSON still contains secret")
	}
}

func TestJSONRedact_escapedStringDecodedMatch(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	// Force JSON escapes in the source while keeping decoded UTF-8 equal to secret.
	escaped := strings.Builder{}
	escaped.WriteString(`{"k":"`)
	for _, r := range secret {
		fmt.Fprintf(&escaped, "\\u%04x", r)
	}
	escaped.WriteString(`"}`)
	in := json.RawMessage(escaped.String())
	out, findings, err := redactJSONPayload(t.Context(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings: %d", len(findings))
	}
	if !json.Valid(out) {
		t.Fatal("invalid JSON after redact")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	tok, _ := root["k"].(string)
	if tok == secret || strings.Contains(tok, secret) {
		t.Fatal("escaped JSON string not redacted after decode")
	}
	if len(tok) != len(secret) {
		t.Fatalf("decoded redacted length=%d want %d", len(tok), len(secret))
	}
	assertNoSyntheticSecrets(t, string(out))
}

func mustCfg(t *testing.T, action string) Config {
	t.Helper()
	cfg, err := DecodeConfig(mustYAML(t, "action: "+action))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
