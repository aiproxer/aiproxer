package openailegacy_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	refcli "github.com/aiproxer/aiproxer/internal/refclient/openaichat"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

func TestIntegration_refclientNonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "integration-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("ping"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Choices) == 0 || res.Choices[0].Message.Content != "integration-ok" {
		t.Fatalf("choices: %+v", res.Choices)
	}
}

func TestIntegration_refclientStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "stream-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	stream := cli.CreateChatCompletionStream(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hi"),
		},
	})
	var got strings.Builder
	for stream.Next() {
		ch := stream.Current()
		for _, c := range ch.Choices {
			if c.Delta.Content != "" {
				got.WriteString(c.Delta.Content)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if got.String() != "stream-ok" {
		t.Fatalf("delta content: got %q", got.String())
	}
}

func TestIntegration_refclientMultimodalCanonicalParts(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	caps := aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityVision,
		aipapi.CapabilityDocuments,
	)
	ex := testkit.NewStubExecutor(t, caps, "seen", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	imgURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	parts := []openai.ChatCompletionContentPartUnionParam{
		openai.TextContentPart("summarize"),
		openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: imgURL}),
		openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
			FileData: openai.String(pdfB64),
			Filename: openai.String("minimal.pdf"),
		}),
	}

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	_, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(parts),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	partsOut := call.Messages[0].Parts
	if len(partsOut) < 3 {
		t.Fatalf("parts: %+v", partsOut)
	}
	if partsOut[1].Kind != aipapi.PartImageRef {
		t.Fatalf("want image part, got %+v", partsOut[1])
	}
	if partsOut[2].Kind != aipapi.PartFileRef || partsOut[2].FileMIME != "application/pdf" {
		t.Fatalf("want pdf file part, got %+v", partsOut[2])
	}
}

func TestIntegration_invalidPath_returns404(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/chat/other", "application/json", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestIntegration_methodNotAllowed(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestIntegration_malformedJSON_returns400(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
}

func TestIntegration_capabilityReject_returns400(t *testing.T) {
	t.Parallel()
	// Stub has vision+documents but negotiation still needs streaming; omit vision to force reject on multimodal user parts.
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "nope", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	imgURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	_, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
				openai.TextContentPart("x"),
				openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: imgURL}),
			}),
		},
	})
	if err == nil {
		t.Fatal("expected error from capability reject")
	}
}

func TestIntegration_toolStubRoundTrip_streaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	stream := cli.CreateChatCompletionStream(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("use the tool"),
		},
		Tools: []openai.ChatCompletionToolUnionParam{
			openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        "plan_fn",
				Description: openai.String("d"),
				Parameters:  shared.FunctionParameters{"type": "object", "properties": map[string]any{}},
			}),
		},
	})
	var toolDeltas []openai.ChatCompletionChunkChoiceDeltaToolCall
	var finishReason string
	for stream.Next() {
		ch := stream.Current()
		for _, c := range ch.Choices {
			toolDeltas = append(toolDeltas, c.Delta.ToolCalls...)
			if c.FinishReason != "" {
				finishReason = string(c.FinishReason)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason: %q", finishReason)
	}
	if len(toolDeltas) == 0 {
		t.Fatal("expected tool call deltas")
	}
	if toolDeltas[0].Function.Name != "plan_fn" {
		t.Fatalf("tool name: %q", toolDeltas[0].Function.Name)
	}
	var argsAcc strings.Builder
	for _, td := range toolDeltas {
		argsAcc.WriteString(td.Function.Arguments)
	}
	acc := argsAcc.String()
	if !strings.Contains(acc, `"q"`) {
		t.Fatalf("accumulated tool args: %q", acc)
	}
}

func TestIntegration_toolStubRoundTrip_nonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"plan_fn","description":"d","parameters":{"type":"object","properties":{}}}}]}`
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	var v struct {
		Choices []struct {
			Message *struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Choices) != 1 || v.Choices[0].Message == nil {
		t.Fatalf("choices: %+v", v)
	}
	tc := v.Choices[0].Message.ToolCalls
	if len(tc) != 1 || tc[0].ID != "call_stub1" || tc[0].Type != "function" || tc[0].Function.Name != "plan_fn" {
		t.Fatalf("tool_calls: %+v", tc)
	}
	if !strings.Contains(tc[0].Function.Arguments, `"q"`) || v.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("args/finish: %+v / %q", tc[0].Function.Arguments, v.Choices[0].FinishReason)
	}
	if !strings.Contains(v.Choices[0].Message.Content, "tail") {
		t.Fatalf("expected assistant text in message.content, got %q", v.Choices[0].Message.Content)
	}
}

func TestIntegration_routeHeaderOverridesDefault(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:default-route", RoutePrefixes: routeselect.NewPrefixSet([]string{"stub"})}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(front.HeaderRouteSelector, "stub:route-from-header")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if call.Route.Selector != "stub:route-from-header" {
		t.Fatalf("route selector %q", call.Route.Selector)
	}
}

func TestIntegration_modelRouteSelectorUsedWhenHeaderAbsent(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:default-route", RoutePrefixes: routeselect.NewPrefixSet([]string{"stub"})}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"stub:model-from-body","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if call.Route.Selector != "stub:model-from-body" {
		t.Fatalf("route selector %q", call.Route.Selector)
	}
}

func TestIntegration_taggedModelWithoutKnownRoutePrefixUsesDefault(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:llama3:latest"}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"llama3:latest","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if call.Route.Selector != "stub:llama3:latest" {
		t.Fatalf("route selector %q", call.Route.Selector)
	}
}
