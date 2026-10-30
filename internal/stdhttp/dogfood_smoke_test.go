// Wiring smoke tests: in-process, deterministic, no external network. Kept in the default test
// suite by policy; see .kiro/steering/testing.md (integration-shaped tests section).
package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/refclient/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/refclient/gemini"
	"github.com/aiproxer/aiproxer/internal/refclient/openaichat"
	"github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"google.golang.org/genai"
)

type dogfoodHarness struct {
	baseURL string
	srv     *httptest.Server
	cleanup func()
}

func startDogfoodHarness(tb testing.TB, configAbsPath string) dogfoodHarness {
	tb.Helper()
	ctx := context.Background()
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      configAbsPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		tb.Fatalf("BuildHost: %v", err)
	}
	h := host.HTTPHandler()
	if h == nil {
		tb.Fatalf("nil host HTTP handler")
	}
	srv := httptest.NewServer(h)
	out := dogfoodHarness{
		baseURL: srv.URL,
		srv:     srv,
		cleanup: func() {
			srv.Close()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = host.Close(shutdownCtx)
		},
	}
	tb.Cleanup(out.cleanup)
	return out
}

func exampleConfig(tb testing.TB, name string) string {
	tb.Helper()
	root := refclienttest.ModuleRoot(tb)
	return bpkit.MaterializeExampleConfig(tb, filepath.Join(root, "config", "examples", name))
}

func TestDogfoodHarness_dogfoodLocalStub(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, bpkit.WriteDogfoodLocalStubConfig(t))

	cli := openairesponses.New(openairesponses.Config{
		BaseURL:           h.baseURL + "/v1",
		APIKey:            "sk-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("stub-default"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
			},
		},
	})
	var gotText strings.Builder
	for stream.Next() {
		ev := stream.Current()
		if ev.Type == "response.output_text.delta" {
			gotText.WriteString(ev.AsResponseOutputTextDelta().Delta)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("dogfood-local stub: stream error: %v", err)
	}
	if want := "[dogfood] local stub"; gotText.String() != want {
		t.Fatalf("dogfood-local stub: assistant text %q want %q", gotText.String(), want)
	}
}

func TestDogfoodHarness_toolCallRepairTruncatedArgs(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "dogfood-tool-call-repair.yaml"))

	cli := openairesponses.New(openairesponses.Config{
		BaseURL:           h.baseURL + "/v1",
		APIKey:            "sk-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("stub-default"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("weather?", responses.EasyInputMessageRoleUser),
			},
		},
		Tools: []responses.ToolUnionParam{{
			OfFunction: &responses.FunctionToolParam{
				Name:        "get_weather",
				Description: openai.String("weather lookup"),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
					"required":             []any{"location"},
					"additionalProperties": false,
				},
			},
		}},
	})
	var raw strings.Builder
	var gotArgs string
	for stream.Next() {
		ev := stream.Current()
		b, _ := json.Marshal(ev)
		raw.Write(b)
		if ev.Type == "response.function_call_arguments.done" {
			gotArgs = ev.AsResponseFunctionCallArgumentsDone().Arguments
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("tool-call repair dogfood: stream error: %v", err)
	}
	if gotArgs != `{"location":"NYC"}` {
		t.Fatalf("tool-call repair dogfood: repaired args %q want %q; stream=%s", gotArgs, `{"location":"NYC"}`, raw.String())
	}
	if !strings.Contains(raw.String(), "get_weather") {
		t.Fatalf("tool-call repair dogfood: missing tool name in stream: %s", raw.String())
	}
}

func TestDogfoodHarness_toolCallRepairOptOutTruncatedArgs(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "dogfood-tool-call-repair-optout.yaml"))

	cli := openairesponses.New(openairesponses.Config{
		BaseURL:           h.baseURL + "/v1",
		APIKey:            "sk-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("stub-default"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("weather?", responses.EasyInputMessageRoleUser),
			},
		},
		Tools: []responses.ToolUnionParam{{
			OfFunction: &responses.FunctionToolParam{
				Name:        "get_weather",
				Description: openai.String("weather lookup"),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
					"required":             []any{"location"},
					"additionalProperties": false,
				},
			},
		}},
	})
	var gotArgs string
	for stream.Next() {
		ev := stream.Current()
		if ev.Type == "response.function_call_arguments.done" {
			gotArgs = ev.AsResponseFunctionCallArgumentsDone().Arguments
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("tool-call repair opt-out dogfood: stream error: %v", err)
	}
	if gotArgs != `{"location":"NYC",` {
		t.Fatalf("opt-out must pass terminal-comma args through; got %q", gotArgs)
	}
}

func TestDogfoodHarness_toolCallRepairPendingValue(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "dogfood-tool-call-repair-pending.yaml"))

	cli := openairesponses.New(openairesponses.Config{
		BaseURL:           h.baseURL + "/v1",
		APIKey:            "sk-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("stub-default"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("weather?", responses.EasyInputMessageRoleUser),
			},
		},
		Tools: []responses.ToolUnionParam{{
			OfFunction: &responses.FunctionToolParam{
				Name:        "get_weather",
				Description: openai.String("weather lookup"),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string", "default": "NYC"},
					},
					"required":             []any{"location"},
					"additionalProperties": false,
				},
			},
		}},
	})
	var gotArgs string
	for stream.Next() {
		ev := stream.Current()
		if ev.Type == "response.function_call_arguments.done" {
			gotArgs = ev.AsResponseFunctionCallArgumentsDone().Arguments
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("pending-value dogfood: stream error: %v", err)
	}
	if gotArgs != `{"location":"NYC"}` {
		t.Fatalf("pending-value dogfood: repaired args %q want %q", gotArgs, `{"location":"NYC"}`)
	}
}

