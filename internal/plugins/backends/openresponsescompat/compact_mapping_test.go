package openresponsescompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// itemAuthorityCompactCall is a canonical context.compaction call in the exact
// shape the OpenResponses frontend produces: item authority, non-streaming
// transport, ordered message window, and a generation control.
func itemAuthorityCompactCall() aipapi.Call {
	return aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationContextCompaction,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
		Options: aipapi.GenerationOptions{Temperature: new(0.2)},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "in_msg_1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartText,
					Text: "Compress the launch plan into a compact window.",
				}},
			},
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "in_msg_2",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleAssistant,
				Content: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartText,
					Text: "The launch is Tuesday; notify support first.",
				}},
			},
		},
	}
}

// completeCompactResourceJSON is a schema-valid pinned response.compaction
// resource carrying a reusable ordered item window, a provider-native
// item_reference, a compaction lineage item, and usage.
const completeCompactResourceJSON = `{
  "id": "comp_native_abc",
  "object": "response.compaction",
  "created_at": 1719900000,
  "status": "completed",
  "model": "model-x",
  "output": [
    {"type": "message", "id": "msg_comp_1", "status": "completed", "role": "user", "content": [{"type": "input_text", "text": "Launch Tuesday, notify support first."}]},
    {"type": "message", "id": "msg_comp_2", "status": "completed", "role": "assistant", "content": [{"type": "output_text", "text": "Summary: launch Tuesday with support notified."}]},
    {"type": "function_call", "id": "fc_comp_1", "status": "completed", "call_id": "call_comp_1", "name": "get_weather", "arguments": "{\"location\":\"Paris\"}"},
    {"type": "reasoning", "id": "rs_comp_1", "status": "completed", "reasoning": "Compressing context."},
    {"type": "item_reference", "id": "native_prev_item"},
    {"type": "compaction", "id": "cmp_comp_1", "status": "completed", "encapsulated_id": "enc_native_1", "dialect": "openresponses.2026-04-24", "implementor": "provider-x", "encrypted_content": "gAAAAABenc_comp_1"}
  ],
  "usage": {
    "input_tokens": 41,
    "input_tokens_details": {"cached_tokens": 11},
    "output_tokens": 12,
    "output_tokens_details": {"reasoning_tokens": 2},
    "total_tokens": 53
  }
}`

func TestCompact_IncompleteDetailsReasonMapped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "absent_reason_defaults_length", reason: `null`, want: "length"},
		{name: "max_output_tokens_maps_length", reason: `{"reason":"max_output_tokens"}`, want: "length"},
		{name: "content_filter_preserved", reason: `{"reason":"content_filter"}`, want: "content_filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := strings.ReplaceAll(completeCompactResourceJSON, `"status": "completed"`, `"status": "incomplete"`)
			body = strings.Replace(body, `"usage": {`, `"incomplete_details": `+tc.reason+`,
  "usage": {`, 1)
			events, _, err := parseCompactResource("my-or", []byte(body), defaultResponseTestLimits())
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Kind != aipapi.EventResponseFinished {
				t.Fatalf("last event = %+v, want response_finished", last)
			}
			if last.FinishReason != tc.want {
				t.Fatalf("finish reason = %q, want %q", last.FinishReason, tc.want)
			}
			if last.ResponseStatus != "incomplete" {
				t.Fatalf("response status = %q, want explicit incomplete", last.ResponseStatus)
			}
		})
	}
}

func TestCompact_OperationPostsStrictNonStreamingBodyToCompactEndpoint(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeCompactResourceJSON)
	})
	es, err := be.Open(context.Background(), itemAuthorityCompactCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	events := drainManagedEvents(t, es)
	if !hasTextDelta(events, "Summary: launch Tuesday with support notified.") {
		t.Fatalf("missing compacted text window in %+v", events)
	}
	if !hasEventKind(events, aipapi.EventUsageDelta) {
		t.Fatalf("missing usage event in %+v", events)
	}
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished {
		t.Fatalf("last event = %+v, want response_finished", last)
	}

	if obs.count() != 1 {
		t.Fatalf("observer request count = %d, want exactly 1", obs.count())
	}
	req := obs.last(t)
	if req.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", req.Method)
	}
	if req.Path != "/responses/compact" {
		t.Fatalf("path = %q, want /responses/compact", req.Path)
	}
	if ct := req.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	if accept := req.Header.Get("Accept"); !strings.HasPrefix(accept, "application/json") {
		t.Fatalf("accept = %q, want application/json", accept)
	}
	if auth := req.Header.Get("Authorization"); auth != "" {
		t.Fatalf("no-auth mode sent Authorization header %q", auth)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(req.Body))
	}
	if got := string(payload["model"]); got != `"model-x"` {
		t.Fatalf("model = %s, want model-x", got)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	if len(input) != 2 {
		t.Fatalf("input items = %d, want 2", len(input))
	}
	if string(input[0]["type"]) != `"message"` || string(input[1]["type"]) != `"message"` {
		t.Fatalf("input types = %s / %s", string(input[0]["type"]), string(input[1]["type"]))
	}
	for _, forbidden := range []string{
		"previous_response_id", `"stream"`, `"store"`, `"background"`,
		"proxy_call", "client-session", "auth-session", "resume_secret",
	} {
		if bodyHasForbiddenField(req.Body, forbidden) {
			t.Fatalf("compact request forwarded forbidden field %q: %s", forbidden, string(req.Body))
		}
	}
}

