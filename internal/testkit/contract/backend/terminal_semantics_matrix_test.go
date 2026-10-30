package backend

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/refbackend/bedrock"
	"github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	"github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
)

// backendTerminalConcern is one cross-cutting terminal-semantics concern for
// essential backend kinds. It mirrors the frontend matrix: every backend kind
// x every concern must name its covering test.
type backendTerminalConcern string

const (
	backendConcernCleanText     backendTerminalConcern = "clean_text"
	backendConcernCleanToolCall backendTerminalConcern = "clean_tool_call"
	backendConcernOutputLimit   backendTerminalConcern = "output_limit"
	backendConcernContentFilter backendTerminalConcern = "content_filter"
	backendConcernEmptyTurn     backendTerminalConcern = "empty_turn"
	backendConcernCancellation  backendTerminalConcern = "cancellation"
	backendConcernUpstreamError backendTerminalConcern = "upstream_error"
	backendConcernUsageReported backendTerminalConcern = "usage_reported"
)

var allBackendTerminalConcerns = []backendTerminalConcern{
	backendConcernCleanText,
	backendConcernCleanToolCall,
	backendConcernOutputLimit,
	backendConcernContentFilter,
	backendConcernEmptyTurn,
	backendConcernCancellation,
	backendConcernUpstreamError,
	backendConcernUsageReported,
}

// backendTerminalCorpusAnchor maps every terminal concern to its shared
// BaselineScenarioCorpus scenario ID (terminal-corpus promotion). Cells keep
// their builders and assertions; the anchor is the traceability link proving
// the concern is inherited through the public corpus.
var backendTerminalCorpusAnchor = map[backendTerminalConcern]string{
	backendConcernCleanText:     "terminal-clean-text",
	backendConcernCleanToolCall: "terminal-clean-tool-call",
	backendConcernOutputLimit:   "terminal-output-limit",
	backendConcernUpstreamError: "terminal-upstream-error",
	backendConcernUsageReported: "terminal-usage-reported",
	backendConcernContentFilter: "terminal-content-filter",
	backendConcernEmptyTurn:     "terminal-empty-turn",
	backendConcernCancellation:  "cancellation",
}

// backendTerminalCell is one matrix cell. run is a compile-time reference to
// the covering test; na is an explicit not-applicable reason; sameAs
// delegates an essential kind to its protocol-family row.
type backendTerminalCell struct {
	run    func(t *testing.T)
	na     string
	sameAs string
}

// backendTerminalMatrix is the registry of every essential backend kind x
// every terminal concern. A missing cell fails the build.
var backendTerminalMatrix = map[string]map[backendTerminalConcern]backendTerminalCell{
	"openai-responses": {
		backendConcernCleanText:     {run: backendCleanCell("openai-responses")},
		backendConcernCleanToolCall: {run: backendToolCell("openai-responses")},
		backendConcernOutputLimit:   {run: backendLimitCell("openai-responses")},
		backendConcernContentFilter: {run: backendRefusalCell("openai-responses")},
		backendConcernEmptyTurn:     {run: backendEmptyCell("openai-responses")},
		backendConcernCancellation:  {run: backendCancelCell("openai-responses")},
		backendConcernUpstreamError: {run: backendUpstreamErrorCell("openai-responses")},
		backendConcernUsageReported: {run: backendUsageCell("openai-responses")},
	},
	"openai-legacy": {
		backendConcernCleanText:     {run: backendCleanCell("openai-legacy")},
		backendConcernCleanToolCall: {run: backendToolCell("openai-legacy")},
		backendConcernOutputLimit:   {run: backendLimitCell("openai-legacy")},
		backendConcernContentFilter: {run: backendRefusalCell("openai-legacy")},
		backendConcernEmptyTurn:     {run: backendEmptyCell("openai-legacy")},
		backendConcernCancellation:  {run: backendCancelCell("openai-legacy")},
		backendConcernUpstreamError: {run: backendUpstreamErrorCell("openai-legacy")},
		backendConcernUsageReported: {run: backendUsageCell("openai-legacy")},
	},
	"anthropic": {
		backendConcernCleanText:     {run: backendCleanCell("anthropic")},
		backendConcernCleanToolCall: {run: backendToolCell("anthropic")},
		backendConcernOutputLimit:   {run: backendLimitCell("anthropic")},
		backendConcernContentFilter: {run: backendRefusalCell("anthropic")},
		backendConcernEmptyTurn:     {run: backendEmptyCell("anthropic")},
		backendConcernCancellation:  {run: backendCancelCell("anthropic")},
		backendConcernUpstreamError: {run: backendUpstreamErrorCell("anthropic")},
		backendConcernUsageReported: {run: backendUsageCell("anthropic")},
	},
	"gemini": {
		backendConcernCleanText:     {run: backendCleanCell("gemini")},
		backendConcernCleanToolCall: {run: backendToolCell("gemini")},
		backendConcernOutputLimit:   {run: backendLimitCell("gemini")},
		backendConcernContentFilter: {run: backendRefusalCell("gemini")},
		backendConcernEmptyTurn:     {run: backendEmptyCell("gemini")},
		backendConcernCancellation:  {run: backendCancelCell("gemini")},
		backendConcernUpstreamError: {run: backendUpstreamErrorCell("gemini")},
		backendConcernUsageReported: {run: backendUsageCell("gemini")},
	},
	"bedrock": {
		backendConcernCleanText:     {run: backendCleanCell("bedrock")},
		backendConcernCleanToolCall: {run: backendToolCell("bedrock")},
		backendConcernOutputLimit:   {run: backendLimitCell("bedrock")},
		backendConcernContentFilter: {run: backendRefusalCell("bedrock")},
		backendConcernEmptyTurn:     {run: backendEmptyCell("bedrock")},
		backendConcernCancellation:  {run: backendCancelCell("bedrock")},
		backendConcernUpstreamError: {run: backendUpstreamErrorCell("bedrock")},
		backendConcernUsageReported: {run: backendUsageCell("bedrock")},
	},
	"custom-openresponses-compatible": {
		backendConcernCleanText:     {run: backendCleanCell("custom-openresponses-compatible")},
		backendConcernCleanToolCall: {run: backendToolCell("custom-openresponses-compatible")},
		backendConcernOutputLimit:   {run: backendLimitCell("custom-openresponses-compatible")},
		backendConcernContentFilter: {run: backendRefusalCell("custom-openresponses-compatible")},
		backendConcernEmptyTurn:     {run: backendEmptyCell("custom-openresponses-compatible")},
		backendConcernCancellation:  {run: backendOpenResponsesCompatCancelCell},
		backendConcernUpstreamError: {run: backendOpenResponsesCompatErrorCell},
		backendConcernUsageReported: {run: backendOpenResponsesCompatUsageCell},
	},
	"custom-systemone-compatible": {
		backendConcernCleanText:     {na: "systemone decision backend has no text terminal"},
		backendConcernCleanToolCall: {na: "systemone decision backend has no tool terminal"},
		backendConcernOutputLimit:   {na: "systemone decision backend has no max_tokens truncation"},
		backendConcernContentFilter: {na: "systemone decision backend has no refusal terminal"},
		backendConcernEmptyTurn:     {na: "systemone decision backend requires one answer per question; an empty turn has no clean shape"},
		backendConcernCancellation:  {run: backendSystemOneCancelCell},
		backendConcernUpstreamError: {run: backendSystemOneErrorCell},
		backendConcernUsageReported: {run: backendSystemOneUsageCell},
	},
	"alibaba-token-plan-intl": {
		backendConcernCleanText:     {sameAs: "anthropic"},
		backendConcernCleanToolCall: {sameAs: "anthropic"},
		backendConcernOutputLimit:   {sameAs: "anthropic"},
		backendConcernContentFilter: {sameAs: "anthropic"},
		backendConcernEmptyTurn:     {sameAs: "anthropic"},
		backendConcernCancellation:  {sameAs: "anthropic"},
		backendConcernUpstreamError: {sameAs: "anthropic"},
		backendConcernUsageReported: {sameAs: "anthropic"},
	},
	"custom-openai-legacy-compatible": {
		backendConcernCleanText:     {sameAs: "openai-legacy"},
		backendConcernCleanToolCall: {sameAs: "openai-legacy"},
		backendConcernOutputLimit:   {sameAs: "openai-legacy"},
		backendConcernContentFilter: {sameAs: "openai-legacy"},
		backendConcernEmptyTurn:     {sameAs: "openai-legacy"},
		backendConcernCancellation:  {sameAs: "openai-legacy"},
		backendConcernUpstreamError: {sameAs: "openai-legacy"},
		backendConcernUsageReported: {sameAs: "openai-legacy"},
	},
	"custom-openai-responses-compatible": {
		backendConcernCleanText:     {sameAs: "openai-responses"},
		backendConcernCleanToolCall: {sameAs: "openai-responses"},
		backendConcernOutputLimit:   {sameAs: "openai-responses"},
		backendConcernContentFilter: {sameAs: "openai-responses"},
		backendConcernEmptyTurn:     {sameAs: "openai-responses"},
		backendConcernCancellation:  {sameAs: "openai-responses"},
		backendConcernUpstreamError: {sameAs: "openai-responses"},
		backendConcernUsageReported: {sameAs: "openai-responses"},
	},
	"custom-anthropic-compatible": {
		backendConcernCleanText:     {sameAs: "anthropic"},
		backendConcernCleanToolCall: {sameAs: "anthropic"},
		backendConcernOutputLimit:   {sameAs: "anthropic"},
		backendConcernContentFilter: {sameAs: "anthropic"},
		backendConcernEmptyTurn:     {sameAs: "anthropic"},
		backendConcernCancellation:  {sameAs: "anthropic"},
		backendConcernUpstreamError: {sameAs: "anthropic"},
		backendConcernUsageReported: {sameAs: "anthropic"},
	},
}