func TestDogfoodHarness_toolCallRepairTruncatedArgs_openaiLegacy(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "dogfood-tool-call-repair.yaml"))

	cli := openaichat.New(openaichat.Config{
		BaseURL:           h.baseURL + "/v1",
		APIKey:            "sk-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateChatCompletionStream(context.Background(), openai.ChatCompletionNewParams{
		Model: openai.ChatModel("stub-default"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("weather?"),
		},
		Tools: []openai.ChatCompletionToolUnionParam{
			openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        "get_weather",
				Description: openai.String("weather lookup"),
				Parameters: shared.FunctionParameters{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
					"required":             []any{"location"},
					"additionalProperties": false,
				},
			}),
		},
	})
	var gotArgs strings.Builder
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) == 0 {
			continue
		}
		for _, tc := range chunk.Choices[0].Delta.ToolCalls {
			if tc.Function.Arguments != "" {
				gotArgs.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("tool-call repair legacy dogfood: stream error: %v", err)
	}
	if gotArgs.String() != `{"location":"NYC"}` {
		t.Fatalf("legacy repaired args %q want %q", gotArgs.String(), `{"location":"NYC"}`)
	}
}

func TestDogfoodHarness_standardStack_openaiResponsesStub(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "openai-responses-stub.yaml"))

	cli := openairesponses.New(openairesponses.Config{
		BaseURL:           h.baseURL + "/v1",
		APIKey:            "sk-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
			},
		},
	})
	var gotText strings.Builder
	for stream.Next() {
		ev := stream.Current()
		if ev.Type == "response.output_text.delta" {
			gotText.WriteString(ev.AsResponseOutputTextDelta().Delta)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("openai-responses smoke: stream error: %v", err)
	}
	if want := "openai-responses smoke stub"; gotText.String() != want {
		t.Fatalf("openai-responses smoke: assistant text %q want %q", gotText.String(), want)
	}
	req, err := http.NewRequest(http.MethodGet, h.baseURL+"/v1/no-such-route", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if tid := resp.Header.Get("X-Trace-ID"); tid == "" {
		t.Fatal("openai-responses smoke: expected X-Trace-ID from standard middleware stack")
	}
}

func TestDogfoodHarness_openaiLegacyChatStub(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "openai-legacy-stub.yaml"))

	cli := openaichat.New(openaichat.Config{
		BaseURL:           h.baseURL + "/v1",
		APIKey:            "sk-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateChatCompletionStream(context.Background(), openai.ChatCompletionNewParams{
		Model: openai.ChatModel("gpt-4o-mini"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("ping"),
		},
	})
	var got strings.Builder
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			got.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("openai-legacy smoke: stream error: %v", err)
	}
	if want := "openai-legacy smoke stub"; got.String() != want {
		t.Fatalf("openai-legacy smoke: assistant text %q want %q", got.String(), want)
	}
}

func TestDogfoodHarness_anthropicStub(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "anthropic-stub.yaml"))

	cli := anthropicmessages.New(anthropicmessages.Config{
		BaseURL:           h.baseURL,
		APIKey:            "sk-ant-dogfood",
		HTTPClient:        h.srv.Client(),
		DisableSDKRetries: true,
	})
	stream := cli.CreateMessageStream(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 64,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
		},
	})
	var got strings.Builder
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				got.WriteString(ev.Delta.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("anthropic smoke: stream error: %v", err)
	}
	if want := "anthropic smoke stub"; got.String() != want {
		t.Fatalf("anthropic smoke: assistant text %q want %q", got.String(), want)
	}
}

func TestDogfoodHarness_geminiStub(t *testing.T) {
	t.Parallel()
	h := startDogfoodHarness(t, exampleConfig(t, "gemini-stub.yaml"))

	cli, err := gemini.New(context.Background(), gemini.Config{
		APIKey:     "fake-key",
		BaseURL:    h.baseURL + "/v1beta",
		HTTPClient: h.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := cli.GenerateContentStream(context.Background(), "gemini-2.0-flash", []*genai.Content{
		genai.NewContentFromText("ping", genai.RoleUser),
	}, nil)
	var got strings.Builder
	for chunk, serr := range stream {
		if serr != nil {
			t.Fatalf("gemini smoke: stream error: %v", serr)
		}
		if chunk == nil {
			continue
		}
		for _, c := range chunk.Candidates {
			if c.Content == nil {
				continue
			}
			for _, p := range c.Content.Parts {
				got.WriteString(p.Text)
			}
		}
	}
	if want := "gemini smoke stub"; got.String() != want {
		t.Fatalf("gemini smoke: assistant text %q want %q", got.String(), want)
	}
}

func TestDogfoodHarness_geminiPostOutputFailure_noFallbackToSecondStub(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	cfgPath := bpkit.MaterializeExampleConfig(t, filepath.Join(root, "internal", "stdhttp", "testdata", "dogfood_gemini_dual_stub_failover.yaml"))
	h := startDogfoodHarness(t, cfgPath)

	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`)
	u := h.baseURL + "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse"
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", "k")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "FALLBACK-WIN") {
		t.Fatalf("gemini dual-stub smoke: must not failover after output; body contains fallback text: %s", s)
	}
	if !strings.Contains(s, "partial-bad") {
		t.Fatalf("gemini dual-stub smoke: expected first stub output partial-bad in SSE body, got: %s", s)
	}
}