func TestCompact_ForwardsSchemaPermittedInstructionsAndPromptCacheKey(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeCompactResourceJSON)
	})
	call := itemAuthorityCompactCall()
	call.Items = append([]aipapi.Item{{
		Kind:    aipapi.ItemKindMessage,
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleSystem,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Summarize concisely."}},
	}}, call.Items...)
	call.PromptCacheKey = "openresponses-compact-test"
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = drainManagedEvents(t, es)
	if obs.count() != 1 {
		t.Fatalf("observer request count = %d, want exactly 1", obs.count())
	}
	req := obs.last(t)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(req.Body))
	}
	if got := string(payload["prompt_cache_key"]); got != `"openresponses-compact-test"` {
		t.Fatalf("prompt_cache_key = %s, want the schema-permitted hint forwarded", got)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	if len(input) < 1 || string(input[0]["type"]) != `"message"` || string(input[0]["role"]) != `"system"` {
		t.Fatalf("instructions must forward as a leading system message item, got %+v", input)
	}
}

func TestCompact_RemoteCompactResourceParsedToCanonicalLifecycleStream(t *testing.T) {
	events, native, err := parseCompactResource("my-or", []byte(completeCompactResourceJSON), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if native.ResponseID != "comp_native_abc" {
		t.Fatalf("native response id = %q", native.ResponseID)
	}
	for _, wantID := range []string{"msg_comp_1", "msg_comp_2", "fc_comp_1", "rs_comp_1", "native_prev_item", "cmp_comp_1"} {
		found := slices.Contains(native.ItemIDs, wantID)
		if !found {
			t.Fatalf("native item ids %v missing %q", native.ItemIDs, wantID)
		}
	}
	if len(native.ToolCallIDs) != 1 || native.ToolCallIDs[0] != "call_comp_1" {
		t.Fatalf("native tool call ids = %v", native.ToolCallIDs)
	}

	events = filterLifecycle(events)
	if len(events) == 0 {
		t.Fatal("no lifecycle events")
	}
	if events[0].Kind != aipapi.EventResponseStarted {
		t.Fatalf("first event = %q, want response_started", events[0].Kind)
	}
	if last := events[len(events)-1]; last.Kind != aipapi.EventResponseFinished {
		t.Fatalf("last event = %+v, want response_finished", last)
	}
	var gotText, gotArgs, gotReasoning bool
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			if ev.Delta == "Summary: launch Tuesday with support notified." {
				gotText = true
			}
		case aipapi.EventToolCallArgsDelta:
			if ev.Delta == `{"location":"Paris"}` {
				gotArgs = true
			}
		case aipapi.EventReasoningDelta:
			if ev.Delta == "Compressing context." {
				gotReasoning = true
			}
		}
		if strings.Contains(ev.Delta, "native") || strings.Contains(ev.ToolCallID, "native") || strings.Contains(string(ev.Kind), "native") {
			t.Fatalf("native id leaked into canonical stream: %+v", events)
		}
	}
	wants := map[string]bool{"text": gotText, "args": gotArgs, "reasoning": gotReasoning}
	for name, ok := range wants {
		if !ok {
			t.Fatalf("missing %s lifecycle event: %+v", name, events)
		}
	}
}