// TestBackendTerminalSemantics_Completeness is the point of the slice: adding
// an essential backend kind without filling its matrix row fails go test. An
// extra row for an unregistered kind also fails, so stale rows are removed.
func TestBackendTerminalSemantics_Completeness(t *testing.T) {
	t.Parallel()
	var missing []string
	var want []string
	want = append(want, standardplugins.EssentialBackendKinds()...)
	sort.Strings(want)
	for _, id := range want {
		row, ok := backendTerminalMatrix[id]
		if !ok {
			for _, c := range allBackendTerminalConcerns {
				missing = append(missing, fmt.Sprintf("(%s, %s)", id, c))
			}
			continue
		}
		for _, c := range allBackendTerminalConcerns {
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
				target, ok := backendTerminalMatrix[cell.sameAs]
				if !ok {
					missing = append(missing, fmt.Sprintf("(%s, %s): sameAs %q missing", id, c, cell.sameAs))
					continue
				}
				if _, ok := target[c]; !ok {
					missing = append(missing, fmt.Sprintf("(%s, %s): sameAs %q lacks concern %s", id, c, cell.sameAs, c))
				}
			}
		}
		for c := range row {
			found := slices.Contains(allBackendTerminalConcerns, c)
			if !found {
				missing = append(missing, fmt.Sprintf("(%s, %s): unknown concern", id, c))
			}
		}
	}
	for id := range backendTerminalMatrix {
		found := slices.Contains(want, id)
		if !found {
			missing = append(missing, fmt.Sprintf("(%s, *): stale row for unregistered kind", id))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("backend terminal-semantics matrix incomplete (%d cells); edit internal/testkit/contract/backend/terminal_semantics_matrix_test.go:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestBackendTerminalSemantics_CorpusAnchors proves every matrix concern
// names its shared BaselineScenarioCorpus scenario: the matrices stay the
// enforcement layer while the corpus carries the inherited coverage.
func TestBackendTerminalSemantics_CorpusAnchors(t *testing.T) {
	t.Parallel()
	ids := make(map[string]bool, len(semantic.BaselineScenarioCorpus()))
	for _, sc := range semantic.BaselineScenarioCorpus() {
		ids[string(sc.ID)] = true
	}
	for _, c := range allBackendTerminalConcerns {
		anchor, ok := backendTerminalCorpusAnchor[c]
		if !ok || anchor == "" {
			t.Fatalf("concern %q has no corpus anchor", c)
		}
		if !ids[anchor] {
			t.Fatalf("concern %q anchors unknown corpus scenario %q", c, anchor)
		}
	}
}

// TestBackendTerminalSemantics_Matrix executes every registered cell,
// resolving sameAs delegation to the protocol-family row. Subtests run
// sequentially: buildStandardFamily uses t.Setenv for compatible kinds,
// which cannot run under t.Parallel.
func TestBackendTerminalSemantics_Matrix(t *testing.T) {
	var subjects []string
	for s := range backendTerminalMatrix {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects)
	for _, subject := range subjects {
		for _, c := range allBackendTerminalConcerns {
			cell := backendTerminalMatrix[subject][c]
			if cell.run == nil && strings.TrimSpace(cell.sameAs) == "" {
				continue
			}
			run := resolveBackendCellOrFail(t, subject, c)
			if run == nil {
				continue
			}
			t.Run(subject+"/"+string(c), func(t *testing.T) {
				run(t)
			})
		}
	}
}

// resolveBackendCellOrFail walks a sameAs delegation chain to the covering
// test. A chain ending in na inherits the not-applicable verdict (skip). A
// chain ending nowhere (missing row/concern or cycle) fails loudly: an
// executable cell must never silently skip.
func resolveBackendCellOrFail(t *testing.T, subject string, c backendTerminalConcern) func(t *testing.T) {
	t.Helper()
	seen := map[string]bool{}
	cur := subject
	for {
		if seen[cur] {
			t.Fatalf("(%s, %s): sameAs delegation cycle at %q", subject, c, cur)
		}
		seen[cur] = true
		row, ok := backendTerminalMatrix[cur]
		if !ok {
			t.Fatalf("(%s, %s): sameAs delegates to missing row %q", subject, c, cur)
		}
		cell, ok := row[c]
		if !ok {
			t.Fatalf("(%s, %s): sameAs target %q lacks concern %s", subject, c, cur, c)
		}
		if cell.run != nil {
			return cell.run
		}
		if strings.TrimSpace(cell.sameAs) == "" {
			return nil
		}
		cur = cell.sameAs
	}
}

// backendUpstreamErrorCell returns the covering test for one protocol
// family's upstream_error concern: a terminal upstream failure (scripted via
// the refbackend temperature-0.22 marker, the same trigger the backend TCK
// uses) must surface as an upstream failure with no successful
// ResponseFinished, exactly one upstream call proving the error came from
// upstream rather than admission. Error cells run over streaming transport,
// matching the TCK terminal-error scenarios: several emulators only script
// errors on their SSE paths.
func backendUpstreamErrorCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		probe := &standardProbe{}
		srv := backendTerminalHostedServer(probe)
		t.Cleanup(srv.Close)
		temp := 0.22
		call := aipapi.Call{
			ID:       "terminal-upstream-error",
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Options:  aipapi.GenerationOptions{Temperature: &temp},
			Invocation: aipapi.Invocation{
				Operation:     operationForFamily(familyID),
				TransportMode: aipapi.TransportModeStreaming,
			},
		}
		events, openErr := driveBackendTerminal(t, familyID, srv, probe, call)
		if probe.RequestCount() != 1 {
			t.Fatalf("%s upstream_error: upstream calls=%d, want 1", familyID, probe.RequestCount())
		}
		assertBackendUpstreamFailureTerminal(t, familyID, events, openErr)
	}
}

// assertBackendUpstreamFailureTerminal pins the upstream-failure terminal
// contract shared by the essential, compatible, decision-only, and connector
// upstream_error cells. The failure may surface either as an open/recv error
// or as a terminal EventError (the transport decides which), but it must
// never accompany a successful ResponseFinished, and it must not be
// classified as a client cancellation (core reads that as a caller
// preemption) or as a capability rejection (core reads that as a routing
// refusal). Both tolerated shapes are asserted, so neither can absorb a
// mapper that launders an upstream failure into a clean terminal.
func assertBackendUpstreamFailureTerminal(t *testing.T, label string, events []aipapi.Event, terminalErr error) {
	t.Helper()
	if hasEventKind(events, aipapi.EventResponseFinished) {
		t.Fatalf("%s upstream_error: successful ResponseFinished alongside upstream failure (err=%v events=%v)", label, terminalErr, eventKinds(events))
	}
	if terminalErr == nil {
		if !hasEventKind(events, aipapi.EventError) {
			t.Fatalf("%s upstream_error: upstream failure surfaced as neither error nor Error event; events=%v", label, eventKinds(events))
		}
		return
	}
	if errors.Is(terminalErr, aipapi.ErrCapabilityReject) || isBackendCancelError(terminalErr) {
		t.Fatalf("%s upstream_error: upstream failure surfaced as %v; want an upstream/internal failure, never a cancellation or capability rejection", label, terminalErr)
	}
}

// backendUsageCell returns the covering test for one protocol family's
// usage_reported concern: the emulator's default OK response carries usage,
// so the canonical stream must contain an EventUsageDelta with explicit
// presence before the terminal ResponseFinished, with exactly one upstream
// call. Usage cells run over non-streaming transport.
func backendUsageCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		probe := &standardProbe{}
		srv := backendTerminalHostedServer(probe)
		t.Cleanup(srv.Close)
		call := aipapi.Call{
			ID:       "terminal-usage-reported",
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Invocation: aipapi.Invocation{
				Operation:     operationForFamily(familyID),
				TransportMode: aipapi.TransportModeNonStreaming,
			},
		}
		events, openErr := driveBackendTerminal(t, familyID, srv, probe, call)
		if openErr != nil {
			t.Fatalf("%s usage_reported: open: %v", familyID, openErr)
		}
		if probe.RequestCount() != 1 {
			t.Fatalf("%s usage_reported: upstream calls=%d, want 1", familyID, probe.RequestCount())
		}
		usageIdx, finishIdx := -1, -1
		for i, ev := range events {
			if ev.Kind == aipapi.EventUsageDelta && ev.UsagePresence.Any() {
				usageIdx = i
			}
			if ev.Kind == aipapi.EventResponseFinished {
				finishIdx = i
			}
		}
		if usageIdx < 0 {
			t.Fatalf("%s usage_reported: no EventUsageDelta with explicit presence; events=%v", familyID, eventKinds(events))
		}
		if finishIdx < 0 {
			t.Fatalf("%s usage_reported: no terminal ResponseFinished; events=%v", familyID, eventKinds(events))
		}
		if usageIdx > finishIdx {
			t.Fatalf("%s usage_reported: usage delta after finish (usage=%d finish=%d)", familyID, usageIdx, finishIdx)
		}
	}
}

