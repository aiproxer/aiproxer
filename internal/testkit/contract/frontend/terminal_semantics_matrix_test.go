package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	frontanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	frontgemini "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	frontopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	frontsystemone "github.com/aiproxer/aiproxer/internal/plugins/frontends/systemone"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

// terminalConcern is one cross-cutting terminal-semantics concern.
// It mirrors the Billion Context exit-matrix idea: every wire exit
// (frontend) x every concern must name its covering test.
type terminalConcern string

const (
	concernCleanText     terminalConcern = "clean_text"
	concernCleanToolCall terminalConcern = "clean_tool_call"
	concernOutputLimit   terminalConcern = "output_limit"
	concernContentFilter terminalConcern = "content_filter"
	concernUpstreamError terminalConcern = "upstream_error"
	concernUsageReported terminalConcern = "usage_reported"
	concernErrorTaxonomy terminalConcern = "error_taxonomy"
	concernEmptyTurn     terminalConcern = "empty_turn"
	concernCancellation  terminalConcern = "cancellation"
)

var allTerminalConcerns = []terminalConcern{
	concernCleanText,
	concernCleanToolCall,
	concernOutputLimit,
	concernContentFilter,
	concernUpstreamError,
	concernUsageReported,
	concernErrorTaxonomy,
	concernEmptyTurn,
	concernCancellation,
}

// terminalCorpusAnchor maps every terminal concern to its shared
// BaselineScenarioCorpus scenario ID (terminal-corpus promotion). Cells keep
// their builders and assertions; the anchor is the traceability link proving
// the concern is inherited through the public corpus.
var terminalCorpusAnchor = map[terminalConcern]string{
	concernCleanText:     "terminal-clean-text",
	concernCleanToolCall: "terminal-clean-tool-call",
	concernOutputLimit:   "terminal-output-limit",
	concernUpstreamError: "terminal-upstream-error",
	concernUsageReported: "terminal-usage-reported",
	concernContentFilter: "terminal-content-filter",
	concernErrorTaxonomy: "terminal-error-taxonomy",
	concernEmptyTurn:     "terminal-empty-turn",
	concernCancellation:  "cancellation",
}

// terminalCell is one matrix cell. The cell IS the test: run is a
// compile-time reference to the covering test function (like exit-matrix
// "implementer"). na is a non-empty explicit not-applicable reason,
// mutually exclusive with run. sameAs is backend-rows only.
type terminalCell struct {
	run    func(t *testing.T)
	na     string
	sameAs string
}

// frontendTerminalMatrix is the registry of every bundled frontend ID
// x every terminal concern. A missing cell fails the build.
var frontendTerminalMatrix = map[string]map[terminalConcern]terminalCell{
	"anthropic": {
		concernCleanText:     {run: testTerminalAnthropicCleanText},
		concernCleanToolCall: {run: testTerminalAnthropicCleanToolCall},
		concernOutputLimit:   {run: testTerminalAnthropicOutputLimit},
		concernContentFilter: {run: testTerminalAnthropicContentFilter},
		concernUpstreamError: {run: testTerminalAnthropicUpstreamError},
		concernUsageReported: {run: testTerminalAnthropicUsageReported},
		concernErrorTaxonomy: {run: testTerminalAnthropicErrorTaxonomy},
		concernEmptyTurn:     {run: testTerminalAnthropicEmptyTurn},
		concernCancellation:  {run: testTerminalAnthropicCancellation},
	},
	"gemini": {
		concernCleanText:     {run: testTerminalGeminiCleanText},
		concernCleanToolCall: {run: testTerminalGeminiCleanToolCall},
		concernOutputLimit:   {run: testTerminalGeminiOutputLimit},
		concernContentFilter: {run: testTerminalGeminiContentFilter},
		concernUpstreamError: {run: testTerminalGeminiUpstreamError},
		concernUsageReported: {run: testTerminalGeminiUsageReported},
		concernErrorTaxonomy: {run: testTerminalGeminiErrorTaxonomy},
		concernEmptyTurn:     {run: testTerminalGeminiEmptyTurn},
		concernCancellation:  {run: testTerminalGeminiCancellation},
	},
	"openai-legacy": {
		concernCleanText:     {run: testTerminalOpenAILegacyCleanText},
		concernCleanToolCall: {run: testTerminalOpenAILegacyCleanToolCall},
		concernOutputLimit:   {run: testTerminalOpenAILegacyOutputLimit},
		concernContentFilter: {run: testTerminalOpenAILegacyContentFilter},
		concernUpstreamError: {run: testTerminalOpenAILegacyUpstreamError},
		concernUsageReported: {run: testTerminalOpenAILegacyUsageReported},
		concernErrorTaxonomy: {run: testTerminalOpenAILegacyErrorTaxonomy},
		concernEmptyTurn:     {run: testTerminalOpenAILegacyEmptyTurn},
		concernCancellation:  {run: testTerminalOpenAILegacyCancellation},
	},
	"openai-responses": {
		concernCleanText:     {run: testTerminalOpenAIResponsesCleanText},
		concernCleanToolCall: {run: testTerminalOpenAIResponsesCleanToolCall},
		concernOutputLimit:   {run: testTerminalOpenAIResponsesOutputLimit},
		concernContentFilter: {run: testTerminalOpenAIResponsesContentFilter},
		concernUpstreamError: {run: testTerminalOpenAIResponsesUpstreamError},
		concernUsageReported: {run: testTerminalOpenAIResponsesUsageReported},
		concernErrorTaxonomy: {run: testTerminalOpenAIResponsesErrorTaxonomy},
		concernEmptyTurn:     {run: testTerminalOpenAIResponsesEmptyTurn},
		concernCancellation:  {run: testTerminalOpenAIResponsesCancellation},
	},
	"openresponses": {
		concernCleanText:     {run: testTerminalOpenResponsesCleanText},
		concernCleanToolCall: {run: testTerminalOpenResponsesCleanToolCall},
		concernOutputLimit:   {run: testTerminalOpenResponsesOutputLimit},
		concernContentFilter: {run: testTerminalOpenResponsesContentFilter},
		concernUpstreamError: {run: testTerminalOpenResponsesUpstreamError},
		concernUsageReported: {run: testTerminalOpenResponsesUsageReported},
		concernErrorTaxonomy: {run: testTerminalOpenResponsesErrorTaxonomy},
		concernEmptyTurn:     {run: testTerminalOpenResponsesEmptyTurn},
		concernCancellation:  {run: testTerminalOpenResponsesCancellation},
	},
	"systemone": {
		concernCleanText:     {na: "systemone is decision-only; no text terminal"},
		concernCleanToolCall: {na: "systemone is decision-only; no tool terminal"},
		concernOutputLimit:   {na: "systemone decision has no max_tokens truncation"},
		concernContentFilter: {na: "systemone decision has no refusal terminal"},
		concernUpstreamError: {run: testTerminalSystemOneUpstreamError},
		concernUsageReported: {run: testTerminalSystemOneUsageReported},
		concernErrorTaxonomy: {run: testTerminalSystemOneErrorTaxonomy},
		concernEmptyTurn:     {na: "systemone requires a decision result with one answer per question; an empty turn is a terminal error, not a clean terminal (see upstream_error)"},
		concernCancellation:  {run: testTerminalSystemOneCancellation},
	},
}

