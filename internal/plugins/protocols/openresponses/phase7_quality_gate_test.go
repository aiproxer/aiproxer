package openresponses

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Phase 7.5 quality-gate coverage: these tests close the reachable defensive,
// error, and fallback branches of the production codec/state-machine package
// (Requirement 12.13/12.14, design Coverage Quality >= 90% statement target).

func TestQualityGate_DecodeRequestEmptyAndOversize(t *testing.T) {
	t.Parallel()
	if _, _, err := DecodeRequest(nil); err == nil {
		t.Fatal("expected error for empty payload")
	}
	oversize := append(bytes.Repeat([]byte(`{"a":1},`), MaxRequestBytes/4), []byte(`{"x":1}`)...)
	if len(oversize) <= MaxRequestBytes {
		oversize = append(oversize, bytes.Repeat([]byte(" "), MaxRequestBytes-len(oversize)+1)...)
	}
	if _, _, err := DecodeRequest(oversize); err == nil {
		t.Fatal("expected error for oversize payload")
	}
}

func TestQualityGate_DecodeRequestInvalidInputForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
		want string
	}{
		{"InvalidStringInput", `{"model":"gpt-4o","input":"unterminated}`, "decode failed"},
		{"InvalidItemArray", `{"model":"gpt-4o","input":[{"type":123}]}`, "decode failed"},
		{"ArrayRoot", `[]`, "decode failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := DecodeRequest([]byte(tc.json))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestQualityGate_ValidateJSONStrictUsesConfiguredLimits(t *testing.T) {
	t.Parallel()

	limits := DefaultLimits()
	limits.MaxRequestSizeBytes = 4
	if err := validateJSONStrictWithLimits([]byte(`{"a":1}`), limits); err == nil {
		t.Fatal("expected configured request-size limit")
	} else {
		var limitErr *LimitExceededError
		if !errors.As(err, &limitErr) || limitErr.Param != "request_size" {
			t.Fatalf("error=%v, want request_size LimitExceededError", err)
		}
	}

	limits = DefaultLimits()
	limits.MaxItemDepth = 1
	if err := validateJSONStrictWithLimits([]byte(`{"nested":{"value":1}}`), limits); err == nil {
		t.Fatal("expected configured depth limit")
	} else if !strings.Contains(err.Error(), "exceeds limit 1") {
		t.Fatalf("error=%v, want configured depth", err)
	}
}

func TestQualityGate_ValidateJSONStrictDoesNotLeakDuplicateMemberNames(t *testing.T) {
	t.Parallel()
	const secretKey = "secret_member_name"
	err := validateJSONStrictWithLimits([]byte(`{"`+secretKey+`":1,"`+secretKey+`":2}`), Limits{MaxItemDepth: 64})
	if err == nil {
		t.Fatal("expected duplicate member error")
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("duplicate member name leaked in error: %v", err)
	}
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("error=%v does not wrap ErrDecodeFailed", err)
	}
}