// backendOpenResponsesCompatErrorCell covers upstream_error for the generic
// OpenResponses-compatible backend against the same stub the standard
// composition TCK uses, with a dedicated always-500 error server: the error
// must surface without a successful ResponseFinished.
func backendOpenResponsesCompatErrorCell(t *testing.T) {
	t.Helper()
	probe := &standardProbe{}
	srv := httptest.NewServer(countingHandler(probe, backendTerminalAlways500("response")))
	t.Cleanup(srv.Close)
	call := aipapi.Call{
		ID:         "terminal-upstream-error",
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{TransportMode: aipapi.TransportModeNonStreaming},
	}
	events, openErr := driveBackendTerminal(t, standardplugins.CustomOpenResponsesCompatibleID, srv, probe, call)
	if probe.RequestCount() != 1 {
		t.Fatalf("custom-openresponses-compatible upstream_error: upstream calls=%d, want 1", probe.RequestCount())
	}
	assertBackendUpstreamFailureTerminal(t, "custom-openresponses-compatible", events, openErr)
}

// backendOpenResponsesCompatUsageCell covers usage_reported for the generic
// OpenResponses-compatible backend against the shared stub: usage must
// precede the terminal finish.
func backendOpenResponsesCompatUsageCell(t *testing.T) {
	t.Helper()
	probe := &standardProbe{}
	mux := http.NewServeMux()
	mux.Handle("/responses", countingHandler(probe, openResponsesCompatHandler()))
	mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	call := aipapi.Call{
		ID:         "terminal-usage-reported",
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{TransportMode: aipapi.TransportModeNonStreaming},
	}
	events, openErr := driveBackendTerminal(t, standardplugins.CustomOpenResponsesCompatibleID, srv, probe, call)
	if openErr != nil {
		t.Fatalf("custom-openresponses-compatible usage_reported: open: %v", openErr)
	}
	usageIdx, finishIdx := -1, -1
	for i, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta && ev.UsagePresence.Any() {
			usageIdx = i
		}
		if ev.Kind == aipapi.EventResponseFinished {
			finishIdx = i
		}
	}
	if usageIdx < 0 {
		t.Fatalf("custom-openresponses-compatible usage_reported: no EventUsageDelta; events=%v", eventKinds(events))
	}
	if finishIdx < 0 || usageIdx > finishIdx {
		t.Fatalf("custom-openresponses-compatible usage_reported: usage must precede finish (usage=%d finish=%d)", usageIdx, finishIdx)
	}
}

// backendSystemOneErrorCell covers upstream_error for the decision-only
// SystemOne-compatible backend: a 500 from upstream must surface as an
// error with no decision result.
func backendSystemOneErrorCell(t *testing.T) {
	t.Helper()
	probe := &standardProbe{}
	srv := httptest.NewServer(countingHandler(probe, backendTerminalAlways500("decision")))
	t.Cleanup(srv.Close)
	call := aipapi.Call{Decision: &aipapi.DecisionRequest{
		Evidence:  []byte(`"tck-evidence"`),
		Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindNoul}},
	}, Invocation: aipapi.Invocation{
		Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming,
	}}
	events, openErr := driveBackendTerminal(t, standardplugins.CustomSystemOneCompatibleID, srv, probe, call)
	if probe.RequestCount() != 1 {
		t.Fatalf("custom-systemone-compatible upstream_error: upstream calls=%d, want 1", probe.RequestCount())
	}
	assertBackendUpstreamFailureTerminal(t, "custom-systemone-compatible", events, openErr)
	if hasEventKind(events, aipapi.EventDecisionResult) {
		t.Fatalf("custom-systemone-compatible upstream_error: decision result alongside error; events=%v", eventKinds(events))
	}
}

// backendSystemOneUsageCell covers usage_reported for the decision-only
// backend against the stub shape the standard composition TCK uses: the
// stub reports input 1 / output 2, so the canonical stream must carry
// exactly that usage before finish.
func backendSystemOneUsageCell(t *testing.T) {
	t.Helper()
	probe := &standardProbe{}
	mux := http.NewServeMux()
	mux.Handle("/systemone", countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","answers":{"q":{"type":"noul","noul":0.7}},"usage":{"input_tokens":1,"output_tokens":2}}`)
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	call := aipapi.Call{Decision: &aipapi.DecisionRequest{
		Evidence:  []byte(`"tck-evidence"`),
		Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindNoul}},
	}, Invocation: aipapi.Invocation{
		Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming,
	}}
	events, openErr := driveBackendTerminal(t, standardplugins.CustomSystemOneCompatibleID, srv, probe, call)
	if openErr != nil {
		t.Fatalf("custom-systemone-compatible usage_reported: open: %v", openErr)
	}
	usageIdx, finishIdx := -1, -1
	for i, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta && ev.InputTokens == 1 && ev.OutputTokens == 2 {
			usageIdx = i
		}
		if ev.Kind == aipapi.EventResponseFinished {
			finishIdx = i
		}
	}
	if usageIdx < 0 {
		t.Fatalf("custom-systemone-compatible usage_reported: no 1/2 usage delta; events=%v", eventKinds(events))
	}
	if finishIdx < 0 || usageIdx > finishIdx {
		t.Fatalf("custom-systemone-compatible usage_reported: usage must precede finish (usage=%d finish=%d)", usageIdx, finishIdx)
	}
}