func TestCompact_RemoteCompactStreamFeedsProductionStateMachine(t *testing.T) {
	events, _, err := parseCompactResource("my-or", []byte(completeCompactResourceJSON), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	sm := proto.NewStateMachine(proto.EnvelopeMetadata{
		ResponseID: "comp_proxy_1",
		CreatedAt:  fixedClockTime(),
		Model:      "model-x",
	}, aipapi.GenerationOptions{})
	for i, ev := range events {
		if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
			t.Fatalf("event %d invalid: %v", i, err)
		}
		if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
			t.Fatalf("state machine rejected compact event %d (%s): %v", i, ev.Kind, err)
		}
	}
	if sm.State() != proto.StateTerminal {
		t.Fatalf("state machine not terminal: %s", sm.State())
	}
	if sm.Status() != "completed" {
		t.Fatalf("state machine status = %q", sm.Status())
	}
	traj := sm.Trajectory()
	if len(traj) != 5 {
		t.Fatalf("trajectory items = %d, want 5 (two messages + one tool call + one reasoning + one compaction)", len(traj))
	}
	last := traj[len(traj)-1]
	if last.Kind != aipapi.ItemKindCompaction {
		t.Fatalf("trajectory last item = %+v, want compaction", last)
	}
	if last.Compaction == nil || last.Compaction.EncryptedContent == "" {
		t.Fatalf("trajectory compaction item lost encrypted_content: %+v", last.Compaction)
	}
	if last.ID != "cmp_comp_1" {
		t.Fatalf("trajectory compaction id = %q, want cmp_comp_1", last.ID)
	}
}

// TestCompact_CompactionItemCarriedOnCanonicalStream pins that the generic
// backend preserves the provider compaction item as an EventItem carrier (the
// compacted ordered window requirement) instead of treating it as private
// evidence, while item_reference stays private.
func TestCompact_CompactionItemCarriedOnCanonicalStream(t *testing.T) {
	events, native, err := parseCompactResource("my-or", []byte(completeCompactResourceJSON), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	var carried *aipapi.Event
	for i := range events {
		if events[i].Kind == aipapi.EventItem && events[i].Item != nil {
			cp := events[i]
			carried = &cp
			break
		}
	}
	if carried == nil {
		t.Fatalf("no EventItem carrier in canonical stream: %+v", events)
	}
	item := carried.Item
	if item.Kind != aipapi.ItemKindCompaction {
		t.Fatalf("carried item kind = %q, want compaction", item.Kind)
	}
	if item.ID != "cmp_comp_1" {
		t.Fatalf("carried compaction id = %q", item.ID)
	}
	if item.Compaction == nil || item.Compaction.EncryptedContent == "" {
		t.Fatalf("carried compaction lost encrypted_content: %+v", item.Compaction)
	}
	if !slicesContains(native.ItemIDs, "cmp_comp_1") {
		t.Fatalf("native evidence must still record the compaction id: %v", native.ItemIDs)
	}
	if !slicesContains(native.ItemIDs, "native_prev_item") {
		t.Fatalf("native evidence must record the item_reference id as private: %v", native.ItemIDs)
	}
	for i := range events {
		if events[i].Kind != aipapi.EventItem || events[i].Item == nil {
			continue
		}
		if events[i].Item.ID == "native_prev_item" {
			t.Fatalf("item_reference must stay private (not carried on the canonical stream): %+v", events)
		}
	}
}

func slicesContains(ids []string, want string) bool {
	return slices.Contains(ids, want)
}

func TestCompact_UsageEventCarriesTokens(t *testing.T) {
	events, _, err := parseCompactResource("my-or", []byte(completeCompactResourceJSON), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	var usage *aipapi.Event
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			u := ev
			usage = &u
		}
	}
	if usage == nil {
		t.Fatal("missing usage event")
	}
	if usage.InputTokens != 41 || usage.OutputTokens != 12 || usage.TotalTokens != 53 ||
		usage.CacheReadTokens != 11 || usage.ReasoningTokens != 2 {
		t.Fatalf("usage event = %+v", usage)
	}

	// Explicit zero counters are provider evidence; only an absent usage object
	// must avoid synthesizing an event.
	zeroUsage := strings.Replace(completeCompactResourceJSON,
		`"usage": {
    "input_tokens": 41,
    "input_tokens_details": {"cached_tokens": 11},
    "output_tokens": 12,
    "output_tokens_details": {"reasoning_tokens": 2},
    "total_tokens": 53
  }`, `"usage": {"input_tokens": 0, "input_tokens_details": {}, "output_tokens": 0, "output_tokens_details": {}, "total_tokens": 0}`, 1)
	events, _, err = parseCompactResource("my-or", []byte(zeroUsage), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	var zero *aipapi.Event
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			copy := ev
			zero = &copy
		}
	}
	if zero == nil || !zero.UsagePresence.InputTokens || !zero.UsagePresence.OutputTokens || !zero.UsagePresence.TotalTokens {
		t.Fatalf("explicit zero usage lost presence: %+v", zero)
	}
	absentUsage := strings.Replace(zeroUsage, `"usage": {"input_tokens": 0, "input_tokens_details": {}, "output_tokens": 0, "output_tokens_details": {}, "total_tokens": 0}`, `"usage": null`, 1)
	events, _, err = parseCompactResource("my-or", []byte(absentUsage), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if hasEventKind(events, aipapi.EventUsageDelta) {
		t.Fatalf("absent usage must not emit a usage event: %+v", events)
	}
}