// TestTerminalSemantics_Completeness is the point of the slice: adding a
// bundled frontend without filling its matrix row fails go test. An extra
// row for an unregistered subject also fails, so stale rows are removed.
func TestTerminalSemantics_Completeness(t *testing.T) {
	t.Parallel()
	var missing []string
	var want []string
	for _, f := range standardplugins.StandardBundle().Frontends {
		want = append(want, f.ID)
	}
	sort.Strings(want)
	for _, id := range want {
		row, ok := frontendTerminalMatrix[id]
		if !ok {
			for _, c := range allTerminalConcerns {
				missing = append(missing, fmt.Sprintf("(%s, %s)", id, c))
			}
			continue
		}
		for _, c := range allTerminalConcerns {
			cell, ok := row[c]
			if !ok {
				missing = append(missing, fmt.Sprintf("(%s, %s)", id, c))
				continue
			}
			hasRun := cell.run != nil
			hasNA := strings.TrimSpace(cell.na) != ""
			hasSameAs := strings.TrimSpace(cell.sameAs) != ""
			if hasRun && (hasNA || hasSameAs) {
				missing = append(missing, fmt.Sprintf("(%s, %s): run mutually exclusive with na/sameAs", id, c))
				continue
			}
			if hasNA && hasSameAs {
				missing = append(missing, fmt.Sprintf("(%s, %s): na mutually exclusive with sameAs", id, c))
				continue
			}
			if !hasRun && !hasNA && !hasSameAs {
				missing = append(missing, fmt.Sprintf("(%s, %s): run XOR na (or sameAs) violated", id, c))
				continue
			}
			if hasSameAs {
				if _, ok := frontendTerminalMatrix[cell.sameAs]; !ok {
					missing = append(missing, fmt.Sprintf("(%s, %s): sameAs %q missing", id, c, cell.sameAs))
				}
			}
		}
		for c := range row {
			found := slices.Contains(allTerminalConcerns, c)
			if !found {
				missing = append(missing, fmt.Sprintf("(%s, %s): unknown concern", id, c))
			}
		}
	}
	for id := range frontendTerminalMatrix {
		found := slices.Contains(want, id)
		if !found {
			missing = append(missing, fmt.Sprintf("(%s, *): stale row for unregistered subject", id))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("terminal-semantics matrix incomplete (%d cells); edit internal/testkit/contract/frontend/terminal_semantics_matrix_test.go:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestTerminalSemantics_CorpusAnchors proves every matrix concern names its
// shared BaselineScenarioCorpus scenario: the matrices stay the enforcement
// layer while the corpus carries the inherited coverage.
func TestTerminalSemantics_CorpusAnchors(t *testing.T) {
	t.Parallel()
	ids := make(map[string]bool, len(semantic.BaselineScenarioCorpus()))
	for _, sc := range semantic.BaselineScenarioCorpus() {
		ids[string(sc.ID)] = true
	}
	for _, c := range allTerminalConcerns {
		anchor, ok := terminalCorpusAnchor[c]
		if !ok || anchor == "" {
			t.Fatalf("concern %q has no corpus anchor", c)
		}
		if !ids[anchor] {
			t.Fatalf("concern %q anchors unknown corpus scenario %q", c, anchor)
		}
	}
}

// TestTerminalSemantics_Matrix executes every registered cell. Each cell is
// already a covering test; this runner only ensures they all run under one
// name for CI visibility.
func TestTerminalSemantics_Matrix(t *testing.T) {
	t.Parallel()
	var subjects []string
	for s := range frontendTerminalMatrix {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects)
	for _, subject := range subjects {
		for _, c := range allTerminalConcerns {
			cell := frontendTerminalMatrix[subject][c]
			if cell.run == nil {
				continue
			}
			t.Run(subject+"/"+string(c), func(t *testing.T) {
				t.Parallel()
				cell.run(t)
			})
		}
	}
}

// terminalFrontendSpec describes how to reach one bundled frontend via the
// existing frontend TCK harness. Mount/path/bodies mirror
// standard_frontends_test.go so terminal cells reuse the same boundary.
type terminalFrontendSpec struct {
	mount          aipsdk.FrontendMount
	pathNonStream  string
	pathStream     string
	bodyNonStream  string
	bodyStream     string
	bodyTool       string
	decisionBody   string
	isDecisionOnly bool
}

func terminalSpecFor(id string) terminalFrontendSpec {
	switch id {
	case "openai-responses":
		return terminalFrontendSpec{
			mount:         frontopenairesponses.Mount,
			pathNonStream: "/v1/responses",
			pathStream:    "/v1/responses",
			bodyNonStream: `{"model":"m","input":"hi"}`,
			bodyStream:    `{"model":"m","input":"hi","stream":true}`,
			bodyTool:      `{"model":"m","input":"hi","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`,
		}
	case "openresponses":
		return terminalFrontendSpec{
			mount:         frontopenresponses.Mount,
			pathNonStream: "/openresponses/v1/responses",
			pathStream:    "/openresponses/v1/responses",
			bodyNonStream: `{"model":"m","input":"hi"}`,
			bodyStream:    `{"model":"m","input":"hi","stream":true}`,
			bodyTool:      `{"model":"m","input":"hi","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`,
		}
	case "openai-legacy":
		return terminalFrontendSpec{
			mount:         frontopenailegacy.Mount,
			pathNonStream: "/v1/chat/completions",
			pathStream:    "/v1/chat/completions",
			bodyNonStream: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			bodyStream:    `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			bodyTool:      `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`,
		}
	case "anthropic":
		return terminalFrontendSpec{
			mount:         frontanthropic.Mount,
			pathNonStream: "/v1/messages",
			pathStream:    "/v1/messages",
			bodyNonStream: `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			bodyStream:    `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			bodyTool:      `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}]}`,
		}
	case "gemini":
		return terminalFrontendSpec{
			mount:         frontgemini.Mount,
			pathNonStream: "/v1beta/models/m:generateContent",
			pathStream:    "/v1beta/models/m:streamGenerateContent?alt=sse",
			bodyNonStream: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			bodyStream:    `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			bodyTool:      `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"object"}}]}]}`,
		}
	case "systemone":
		return terminalFrontendSpec{
			mount:          frontsystemone.Mount,
			pathNonStream:  "/v1/systemone",
			pathStream:     "/v1/systemone",
			decisionBody:   `{"model":"decision:jev","state":"x","questions":{"q":{"type":"noul"}}}`,
			isDecisionOnly: true,
		}
	default:
		return terminalFrontendSpec{}
	}
}

func terminalCleanTextEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished},
	}
}

func terminalCleanToolEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"q":1}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventResponseFinished},
	}
}

func terminalUsageEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
		{Kind: aipapi.EventResponseFinished},
	}
}

func terminalErrorEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventError, ErrorCode: "upstream_error", ErrorMessage: "boom"},
	}
}

func terminalDecisionUsageEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventDecisionResult, Decision: &aipapi.DecisionResult{
			Model:   "jev",
			Answers: []aipapi.DecisionAnswer{{QuestionID: "q", Kind: aipapi.DecisionKindNoul, PTrue: 0.8}},
		}},
		{Kind: aipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		{Kind: aipapi.EventResponseFinished},
	}
}

func terminalDecisionErrorEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventError, ErrorCode: "upstream_error", ErrorMessage: "boom"},
	}
}

// driveTerminal sends one request to one frontend with a scripted canonical
// stream and returns the HTTP status and wire body. It uses the existing
// MountedHarness so terminal cells cover the same boundary as the TCK.
func driveTerminal(t *testing.T, frontendID, path, body string, events []aipapi.Event) (int, string) {
	t.Helper()
	return driveTerminalWithScript(t, frontendID, path, body, EventScript{Events: events})
}