// backendTerminalHostedServer serves the five hosted protocol-family
// emulators on the same routes the standard composition TCK uses.
func backendTerminalHostedServer(probe *standardProbe) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{AllowMissingBearer: true})))
	mux.Handle("/v1/chat/completions", countingHandler(probe, openaichat.NewHandler(openaichat.Config{AllowMissingBearer: true})))
	mux.Handle("/v1/messages", countingHandler(probe, anthropicmessages.NewHandler(anthropicmessages.Config{AllowMissingAPIKey: true})))
	mux.Handle("/v1beta/models/m:generateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{AllowMissingAPIKey: true})))
	mux.Handle("/v1beta/models/m:streamGenerateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{AllowMissingAPIKey: true})))
	mux.Handle("/model/m/converse", countingHandler(probe, bedrock.NewHandler(bedrock.Config{AllowMissingAuthorization: true})))
	mux.Handle("/model/m/converse-stream", countingHandler(probe, bedrock.NewHandler(bedrock.Config{AllowMissingAuthorization: true})))
	return httptest.NewServer(mux)
}

// backendTerminalAlways500 is a minimal error stub for families whose
// emulator has no cheap scripted-error trigger. It follows the same
// inline-stub precedent as openResponsesCompatHandler in this package: any
// 500 surfaces as a terminal backend error without a successful finish.
func backendTerminalAlways500(kind string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		if kind == "decision" {
			_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
	})
}

func driveBackendTerminal(t *testing.T, familyID string, srv *httptest.Server, probe *standardProbe, call aipapi.Call) ([]aipapi.Event, error) {
	t.Helper()
	keys := standardplugins.UpstreamAPIKeys{OpenAI: []string{"test-openai"}, Anthropic: []string{"test-anthropic"}, Gemini: []string{"test-gemini"}, AlibabaTokenPlan: []string{"test-alibaba"}}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	be, model := buildStandardFamily(t, reg, familyID, srv.URL, srv.Client())
	view := ExecBackendView{Backend: be, Candidate: routing.AttemptCandidate{Primary: routing.Primary{Backend: familyID, Model: model}}, Invocation: call.Invocation}
	stream, openErr := view.Open(context.Background(), &call)
	if openErr != nil {
		return nil, openErr
	}
	if stream == nil {
		t.Fatalf("%s: real Open returned nil stream", familyID)
	}
	defer func() { _ = stream.Close() }()
	var events []aipapi.Event
	for {
		ev, recvErr := stream.Recv(context.Background())
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return events, recvErr
		}
		events = append(events, ev)
	}
	return events, nil
}

func hasEventKind(events []aipapi.Event, kind aipapi.EventKind) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func eventKinds(events []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

// backendCleanCell returns the covering test for one family's clean_text
// concern: the emulator's default OK response must terminate with an empty
// FinishReason and no incomplete status, with exactly one upstream call.
func backendCleanCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		events := driveBackendTerminalCall(t, familyID, false)
		assertBackendCleanTerminal(t, familyID, "clean_text", events)
	}
}

// backendToolCell returns the covering test for one family's clean_tool_call
// concern: a tools-bearing call must surface the tool lifecycle and
// terminate clean, with exactly one upstream call.
func backendToolCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		events := driveBackendTerminalCall(t, familyID, true)
		if !hasEventKind(events, aipapi.EventToolCallStarted) || !hasEventKind(events, aipapi.EventToolCallFinished) {
			t.Fatalf("%s clean_tool_call: missing tool lifecycle events; events=%v", familyID, eventKinds(events))
		}
		assertBackendCleanTerminal(t, familyID, "clean_tool_call", events)
	}
}

// backendLimitCell returns the covering test for one family's output_limit
// concern: a limit-shaped upstream payload (injected through the emulator's
// existing Config override hooks, so no emulator change is needed) must
// terminate with ResponseStatus "incomplete" plus the family's vendor
// reason, with exactly one upstream call.
func backendLimitCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		probe := &standardProbe{}
		srv := backendTerminalLimitServer(t, probe, familyID)
		t.Cleanup(srv.Close)
		call := aipapi.Call{
			ID:       "terminal-output-limit",
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Invocation: aipapi.Invocation{
				Operation:     operationForFamily(familyID),
				TransportMode: aipapi.TransportModeNonStreaming,
			},
		}
		events, openErr := driveBackendTerminal(t, familyID, srv, probe, call)
		if openErr != nil {
			t.Fatalf("%s output_limit: open: %v", familyID, openErr)
		}
		if probe.RequestCount() != 1 {
			t.Fatalf("%s output_limit: upstream calls=%d, want 1", familyID, probe.RequestCount())
		}
		fin := lastResponseFinished(events)
		if fin == nil {
			t.Fatalf("%s output_limit: no terminal ResponseFinished; events=%v", familyID, eventKinds(events))
		}
		if fin.ResponseStatus != "incomplete" {
			t.Fatalf("%s output_limit: ResponseStatus=%q, want incomplete; events=%v", familyID, fin.ResponseStatus, eventKinds(events))
		}
		if want := backendLimitReason(familyID); fin.FinishReason != want {
			t.Fatalf("%s output_limit: FinishReason=%q, want %q", familyID, fin.FinishReason, want)
		}
	}
}

// backendLimitReason is the vendor output-limit lexeme each family reports.
func backendLimitReason(familyID string) string {
	switch familyID {
	case "openai-legacy":
		return "length"
	case "anthropic":
		return "max_tokens"
	case "openai-responses":
		return "length"
	case "gemini":
		return "MAX_TOKENS"
	case "bedrock":
		return "max_tokens"
	case "custom-openresponses-compatible":
		return "length"
	default:
		return "length"
	}
}

// backendTerminalLimitServer serves one family's emulator with a
// limit-shaped upstream payload injected through its existing Config
// override hook. The payloads mirror the emulator defaults with only the
// terminal lexeme changed to the output-limit vocabulary.
func backendTerminalLimitServer(t *testing.T, probe *standardProbe, familyID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	switch familyID {
	case "openai-responses":
		mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{
			AllowMissingBearer: true,
			NonStreamJSON:      `{"id":"resp_lim","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_lim","status":"completed","role":"assistant","content":[{"type":"output_text","text":"cut"}]}],"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
			StreamSSE:          "event: response.incomplete\n" + `data: {"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_lim","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_lim","status":"completed","role":"assistant","content":[{"type":"output_text","text":"cut"}]}],"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n" + "data: [DONE]\n\n",
		})))
	case "openai-legacy":
		mux.Handle("/v1/chat/completions", countingHandler(probe, openaichat.NewHandler(openaichat.Config{
			AllowMissingBearer: true,
			NonStreamJSON:      `{"id":"chatcmpl_lim","object":"chat.completion","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"cut"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			StreamSSE:          "data: {\"id\":\"chatcmpl_lim\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"cut\"},\"finish_reason\":null}]}\n\n" + "data: {\"id\":\"chatcmpl_lim\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" + "data: [DONE]\n\n",
		})))
	case "anthropic":
		mux.Handle("/v1/messages", countingHandler(probe, anthropicmessages.NewHandler(anthropicmessages.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"id":"msg_lim","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[{"type":"text","text":"cut"}],"stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":5}}`,
			StreamSSE:          "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_lim","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" + "event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":5}}` + "\n\n" + "event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
		})))
	case "gemini":
		mux.Handle("/v1beta/models/m:generateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"candidates":[{"content":{"role":"model","parts":[{"text":"cut"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
			StreamSSE:          "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"cut\"}]},\"finishReason\":\"MAX_TOKENS\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n",
		})))
		mux.Handle("/v1beta/models/m:streamGenerateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"candidates":[{"content":{"role":"model","parts":[{"text":"cut"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
			StreamSSE:          "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"cut\"}]},\"finishReason\":\"MAX_TOKENS\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n",
		})))
	case "bedrock":
		bedrockCfg := bedrock.Config{
			AllowMissingAuthorization: true,
			ConverseJSON:              `{"metrics":{"latencyMs":1},"output":{"message":{"role":"assistant","content":[{"text":"cut"}]}},"stopReason":"max_tokens","usage":{"inputTokens":10,"outputTokens":5,"totalTokens":15}}`,
			StreamEvents:              bedrock.LimitStreamEvents(),
		}
		mux.Handle("/model/m/converse", countingHandler(probe, bedrock.NewHandler(bedrockCfg)))
		mux.Handle("/model/m/converse-stream", countingHandler(probe, bedrock.NewHandler(bedrockCfg)))
	case "custom-openresponses-compatible":
		mux.Handle("/responses", countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"incomplete","model":"m","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"cut"}]}],"incomplete_details":{"reason":"length"},"usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1}}`)
		})))
		mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
	default:
		t.Fatalf("no limit server for family %q", familyID)
	}
	return httptest.NewServer(mux)
}

