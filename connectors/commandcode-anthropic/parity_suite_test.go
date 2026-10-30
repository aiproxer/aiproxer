package commandcodeanthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/anthropic"
	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestDescribe_FactoryKind(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f := d.Factories[0]
	if f.Kind != service.FactoryKind || f.RoutePrefixes[0] != service.FactoryKind || f.SupportsFinalizeBilling {
		t.Fatalf("%+v", f)
	}
}

func TestConfigure_RequiresAPIKey(t *testing.T) {
	t.Setenv("COMMANDCODE_API_KEY", "")
	req := mustCfg(t, "base_url: http://127.0.0.1:9\n")
	req.Secrets = backendplugin.SecretBundle{}
	_, err := service.New().Configure(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_StreamingMessages(t *testing.T) {
	t.Parallel()
	var headers http.Header
	var mu sync.Mutex
	srv := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		RequireAuth: true,
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			headers = h.Clone()
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)

	cl := anthropic.NewClient(srv.URL, "test-key", srv.Client())
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}},
		},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.Operation("anthropic.messages"),
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}

	es, err := cl.Open(context.Background(), call, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()

	var textBuilder strings.Builder
	var sawUsage bool
	var sawStopReason bool
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case aipapi.EventTextDelta:
			textBuilder.WriteString(ev.Delta)
		case aipapi.EventUsageDelta:
			sawUsage = true
		case aipapi.EventResponseFinished:
			if ev.FinishReason == "end_turn" {
				sawStopReason = true
			}
		}
	}

	mu.Lock()
	apiKeyHeader := headers.Get("x-api-key")
	versionHeader := headers.Get("anthropic-version")
	mu.Unlock()

	if apiKeyHeader != "test-key" {
		t.Fatalf("expected x-api-key: test-key, got: %q", apiKeyHeader)
	}
	if versionHeader != "2023-06-01" {
		t.Fatalf("expected anthropic-version: 2023-06-01, got: %q", versionHeader)
	}
	if textBuilder.String() != "Hello" {
		t.Fatalf("expected 'Hello', got: %q", textBuilder.String())
	}
	if !sawUsage {
		t.Fatal("expected usage delta")
	}
	if !sawStopReason {
		t.Fatal("expected finish reason stop")
	}
}

func TestParity_NonStreamingMessages(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		RequireAuth: true,
	}))
	t.Cleanup(srv.Close)

	cl := anthropic.NewClient(srv.URL, "test-key", srv.Client())
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}},
		},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.Operation("anthropic.messages"),
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}

	es, err := cl.Open(context.Background(), call, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()

	var textBuilder strings.Builder
	var sawUsage bool
	var sawStopReason bool
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case aipapi.EventTextDelta:
			textBuilder.WriteString(ev.Delta)
		case aipapi.EventUsageDelta:
			sawUsage = true
		case aipapi.EventResponseFinished:
			if ev.FinishReason == "end_turn" {
				sawStopReason = true
			}
		}
	}

	if textBuilder.String() != "Hello" {
		t.Fatalf("expected 'Hello', got: %q", textBuilder.String())
	}
	if !sawUsage {
		t.Fatal("expected usage delta")
	}
	if !sawStopReason {
		t.Fatal("expected finish reason stop")
	}
}

func TestParity_ToolUseStream(t *testing.T) {
	t.Parallel()
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":15}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_call_123","name":"get_stock_price","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"symbol\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"AAPL\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":25}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	srv := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		RequireAuth: true,
		MessagesSSE: sse,
	}))
	t.Cleanup(srv.Close)

	cl := anthropic.NewClient(srv.URL, "test-key", srv.Client())
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Stock price of AAPL"}}},
		},
		Tools: []aipapi.ToolDef{
			{Name: "get_stock_price", Description: "Get stock price", Parameters: []byte(`{"type":"object","properties":{"symbol":{"type":"string"}}}`)},
		},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.Operation("anthropic.messages"),
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}

	es, err := cl.Open(context.Background(), call, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()

	var sawStarted, sawFinished bool
	var args strings.Builder
	var sawToolUseFinishReason bool
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			if ev.ToolCallID == "tool_call_123" && ev.ToolName == "get_stock_price" {
				sawStarted = true
			}
		case aipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "tool_call_123" {
				args.WriteString(ev.Delta)
			}
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "tool_call_123" {
				sawFinished = true
			}
		case aipapi.EventResponseFinished:
			if ev.FinishReason == "tool_use" {
				sawToolUseFinishReason = true
			}
		}
	}

	if !sawStarted {
		t.Fatal("expected ToolCallStarted")
	}
	if args.String() != `{"symbol":"AAPL"}` {
		t.Fatalf("expected args: '{\"symbol\":\"AAPL\"}', got: %q", args.String())
	}
	if !sawFinished {
		t.Fatal("expected ToolCallFinished")
	}
	if !sawToolUseFinishReason {
		t.Fatal("expected finish_reason tool_use")
	}
}