func TestQualityGate_ValidateJSONStrictBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
		want string
	}{
		// Depth and root-form branches that the streaming JSON decoder admits
		// (so the strict validator's own state machine observes them). Structural
		// syntax errors such as `{{`, `}`, `{[`, `]`, and `{1}` are rejected by
		// the underlying decoder before these branches, so they are not listed.
		{"ArrayDepthExceeded", `[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[1]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]`, "exceeds limit"},
		{"StringRoot", `"hello"`, ""},
		{"ValueRoot", `123`, ""},
		{"ObjectRoot", `{"a":1}`, ""},
		{"TrailingData", `{"a":1}{"b":2}`, "trailing data"},
		{"Unclosed", `{"a":1`, "unclosed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateJSONStrictWithLimits([]byte(tc.json), Limits{MaxItemDepth: 64})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected valid JSON, got error %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestQualityGate_DecodeToolChoiceReachableErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"InvalidString", `"unterminated`, "invalid string tool_choice"},
		{"UnknownString", `"bogus"`, "unknown string tool_choice"},
		{"InvalidObject", `{"type":`, "invalid object tool_choice"},
		{"InvalidObjectType", `{"type":123}`, "invalid object tool_choice type"},
		{"FunctionMissingName", `{"type":"function","function":{}}`, "missing name"},
		{"UnknownObjectType", `{"type":"bogus"}`, "unknown object tool_choice type"},
		{"InvalidFormat", `12345`, "invalid tool_choice format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeToolChoice([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestQualityGate_DecodeItemCallIDFallbacksAndFallbacks(t *testing.T) {
	t.Parallel()
	// function_call without call_id falls back to item ID.
	fc := WireItem{Type: "function_call", ID: "fc_1", Name: "fn", Arguments: []byte(`{}`)}
	item, err := DecodeItem(fc, DefaultLimits())
	if err != nil {
		t.Fatalf("function_call decode failed: %v", err)
	}
	if item.ToolCall == nil || item.ToolCall.CallID != "fc_1" {
		t.Fatalf("expected call_id fallback to item ID, got %+v", item.ToolCall)
	}

	// function_call_output without call_id falls back to item ID.
	fo := WireItem{Type: "function_call_output", ID: "fc_out_1", Output: []byte(`"ok"`)}
	item, err = DecodeItem(fo, DefaultLimits())
	if err != nil {
		t.Fatalf("function_call_output decode failed: %v", err)
	}
	if item.ToolResult == nil || item.ToolResult.CallID != "fc_out_1" {
		t.Fatalf("expected call_id fallback to item ID, got %+v", item.ToolResult)
	}

	// tool result output that is a string but not valid JSON string falls back to raw text.
	foRaw := WireItem{Type: "function_call_output", ID: "id", Output: []byte(`unquoted raw`)}
	item, err = DecodeItem(foRaw, DefaultLimits())
	if err != nil {
		t.Fatalf("raw output decode failed: %v", err)
	}
	if item.ToolResult == nil || item.ToolResult.Output != "unquoted raw" {
		t.Fatalf("expected raw output fallback, got %+v", item.ToolResult)
	}

	// tool result output as array of content parts.
	foParts := WireItem{Type: "function_call_output", ID: "id", Output: []byte(`[{"type":"input_text","text":"part one"}]`)}
	item, err = DecodeItem(foParts, DefaultLimits())
	if err != nil {
		t.Fatalf("parts output decode failed: %v", err)
	}
	if item.ToolResult == nil || len(item.ToolResult.Parts) != 1 || item.ToolResult.Parts[0].Text != "part one" {
		t.Fatalf("expected tool_result parts, got %+v", item.ToolResult)
	}

	// reasoning with raw (non-string) payload falls back to raw text.
	rs := WireItem{Type: "reasoning", ID: "rs_1", Reasoning: []byte(`plain reasoning`)}
	item, err = DecodeItem(rs, DefaultLimits())
	if err != nil {
		t.Fatalf("reasoning decode failed: %v", err)
	}
	if item.Reasoning == nil || item.Reasoning.Reasoning == nil || item.Reasoning.Reasoning.Text != "plain reasoning" {
		t.Fatalf("expected reasoning raw fallback, got %+v", item.Reasoning)
	}

	// reasoning without reasoning payload but with content string.
	rsContent := WireItem{Type: "reasoning", ID: "rs_2", Content: []byte(`"string content"`)}
	item, err = DecodeItem(rsContent, DefaultLimits())
	if err != nil {
		t.Fatalf("reasoning content decode failed: %v", err)
	}
	if item.Reasoning == nil || item.Reasoning.Reasoning == nil || item.Reasoning.Reasoning.Text != "string content" {
		t.Fatalf("expected reasoning content string, got %+v", item.Reasoning)
	}

	// reasoning with content as array of parts joins their text.
	rsParts := WireItem{Type: "reasoning", ID: "rs_3", Content: []byte(`[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]`)}
	item, err = DecodeItem(rsParts, DefaultLimits())
	if err != nil {
		t.Fatalf("reasoning parts decode failed: %v", err)
	}
	if item.Reasoning == nil || item.Reasoning.Reasoning == nil || item.Reasoning.Reasoning.Text != "a\nb" {
		t.Fatalf("expected reasoning parts join, got %q", item.Reasoning.Reasoning.Text)
	}

	// extension with received status normalizes to completed.
	ext := WireItem{Type: "acme:thing", ID: "ext_1", Status: "received", Namespace: "acme", Direction: "in", Data: []byte(`{"k":"v"}`)}
	item, err = DecodeItem(ext, DefaultLimits())
	if err != nil {
		t.Fatalf("extension decode failed: %v", err)
	}
	if item.Kind != aipapi.ItemKindExtension || item.Status != aipapi.ItemStatusCompleted {
		t.Fatalf("expected completed extension, got %+v", item)
	}

	// message with status received normalizes to completed.
	msg := WireItem{Type: "message", ID: "m_1", Role: "user", Status: "received", Content: []byte(`[{"type":"input_text","text":"hi"}]`)}
	item, err = DecodeItem(msg, DefaultLimits())
	if err != nil {
		t.Fatalf("message decode failed: %v", err)
	}
	if item.Status != aipapi.ItemStatusCompleted {
		t.Fatalf("expected completed message, got %+v", item)
	}
}

func TestQualityGate_DecodeContentPartsErrorPaths(t *testing.T) {
	t.Parallel()
	// empty content decodes to nil parts.
	parts, err := decodeContentParts([]byte("   "))
	if err != nil || parts != nil {
		t.Fatalf("expected nil parts for empty content, got %v err %v", parts, err)
	}

	// non-array content rejected.
	if _, err := decodeContentParts([]byte(`{"type":"input_text"}`)); err == nil {
		t.Fatal("expected error for non-array content")
	}

	// malformed part entry rejected.
	if _, err := decodeContentParts([]byte(`[123]`)); err == nil {
		t.Fatal("expected error for malformed part entry")
	}

	// An image_url object with a non-string URL is malformed and must not be
	// retained as raw JSON text.
	if _, err := decodeContentParts([]byte(`[{"type":"input_image","image_url":{"url":123}}]`)); err == nil {
		t.Fatal("expected malformed image_url object to be rejected")
	}
}

func TestQualityGate_EncodeRequestAndContentPartFallbacks(t *testing.T) {
	t.Parallel()
	// unknown item kind fails EncodeRequest with a wrapped error.
	if _, err := EncodeRequest(aipapi.Call{Items: []aipapi.Item{{Kind: aipapi.ItemKind("nope")}}}); err == nil {
		t.Fatal("expected error for unknown item kind in EncodeRequest")
	}

	// encodeContentPart with an unknown kind falls back to input_text/output_text.
	w := encodeContentPart(aipapi.ContentPart{Kind: aipapi.ContentPartKind("mystery"), Text: "txt"}, aipapi.RoleUser)
	if w.Type != "input_text" || w.Text != "txt" {
		t.Fatalf("unexpected fallback content part: %+v", w)
	}
	wAsst := encodeContentPart(aipapi.ContentPart{Kind: aipapi.ContentPartKind("mystery"), Text: "txt"}, aipapi.RoleAssistant)
	if wAsst.Type != "output_text" {
		t.Fatalf("unexpected assistant fallback type: %+v", wAsst)
	}
}

func TestQualityGate_MapErrorToWireNilAndSequence(t *testing.T) {
	t.Parallel()
	status, env, cls := MapErrorToWire(nil)
	if status != 200 || cls != "" || env.Error.Type != "" {
		t.Fatalf("unexpected nil-error mapping: status=%d class=%q env=%+v", status, cls, env)
	}

	seq := &SequenceError{Code: "seq", Event: "evt", Sequence: 3, Message: "boom"}
	status, env, cls = MapErrorToWire(seq)
	if status != 400 || cls != ClassificationInvalidRequest || env.Error.Code != "seq" {
		t.Fatalf("unexpected SequenceError mapping: status=%d class=%q env=%+v", status, cls, env)
	}
}

func TestQualityGate_StateMachineSequenceErrors(t *testing.T) {
	t.Parallel()
	envelope := EnvelopeMetadata{ResponseID: "resp_g", CreatedAt: time.Unix(1715620000, 0), Model: "gpt-4o"}
	opts := aipapi.GenerationOptions{}

	// missing_start: event before response started.
	sm := NewStateMachine(envelope, opts)
	_, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
	if err == nil || !strings.Contains(err.Error(), "response not started before event") {
		t.Fatalf("expected missing_start error, got %v", err)
	}

	// text delta without active message item.
	sm2 := NewStateMachine(envelope, opts)
	_, _ = sm2.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, err = sm2.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	if err == nil || !strings.Contains(err.Error(), "text delta without active message item") {
		t.Fatalf("expected text_delta_without_message error, got %v", err)
	}

	// tool args delta without active tool call item.
	sm3 := NewStateMachine(envelope, opts)
	_, _ = sm3.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, err = sm3.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c", Delta: "{}"})
	if err == nil || !strings.Contains(err.Error(), "tool call args delta without active tool call item") {
		t.Fatalf("expected tool_args_without_item error, got %v", err)
	}

	// tool call finished without active tool call item.
	sm4 := NewStateMachine(envelope, opts)
	_, _ = sm4.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, err = sm4.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c"})
	if err == nil || !strings.Contains(err.Error(), "tool call finished without active tool call item") {
		t.Fatalf("expected tool_finished_without_item error, got %v", err)
	}

	// tool call started without an explicit call id gets a generated id.
	sm5 := NewStateMachine(envelope, opts)
	_, _ = sm5.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	evs, err := sm5.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolName: "fn"})
	if err != nil {
		t.Fatalf("ToolCallStarted failed: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("expected events for tool call started")
	}
}

func TestQualityGate_StateMachineSnapshotRollbackActiveContent(t *testing.T) {
	t.Parallel()
	envelope := EnvelopeMetadata{ResponseID: "resp_rb", CreatedAt: time.Unix(1715620000, 0), Model: "gpt-4o"}
	opts := aipapi.GenerationOptions{}

	sm := NewStateMachine(envelope, opts)
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
	// ToolCallStarted closes the active content part (activeContentIdx -> -1)
	// and opens a new tool item, so a later rollback must nil the content part
	// while keeping the tool item pointer valid.
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c", ToolName: "fn"})
	_, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventKind("unknown_unprefixed_kind")})
	if err == nil {
		t.Fatal("expected rollback error")
	}
	if sm.activeContentPart != nil {
		t.Fatalf("expected nil active content part after rollback, got %p", sm.activeContentPart)
	}
	if sm.activeItem == nil || sm.activeItem.Kind != aipapi.ItemKindToolCall {
		t.Fatalf("expected tool call item restored, got %+v", sm.activeItem)
	}
}

func TestQualityGate_StateMachineSnapshotWithStreamError(t *testing.T) {
	t.Parallel()
	envelope := EnvelopeMetadata{ResponseID: "resp_se", CreatedAt: time.Unix(1715620000, 0), Model: "gpt-4o"}
	opts := aipapi.GenerationOptions{}

	sm := NewStateMachine(envelope, opts)
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventError, ErrorCode: "boom", ErrorMessage: "failed"})
	if err != nil {
		t.Fatalf("EventError failed: %v", err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != "response.failed" {
		t.Fatalf("expected response.failed as the terminal event, got %v", evs)
	}
	// takeSnapshot must copy the stream error; next event triggers snapshot + error.
	_, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "late"})
	if err == nil || !strings.Contains(err.Error(), "event received after terminal state") {
		t.Fatalf("expected output_after_terminal error, got %v", err)
	}
}

func TestQualityGate_ConservativeLegacyNormalizeErrorPropagation(t *testing.T) {
	t.Parallel()
	envelope := EnvelopeMetadata{ResponseID: "resp_norm_err", CreatedAt: time.Unix(1715620000, 0), Model: "gpt-4o"}
	// First event is a message start without a response start -> error propagates.
	_, _, err := ConservativeLegacyNormalize(envelope, aipapi.GenerationOptions{}, []aipapi.Event{{Kind: aipapi.EventMessageStarted}})
	if err == nil {
		t.Fatal("expected ConservativeLegacyNormalize error propagation")
	}
}

func TestQualityGate_ResourceBuilderReachableBranches(t *testing.T) {
	t.Parallel()
	now := time.Unix(1715620000, 0)
	store := false
	prev := "resp_prev"
	env := EnvelopeMetadata{
		ResponseID:         "resp_rb2",
		CreatedAt:          now,
		Model:              "gpt-4o",
		Status:             "",
		Store:              &store,
		PreviousResponseID: prev,
	}
	parallel := true
	opts := aipapi.GenerationOptions{ParallelToolCalls: &parallel}
	items := []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}}}
	res, _, err := BuildResponseResource(env, items, UsageStats{}, opts, nil)
	if err != nil {
		t.Fatalf("BuildResponseResource failed: %v", err)
	}
	if res.Status != "completed" || res.Store != false || res.ParallelToolCalls != true || res.PreviousResponseID == nil || *res.PreviousResponseID != prev {
		t.Fatalf("unexpected resource defaults/overrides: %+v", res)
	}

	// EncodeItem failure inside the trajectory loop.
	if _, _, err := BuildResponseResource(env, []aipapi.Item{{Kind: aipapi.ItemKind("nope")}}, UsageStats{}, opts, nil); err == nil {
		t.Fatal("expected BuildResponseResource trajectory error")
	}
	if _, _, err := BuildCompactResource(env, []aipapi.Item{{Kind: aipapi.ItemKind("nope")}}, UsageStats{}); err == nil {
		t.Fatal("expected BuildCompactResource trajectory error")
	}

	// Compact resource default status.
	cres, _, err := BuildCompactResource(env, items, UsageStats{})
	if err != nil {
		t.Fatalf("BuildCompactResource failed: %v", err)
	}
	if cres.Status != "completed" {
		t.Fatalf("expected compact default status completed, got %s", cres.Status)
	}
}

func TestQualityGate_SSEWriterWriteErrors(t *testing.T) {
	t.Parallel()
	failing := &errWriter{err: errors.New("write boom")}
	w := NewSSEWriter(failing)
	if err := w.WriteEvent(StreamEvent{Type: "response.created"}); err == nil {
		t.Fatal("expected WriteEvent writer error")
	}

	w2 := NewSSEWriter(failing)
	_ = w2.WriteEvent(StreamEvent{Type: "response.completed"})
	if err := w2.WriteDONE(); err == nil {
		t.Fatal("expected WriteDONE writer error")
	}

	// WriteEvent with an unsafe type fails before touching the writer.
	w3 := NewSSEWriter(&bytes.Buffer{})
	if err := w3.WriteEvent(StreamEvent{Type: "bad\ntype"}); err == nil {
		t.Fatal("expected WriteEvent format error")
	}
}

func TestQualityGate_DecodeRequestUnknownTopLevelField(t *testing.T) {
	t.Parallel()
	_, _, err := DecodeRequest([]byte(`{"model":"gpt-4o","input":"hi","bogus_top_level":1}`))
	if err == nil {
		t.Fatal("expected error for unknown unprefixed top-level field")
	}
}

type errWriter struct {
	err error
}

func (w *errWriter) Write(p []byte) (int, error) {
	return 0, w.err
}