// backendTerminalDefaultServer serves the default emulator payloads for one
// family: the shared hosted routes, or the OpenResponses-compatible stub the
// standard composition TCK uses.
func backendTerminalDefaultServer(probe *standardProbe, familyID string) *httptest.Server {
	if familyID == standardplugins.CustomOpenResponsesCompatibleID {
		mux := http.NewServeMux()
		mux.Handle("/responses", countingHandler(probe, openResponsesCompatHandler()))
		mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
		return httptest.NewServer(mux)
	}
	return backendTerminalHostedServer(probe)
}

// driveBackendTerminalCall drives one family's real backend with a text call
// (or a tools-bearing call when withTools) against the default emulator
// server for that family and returns the collected canonical events.
func driveBackendTerminalCall(t *testing.T, familyID string, withTools bool) []aipapi.Event {
	t.Helper()
	probe := &standardProbe{}
	srv := backendTerminalDefaultServer(probe, familyID)
	t.Cleanup(srv.Close)
	call := aipapi.Call{
		ID:       "terminal-clean",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			Operation:     operationForFamily(familyID),
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	if withTools {
		call.Tools = []aipapi.ToolDef{{Name: "weather", Parameters: []byte(`{"type":"object"}`)}}
	}
	events, openErr := driveBackendTerminal(t, familyID, srv, probe, call)
	if openErr != nil {
		t.Fatalf("%s: open: %v", familyID, openErr)
	}
	if probe.RequestCount() != 1 {
		t.Fatalf("%s: upstream calls=%d, want 1", familyID, probe.RequestCount())
	}
	return events
}

// assertBackendCleanTerminal pins the Handoff 1 clean-terminal contract: the
// stream ends with ResponseFinished carrying an empty FinishReason and no
// incomplete status.
func assertBackendCleanTerminal(t *testing.T, familyID, concern string, events []aipapi.Event) {
	t.Helper()
	fin := lastResponseFinished(events)
	if fin == nil {
		t.Fatalf("%s %s: no terminal ResponseFinished; events=%v", familyID, concern, eventKinds(events))
	}
	if fin.FinishReason != "" {
		t.Fatalf("%s %s: clean FinishReason=%q, want empty", familyID, concern, fin.FinishReason)
	}
	if fin.ResponseStatus == "incomplete" {
		t.Fatalf("%s %s: clean ResponseStatus=%q, want not incomplete", familyID, concern, fin.ResponseStatus)
	}
}

func lastResponseFinished(events []aipapi.Event) *aipapi.Event {
	for _, ev := range slices.Backward(events) {
		if ev.Kind == aipapi.EventResponseFinished {
			return &ev
		}
	}
	return nil
}

// backendRefusalCell returns the covering test for one family's content_filter
// (refusal) concern: a refusal-shaped upstream payload (injected through the
// emulator's existing Config override hooks, so no emulator change is needed)
// must terminate with the family's refusal lexeme preserved verbatim on the
// canonical terminal — never rewritten to the output-limit (length) taxonomy
// and never dropped to a clean stop — with exactly one upstream call.
func backendRefusalCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		probe := &standardProbe{}
		srv := backendTerminalRefusalServer(t, probe, familyID)
		t.Cleanup(srv.Close)
		call := aipapi.Call{
			ID:       "terminal-content-filter",
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Invocation: aipapi.Invocation{
				Operation:     operationForFamily(familyID),
				TransportMode: aipapi.TransportModeNonStreaming,
			},
		}
		events, openErr := driveBackendTerminal(t, familyID, srv, probe, call)
		if openErr != nil {
			t.Fatalf("%s content_filter: open: %v", familyID, openErr)
		}
		if probe.RequestCount() != 1 {
			t.Fatalf("%s content_filter: upstream calls=%d, want 1", familyID, probe.RequestCount())
		}
		fin := lastResponseFinished(events)
		if fin == nil {
			t.Fatalf("%s content_filter: no terminal ResponseFinished; events=%v", familyID, eventKinds(events))
		}
		wantStatus, wantReason := backendRefusalTerminal(familyID)
		if fin.ResponseStatus != wantStatus {
			t.Fatalf("%s content_filter: ResponseStatus=%q, want %q; events=%v", familyID, fin.ResponseStatus, wantStatus, eventKinds(events))
		}
		if fin.FinishReason != wantReason {
			t.Fatalf("%s content_filter: FinishReason=%q, want %q (verbatim refusal lexeme, never length taxonomy)", familyID, fin.FinishReason, wantReason)
		}
	}
}

// backendRefusalTerminal is the canonical refusal terminal each family must
// report: the family's native refusal lexeme preserved verbatim plus the
// ResponseStatus the upstream envelope implies. Non-envelope families (chat,
// messages, generateContent, converse) carry no upstream terminal status, so
// the terminal carries the lexeme with no incomplete classification: a
// refusal is a complete upstream decision, and the incomplete status is
// reserved for the output-limit taxonomy. Envelope families (responses,
// openresponses) report the upstream incomplete resource honestly while
// keeping the refusal reason verbatim, per the existing
// incompleteFinishReason contract (never rewrite to length).
func backendRefusalTerminal(familyID string) (status, reason string) {
	switch familyID {
	case "openai-legacy":
		return "", "content_filter"
	case "anthropic":
		return "", "refusal"
	case "gemini":
		return "", "SAFETY"
	case "bedrock":
		return "", "guardrail_intervened"
	case "openai-responses":
		return "incomplete", "content_filter"
	case "custom-openresponses-compatible":
		return "incomplete", "content_filter"
	default:
		return "", "content_filter"
	}
}

// backendTerminalRefusalServer serves one family's emulator with a
// refusal-shaped upstream payload injected through its existing Config
// override hook. The payloads mirror the emulator defaults with only the
// terminal lexeme changed to the family's refusal vocabulary. Stream
// overrides mirror backendTerminalLimitServer; the refusal cells drive the
// non-streaming transport.
func backendTerminalRefusalServer(t *testing.T, probe *standardProbe, familyID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	switch familyID {
	case "openai-responses":
		mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{
			AllowMissingBearer: true,
			NonStreamJSON:      `{"id":"resp_ref","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_ref","status":"completed","role":"assistant","content":[{"type":"output_text","text":"I cannot help with that"}]}],"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
			StreamSSE:          "event: response.incomplete\n" + `data: {"type":"response.incomplete","sequence_number":1,"response":{"id":"resp_ref","object":"response","created_at":1715620000,"status":"incomplete","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_ref","status":"completed","role":"assistant","content":[{"type":"output_text","text":"I cannot help with that"}]}],"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n" + "data: [DONE]\n\n",
		})))
	case "openai-legacy":
		mux.Handle("/v1/chat/completions", countingHandler(probe, openaichat.NewHandler(openaichat.Config{
			AllowMissingBearer: true,
			NonStreamJSON:      `{"id":"chatcmpl_ref","object":"chat.completion","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"I cannot help with that"},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			StreamSSE:          "data: {\"id\":\"chatcmpl_ref\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"I cannot help with that\"},\"finish_reason\":null}]}\n\n" + "data: {\"id\":\"chatcmpl_ref\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n" + "data: [DONE]\n\n",
		})))
	case "anthropic":
		mux.Handle("/v1/messages", countingHandler(probe, anthropicmessages.NewHandler(anthropicmessages.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"id":"msg_ref","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[{"type":"text","text":"I cannot help with that"}],"stop_reason":"refusal","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":5}}`,
			StreamSSE:          "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_ref","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" + "event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":5}}` + "\n\n" + "event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
		})))
	case "gemini":
		mux.Handle("/v1beta/models/m:generateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"candidates":[{"content":{"role":"model","parts":[{"text":"I cannot help with that"}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
			StreamSSE:          "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"I cannot help with that\"}]},\"finishReason\":\"SAFETY\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n",
		})))
		mux.Handle("/v1beta/models/m:streamGenerateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"candidates":[{"content":{"role":"model","parts":[{"text":"I cannot help with that"}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
			StreamSSE:          "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"I cannot help with that\"}]},\"finishReason\":\"SAFETY\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n",
		})))
	case "bedrock":
		bedrockCfg := bedrock.Config{
			AllowMissingAuthorization: true,
			ConverseJSON:              `{"metrics":{"latencyMs":1},"output":{"message":{"role":"assistant","content":[{"text":"I cannot help with that"}]}},"stopReason":"guardrail_intervened","usage":{"inputTokens":10,"outputTokens":5,"totalTokens":15}}`,
			StreamEvents:              bedrock.RefusalStreamEvents(),
		}
		mux.Handle("/model/m/converse", countingHandler(probe, bedrock.NewHandler(bedrockCfg)))
		mux.Handle("/model/m/converse-stream", countingHandler(probe, bedrock.NewHandler(bedrockCfg)))
	case "custom-openresponses-compatible":
		mux.Handle("/responses", countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"incomplete","model":"m","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"I cannot help with that"}]}],"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1}}`)
		})))
		mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
	default:
		t.Fatalf("no refusal server for family %q", familyID)
	}
	return httptest.NewServer(mux)
}

