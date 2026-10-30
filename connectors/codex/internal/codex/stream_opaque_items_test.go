package codex

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/streampump"
)

func TestCodexEventMapper_OutputItemDonePreservesOpaqueItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		item          string
		wantReasoning bool
	}{
		{
			name:          "encrypted reasoning",
			item:          `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"encrypted-state","status":"completed"}`,
			wantReasoning: true,
		},
		{
			name: "compaction",
			item: `{"type":"compaction","id":"cmp_1","encrypted_content":"compact-state"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newCodexEventMapper(0)
			if err := m.handleData(`{"type":"response.output_item.done","item":` + tt.item + `}`); err != nil {
				t.Fatal(err)
			}
			if len(m.outputItems) != 1 {
				t.Fatalf("retained output items = %d, want 1", len(m.outputItems))
			}
			assertJSONEqual(t, m.outputItems[0], tt.item)

			events := streampump.DrainPending(&m.pending)
			var reasoning *aipapi.Event
			for i := range events {
				if events[i].Kind == aipapi.EventReasoningPart {
					reasoning = &events[i]
				}
			}
			if !tt.wantReasoning {
				if reasoning != nil {
					t.Fatalf("unexpected reasoning event: %+v", *reasoning)
				}
				return
			}
			if reasoning == nil || reasoning.Reasoning == nil {
				t.Fatalf("events = %+v, want reasoning part", events)
			}
			if reasoning.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
				t.Fatalf("dialect = %q", reasoning.Reasoning.Dialect)
			}
			assertJSONEqual(t, reasoning.Reasoning.Opaque, tt.item)
		})
	}
}

func TestCodexEventMapper_ReasoningDeltaDoesNotDuplicateExactItem(t *testing.T) {
	t.Parallel()

	m := newCodexEventMapper(0)
	if err := m.handleData(`{"type":"response.reasoning_summary_text.delta","delta":"summary"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.handleData(`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"summary"}],"encrypted_content":"encrypted-state"}}`); err != nil {
		t.Fatal(err)
	}
	reasoningParts := 0
	for _, event := range streampump.DrainPending(&m.pending) {
		if event.Kind == aipapi.EventReasoningDelta {
			t.Fatal("progressive delta must not create a lossy duplicate reasoning artifact")
		}
		if event.Kind == aipapi.EventReasoningPart {
			reasoningParts++
		}
	}
	if reasoningParts != 1 {
		t.Fatalf("reasoning parts = %d, want 1", reasoningParts)
	}
}

func TestCodexEventMapper_ResponseCompletedPreservesOpaqueFallback(t *testing.T) {
	t.Parallel()

	m := newCodexEventMapper(0)
	data := `{"type":"response.completed","response":{"id":"resp_1","output":[` +
		`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":null,"status":"completed"},` +
		`{"type":"compaction","id":"cmp_1","encrypted_content":"compact-state"}` +
		`]}}`
	if err := m.handleData(data); err != nil {
		t.Fatal(err)
	}
	if len(m.outputItems) != 2 {
		t.Fatalf("retained output items = %d, want 2", len(m.outputItems))
	}

	events := streampump.DrainPending(&m.pending)
	reasoningParts := 0
	for i := range events {
		if events[i].Kind == aipapi.EventReasoningPart {
			reasoningParts++
			assertJSONEqual(t, events[i].Reasoning.Opaque, `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":null,"status":"completed"}`)
		}
	}
	if reasoningParts != 1 {
		t.Fatalf("reasoning parts = %d, events = %+v", reasoningParts, events)
	}
}

func TestCodexEventMapper_ResponseCompletedDeduplicatesDoneItems(t *testing.T) {
	t.Parallel()

	m := newCodexEventMapper(0)
	reasoning := `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"encrypted-state"}`
	compaction := `{"type":"compaction","id":"cmp_1","encrypted_content":"compact-state"}`
	for _, item := range []string{reasoning, compaction} {
		if err := m.handleData(`{"type":"response.output_item.done","item":` + item + `}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.handleData(`{"type":"response.completed","response":{"id":"resp_1","output":[` + reasoning + `,` + compaction + `]}}`); err != nil {
		t.Fatal(err)
	}
	if len(m.outputItems) != 2 {
		t.Fatalf("retained output items = %d, want 2", len(m.outputItems))
	}
	reasoningParts := 0
	for _, event := range streampump.DrainPending(&m.pending) {
		if event.Kind == aipapi.EventReasoningPart {
			reasoningParts++
		}
	}
	if reasoningParts != 1 {
		t.Fatalf("reasoning parts = %d, want 1", reasoningParts)
	}
}

func TestCodexEventMapper_ResponseCompletedReplacesEarlierIncompleteReasoning(t *testing.T) {
	t.Parallel()

	m := newCodexEventMapper(0)
	initial := `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`
	if err := m.handleData(`{"type":"response.output_item.done","item":` + initial + `}`); err != nil {
		t.Fatal(err)
	}
	enriched := `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"late-state","status":"completed"}`
	if err := m.handleData(`{"type":"response.completed","response":{"id":"resp_1","output":[` + enriched + `]}}`); err != nil {
		t.Fatal(err)
	}
	if len(m.outputItems) != 1 {
		t.Fatalf("retained output items = %d, want 1", len(m.outputItems))
	}
	assertJSONEqual(t, m.outputItems[0], enriched)

	var reasoningParts []*aipapi.ReasoningPart
	for _, event := range streampump.DrainPending(&m.pending) {
		if event.Kind == aipapi.EventReasoningPart {
			reasoningParts = append(reasoningParts, event.Reasoning)
		}
	}
	if len(reasoningParts) != 2 {
		t.Fatalf("reasoning parts = %d, want 2", len(reasoningParts))
	}
	assertJSONEqual(t, reasoningParts[len(reasoningParts)-1].Opaque, enriched)
}

func TestBuildInputItems_RejectsUnsupportedReasoning(t *testing.T) {
	t.Parallel()

	for _, part := range []aipapi.Part{
		{Kind: aipapi.PartReasoning},
		{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "summary"}},
	} {
		call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{part}}}}
		if _, err := buildInputItems(call); err == nil {
			t.Fatal("expected unsupported reasoning error")
		}
	}
}

func TestBuildInputItems_PreservesExactReasoningAndCompaction(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{Messages: []aipapi.Message{{
		Role: aipapi.RoleAssistant,
		Parts: []aipapi.Part{
			{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
					Opaque:  json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"encrypted-state"}`),
				},
			},
			{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"compact-state"}`)},
		},
	}}}

	items, err := buildInputItems(call)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("input items = %d, want 2", len(items))
	}
	assertJSONEqual(t, items[0], call.Messages[0].Parts[0].Reasoning.Opaque)
	assertJSONEqual(t, items[1], call.Messages[0].Parts[1].Content)
}

func TestSliceInputAfterReplayedOutputItems_AcceptsOpaqueItems(t *testing.T) {
	t.Parallel()

	priorItem := textMessageItem{Type: "message", Role: "user", Content: "prompt"}
	reasoning := opaqueResponseItem{raw: json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"encrypted-state"}`)}
	compaction := opaqueResponseItem{raw: json.RawMessage(`{"type":"compaction","id":"cmp_1","encrypted_content":"compact-state"}`)}
	toolOutput := functionCallOutputItem{Type: "function_call_output", CallID: "call_1", Output: "done"}
	current := []inputItem{priorItem, reasoning, compaction, toolOutput}

	got, ok := sliceInputAfterReplayedOutputItems(fingerprintInputItems([]inputItem{priorItem}), 2, current, nil)
	if !ok {
		t.Fatal("expected continuation slicing to accept replayed opaque output items")
	}
	if len(got) != 1 {
		t.Fatalf("sliced input items = %d, want 1", len(got))
	}
	assertJSONEqual(t, got[0], toolOutput)
}

func assertJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wantJSON []byte
	switch value := want.(type) {
	case string:
		wantJSON = []byte(value)
	case json.RawMessage:
		wantJSON = value
	default:
		wantJSON, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatalf("decode got %s: %v", gotJSON, err)
	}
	if err := json.Unmarshal(wantJSON, &wantValue); err != nil {
		t.Fatalf("decode want %s: %v", wantJSON, err)
	}
	gotCanonical, err := json.Marshal(gotValue)
	if err != nil {
		t.Fatalf("marshal got value: %v", err)
	}
	wantCanonical, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("marshal want value: %v", err)
	}
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("JSON = %s, want %s", gotCanonical, wantCanonical)
	}
}