// driveTerminalWithScript is driveTerminal for an arbitrary executor script.
// The error-taxonomy concern uses it to fail Execute itself with a catalogued
// canonical error instead of streaming scripted events.
func driveTerminalWithScript(t *testing.T, frontendID, path, body string, script EventScript) (int, string) {
	t.Helper()
	spec := terminalSpecFor(frontendID)
	if spec.mount == nil {
		t.Fatalf("unknown frontend %q", frontendID)
	}
	executor := &CapturingExecutor{Script: script}
	h := &MountedHarness{
		Descriptor:        semantic.SubjectDescriptor{ID: frontendID, Kind: semantic.KindFrontend},
		Mount:             spec.mount,
		Path:              func(semantic.ScenarioDescriptor) string { return path },
		Body:              func(semantic.ScenarioDescriptor) []byte { return []byte(body) },
		ExecutorBoundary:  executor,
		ContinuationStore: aipcont.NewMemoryStore(),
		AuthProvider:      allowFrontendTCKAuth{},
	}
	if err := h.Reset(); err != nil {
		t.Fatalf("%s reset: %v", frontendID, err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer frontend-tck")
	req.Header.Set("X-AIP-Session-ID", "frontend-tck-session")
	rec := httptest.NewRecorder()
	h.Handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func assertContains(t *testing.T, frontendID, concern, wire string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(wire, w) {
			t.Fatalf("%s %s: wire missing %q; got status body=%q", frontendID, concern, w, wire)
		}
	}
}

func assertNotContains(t *testing.T, frontendID, concern, wire string, avoid ...string) {
	t.Helper()
	for _, a := range avoid {
		if strings.Contains(wire, a) {
			t.Fatalf("%s %s: wire must not contain %q; got body=%q", frontendID, concern, a, wire)
		}
	}
}

// Anthropic cells.

func testTerminalAnthropicCleanText(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("anthropic")
	events := terminalCleanTextEvents()
	code, nonStream := driveTerminal(t, "anthropic", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("anthropic clean_text non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "anthropic", "clean_text", nonStream, `"stop_reason":"end_turn"`, "hello")
	assertNotContains(t, "anthropic", "clean_text", nonStream, "max_tokens")
	code, stream := driveTerminal(t, "anthropic", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("anthropic clean_text stream status=%d body=%q", code, stream)
	}
	assertContains(t, "anthropic", "clean_text", stream, `"stop_reason":"end_turn"`, "message_stop")
}

func testTerminalAnthropicCleanToolCall(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("anthropic")
	events := terminalCleanToolEvents()
	code, nonStream := driveTerminal(t, "anthropic", spec.pathNonStream, spec.bodyTool, events)
	if code != http.StatusOK {
		t.Fatalf("anthropic clean_tool_call non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "anthropic", "clean_tool_call", nonStream, `"stop_reason":"tool_use"`, `"type":"tool_use"`, "weather")
	code, stream := driveTerminal(t, "anthropic", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("anthropic clean_tool_call stream status=%d body=%q", code, stream)
	}
	assertContains(t, "anthropic", "clean_tool_call", stream, `"stop_reason":"tool_use"`, "tool_use")
}

func testTerminalAnthropicUpstreamError(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("anthropic")
	events := terminalErrorEvents()
	code, nonStream := driveTerminal(t, "anthropic", spec.pathNonStream, spec.bodyNonStream, events)
	if code < 400 || code >= 600 {
		t.Fatalf("anthropic upstream_error non-stream status=%d body=%q, want 4xx/5xx", code, nonStream)
	}
	assertContains(t, "anthropic", "upstream_error", nonStream, `"type":"error"`)
	code, stream := driveTerminal(t, "anthropic", spec.pathStream, spec.bodyStream, events)
	if code < 400 || code >= 600 {
		return
	}
	// Streaming error after headers is signaled by truncating the SSE stream
	// without a clean terminal (no message_stop/end_turn). The existing
	// PumpSSE contract returns StreamError after the 200 framing, so the
	// client observes termination, not an error envelope.
	if code != http.StatusOK {
		t.Fatalf("anthropic upstream_error stream status=%d body=%q, want 4xx/5xx or truncated 200", code, stream)
	}
	assertNotContains(t, "anthropic", "upstream_error", stream, `"stop_reason":"end_turn"`, "message_stop")
}

func testTerminalAnthropicUsageReported(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("anthropic")
	events := terminalUsageEvents()
	code, nonStream := driveTerminal(t, "anthropic", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("anthropic usage_reported non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "anthropic", "usage_reported", nonStream, `"input_tokens":7`, `"output_tokens":3`)
	code, stream := driveTerminal(t, "anthropic", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("anthropic usage_reported stream status=%d body=%q", code, stream)
	}
	assertContains(t, "anthropic", "usage_reported", stream, `"input_tokens":7`)
}

// Gemini cells.

func testTerminalGeminiCleanText(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("gemini")
	events := terminalCleanTextEvents()
	code, nonStream := driveTerminal(t, "gemini", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("gemini clean_text non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "gemini", "clean_text", nonStream, `"candidates"`, "hello", `"STOP"`)
	code, stream := driveTerminal(t, "gemini", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("gemini clean_text stream status=%d body=%q", code, stream)
	}
	assertContains(t, "gemini", "clean_text", stream, `"candidates"`, "hello")
}

func testTerminalGeminiCleanToolCall(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("gemini")
	events := terminalCleanToolEvents()
	code, nonStream := driveTerminal(t, "gemini", spec.pathNonStream, spec.bodyTool, events)
	if code != http.StatusOK {
		t.Fatalf("gemini clean_tool_call non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "gemini", "clean_tool_call", nonStream, "functionCall", "weather", `"STOP"`)
	code, stream := driveTerminal(t, "gemini", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("gemini clean_tool_call stream status=%d body=%q", code, stream)
	}
	assertContains(t, "gemini", "clean_tool_call", stream, "functionCall", `"STOP"`)
}

func testTerminalGeminiUpstreamError(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("gemini")
	events := terminalErrorEvents()
	code, nonStream := driveTerminal(t, "gemini", spec.pathNonStream, spec.bodyNonStream, events)
	if code < 400 || code >= 600 {
		t.Fatalf("gemini upstream_error non-stream status=%d body=%q, want 4xx/5xx", code, nonStream)
	}
	assertContains(t, "gemini", "upstream_error", strings.ToLower(nonStream), "error")
	code, stream := driveTerminal(t, "gemini", spec.pathStream, spec.bodyStream, events)
	if code < 400 || code >= 600 {
		return
	}
	if code != http.StatusOK {
		t.Fatalf("gemini upstream_error stream status=%d body=%q, want 4xx/5xx or truncated 200", code, stream)
	}
	assertNotContains(t, "gemini", "upstream_error", stream, "hello")
}

func testTerminalGeminiUsageReported(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("gemini")
	events := terminalUsageEvents()
	code, nonStream := driveTerminal(t, "gemini", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("gemini usage_reported non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "gemini", "usage_reported", nonStream, `"promptTokenCount":7`, `"candidatesTokenCount":3`)
	code, stream := driveTerminal(t, "gemini", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("gemini usage_reported stream status=%d body=%q", code, stream)
	}
	assertContains(t, "gemini", "usage_reported", stream, `"promptTokenCount":7`)
}

// OpenAI legacy cells.

func testTerminalOpenAILegacyCleanText(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-legacy")
	events := terminalCleanTextEvents()
	code, nonStream := driveTerminal(t, "openai-legacy", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-legacy clean_text non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openai-legacy", "clean_text", nonStream, `"finish_reason":"stop"`, "hello")
	assertNotContains(t, "openai-legacy", "clean_text", nonStream, `"finish_reason":"length"`)
	code, stream := driveTerminal(t, "openai-legacy", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-legacy clean_text stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openai-legacy", "clean_text", stream, `"finish_reason":"stop"`)
}

func testTerminalOpenAILegacyCleanToolCall(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-legacy")
	events := terminalCleanToolEvents()
	code, nonStream := driveTerminal(t, "openai-legacy", spec.pathNonStream, spec.bodyTool, events)
	if code != http.StatusOK {
		t.Fatalf("openai-legacy clean_tool_call non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openai-legacy", "clean_tool_call", nonStream, `"finish_reason":"tool_calls"`, "tool_calls", "weather")
	code, stream := driveTerminal(t, "openai-legacy", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-legacy clean_tool_call stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openai-legacy", "clean_tool_call", stream, `"finish_reason":"tool_calls"`)
}

func testTerminalOpenAILegacyUpstreamError(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-legacy")
	events := terminalErrorEvents()
	code, nonStream := driveTerminal(t, "openai-legacy", spec.pathNonStream, spec.bodyNonStream, events)
	if code < 400 || code >= 600 {
		t.Fatalf("openai-legacy upstream_error non-stream status=%d body=%q, want 4xx/5xx", code, nonStream)
	}
	assertContains(t, "openai-legacy", "upstream_error", nonStream, `"error"`)
	code, stream := driveTerminal(t, "openai-legacy", spec.pathStream, spec.bodyStream, events)
	if code < 400 || code >= 600 {
		return
	}
	if code != http.StatusOK {
		t.Fatalf("openai-legacy upstream_error stream status=%d body=%q, want 4xx/5xx or truncated 200", code, stream)
	}
	assertNotContains(t, "openai-legacy", "upstream_error", stream, `"finish_reason":"stop"`, `"finish_reason":"tool_calls"`)
}

func testTerminalOpenAILegacyUsageReported(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-legacy")
	events := terminalUsageEvents()
	code, nonStream := driveTerminal(t, "openai-legacy", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-legacy usage_reported non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openai-legacy", "usage_reported", nonStream, `"prompt_tokens":7`, `"completion_tokens":3`)
	code, stream := driveTerminal(t, "openai-legacy", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-legacy usage_reported stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openai-legacy", "usage_reported", stream, `"prompt_tokens":7`)
}

// OpenAI Responses cells.

func testTerminalOpenAIResponsesCleanText(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-responses")
	events := terminalCleanTextEvents()
	code, nonStream := driveTerminal(t, "openai-responses", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-responses clean_text non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openai-responses", "clean_text", nonStream, `"status":"completed"`, "hello")
	assertNotContains(t, "openai-responses", "clean_text", nonStream, `"status":"incomplete"`)
	code, stream := driveTerminal(t, "openai-responses", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-responses clean_text stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openai-responses", "clean_text", stream, "response.completed", `"status":"completed"`)
}

func testTerminalOpenAIResponsesCleanToolCall(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-responses")
	events := terminalCleanToolEvents()
	code, nonStream := driveTerminal(t, "openai-responses", spec.pathNonStream, spec.bodyTool, events)
	if code != http.StatusOK {
		t.Fatalf("openai-responses clean_tool_call non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openai-responses", "clean_tool_call", nonStream, "function_call", `"status":"completed"`, "weather")
	code, stream := driveTerminal(t, "openai-responses", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-responses clean_tool_call stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openai-responses", "clean_tool_call", stream, "function_call")
}

func testTerminalOpenAIResponsesUpstreamError(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-responses")
	events := terminalErrorEvents()
	code, nonStream := driveTerminal(t, "openai-responses", spec.pathNonStream, spec.bodyNonStream, events)
	if code < 400 || code >= 600 {
		t.Fatalf("openai-responses upstream_error non-stream status=%d body=%q, want 4xx/5xx", code, nonStream)
	}
	assertContains(t, "openai-responses", "upstream_error", nonStream, `"error"`)
	code, stream := driveTerminal(t, "openai-responses", spec.pathStream, spec.bodyStream, events)
	if code < 400 || code >= 600 {
		return
	}
	if code != http.StatusOK {
		t.Fatalf("openai-responses upstream_error stream status=%d body=%q, want 4xx/5xx or truncated 200", code, stream)
	}
	assertNotContains(t, "openai-responses", "upstream_error", stream, "response.completed")
}

func testTerminalOpenAIResponsesUsageReported(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openai-responses")
	events := terminalUsageEvents()
	code, nonStream := driveTerminal(t, "openai-responses", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-responses usage_reported non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openai-responses", "usage_reported", nonStream, `"input_tokens":7`, `"output_tokens":3`)
	code, stream := driveTerminal(t, "openai-responses", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openai-responses usage_reported stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openai-responses", "usage_reported", stream, "response.completed")
	if !strings.Contains(stream, "7") {
		t.Fatalf("openai-responses usage_reported stream missing usage 7; body=%q", stream)
	}
}

// OpenResponses cells.

func testTerminalOpenResponsesCleanText(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openresponses")
	events := terminalCleanTextEvents()
	code, nonStream := driveTerminal(t, "openresponses", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("openresponses clean_text non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openresponses", "clean_text", nonStream, `"status":"completed"`, "hello")
	assertNotContains(t, "openresponses", "clean_text", nonStream, `"status":"incomplete"`)
	code, stream := driveTerminal(t, "openresponses", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openresponses clean_text stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openresponses", "clean_text", stream, "response.completed")
}

func testTerminalOpenResponsesCleanToolCall(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openresponses")
	events := terminalCleanToolEvents()
	code, nonStream := driveTerminal(t, "openresponses", spec.pathNonStream, spec.bodyTool, events)
	if code != http.StatusOK {
		t.Fatalf("openresponses clean_tool_call non-stream status=%d body=%q", code, nonStream)
	}
	assertContains(t, "openresponses", "clean_tool_call", nonStream, "function_call", "weather")
	code, stream := driveTerminal(t, "openresponses", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openresponses clean_tool_call stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openresponses", "clean_tool_call", stream, "function_call")
}

func testTerminalOpenResponsesUpstreamError(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openresponses")
	events := terminalErrorEvents()
	code, nonStream := driveTerminal(t, "openresponses", spec.pathNonStream, spec.bodyNonStream, events)
	if code < 400 || code >= 600 {
		t.Fatalf("openresponses upstream_error non-stream status=%d body=%q, want 4xx/5xx", code, nonStream)
	}
	assertContains(t, "openresponses", "upstream_error", strings.ToLower(nonStream), "error")
	// WS transport: the same canonical error must terminate the turn without
	// a completed response, either as a classified error envelope (pre-output)
	// or as a failed terminal (post-output), mirroring the SSE vocabulary.
	wsFrames := driveTerminalWS(t, terminalErrorEvents())
	assertNotContains(t, "openresponses", "upstream_error/ws", wsFrames, `"type":"response.completed"`)
	if !strings.Contains(wsFrames, `"type":"response.failed"`) && !strings.Contains(wsFrames, `"type":"error"`) {
		t.Fatalf("openresponses upstream_error/ws: want response.failed or an error envelope; frames=%q", wsFrames)
	}
	code, stream := driveTerminal(t, "openresponses", spec.pathStream, spec.bodyStream, events)
	if code < 400 || code >= 600 {
		if code == http.StatusOK && strings.Contains(strings.ToLower(stream), "error") {
			return
		}
		t.Fatalf("openresponses upstream_error stream status=%d body=%q, want 4xx/5xx or SSE error", code, stream)
	}
}

func testTerminalOpenResponsesUsageReported(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("openresponses")
	events := terminalUsageEvents()
	code, nonStream := driveTerminal(t, "openresponses", spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("openresponses usage_reported non-stream status=%d body=%q", code, nonStream)
	}
	if !strings.Contains(nonStream, "7") {
		t.Fatalf("openresponses usage_reported non-stream missing usage 7; body=%q", nonStream)
	}
	code, stream := driveTerminal(t, "openresponses", spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("openresponses usage_reported stream status=%d body=%q", code, stream)
	}
	assertContains(t, "openresponses", "usage_reported", stream, "response.completed")
	// WS transport: usage must surface on the wire alongside the completed
	// terminal, using the same usage vocabulary as HTTP/SSE.
	wsFrames := driveTerminalWS(t, terminalUsageEvents())
	assertContains(t, "openresponses", "usage_reported/ws", wsFrames, "response.completed", `"input_tokens":7`)
}

// SystemOne cells (decision-only).

func testTerminalSystemOneUpstreamError(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("systemone")
	events := terminalDecisionErrorEvents()
	code, body := driveTerminal(t, "systemone", spec.pathNonStream, spec.decisionBody, events)
	if code < 400 || code >= 600 {
		t.Fatalf("systemone upstream_error status=%d body=%q, want 4xx/5xx", code, body)
	}
	assertContains(t, "systemone", "upstream_error", strings.ToLower(body), "error")
}

func testTerminalSystemOneUsageReported(t *testing.T) {
	t.Helper()
	spec := terminalSpecFor("systemone")
	events := terminalDecisionUsageEvents()
	code, body := driveTerminal(t, "systemone", spec.pathNonStream, spec.decisionBody, events)
	if code != http.StatusOK {
		t.Fatalf("systemone usage_reported status=%d body=%q", code, body)
	}
	assertContains(t, "systemone", "usage_reported", body, `"input_tokens":1`, `"output_tokens":2`)
}

// terminalOutputLimitEvents builds the canonical streams for the output_limit
// concern, mirroring the absorbed finish-reason parity cases: an incomplete
// resource with a vendor length reason, a legacy producer that sets the
// vendor reason without ResponseStatus, a truncation inside a tool call
// (truncation wins over the tool vocabulary, with parseable arguments so the
// non-stream encoders stay drivable), a truncation with partial tool
// arguments (the response must stay usable), and an explicit completed
// status that wins over a vendor reason.
func terminalOutputLimitEvents() map[string][]aipapi.Event {
	textPrefix := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "cut off he"},
	}
	toolPrefix := func(args string) []aipapi.Event {
		return []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "get_weather"},
			{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: args},
			{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		}
	}
	incompleteLength := aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "incomplete", FinishReason: "length"}
	return map[string][]aipapi.Event{
		"incomplete_length":      append(append([]aipapi.Event(nil), textPrefix...), incompleteLength),
		"legacy_max_tokens":      append(append([]aipapi.Event(nil), textPrefix...), aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "max_tokens"}),
		"truncated_tool_call":    append(toolPrefix(`{"city":"Oslo"}`), incompleteLength),
		"truncated_partial_args": append(toolPrefix(`{"city":`), incompleteLength),
		"explicit_completed":     append(append([]aipapi.Event(nil), textPrefix...), aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed", FinishReason: "length"}),
	}
}

// outputLimitMarkers holds one frontend's native wire vocabulary for the
// output_limit concern: the truncation discriminator per transport and the
// clean discriminator the explicit-completed drive must carry instead.
// truncStatusNS / cleanStatusNS optionally pin the top-level envelope status
// (needed where a nested item carries its own status, so a substring would
// collide).
type outputLimitMarkers struct {
	truncNonStream []string
	truncStream    []string
	truncAbsent    []string
	cleanNonStream []string
	cleanStream    []string
	truncStatusNS  string
	cleanStatusNS  string
}

// terminalTopLevelStatus extracts the top-level status discriminator from a
// non-stream JSON wire body.
func terminalTopLevelStatus(t *testing.T, frontendID, concern, body string) string {
	t.Helper()
	var envelope struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("%s %s: non-stream body is not JSON with a status field: %v; body=%q", frontendID, concern, err, body)
	}
	return envelope.Status
}

// runTerminalOutputLimit drives every output_limit script through one
// frontend in both transports. Every drive must succeed with HTTP 200: a
// truncation is reported in the wire vocabulary, never as a failed
// response, including the partial-arguments shape.
func runTerminalOutputLimit(t *testing.T, frontendID string, m outputLimitMarkers) {
	t.Helper()
	spec := terminalSpecFor(frontendID)
	scripts := terminalOutputLimitEvents()
	truncated := []string{"incomplete_length", "legacy_max_tokens", "truncated_tool_call", "truncated_partial_args"}
	for _, name := range truncated {
		events := scripts[name]
		bodyNS, bodyS := spec.bodyNonStream, spec.bodyStream
		if name == "truncated_tool_call" || name == "truncated_partial_args" {
			bodyNS = spec.bodyTool
		}
		code, nonStream := driveTerminal(t, frontendID, spec.pathNonStream, bodyNS, events)
		if code != http.StatusOK {
			t.Fatalf("%s output_limit %s non-stream status=%d body=%q, want 200 with truncation vocabulary", frontendID, name, code, nonStream)
		}
		assertContains(t, frontendID, "output_limit/"+name, nonStream, m.truncNonStream...)
		assertNotContains(t, frontendID, "output_limit/"+name, nonStream, m.truncAbsent...)
		if m.truncStatusNS != "" {
			if got := terminalTopLevelStatus(t, frontendID, "output_limit/"+name, nonStream); got != m.truncStatusNS {
				t.Fatalf("%s output_limit %s: top-level status=%q, want %q; body=%q", frontendID, name, got, m.truncStatusNS, nonStream)
			}
		}
		code, stream := driveTerminal(t, frontendID, spec.pathStream, bodyS, events)
		if code != http.StatusOK {
			t.Fatalf("%s output_limit %s stream status=%d body=%q, want 200 with truncation vocabulary", frontendID, name, code, stream)
		}
		assertContains(t, frontendID, "output_limit/"+name, stream, m.truncStream...)
	}
	events := scripts["explicit_completed"]
	code, nonStream := driveTerminal(t, frontendID, spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("%s output_limit explicit_completed non-stream status=%d body=%q", frontendID, code, nonStream)
	}
	assertContains(t, frontendID, "output_limit/explicit_completed", nonStream, m.cleanNonStream...)
	if m.cleanStatusNS != "" {
		if got := terminalTopLevelStatus(t, frontendID, "output_limit/explicit_completed", nonStream); got != m.cleanStatusNS {
			t.Fatalf("%s output_limit explicit_completed: top-level status=%q, want %q; body=%q", frontendID, got, m.cleanStatusNS, nonStream)
		}
	}
	code, stream := driveTerminal(t, frontendID, spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("%s output_limit explicit_completed stream status=%d body=%q", frontendID, code, stream)
	}
	assertContains(t, frontendID, "output_limit/explicit_completed", stream, m.cleanStream...)
}

func testTerminalAnthropicOutputLimit(t *testing.T) {
	t.Helper()
	runTerminalOutputLimit(t, "anthropic", outputLimitMarkers{
		truncNonStream: []string{`"stop_reason":"max_tokens"`},
		truncStream:    []string{`"stop_reason":"max_tokens"`},
		truncAbsent:    []string{`"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`},
		cleanNonStream: []string{`"stop_reason":"end_turn"`},
		cleanStream:    []string{`"stop_reason":"end_turn"`},
	})
}

func testTerminalOpenAILegacyOutputLimit(t *testing.T) {
	t.Helper()
	runTerminalOutputLimit(t, "openai-legacy", outputLimitMarkers{
		truncNonStream: []string{`"finish_reason":"length"`},
		truncStream:    []string{`"finish_reason":"length"`},
		truncAbsent:    []string{`"finish_reason":"stop"`, `"finish_reason":"tool_calls"`},
		cleanNonStream: []string{`"finish_reason":"stop"`},
		cleanStream:    []string{`"finish_reason":"stop"`},
	})
}

func testTerminalOpenAIResponsesOutputLimit(t *testing.T) {
	t.Helper()
	runTerminalOutputLimit(t, "openai-responses", outputLimitMarkers{
		truncNonStream: []string{`"status":"incomplete"`, `"reason":"max_output_tokens"`},
		truncStream:    []string{`response.incomplete`},
		cleanNonStream: []string{`"status":"completed"`},
		cleanStream:    []string{`response.completed`},
		truncStatusNS:  "incomplete",
		cleanStatusNS:  "completed",
	})
}

func testTerminalGeminiOutputLimit(t *testing.T) {
	t.Helper()
	runTerminalOutputLimit(t, "gemini", outputLimitMarkers{
		truncNonStream: []string{`"MAX_TOKENS"`},
		truncStream:    []string{`"MAX_TOKENS"`},
		truncAbsent:    []string{`"STOP"`},
		cleanNonStream: []string{`"STOP"`},
		cleanStream:    []string{`"candidates"`},
	})
}

func testTerminalOpenResponsesOutputLimit(t *testing.T) {
	t.Helper()
	runTerminalOutputLimit(t, "openresponses", outputLimitMarkers{
		truncNonStream: []string{`"status":"incomplete"`},
		truncStream:    []string{`response.incomplete`},
		cleanNonStream: []string{`"status":"completed"`},
		cleanStream:    []string{`response.completed`},
		truncStatusNS:  "incomplete",
		cleanStatusNS:  "completed",
	})
	// WS transport: the same truncation discriminator must hold over WS
	// frames, both ways (incomplete stays incomplete, completed stays
	// completed).
	scripts := terminalOutputLimitEvents()
	truncated := driveTerminalWS(t, scripts["incomplete_length"])
	assertContains(t, "openresponses", "output_limit/ws", truncated, "response.incomplete")
	assertNotContains(t, "openresponses", "output_limit/ws", truncated, "response.completed")
	completed := driveTerminalWS(t, scripts["explicit_completed"])
	assertContains(t, "openresponses", "output_limit/ws", completed, "response.completed")
	assertNotContains(t, "openresponses", "output_limit/ws", completed, "response.incomplete")
}

// Error-taxonomy concern (issue #1109).
//
// upstream_error cells assert per-frontend envelopes for mid-stream
// EventError terminals but not cross-frontend parity. This concern feeds one
// catalog of canonical executor errors through every frontend's
// MountedHarness with Execute itself failing, and asserts the wire matches
// the shared frontends/execerr contract: the same canonical error maps to
// the same severity (HTTP status) and client-safe message everywhere. The
// oracle is execerr.ClassifyExecute plus execerr.OpenAIWireErrorType; the
// test never hardcodes the mapping table, so a frontend inventing its own
// error classes fails this concern instead of drifting silently.
// openresponses delegates to execerr.ClassifyExecute too; wire-specific
// branches (*aipapi.StreamError, context cancel/timeout) stay frontend-owned
// and are not part of this catalog.
type taxonomyCase struct {
	name string
	err  error
}

// terminalErrorTaxonomyCatalog holds one canonical error per
// execerr.ClassifyExecute branch. Internal detail strings are unique per
// case so the runner can also assert they never reach the wire.
func terminalErrorTaxonomyCatalog() []taxonomyCase {
	return []taxonomyCase{
		{name: "session_denial", err: aipapi.NewSessionDenialMissingPrincipal("taxonomy-internal-session")},
		{name: "policy_denied", err: aipapi.NewPolicyDeniedError("pre_request", "taxonomy", "taxonomy_denied", "taxonomy_denied", "taxonomy denied by policy", nil)},
		{name: "policy_failure", err: aipapi.NewPolicyFailureError("tool_policy", "taxonomy", "taxonomy_timeout", "taxonomy_failure", "", nil)},
		{name: "policy_malformed", err: aipapi.NewPolicyMalformedError("completion", "taxonomy", "taxonomy_malformed", "taxonomy_malformed", "", nil)},
		{name: "unsafe_composition", err: fmt.Errorf("taxonomy execute: %w", routing.ErrUnsafeExecutionComposition)},
		{name: "mandatory_buffering", err: &runtime.MandatoryBufferingError{Reason: runtime.ReasonMandatoryBufferingOverflow, FinalizerID: "taxonomy", ToolCallID: "call_1", MaxArgsBytes: 8}},
		{name: "aipapi_reject", err: &aipapi.RejectError{Reason: "taxonomy missing capability"}},
		{name: "prerequest_reject", err: prerequest.NewRejectError("taxonomy-policy", "taxonomy blocked")},
		{name: "context_limit", err: fmt.Errorf("taxonomy execute: %w", aipapi.ErrAllCandidatesContextLimitExceeded)},
		{name: "billing_denied", err: fmt.Errorf("taxonomy admission: %w", billingport.ErrInsufficientSpendable)},
		{name: "billing_unavailable", err: fmt.Errorf("taxonomy admission: %w", billingport.ErrBillingStoreUnavailable)},
		{name: "internal", err: errors.New("taxonomy boom")},
	}
}

// runTerminalErrorTaxonomy drives every catalog error through one frontend in
// each applicable transport. Execute fails before headers, so both
// transports must render the oracle envelope; the stream drive mirrors the
// upstream_error logic defensively (an error status must equal the oracle, a
// 200 must be truncated without a clean terminal).
func runTerminalErrorTaxonomy(t *testing.T, frontendID string) {
	t.Helper()
	spec := terminalSpecFor(frontendID)
	bodyNonStream := spec.bodyNonStream
	if spec.isDecisionOnly {
		bodyNonStream = spec.decisionBody
	}
	transports := []struct {
		name string
		path string
		body string
	}{
		{name: "non-stream", path: spec.pathNonStream, body: bodyNonStream},
	}
	if !spec.isDecisionOnly {
		transports = append(transports, struct {
			name string
			path string
			body string
		}{name: "stream", path: spec.pathStream, body: spec.bodyStream})
	}
	for _, tc := range terminalErrorTaxonomyCatalog() {
		t.Run(tc.name, func(t *testing.T) {
			out := execerr.ClassifyExecute(tc.err)
			for _, tr := range transports {
				code, wire := driveTerminalWithScript(t, frontendID, tr.path, tr.body, EventScript{Err: tc.err})
				assertTaxonomyWire(t, frontendID, tc.name, tr.name, out, code, wire)
			}
		})
	}
}

// assertTaxonomyWire asserts one wire response against the shared oracle.
func assertTaxonomyWire(t *testing.T, frontendID, caseName, transport string, out execerr.Outcome, code int, wire string) {
	t.Helper()
	label := frontendID + " error_taxonomy/" + caseName + " " + transport
	if code >= 400 && code < 600 {
		if code != out.Status {
			t.Fatalf("%s: status=%d, want oracle status=%d (kind=%d); body=%q", label, code, out.Status, out.Kind, wire)
		}
		msg, wireType := decodeTaxonomyWire(t, frontendID, label, wire)
		if !strings.Contains(msg, out.Message) {
			t.Fatalf("%s: wire message %q missing oracle message %q; body=%q", label, msg, out.Message, wire)
		}
		assertTaxonomyType(t, frontendID, label, out, wireType)
		assertTaxonomyNoLeak(t, label, caseName, msg)
		return
	}
	if code != http.StatusOK {
		t.Fatalf("%s: status=%d, want oracle status=%d or truncated 200; body=%q", label, code, out.Status, wire)
	}
	assertTaxonomyTruncated(t, frontendID, label, wire)
}

// decodeTaxonomyWire extracts the client-visible message and type discriminator
// from one frontend's error envelope. It only parses envelopes; the expected
// values always come from the execerr oracle.
func decodeTaxonomyWire(t *testing.T, frontendID, label, wire string) (message, wireType string) {
	t.Helper()
	switch frontendID {
	case "openai-legacy", "openai-responses":
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(wire), &payload); err != nil {
			t.Fatalf("%s: decode openai envelope: %v; body=%q", label, err, wire)
		}
		return payload.Error.Message, payload.Error.Type
	case "anthropic":
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(wire), &payload); err != nil {
			t.Fatalf("%s: decode anthropic envelope: %v; body=%q", label, err, wire)
		}
		return payload.Error.Message, payload.Error.Type
	case "gemini":
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(wire), &payload); err != nil {
			t.Fatalf("%s: decode gemini envelope: %v; body=%q", label, err, wire)
		}
		return payload.Error.Message, payload.Error.Status
	case "systemone":
		var payload struct {
			Detail json.RawMessage `json:"detail"`
		}
		if err := json.Unmarshal([]byte(wire), &payload); err != nil {
			t.Fatalf("%s: decode systemone envelope: %v; body=%q", label, err, wire)
		}
		var detail string
		if err := json.Unmarshal(payload.Detail, &detail); err == nil {
			return detail, ""
		}
		return string(payload.Detail), ""
	case "openresponses":
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(wire), &payload); err != nil {
			t.Fatalf("%s: decode openresponses envelope: %v; body=%q", label, err, wire)
		}
		return payload.Error.Message, payload.Error.Type
	default:
		t.Fatalf("%s: unknown frontend %q", label, frontendID)
		return "", ""
	}
}

// assertTaxonomyType asserts the OpenAI-family wire type against the shared
// OpenAIWireErrorType oracle. Other frontends own their protocol type
// vocabulary (permission_error, Google RPC statuses, detail-only); their
// parity contract is status plus message.
func assertTaxonomyType(t *testing.T, frontendID, label string, out execerr.Outcome, wireType string) {
	t.Helper()
	switch frontendID {
	case "openai-legacy", "openai-responses":
		if out.Kind == execerr.KindBillingDenied {
			return // registered insufficient_quota code; covered by frontend characterization tests
		}
		if want := execerr.OpenAIWireErrorType(out.Status); wireType != want {
			t.Fatalf("%s: wire type=%q, want oracle type=%q (status=%d)", label, wireType, want, out.Status)
		}
	}
}

// assertTaxonomyNoLeak asserts executor-internal detail never reaches the
// client-visible message for cases carrying unique internal strings.
func assertTaxonomyNoLeak(t *testing.T, label, caseName, msg string) {
	t.Helper()
	switch caseName {
	case "internal":
		if strings.Contains(msg, "taxonomy boom") {
			t.Fatalf("%s: internal wire message leaks executor detail: %q", label, msg)
		}
	case "session_denial":
		if strings.Contains(msg, "taxonomy-internal-session") {
			t.Fatalf("%s: session wire message leaks internal reason: %q", label, msg)
		}
	}
}

// assertTaxonomyTruncated mirrors the upstream_error stream logic for the
// defensive 200 branch: a truncated stream must carry no clean terminal.
func assertTaxonomyTruncated(t *testing.T, frontendID, label, wire string) {
	t.Helper()
	switch frontendID {
	case "anthropic":
		assertNotContains(t, frontendID, "error_taxonomy", wire, `"stop_reason":"end_turn"`, "message_stop")
	case "gemini":
		assertNotContains(t, frontendID, "error_taxonomy", wire, "hello")
	case "openai-legacy":
		assertNotContains(t, frontendID, "error_taxonomy", wire, `"finish_reason":"stop"`, `"finish_reason":"tool_calls"`)
	case "openai-responses":
		assertNotContains(t, frontendID, "error_taxonomy", wire, "response.completed")
	case "openresponses":
		if !strings.Contains(strings.ToLower(wire), "error") {
			t.Fatalf("%s: truncated stream missing error signal; body=%q", label, wire)
		}
	default:
		t.Fatalf("%s: unexpected 200 for execute error; body=%q", label, wire)
	}
}

func testTerminalAnthropicErrorTaxonomy(t *testing.T) {
	t.Helper()
	runTerminalErrorTaxonomy(t, "anthropic")
}

func testTerminalGeminiErrorTaxonomy(t *testing.T) {
	t.Helper()
	runTerminalErrorTaxonomy(t, "gemini")
}

func testTerminalOpenAILegacyErrorTaxonomy(t *testing.T) {
	t.Helper()
	runTerminalErrorTaxonomy(t, "openai-legacy")
}

func testTerminalOpenAIResponsesErrorTaxonomy(t *testing.T) {
	t.Helper()
	runTerminalErrorTaxonomy(t, "openai-responses")
}

func testTerminalOpenResponsesErrorTaxonomy(t *testing.T) {
	t.Helper()
	runTerminalErrorTaxonomy(t, "openresponses")
}

func testTerminalSystemOneErrorTaxonomy(t *testing.T) {
	t.Helper()
	runTerminalErrorTaxonomy(t, "systemone")
}

// terminalRefusalEvents scripts the canonical refusal terminal: assistant
// refusal text closed by a ResponseFinished carrying the refusal lexeme
// (content_filter) with authoritative completed status. The completed status
// follows the compactiondetect precedent (rules_test.go: authoritative
// completed status wins over the ambiguous reason): a refusal is a complete
// upstream decision, never a truncation, so it must never be rewritten to
// the output-limit (length) taxonomy.
func terminalRefusalEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "I cannot help with that"},
		{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed", FinishReason: "content_filter"},
	}
}

// refusalMarkers holds one frontend's native wire vocabulary for the
// content_filter (refusal) concern: the refusal discriminator per transport
// and the clean/length vocabulary the refusal drive must not carry.
type refusalMarkers struct {
	refusalNonStream []string
	refusalStream    []string
	refusalAbsent    []string
}

// runTerminalRefusal drives the canonical refusal script through one frontend
// in both transports. Every drive must succeed with HTTP 200 carrying the
// protocol's refusal shape: a refusal is reported in the wire vocabulary,
// never as a failed response, never as a clean stop, and never with the
// output-limit (length) vocabulary.
func runTerminalRefusal(t *testing.T, frontendID string, m refusalMarkers) {
	t.Helper()
	spec := terminalSpecFor(frontendID)
	events := terminalRefusalEvents()
	code, nonStream := driveTerminal(t, frontendID, spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("%s content_filter non-stream status=%d body=%q, want 200 with refusal vocabulary", frontendID, code, nonStream)
	}
	assertContains(t, frontendID, "content_filter", nonStream, m.refusalNonStream...)
	assertNotContains(t, frontendID, "content_filter", nonStream, m.refusalAbsent...)
	code, stream := driveTerminal(t, frontendID, spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("%s content_filter stream status=%d body=%q, want 200 with refusal vocabulary", frontendID, code, stream)
	}
	assertContains(t, frontendID, "content_filter", stream, m.refusalStream...)
	assertNotContains(t, frontendID, "content_filter", stream, m.refusalAbsent...)
}

func testTerminalAnthropicContentFilter(t *testing.T) {
	t.Helper()
	runTerminalRefusal(t, "anthropic", refusalMarkers{
		refusalNonStream: []string{`"stop_reason":"refusal"`, "I cannot help with that"},
		refusalStream:    []string{`"stop_reason":"refusal"`, "I cannot help with that"},
		refusalAbsent:    []string{`"stop_reason":"end_turn"`, `"stop_reason":"max_tokens"`},
	})
}

func testTerminalOpenAILegacyContentFilter(t *testing.T) {
	t.Helper()
	runTerminalRefusal(t, "openai-legacy", refusalMarkers{
		refusalNonStream: []string{`"finish_reason":"content_filter"`, "I cannot help with that"},
		refusalStream:    []string{`"finish_reason":"content_filter"`, "I cannot help with that"},
		refusalAbsent:    []string{`"finish_reason":"stop"`, `"finish_reason":"length"`},
	})
}

func testTerminalOpenAIResponsesContentFilter(t *testing.T) {
	t.Helper()
	runTerminalRefusal(t, "openai-responses", refusalMarkers{
		refusalNonStream: []string{`content_filter`, "I cannot help with that"},
		refusalStream:    []string{`content_filter`, "I cannot help with that"},
		refusalAbsent:    []string{`"max_output_tokens"`, `"reason":"length"`},
	})
}

func testTerminalGeminiContentFilter(t *testing.T) {
	t.Helper()
	runTerminalRefusal(t, "gemini", refusalMarkers{
		refusalNonStream: []string{`"SAFETY"`, "I cannot help with that"},
		refusalStream:    []string{`"SAFETY"`, "I cannot help with that"},
		refusalAbsent:    []string{`"MAX_TOKENS"`, `"STOP"`},
	})
}

func testTerminalOpenResponsesContentFilter(t *testing.T) {
	t.Helper()
	// Absent discriminators are reason-valued: the resource always
	// serializes max_output_tokens (null when unset), so a bare field
	// check could never pass. "reason":"length" keeps the no-length-
	// taxonomy protection that matters.
	runTerminalRefusal(t, "openresponses", refusalMarkers{
		refusalNonStream: []string{`content_filter`, "I cannot help with that"},
		refusalStream:    []string{`content_filter`, "I cannot help with that"},
		refusalAbsent:    []string{`"reason":"length"`, `"reason":"max_output_tokens"`},
	})
}

// terminalEmptyTurnEvents scripts the degenerate turn for the empty_turn
// concern: a clean terminal with zero visible deltas (no TextDelta, tool,
// reasoning, or usage events). The fully-empty shape has no other coverage:
// reasoning-only and tool-only turns carry their own deltas, but nothing pins
// what a turn with no visible output at all means on the wire.
func terminalEmptyTurnEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventResponseFinished},
	}
}

// emptyTurnMarkers holds one frontend's clean-terminal wire vocabulary for the
// empty_turn concern: the same terminal discriminator its clean_text cell
// asserts, minus any text payload. The absent list mirrors the clean cell's
// truncation/error discriminators.
type emptyTurnMarkers struct {
	nonStream []string
	stream    []string
	absent    []string
}

// runTerminalEmptyTurn drives the degenerate zero-visible-delta stream through
// one frontend in both transports. Both drives must succeed with HTTP 200 and
// carry the clean terminal vocabulary: a turn with no visible output is a
// successful empty turn, never an empty-body 500, an error envelope, or a
// stream that never terminates. The non-stream body must be valid JSON and the
// stream body valid SSE; driveTerminal serves synchronously via httptest, so a
// non-terminating stream would block the drive, and the terminal-marker
// assertions prove termination at the wire level.
func runTerminalEmptyTurn(t *testing.T, frontendID string, m emptyTurnMarkers) {
	t.Helper()
	spec := terminalSpecFor(frontendID)
	events := terminalEmptyTurnEvents()
	code, nonStream := driveTerminal(t, frontendID, spec.pathNonStream, spec.bodyNonStream, events)
	if code != http.StatusOK {
		t.Fatalf("%s empty_turn non-stream status=%d body=%q, want 200 with clean terminal vocabulary", frontendID, code, nonStream)
	}
	if !json.Valid([]byte(nonStream)) {
		t.Fatalf("%s empty_turn non-stream body is not valid JSON; body=%q", frontendID, nonStream)
	}
	assertContains(t, frontendID, "empty_turn", nonStream, m.nonStream...)
	assertNotContains(t, frontendID, "empty_turn", nonStream, m.absent...)
	code, stream := driveTerminal(t, frontendID, spec.pathStream, spec.bodyStream, events)
	if code != http.StatusOK {
		t.Fatalf("%s empty_turn stream status=%d body=%q, want 200 with clean terminal vocabulary", frontendID, code, stream)
	}
	assertContains(t, frontendID, "empty_turn", stream, append([]string{"data:"}, m.stream...)...)
	assertNotContains(t, frontendID, "empty_turn", stream, m.absent...)
}

func testTerminalAnthropicEmptyTurn(t *testing.T) {
	t.Helper()
	runTerminalEmptyTurn(t, "anthropic", emptyTurnMarkers{
		nonStream: []string{`"stop_reason":"end_turn"`},
		stream:    []string{`"stop_reason":"end_turn"`, "message_stop"},
		absent:    []string{`"stop_reason":"max_tokens"`, `"type":"error"`},
	})
}

func testTerminalGeminiEmptyTurn(t *testing.T) {
	t.Helper()
	runTerminalEmptyTurn(t, "gemini", emptyTurnMarkers{
		nonStream: []string{`"candidates"`, `"STOP"`},
		stream:    []string{`"candidates"`, `"STOP"`},
		absent:    []string{`"MAX_TOKENS"`},
	})
}

func testTerminalOpenAILegacyEmptyTurn(t *testing.T) {
	t.Helper()
	runTerminalEmptyTurn(t, "openai-legacy", emptyTurnMarkers{
		nonStream: []string{`"finish_reason":"stop"`},
		stream:    []string{`"finish_reason":"stop"`},
		absent:    []string{`"finish_reason":"length"`},
	})
}

func testTerminalOpenAIResponsesEmptyTurn(t *testing.T) {
	t.Helper()
	runTerminalEmptyTurn(t, "openai-responses", emptyTurnMarkers{
		nonStream: []string{`"status":"completed"`},
		stream:    []string{`response.completed`, `"status":"completed"`},
		absent:    []string{`"status":"incomplete"`},
	})
}

func testTerminalOpenResponsesEmptyTurn(t *testing.T) {
	t.Helper()
	runTerminalEmptyTurn(t, "openresponses", emptyTurnMarkers{
		nonStream: []string{`"status":"completed"`},
		stream:    []string{`response.completed`},
		absent:    []string{`"status":"incomplete"`},
	})
}

// cancelMarkers holds one frontend's clean-terminal vocabulary for the
// cancellation concern: wire fragments that must be absent from every
// cancellation drive, in both transports and both legs.
type cancelMarkers struct {
	absent []string
}

// terminalCancelStream is a context-aware canonical stream for the
// cancellation concern. It emits ResponseStarted, MessageStarted (or a
// DecisionResult for decision-only frontends) and one content event, then
// blocks on the Execute context and returns its cancellation truncated:
// never a ResponseFinished and never an EventError.
type terminalCancelStream struct {
	ctx         context.Context
	mu          sync.Mutex
	recvs       int
	content     chan struct{}
	contentOnce sync.Once
	decision    bool
}

func (s *terminalCancelStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	s.recvs++
	n := s.recvs
	s.mu.Unlock()
	signalContent := func() {
		s.contentOnce.Do(func() { close(s.content) })
	}
	switch n {
	case 1:
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	case 2:
		if s.decision {
			signalContent()
			return aipapi.Event{Kind: aipapi.EventDecisionResult, Decision: &aipapi.DecisionResult{
				Model:   "jev",
				Answers: []aipapi.DecisionAnswer{{QuestionID: "q", Kind: aipapi.DecisionKindNoul, PTrue: 0.8}},
			}}, nil
		}
		return aipapi.Event{Kind: aipapi.EventMessageStarted}, nil
	case 3:
		if s.decision {
			break
		}
		signalContent()
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}, nil
	default:
	}
	<-s.ctx.Done()
	return aipapi.Event{}, s.ctx.Err()
}

func (s *terminalCancelStream) Close() error { return nil }

// driveTerminalCancel sends one request with a cancellable A-leg context and
// a context-aware canonical stream. When beforeFirst the context is already
// canceled on entry; otherwise ServeHTTP runs until the first content event
// is consumed downstream, then the context is canceled and the drive waits
// for the handler to return. Synchronization is channels plus context
// cancellation only; the timeout in waitSignal only bounds a stuck drive.
func driveTerminalCancel(t *testing.T, frontendID, path, body string, decision, beforeFirst bool) (int, string) {
	t.Helper()
	spec := terminalSpecFor(frontendID)
	if spec.mount == nil {
		t.Fatalf("unknown frontend %q", frontendID)
	}
	executor := &CapturingExecutor{}
	stream := &terminalCancelStream{content: make(chan struct{}), decision: decision}
	executor.Script.Stream = func(ctx context.Context) aipapi.EventStream {
		stream.ctx = ctx
		return stream
	}
	h := &MountedHarness{
		Descriptor:        semantic.SubjectDescriptor{ID: frontendID, Kind: semantic.KindFrontend},
		Mount:             spec.mount,
		Path:              func(semantic.ScenarioDescriptor) string { return path },
		Body:              func(semantic.ScenarioDescriptor) []byte { return []byte(body) },
		ExecutorBoundary:  executor,
		ContinuationStore: aipcont.NewMemoryStore(),
		AuthProvider:      allowFrontendTCKAuth{},
	}
	if err := h.Reset(); err != nil {
		t.Fatalf("%s reset: %v", frontendID, err)
	}
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newRequest := func() (*httptest.ResponseRecorder, *http.Request) {
		req := httptest.NewRequestWithContext(reqCtx, http.MethodPost, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer frontend-tck")
		req.Header.Set("X-AIP-Session-ID", "frontend-tck-session")
		return httptest.NewRecorder(), req
	}
	if beforeFirst {
		cancel()
		rec, req := newRequest()
		h.Handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	rec, req := newRequest()
	done := make(chan struct{})
	go func() { h.Handler.ServeHTTP(rec, req); close(done) }()
	if err := waitSignal(stream.content, "cancellation stream never delivered first content"); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := waitSignal(done, "cancellation request stalled"); err != nil {
		t.Fatal(err)
	}
	return rec.Code, rec.Body.String()
}

// assertTerminalCancel pins the cancellation wire contract: a canceled A-leg
// never yields a successful terminal. wantStream selects the mid-flight
// streaming leg, which must hold the 200 framing with a truncated body (cf
// the upstream_error stream contract). strictNon200 selects the
// before-first-content legs, which must fail the response outright. The
// mid-flight non-streaming leg accepts either shape: most frontends fail it
// outright, while openresponses abandons an unwritten 200 (truncated empty),
// so only the absent-terminal vocabulary is asserted there.
func assertTerminalCancel(t *testing.T, frontendID, leg, transport string, code int, wire string, absent []string, wantStream, strictNon200 bool) {
	t.Helper()
	assertNotContains(t, frontendID, "cancellation/"+leg, wire, absent...)
	switch {
	case strictNon200:
		if code == http.StatusOK {
			t.Fatalf("%s cancellation %s %s: status=200 with body=%q, want non-200 for pre-content cancel", frontendID, leg, transport, wire)
		}
	case wantStream:
		if code != http.StatusOK {
			t.Fatalf("%s cancellation %s %s: status=%d body=%q, want truncated 200 without clean terminal", frontendID, leg, transport, code, wire)
		}
	default:
		if code == http.StatusOK && strings.TrimSpace(wire) != "" {
			t.Fatalf("%s cancellation %s %s: status=200 with body=%q, want non-200 or truncated empty", frontendID, leg, transport, wire)
		}
	}
}

// runTerminalCancellation drives both cancellation legs in both transports:
// cancel before first content (context canceled on entry) and cancel after
// first content (context canceled once downstream consumed one content
// event). Decision-only frontends run the non-streaming legs with a
// decision-flavored script.
func runTerminalCancellation(t *testing.T, frontendID string, m cancelMarkers) {
	t.Helper()
	spec := terminalSpecFor(frontendID)
	if spec.isDecisionOnly {
		code, wire := driveTerminalCancel(t, frontendID, spec.pathNonStream, spec.decisionBody, true, true)
		assertTerminalCancel(t, frontendID, "before_first_content", "nonstream", code, wire, m.absent, false, true)
		code, wire = driveTerminalCancel(t, frontendID, spec.pathNonStream, spec.decisionBody, true, false)
		assertTerminalCancel(t, frontendID, "after_first_content", "nonstream", code, wire, m.absent, false, false)
		return
	}
	code, wire := driveTerminalCancel(t, frontendID, spec.pathNonStream, spec.bodyNonStream, false, true)
	assertTerminalCancel(t, frontendID, "before_first_content", "nonstream", code, wire, m.absent, false, true)
	code, wire = driveTerminalCancel(t, frontendID, spec.pathStream, spec.bodyStream, false, true)
	assertTerminalCancel(t, frontendID, "before_first_content", "stream", code, wire, m.absent, false, true)
	code, wire = driveTerminalCancel(t, frontendID, spec.pathNonStream, spec.bodyNonStream, false, false)
	assertTerminalCancel(t, frontendID, "after_first_content", "nonstream", code, wire, m.absent, false, false)
	code, wire = driveTerminalCancel(t, frontendID, spec.pathStream, spec.bodyStream, false, false)
	assertTerminalCancel(t, frontendID, "after_first_content", "stream", code, wire, m.absent, true, false)
}

func testTerminalAnthropicCancellation(t *testing.T) {
	t.Helper()
	runTerminalCancellation(t, "anthropic", cancelMarkers{
		absent: []string{`"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`, "message_stop"},
	})
}

func testTerminalGeminiCancellation(t *testing.T) {
	t.Helper()
	runTerminalCancellation(t, "gemini", cancelMarkers{
		absent: []string{`"finishReason"`, `"STOP"`, `"MAX_TOKENS"`},
	})
}

func testTerminalOpenAILegacyCancellation(t *testing.T) {
	t.Helper()
	runTerminalCancellation(t, "openai-legacy", cancelMarkers{
		absent: []string{`"finish_reason":"stop"`, `"finish_reason":"tool_calls"`, `"finish_reason":"length"`, "data: [DONE]"},
	})
}

func testTerminalOpenAIResponsesCancellation(t *testing.T) {
	t.Helper()
	runTerminalCancellation(t, "openai-responses", cancelMarkers{
		absent: []string{"response.completed", `"status":"completed"`, `"status":"incomplete"`},
	})
}

func testTerminalOpenResponsesCancellation(t *testing.T) {
	t.Helper()
	runTerminalCancellation(t, "openresponses", cancelMarkers{
		absent: []string{"response.completed", `"status":"completed"`, `"status":"incomplete"`},
	})
}

func testTerminalSystemOneCancellation(t *testing.T) {
	t.Helper()
	runTerminalCancellation(t, "systemone", cancelMarkers{
		absent: []string{`"answers"`, `"input_tokens"`},
	})
}