// backendEmptyCell returns the covering test for one family's empty_turn
// (degenerate) concern: an upstream payload with no visible content but a
// clean terminal must collect to EOF with exactly one upstream call per
// transport, terminate with ResponseFinished carrying an empty FinishReason
// and no incomplete status, and carry zero visible-output deltas. Usage deltas
// are orthogonal (see usage_reported) and are not asserted here.
func backendEmptyCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		probe := &standardProbe{}
		srv := backendTerminalEmptyServer(t, probe, familyID)
		t.Cleanup(srv.Close)
		modes := []aipapi.TransportMode{aipapi.TransportModeNonStreaming, aipapi.TransportModeStreaming}
		if familyID == standardplugins.CustomOpenResponsesCompatibleID {
			// The compatible backend's certified transports are HTTP-only: the
			// standard composition TCK deletes its streaming cap, so the
			// empty_turn cell drives non-streaming only.
			modes = []aipapi.TransportMode{aipapi.TransportModeNonStreaming}
		}
		for _, mode := range modes {
			probe.Reset()
			call := aipapi.Call{
				ID:       "terminal-empty-turn",
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
				Invocation: aipapi.Invocation{
					Operation:     operationForFamily(familyID),
					TransportMode: mode,
				},
			}
			events, openErr := driveBackendTerminal(t, familyID, srv, probe, call)
			if openErr != nil {
				t.Fatalf("%s empty_turn %s: open: %v", familyID, mode, openErr)
			}
			if probe.RequestCount() != 1 {
				t.Fatalf("%s empty_turn %s: upstream calls=%d, want 1", familyID, mode, probe.RequestCount())
			}
			fin := lastResponseFinished(events)
			if fin == nil {
				t.Fatalf("%s empty_turn %s: stream ended without terminal ResponseFinished; events=%v", familyID, mode, eventKinds(events))
			}
			if fin.FinishReason != "" {
				t.Fatalf("%s empty_turn %s: FinishReason=%q, want empty", familyID, mode, fin.FinishReason)
			}
			if fin.ResponseStatus == "incomplete" {
				t.Fatalf("%s empty_turn %s: ResponseStatus=%q, want not incomplete", familyID, mode, fin.ResponseStatus)
			}
			assertBackendEmptyVisible(t, familyID, mode, events)
		}
	}
}

// assertBackendEmptyVisible pins the degenerate contract: no visible-output
// events (text, tool lifecycle, reasoning, or assistant media refs) and no
// terminal error may appear on a clean empty turn.
func assertBackendEmptyVisible(t *testing.T, familyID string, mode aipapi.TransportMode, events []aipapi.Event) {
	t.Helper()
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta,
			aipapi.EventReasoningDelta, aipapi.EventReasoningSignatureDelta,
			aipapi.EventReasoningOpaqueDelta, aipapi.EventReasoningPart,
			aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished,
			aipapi.EventAssistantImageRef, aipapi.EventAssistantFileRef:
			t.Fatalf("%s empty_turn %s: visible %s event on an empty turn (delta=%q tool=%q)", familyID, mode, ev.Kind, ev.Delta, ev.ToolCallID)
		case aipapi.EventError:
			t.Fatalf("%s empty_turn %s: terminal EventError on an empty turn: %s %s", familyID, mode, ev.ErrorCode, ev.ErrorMessage)
		}
	}
}

// backendTerminalEmptyServer serves one family's emulator with an
// empty-but-clean upstream payload injected through its existing Config
// override hook, the same precedent backendTerminalLimitServer follows, so no
// emulator change is needed. Every payload carries the family's clean terminal
// lexeme with zero visible content: empty output/message/candidate lists,
// empty-string content, or a stream with no delta events.
func backendTerminalEmptyServer(t *testing.T, probe *standardProbe, familyID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	switch familyID {
	case "openai-responses":
		mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{
			AllowMissingBearer: true,
			NonStreamJSON:      `{"id":"resp_empty","object":"response","created_at":1715620000,"status":"completed","model":"m","output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}`,
			StreamSSE:          "event: response.completed\n" + `data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_empty","object":"response","created_at":1715620000,"status":"completed","model":"m","output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}` + "\n\n" + "data: [DONE]\n\n",
		})))
	case "openai-legacy":
		mux.Handle("/v1/chat/completions", countingHandler(probe, openaichat.NewHandler(openaichat.Config{
			AllowMissingBearer: true,
			NonStreamJSON:      `{"id":"chatcmpl_empty","object":"chat.completion","created":1715620000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}`,
			StreamSSE:          "data: {\"id\":\"chatcmpl_empty\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" + "data: [DONE]\n\n",
		})))
	case "anthropic":
		mux.Handle("/v1/messages", countingHandler(probe, anthropicmessages.NewHandler(anthropicmessages.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"id":"msg_empty","type":"message","role":"assistant","model":"m","content":[],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}`,
			StreamSSE: "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_empty","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
				"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}` + "\n\n" +
				"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
		})))
	case "gemini":
		empty := gemini.Config{
			AllowMissingAPIKey: true,
			NonStreamJSON:      `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"totalTokenCount":10}}`,
			StreamSSE:          "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":0,\"totalTokenCount\":10}}\n\n",
		}
		mux.Handle("/v1beta/models/m:generateContent", countingHandler(probe, gemini.NewHandler(empty)))
		mux.Handle("/v1beta/models/m:streamGenerateContent", countingHandler(probe, gemini.NewHandler(empty)))
	case "bedrock":
		bedrockCfg := bedrock.Config{
			AllowMissingAuthorization: true,
			ConverseJSON:              `{"metrics":{"latencyMs":1},"output":{"message":{"role":"assistant","content":[]}},"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":0,"totalTokens":10}}`,
			StreamEvents:              bedrockEmptyStreamEvents(),
		}
		mux.Handle("/model/m/converse", countingHandler(probe, bedrock.NewHandler(bedrockCfg)))
		mux.Handle("/model/m/converse-stream", countingHandler(probe, bedrock.NewHandler(bedrockCfg)))
	case "custom-openresponses-compatible":
		mux.Handle("/responses", countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"completed","model":"m","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`)
		})))
		mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
	default:
		t.Fatalf("no empty server for family %q", familyID)
	}
	return httptest.NewServer(mux)
}

