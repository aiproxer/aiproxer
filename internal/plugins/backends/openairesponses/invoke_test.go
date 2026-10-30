package openairesponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestParamsForCall_textOnly(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "t1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
		Route: aipapi.RouteIntent{Selector: "openai-responses:gpt-4o-mini"},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "openai-responses", Model: "gpt-4o-mini"},
		Key:     "openai-responses:gpt-4o-mini",
	}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "gpt-4o-mini" {
		t.Fatalf("model: %s", p.Model)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"model":"gpt-4o-mini"`) {
		t.Fatalf("marshaled params: %s", raw)
	}
}

func TestParamsForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("gpt-4o-mini")
	call := aipapi.Call{
		ID: "t2",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"openairesponses.model": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "gpt-4o-mini" {
		t.Fatalf("model: %s", p.Model)
	}
}

func TestParamsForCall_multimodalParts(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "t3",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("describe"),
				func() aipapi.Part {
					p := aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"}
					return p
				}(),
				aipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "minimal.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "input_image") || !strings.Contains(s, "input_file") {
		t.Fatalf("expected multimodal markers, got: %s", s)
	}
}

func TestParamsForCall_emptyTextPartSkipped(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "empty-text",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart(""),
				{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("empty text part should be skipped, not error: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "input_text") {
		t.Fatalf("empty text part should not appear in output: %s", s)
	}
	if !strings.Contains(s, "input_image") {
		t.Fatalf("image part should be present: %s", s)
	}
}

func TestParamsForCall_fileRefNonDataURL_rejected(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "bad-file-ref",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("x"),
				aipapi.FilePart("https://example.com/file.pdf", "application/pdf", "file.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	_, err := backend.ParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("expected error for non-data URL file ref")
	}
	if !strings.Contains(err.Error(), "data URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParamsForCall_instructions(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "inst",
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart("Be concise.")},
		}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"instructions":"Be concise."`) {
		t.Fatalf("expected instructions in params: %s", s)
	}
}

func TestParamsForCall_reasoningEffort(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "reas",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("think")},
		}},
		Options: aipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"reasoning"`) || !strings.Contains(s, `"effort":"high"`) {
		t.Fatalf("expected reasoning effort in params: %s", s)
	}
}

func TestParamsForCall_verbosityIsNestedUnderText(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
		Options:  aipapi.GenerationOptions{Verbosity: aipapi.VerbosityMedium},
	}
	p, err := backend.ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"text":{"verbosity":"medium"}`) {
		t.Fatalf("expected nested text verbosity: %s", raw)
	}
}

func TestParamsForCall_toolsAndToolChoice(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	call := aipapi.Call{
		ID: "tools",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("weather?")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  schema,
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_weather"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"name":"get_weather"`) || !strings.Contains(s, `"type":"function"`) {
		t.Fatalf("expected tools in params: %s", s)
	}
}

func TestParamsForCall_toolChoiceModes(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object"}`)
	base := aipapi.Call{
		ID: "tc",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
		Tools: []aipapi.ToolDef{{Name: "fn", Parameters: schema}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}

	for _, tc := range []struct {
		name   string
		mode   aipapi.ToolChoiceMode
		want   string
		substr string
	}{
		{"none", aipapi.ToolChoiceNone, "none", `"tool_choice":"none"`},
		{"any", aipapi.ToolChoiceAny, "required", `"tool_choice":"required"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := base
			c.ToolChoice = aipapi.ToolChoice{Mode: tc.mode}
			p, err := backend.ParamsForCall(&c, cand)
			if err != nil {
				t.Fatalf("%s: %v", tc.want, err)
			}
			raw, _ := json.Marshal(p)
			if !strings.Contains(string(raw), tc.substr) {
				t.Fatalf("%s: want %q in %s", tc.want, tc.substr, raw)
			}
		})
	}
}

func TestParamsForCall_toolResultMessage(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "tool-out",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("call the tool")}},
			{
				Role: aipapi.RoleTool,
				Parts: []aipapi.Part{{
					Kind:       aipapi.PartToolResult,
					ToolCallID: "call_1",
					Content:    []byte(`{"ok":true}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "function_call_output") || !strings.Contains(s, "call_1") {
		t.Fatalf("expected function_call_output for tool result: %s", s)
	}
}

func TestParamsForCall_functionCallInputFromAssistantJSON(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "fc-in",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{{
					Kind:    aipapi.PartJSON,
					Content: []byte(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"function_call"`) || !strings.Contains(s, "call_1") || !strings.Contains(s, "get_weather") {
		t.Fatalf("expected function_call input item: %s", s)
	}
}

func TestUpstreamError_returnsAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad","type":"invalid_request_error","param":"","code":"invalid_request_error"}}`))
	}))
	t.Cleanup(srv.Close)

	cli := openai.NewClient(
		option.WithBaseURL(srv.URL+"/v1"),
		option.WithAPIKey("sk-test"),
	)
	_, err := cli.Responses.New(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("x", responses.EasyInputMessageRoleUser),
			},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *openai.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", apiErr.StatusCode)
	}
}