func TestParity_ThinkingStream(t *testing.T) {
	t.Parallel()
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_think","type":"message","role":"assistant","model":"claude-3-7-sonnet-20250219","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Final answer"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	srv := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		RequireAuth: true,
		MessagesSSE: sse,
	}))
	t.Cleanup(srv.Close)

	cl := anthropic.NewClient(srv.URL, "test-key", srv.Client())
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Solve puzzle"}}},
		},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.Operation("anthropic.messages"),
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}

	es, err := cl.Open(context.Background(), call, "claude-3-7-sonnet-20250219")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()

	var thinking strings.Builder
	var signature string
	var text strings.Builder
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case aipapi.EventReasoningDelta:
			thinking.WriteString(ev.Delta)
		case aipapi.EventReasoningSignatureDelta:
			signature = ev.Signature
		case aipapi.EventTextDelta:
			text.WriteString(ev.Delta)
		}
	}

	if thinking.String() != "Let me think..." {
		t.Fatalf("expected thinking: 'Let me think...', got: %q", thinking.String())
	}
	if signature != "sig123" {
		t.Fatalf("expected signature: 'sig123', got: %q", signature)
	}
	if text.String() != "Final answer" {
		t.Fatalf("expected text: 'Final answer', got: %q", text.String())
	}
}

func TestParity_ErrorsAndStatusHandling(t *testing.T) {
	t.Parallel()

	// 400 Bad Request
	srv400 := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		ForcedStatus: 400,
		ForcedBody:   `{"type":"error","error":{"type":"invalid_request_error","message":"Model not supported"}}`,
	}))
	t.Cleanup(srv400.Close)
	cl400 := anthropic.NewClient(srv400.URL, "key", srv400.Client())
	_, err := cl400.Open(context.Background(), aipapi.Call{}, "bad-model")
	var he *anthropic.HTTPError
	if !errors.As(err, &he) || he.StatusCode != 400 || he.Type != "invalid_request_error" {
		t.Fatalf("expected HTTP 400 error, got: %T %v", err, err)
	}

	// 401 Unauthorized
	srv401 := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		ForcedStatus: 401,
		ForcedBody:   `{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`,
	}))
	t.Cleanup(srv401.Close)
	cl401 := anthropic.NewClient(srv401.URL, "bad-key", srv401.Client())
	_, err = cl401.Open(context.Background(), aipapi.Call{}, "claude-3-5-sonnet-20241022")
	if !errors.As(err, &he) || he.StatusCode != 401 || he.Type != "authentication_error" {
		t.Fatalf("expected HTTP 401 error, got: %T %v", err, err)
	}

	// 403 Forbidden
	srv403 := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		ForcedStatus: 403,
		ForcedBody:   `{"type":"error","error":{"type":"permission_error","message":"MODEL_NOT_IN_PLAN"}}`,
	}))
	t.Cleanup(srv403.Close)
	cl403 := anthropic.NewClient(srv403.URL, "key", srv403.Client())
	_, err = cl403.Open(context.Background(), aipapi.Call{}, "claude-haiku-4-5-20251001")
	if !errors.As(err, &he) || he.StatusCode != 403 || he.Type != "permission_error" {
		t.Fatalf("expected HTTP 403 error, got: %T %v", err, err)
	}

	// 429 Rate Limit
	srv429 := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		ForcedStatus: 429,
		ForcedBody:   `{"type":"error","error":{"type":"rate_limit_error","message":"Too many requests"}}`,
	}))
	t.Cleanup(srv429.Close)
	cl429 := anthropic.NewClient(srv429.URL, "key", srv429.Client())
	_, err = cl429.Open(context.Background(), aipapi.Call{}, "claude-3-5-sonnet-20241022")
	if !errors.As(err, &he) || he.StatusCode != 429 || he.Type != "rate_limit_error" {
		t.Fatalf("expected HTTP 429 error, got: %T %v", err, err)
	}
}

func TestParity_ExtraBody(t *testing.T) {
	t.Parallel()
	var body []byte
	var mu sync.Mutex
	srv := httptest.NewServer(anthropic.NewEmulator(anthropic.EmulatorConfig{
		RequireAuth: true,
		OnRequestBody: func(b []byte) {
			mu.Lock()
			body = append([]byte(nil), b...)
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)

	cl := anthropic.NewClient(srv.URL, "test-key", srv.Client())
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}},
		},
		Extensions: map[string]json.RawMessage{
			"commandcode.extra_body.custom_opt": json.RawMessage(`"custom_val"`),
			"anthropic.extra_body.anth_opt":     json.RawMessage(`123`),
		},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.Operation("anthropic.messages"),
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}

	es, err := cl.Open(context.Background(), call, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatal(err)
	}
	_ = es.Close()

	mu.Lock()
	defer mu.Unlock()
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `"custom_opt":"custom_val"`) || !strings.Contains(bodyStr, `"anth_opt":123`) {
		t.Fatalf("expected extra body in request, got: %s", bodyStr)
	}
}

func TestParity_Cancellation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Block until client disconnects
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	cl := anthropic.NewClient(srv.URL, "key", srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	es, err := cl.Open(ctx, aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:     aipapi.Operation("anthropic.messages"),
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	_, err = es.Recv(ctx)
	if err == nil {
		t.Fatal("expected context canceled error")
	}
	_ = es.Close()
}