// bedrockEmptyStreamEvents encodes a content-free converse-stream sequence
// (message open, immediate end_turn stop, usage metadata) with the same AWS
// eventstream framing the refbackend uses, so no emulator change is needed.
func bedrockEmptyStreamEvents() []byte {
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	for _, tc := range []struct {
		eventType string
		payload   string
	}{
		{"messageStart", `{"role":"assistant"}`},
		{"messageStop", `{"stopReason":"end_turn"}`},
		{"metadata", `{"usage":{"inputTokens":10,"outputTokens":0,"totalTokens":10}}`},
	} {
		msg := eventstream.Message{
			Headers: []eventstream.Header{
				{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
				{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue(tc.eventType)},
				{Name: eventstreamapi.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
			},
			Payload: []byte(tc.payload),
		}
		if err := enc.Encode(&buf, msg); err != nil {
			panic(err)
		}
	}
	return buf.Bytes()
}

// backendCancelCell returns the covering test for one protocol family's
// cancellation concern: a canceled B-leg never yields a successful terminal.
// It mirrors the upstream_error cell structure (upstream probe with an exact
// RequestCount proof, early return when Open surfaces the terminal). Two legs:
// cancel before Open (the request context is already canceled, so the attempt
// must fail with a cancellation error before any upstream call) and cancel
// after first content (a gate server emits one content prefix, then holds the
// connection open until the client cancels; the drained stream must carry no
// successful ResponseFinished and terminate with an EventError or a
// cancellation error). Streaming transport, httptest servers only, channels
// plus context cancellation for synchronization (timeouts only bound stuck
// drives, never order them).
func backendCancelCell(familyID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		backendCancelBeforeOpen(t, familyID)
		backendCancelAfterFirstContent(t, familyID)
	}
}

// backendCancelBeforeOpen covers cancel before first content: with an already
// canceled context the real backend must surface a cancellation error and
// perform no upstream call (zero-upstream proof, like the hard-negative
// boundary). The emulators serve as the upstream; the canceled attempt never
// dials them.
func backendCancelBeforeOpen(t *testing.T, familyID string) {
	t.Helper()
	probe := &standardProbe{}
	srv := backendTerminalHostedServer(probe)
	t.Cleanup(srv.Close)
	view, call := backendCancelView(t, familyID, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, openErr := view.Open(ctx, &call)
	if stream != nil {
		defer func() { _ = stream.Close() }()
	}
	if probe.RequestCount() != 0 {
		t.Fatalf("%s cancellation/before_first_content: upstream calls=%d, want 0 (canceled attempt must not dial)", familyID, probe.RequestCount())
	}
	if openErr != nil {
		if !isBackendCancelError(openErr) {
			t.Fatalf("%s cancellation/before_first_content: open error=%v, want cancellation", familyID, openErr)
		}
		if stream != nil {
			t.Fatalf("%s cancellation/before_first_content: non-nil stream alongside open error", familyID)
		}
		return
	}
	if stream == nil {
		t.Fatalf("%s cancellation/before_first_content: nil stream with nil error", familyID)
	}
	events := drainBackendCancelStream(t, familyID, "before_first_content", stream, 8)
	if hasEventKind(events, aipapi.EventResponseFinished) {
		t.Fatalf("%s cancellation/before_first_content: successful ResponseFinished on canceled attempt; events=%v", familyID, eventKinds(events))
	}
}

// backendCancelAfterFirstContent covers cancel after first content: the gate
// server emits one wire-faithful content prefix, then holds the connection
// open until the client context is done and closes without any terminal
// bytes, so no successful ResponseFinished can reach the canonical stream.
// Exactly one upstream call proves the truncation came from upstream.
func backendCancelAfterFirstContent(t *testing.T, familyID string) {
	t.Helper()
	probe := &standardProbe{}
	arrived, sent := make(chan struct{}), make(chan struct{})
	prefix, contentType := backendCancelPrefix(t, familyID)
	srv := httptest.NewServer(countingHandler(probe, backendCancelGate(prefix, contentType, arrived, sent)))
	t.Cleanup(srv.Close)
	view, call := backendCancelView(t, familyID, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, openErr := view.Open(ctx, &call)
	if openErr != nil {
		t.Fatalf("%s cancellation/after_first_content: open: %v", familyID, openErr)
	}
	if stream == nil {
		t.Fatalf("%s cancellation/after_first_content: nil stream with nil error", familyID)
	}
	defer func() { _ = stream.Close() }()
	var events []aipapi.Event
	if err := waitBackendSignal(sent, "cancellation gate never flushed content prefix"); err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		ev, recvErr := recvBackendCancel(stream)
		if recvErr != nil {
			t.Fatalf("%s cancellation/after_first_content: no content before cancel: %v (events=%v)", familyID, recvErr, eventKinds(events))
		}
		events = append(events, ev)
		if ev.Kind == aipapi.EventTextDelta {
			break
		}
		if i == 7 {
			t.Fatalf("%s cancellation/after_first_content: no TextDelta in first 8 events; events=%v", familyID, eventKinds(events))
		}
	}
	cancel()
	drainErr := error(nil)
	for range 16 {
		ev, recvErr := recvBackendCancel(stream)
		if recvErr != nil {
			drainErr = recvErr
			break
		}
		events = append(events, ev)
	}
	if probe.RequestCount() != 1 {
		t.Fatalf("%s cancellation/after_first_content: upstream calls=%d, want 1", familyID, probe.RequestCount())
	}
	if hasEventKind(events, aipapi.EventResponseFinished) {
		t.Fatalf("%s cancellation/after_first_content: successful ResponseFinished on truncated stream; events=%v", familyID, eventKinds(events))
	}
	if drainErr == nil {
		t.Fatalf("%s cancellation/after_first_content: truncated stream drained without terminal error; events=%v", familyID, eventKinds(events))
	}
	if !isBackendCancelError(drainErr) && !hasEventKind(events, aipapi.EventError) && !isBackendCancelTeardown(drainErr) {
		t.Fatalf("%s cancellation/after_first_content: terminal=%v, want EventError or cancellation error; events=%v", familyID, drainErr, eventKinds(events))
	}
}

// backendCancelView builds the real backend view plus its call for the
// cancellation legs: streaming text calls for the protocol families,
// non-streaming calls for the decision-only and compatible stubs.
func backendCancelView(t *testing.T, familyID string, srv *httptest.Server) (ExecBackendView, aipapi.Call) {
	t.Helper()
	keys := standardplugins.UpstreamAPIKeys{OpenAI: []string{"test-openai"}, Anthropic: []string{"test-anthropic"}, Gemini: []string{"test-gemini"}, AlibabaTokenPlan: []string{"test-alibaba"}}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TCK_KEY", "test-key")
	be, model := buildStandardFamily(t, reg, familyID, srv.URL, srv.Client())
	var call aipapi.Call
	switch familyID {
	case standardplugins.CustomSystemOneCompatibleID:
		call = aipapi.Call{Decision: &aipapi.DecisionRequest{
			Evidence:  []byte(`"tck-evidence"`),
			Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindNoul}},
		}, Invocation: aipapi.Invocation{
			Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming,
		}}
	case standardplugins.CustomOpenResponsesCompatibleID:
		call = aipapi.Call{
			ID:         "terminal-cancellation",
			Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Invocation: aipapi.Invocation{TransportMode: aipapi.TransportModeNonStreaming},
		}
	default:
		call = aipapi.Call{
			ID:       "terminal-cancellation",
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Invocation: aipapi.Invocation{
				Operation:     operationForFamily(familyID),
				TransportMode: aipapi.TransportModeStreaming,
			},
		}
	}
	view := ExecBackendView{Backend: be, Candidate: routing.AttemptCandidate{Primary: routing.Primary{Backend: familyID, Model: model}}, Invocation: call.Invocation}
	return view, call
}

// backendCancelPrefix returns one wire-faithful content prefix per protocol
// family for the after-first-content leg: a single content event with no
// terminal bytes, so the gate can hold the stream open and any successful
// finish observed downstream would prove a backend synthesis bug. SSE shapes
// follow the refbackend emulator defaults; the bedrock prefix cuts the first
// two eventstream messages (messageStart, contentBlockDelta) out of the
// emulator's own LimitStreamEvents bytes using the framing length prefix.
func backendCancelPrefix(t *testing.T, familyID string) ([]byte, string) {
	t.Helper()
	const sseType = "text/event-stream; charset=utf-8"
	switch familyID {
	case "openai-legacy":
		return []byte("data: {\"id\":\"chatcmpl_cancel\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"), sseType
	case "openai-responses":
		return []byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_cancel\",\"status\":\"in_progress\",\"model\":\"gpt-4o-mini\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_cancel\",\"content_index\":0,\"delta\":\"hi\"}\n\n"), sseType
	case "anthropic":
		return []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cancel\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-haiku-20241022\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"), sseType
	case "gemini":
		return []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"), sseType
	case "bedrock":
		full := bedrock.LimitStreamEvents()
		if len(full) < 8 {
			t.Fatalf("bedrock cancel fixture too short (%d bytes)", len(full))
		}
		first := int(binary.BigEndian.Uint32(full[0:4]))
		if first <= 0 || first >= len(full) || len(full)-first < 4 {
			t.Fatalf("bedrock cancel fixture has bad first message length %d (total %d)", first, len(full))
		}
		second := int(binary.BigEndian.Uint32(full[first : first+4]))
		if second <= 0 || first+second > len(full) {
			t.Fatalf("bedrock cancel fixture has bad second message length %d", second)
		}
		return full[:first+second], "application/vnd.amazon.eventstream"
	default:
		t.Fatalf("no cancellation prefix for family %q", familyID)
		return nil, ""
	}
}

