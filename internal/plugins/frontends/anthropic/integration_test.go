package anthropic_test

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

	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	refcli "github.com/aiproxer/aiproxer/internal/refclient/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/anthropics/anthropic-sdk-go"
)

func TestIntegration_refclientNonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "integration-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	msg, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 64,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Content) == 0 {
		t.Fatalf("content: %+v", msg)
	}
	txt := msg.Content[0].AsText()
	if !strings.Contains(txt.Text, "integration-ok") {
		t.Fatalf("text: %+v", txt)
	}
}

func TestIntegration_refclientStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "stream-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	stream := cli.CreateMessageStream(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 16,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		},
	})
	var sawText bool
	for stream.Next() {
		ev := stream.Current()
		if ev.Type != "content_block_delta" {
			continue
		}
		cb := ev.AsContentBlockDelta()
		td := cb.Delta.AsTextDelta()
		if strings.Contains(td.Text, "stream-ok") {
			sawText = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawText {
		t.Fatal("expected content_block_delta with stream-ok")
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
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")

	img := anthropic.NewImageBlock(anthropic.Base64ImageSourceParam{
		Data:      base64.StdEncoding.EncodeToString(png),
		MediaType: anthropic.Base64ImageSourceMediaTypeImagePNG,
	})
	doc := anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
		Data: base64.StdEncoding.EncodeToString(pdf),
	})

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	_, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 128,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("describe attachments"), img, doc),
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
	parts := call.Messages[0].Parts
	if len(parts) < 3 {
		t.Fatalf("parts: %+v", parts)
	}
	if parts[1].Kind != aipapi.PartImageRef {
		t.Fatalf("want image part, got %+v", parts[1])
	}
	if parts[2].Kind != aipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("want pdf file part, got %+v", parts[2])
	}
}

func TestIntegration_invalidPath_returns404(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/other", "application/json", strings.NewReader(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"x"}]}`))
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
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/messages", nil)
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
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{`))
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
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "nope", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	img := anthropic.NewImageBlock(anthropic.Base64ImageSourceParam{
		Data:      base64.StdEncoding.EncodeToString(png),
		MediaType: anthropic.Base64ImageSourceMediaTypeImagePNG,
	})
	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	_, err := cli.CreateMessage(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 64,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("x"), img),
		},
	})
	if err == nil {
		t.Fatal("expected error from capability reject")
	}
}

func TestIntegration_toolStubRoundTrip_streaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL, APIKey: testkit.SyntheticAnthropicAPIKey})
	stream := cli.CreateMessageStream(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 128,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("use the tool")),
		},
		Tools: []anthropic.ToolUnionParam{{
			OfTool: &anthropic.ToolParam{
				Name:        "stub_fn",
				InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
			},
		}},
	})
	var toolUseStarted, toolUseDelta, toolUseStopped bool
	var stopReason string
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "content_block_start":
			cb := ev.AsContentBlockStart()
			if cb.ContentBlock.Type == "tool_use" {
				toolUseStarted = true
				if cb.ContentBlock.Name != "stub_fn" {
					t.Fatalf("tool name: %q", cb.ContentBlock.Name)
				}
				if cb.ContentBlock.ID != "call_stub1" {
					t.Fatalf("tool id: %q", cb.ContentBlock.ID)
				}
			}
		case "content_block_delta":
			cb := ev.AsContentBlockDelta()
			if cb.Delta.Type == "input_json_delta" {
				toolUseDelta = true
			}
		case "content_block_stop":
			toolUseStopped = true
		case "message_delta":
			md := ev.AsMessageDelta()
			stopReason = string(md.Delta.StopReason)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !toolUseStarted {
		t.Fatal("expected tool_use content_block_start")
	}
	if !toolUseDelta {
		t.Fatal("expected input_json_delta")
	}
	if !toolUseStopped {
		t.Fatal("expected content_block_stop for tool block")
	}
	if stopReason != "tool_use" {
		t.Fatalf("stop_reason: %q", stopReason)
	}
}

func TestIntegration_toolStubRoundTrip_nonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := `{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "stream": false,
  "messages": [{"role":"user","content":"hi"}],
  "tools": [{"name": "stub_fn", "input_schema": {"type": "object", "properties": {}}}]
}`
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	var v struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			ID    string          `json:"id"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Content) < 2 {
		t.Fatalf("content: %+v", v.Content)
	}
	var sawTool bool
	for _, c := range v.Content {
		if c.Type == "tool_use" && c.Name == "stub_fn" && c.ID == "call_stub1" {
			sawTool = true
			if !strings.Contains(string(c.Input), `"q"`) {
				t.Fatalf("input %s", string(c.Input))
			}
		}
	}
	if !sawTool {
		t.Fatalf("missing tool_use: %+v", v.Content)
	}
	if v.StopReason != "tool_use" {
		t.Fatalf("stop_reason %q", v.StopReason)
	}
}

func TestIntegration_routeHeaderOverridesDefault(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:default-route", RoutePrefixes: routeselect.NewPrefixSet([]string{"stub"})}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"x"}]}`))
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
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/messages",
		strings.NewReader(`{"model":"stub:model-from-body","max_tokens":64,"messages":[{"role":"user","content":"x"}]}`))
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

func TestIntegration_anthropicVersionHeader_accepted(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(front.HeaderAnthropicVersion, "2023-06-01")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	// The handler must accept any anthropic-version header value without rejecting.
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
}

func TestIntegration_methodNotAllowed_returns405(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:claude-3-5-haiku-20241022"}
	mux := http.NewServeMux()
	mux.Handle("/v1/messages", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Get(srv.URL + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", res.StatusCode)
	}
}