func TestCompact_ExactCapabilityAndDialectAdmissionBeforeNetwork(t *testing.T) {
	t.Run("capability_missing", func(t *testing.T) {
		be, obs := newObserverBackend(t, "capabilities: [ordered_items, streaming, tools]\n", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		})
		_, err := be.Open(context.Background(), itemAuthorityCompactCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
		if err == nil {
			t.Fatal("expected compaction capability rejection")
		}
		if !errors.Is(err, ErrUnrepresentable) {
			t.Fatalf("error = %v, want ErrUnrepresentable", err)
		}
		if obs.count() != 0 {
			t.Fatalf("missing capability caused %d round trips, want 0", obs.count())
		}
	})

	t.Run("undeclared_compaction_dialect", func(t *testing.T) {
		be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		})
		call := itemAuthorityCompactCall()
		call.Items = append(call.Items, aipapi.Item{
			Kind:   aipapi.ItemKindCompaction,
			ID:     "cmp_in_1",
			Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{
				EncapsulatedID: "enc_in_1",
				Dialect:        "vendor.other",
				Implementor:    "vendor-x",
			},
		})
		_, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
		if err == nil {
			t.Fatal("expected undeclared compaction dialect rejection")
		}
		if !errors.Is(err, ErrUnrepresentable) {
			t.Fatalf("error = %v, want ErrUnrepresentable", err)
		}
		if obs.count() != 0 {
			t.Fatalf("undeclared dialect caused %d round trips, want 0", obs.count())
		}
	})

	t.Run("declared_compaction_dialect_passes", func(t *testing.T) {
		be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, completeCompactResourceJSON)
		})
		call := itemAuthorityCompactCall()
		call.Items = append(call.Items, aipapi.Item{
			Kind:   aipapi.ItemKindCompaction,
			ID:     "cmp_in_1",
			Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{
				EncapsulatedID: "enc_in_1",
				Dialect:        "openresponses.2026-04-24",
			},
		})
		es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
		if err != nil {
			t.Fatalf("declared dialect must pass admission: %v", err)
		}
		_ = drainManagedEvents(t, es)
		if obs.count() != 1 {
			t.Fatalf("request count = %d, want exactly 1", obs.count())
		}
	})
}

func TestCompact_StreamingTransportRejectedWithZeroNetwork(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	})
	call := itemAuthorityCompactCall()
	call.Invocation.TransportMode = aipapi.TransportModeStreaming
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected streaming compact rejection")
	}
	if !errors.Is(err, ErrUnrepresentable) {
		t.Fatalf("error = %v, want ErrUnrepresentable", err)
	}
	if obs.count() != 0 {
		t.Fatalf("streaming compact caused %d round trips, want 0", obs.count())
	}
}

func TestCompact_UnsupportedSemanticsZeroNetwork(t *testing.T) {
	cases := []struct {
		name    string
		call    aipapi.Call
		cand    routing.AttemptCandidate
		wantErr error
	}{
		{
			name:    "missing_model",
			call:    itemAuthorityCompactCall(),
			wantErr: ErrUnrepresentable,
		},
		{
			name: "conflicting_authority",
			call: func() aipapi.Call {
				c := itemAuthorityCompactCall()
				c.Messages = []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("y")}}}
				return c
			}(),
			cand:    routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}},
			wantErr: aipapi.ErrInvalidCall,
		},
		{
			name:    "unknown_operation",
			call:    openResponsesCall(aipapi.Operation("example.unknown")),
			wantErr: ErrOperationUnsupported,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			})
			_, err := be.Open(context.Background(), tc.call, tc.cand)
			if err == nil {
				t.Fatal("expected pre-network rejection")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if obs.count() != 0 {
				t.Fatalf("unsupported compact call caused %d round trips, want 0", obs.count())
			}
		})
	}
}