// backendCancelGate serves one cancellation-leg response: headers plus the
// content prefix, flushed, then holds the connection open until the client
// context is done and returns without any terminal bytes. arrived fires when
// the upstream request is received; sent fires after the prefix is flushed.
func backendCancelGate(prefix []byte, contentType string, arrived, sent chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		if len(prefix) > 0 {
			_, _ = w.Write(prefix)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			close(sent)
		}
		<-r.Context().Done()
	})
}

// backendOpenResponsesCompatCancelCell covers cancellation for the generic
// OpenResponses-compatible backend (non-streaming only): a pre-canceled Open
// must fail without an upstream call, and an Open blocked in a gate that
// never sends content must surface cancellation after exactly one upstream
// call, with no successful ResponseFinished.
func backendOpenResponsesCompatCancelCell(t *testing.T) {
	t.Helper()
	backendCancelNonStreamBeforeOpen(t, standardplugins.CustomOpenResponsesCompatibleID, backendOpenResponsesCompatServer)
	backendCancelNonStreamBlockedOpen(t, standardplugins.CustomOpenResponsesCompatibleID)
}

// backendSystemOneCancelCell covers cancellation for the decision-only
// backend: a pre-canceled Open must fail without an upstream call, and an
// Open blocked in a gate that never sends content must surface cancellation
// after exactly one upstream call, with no decision result.
func backendSystemOneCancelCell(t *testing.T) {
	t.Helper()
	backendCancelNonStreamBeforeOpen(t, standardplugins.CustomSystemOneCompatibleID, backendSystemOneServer)
	backendCancelNonStreamBlockedOpen(t, standardplugins.CustomSystemOneCompatibleID)
}

// backendCancelNonStreamBeforeOpen asserts a pre-canceled non-streaming Open
// fails with a cancellation error and performs no upstream call.
func backendCancelNonStreamBeforeOpen(t *testing.T, familyID string, serve func(probe *standardProbe) *httptest.Server) {
	t.Helper()
	probe := &standardProbe{}
	srv := serve(probe)
	t.Cleanup(srv.Close)
	view, call := backendCancelView(t, familyID, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, openErr := view.Open(ctx, &call)
	if stream != nil {
		defer func() { _ = stream.Close() }()
	}
	if probe.RequestCount() != 0 {
		t.Fatalf("%s cancellation/before_first_content: upstream calls=%d, want 0 (canceled attempt must not dial)", familyID, probe.RequestCount())
	}
	if openErr == nil {
		t.Fatalf("%s cancellation/before_first_content: pre-canceled Open succeeded", familyID)
	}
	if !isBackendCancelError(openErr) {
		t.Fatalf("%s cancellation/before_first_content: open error=%v, want cancellation", familyID, openErr)
	}
}

// backendCancelNonStreamBlockedOpen asserts an Open held server-side without
// any content surfaces cancellation after exactly one upstream call, with no
// successful terminal events.
func backendCancelNonStreamBlockedOpen(t *testing.T, familyID string) {
	t.Helper()
	probe := &standardProbe{}
	arrived := make(chan struct{})
	mux := serveMuxWithGate(t, familyID, probe, arrived)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	view, call := backendCancelView(t, familyID, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan struct{})
	var stream []aipapi.Event
	var openErr error
	var raw aipapi.EventStream
	go func() {
		defer close(opened)
		raw, openErr = view.Open(ctx, &call)
	}()
	if err := waitBackendSignal(arrived, "cancellation gate never received upstream request"); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := waitBackendSignal(opened, "blocked Open did not return after cancel"); err != nil {
		t.Fatal(err)
	}
	if probe.RequestCount() != 1 {
		t.Fatalf("%s cancellation/after_first_content: upstream calls=%d, want 1", familyID, probe.RequestCount())
	}
	if openErr == nil {
		if raw == nil {
			t.Fatalf("%s cancellation/after_first_content: nil stream with nil error", familyID)
		}
		defer func() { _ = raw.Close() }()
		stream = drainBackendCancelStream(t, familyID, "after_first_content", raw, 8)
		if hasEventKind(stream, aipapi.EventResponseFinished) {
			t.Fatalf("%s cancellation/after_first_content: successful ResponseFinished on canceled attempt; events=%v", familyID, eventKinds(stream))
		}
		if hasEventKind(stream, aipapi.EventDecisionResult) {
			t.Fatalf("%s cancellation/after_first_content: decision result on canceled attempt; events=%v", familyID, eventKinds(stream))
		}
		return
	}
	if !isBackendCancelError(openErr) {
		t.Fatalf("%s cancellation/after_first_content: open error=%v, want cancellation", familyID, openErr)
	}
}

// serveMuxWithGate serves the stub routes for a non-streaming family with
// every route replaced by a gate that holds the connection open without
// content until the client context is done.
func serveMuxWithGate(t *testing.T, familyID string, probe *standardProbe, arrived chan struct{}) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	gate := countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	}))
	switch familyID {
	case standardplugins.CustomOpenResponsesCompatibleID:
		mux.Handle("/responses", gate)
		mux.Handle("/responses/compact", gate)
	case standardplugins.CustomSystemOneCompatibleID:
		mux.Handle("/systemone", gate)
	default:
		t.Fatalf("no cancellation gate mux for family %q", familyID)
	}
	return mux
}

// backendOpenResponsesCompatServer serves the shared stub for the generic
// OpenResponses-compatible backend.
func backendOpenResponsesCompatServer(probe *standardProbe) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/responses", countingHandler(probe, openResponsesCompatHandler()))
	mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
	return httptest.NewServer(mux)
}

// backendSystemOneServer serves the decision stub for the SystemOne backend.
func backendSystemOneServer(probe *standardProbe) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/systemone", countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","answers":{"q":{"type":"noul","noul":0.7}},"usage":{"input_tokens":1,"output_tokens":2}}`)
	})))
	return httptest.NewServer(mux)
}

// isBackendCancelError reports whether err is a context cancellation terminal
// (preemption by the caller) rather than an upstream or mapping failure.
func isBackendCancelError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// isBackendCancelTeardown tolerates the physical transport teardown a
// self-initiated cancel can surface before an adapter maps it to the
// canonical cancellation error: a closed connection under an in-flight read
// is a direct consequence of the cancel, not post-cancel content, and this
// leg already proves no content follows. Without the tolerance the
// cancel-after-first-content cell flakes under CI load when the transport
// close wins the race against the adapter's error mapping.
func isBackendCancelTeardown(err error) bool {
	return errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

// recvTimeoutContext bounds one Recv: the timeout only guards a stuck drive,
// channel/cancel ordering comes from the test choreography.
func recvBackendCancel(stream aipapi.EventStream) (aipapi.Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return stream.Recv(ctx)
}

// waitBackendSignal waits for a choreography channel with a bound, mirroring
// the frontend TCK waitSignal pattern: the timeout reports a stuck drive, it
// never orders events.
func waitBackendSignal(ch <-chan struct{}, message string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("backend TCK: " + message)
	}
}

// drainBackendCancelStream collects a canceled stream to its terminal error.
// A clean io.EOF without events is reported as an error: a truncated stream
// carries no successful terminal by construction, so the drain must end in
// a terminal error or in events without ResponseFinished (asserted by the
// caller).
func drainBackendCancelStream(t *testing.T, familyID, leg string, stream aipapi.EventStream, max int) []aipapi.Event {
	t.Helper()
	var events []aipapi.Event
	for range max {
		ev, recvErr := recvBackendCancel(stream)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) && len(events) == 0 {
				t.Fatalf("%s cancellation/%s: clean EOF with no events on canceled attempt", familyID, leg)
			}
			return events
		}
		events = append(events, ev)
	}
	t.Fatalf("%s cancellation/%s: stream did not terminate within %d Recvs", familyID, leg, max)
	return nil
}