func TestCompact_PreOutputFailuresClassifiedForFailover(t *testing.T) {
	cases := []struct {
		name        string
		handler     http.HandlerFunc
		wantRecover bool
		wantRoot    error
	}{
		{name: "rate_limit_429", handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "slow down", http.StatusTooManyRequests) }, wantRecover: true},
		{name: "server_500", handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }, wantRecover: true},
		{name: "validation_422", handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusUnprocessableEntity) }, wantRecover: false},
		{name: "unauthorized_401", handler: func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusUnauthorized) }, wantRecover: false},
		{name: "wrong_content_type", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "not json")
		}, wantRecover: true, wantRoot: ErrMalformedResponse},
		{name: "malformed_compact_body", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"response.compaction","status":"completed"`)
		}, wantRecover: true, wantRoot: ErrMalformedResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be, _ := newObserverBackend(t, "", tc.handler)
			_, err := be.Open(context.Background(), itemAuthorityCompactCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected error")
			}
			if got := aipapi.IsRecoverablePreOutput(err); got != tc.wantRecover {
				t.Fatalf("recoverable = %v, want %v (err=%v)", got, tc.wantRecover, err)
			}
			if tc.wantRoot != nil && !errors.Is(err, tc.wantRoot) {
				t.Fatalf("error = %v, want root %v", err, tc.wantRoot)
			}
		})
	}
}

func TestCompact_NoEchoOfProviderBodyInError(t *testing.T) {
	secret := "sk-compact-provider-secret"
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, "compaction denied "+secret, http.StatusUnprocessableEntity)
	})
	_, err := be.Open(context.Background(), itemAuthorityCompactCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed provider body secret: %v", err)
	}
}

func TestCompact_StrictStatusBodyAndContentTypeBounds(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "not_json", body: `{`},
		{name: "trailing_data", body: `{"object":"response.compaction","status":"completed"} {"x":1}`},
		{name: "wrong_object", body: `{"id":"c","object":"response","created_at":0,"status":"completed","model":"m","output":[]}`},
		{name: "unexpected_status", body: `{"id":"c","object":"response.compaction","created_at":0,"status":"in_progress","model":"m","output":[]}`},
		{name: "extension_output", body: `{"id":"c","object":"response.compaction","created_at":0,"status":"completed","model":"m","output":[{"type":"acme:widget","namespace":"acme","data":{"k":1}}]}`},
		{name: "message_no_text", body: `{"id":"c","object":"response.compaction","created_at":0,"status":"completed","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_image","image_url":"x"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseCompactResource("my-or", []byte(tc.body), defaultResponseTestLimits())
			if err == nil {
				t.Fatal("expected malformed compact resource rejection")
			}
			if !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("error = %v, want ErrMalformedResponse", err)
			}
		})
	}

	limits := defaultResponseTestLimits()
	limits.MaxItems = 1
	_, _, err := parseCompactResource("my-or", []byte(completeCompactResourceJSON), limits)
	if err == nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("compact items limit error = %v", err)
	}

	limits = defaultResponseTestLimits()
	limits.MaxTextBytes = 1
	_, _, err = parseCompactResource("my-or", []byte(completeCompactResourceJSON), limits)
	if err == nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("compact text limit error = %v", err)
	}
}

func TestCompact_FailedStatusEmitsErrorEvent(t *testing.T) {
	body := `{"id":"comp_native_fail","object":"response.compaction","created_at":0,"status":"failed","model":"m","output":[]}`
	events, _, err := parseCompactResource("my-or", []byte(body), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventError {
		t.Fatalf("last event = %+v, want error event", last)
	}
	if last.ErrorCode == "" {
		t.Fatalf("error code must not be empty: %+v", last)
	}
}

func TestMapping_ResolveCompactEndpoint(t *testing.T) {
	ep, err := resolveCompactEndpoint("https://api.example.com/openresponses/v1")
	if err != nil {
		t.Fatal(err)
	}
	if ep != "https://api.example.com/openresponses/v1/responses/compact" {
		t.Fatalf("endpoint = %q", ep)
	}
	for _, base := range []string{
		"https://api.example.com/v1/../x",
		"https://api.example.com/%2e%2e/x",
	} {
		if _, err := resolveCompactEndpoint(base); err == nil {
			t.Fatalf("expected traversal rejection for %q", base)
		}
	}
}
