package openaicompat_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestResolveEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    string
		flavor  openaicompat.Flavor
		want    string
		wantErr bool
	}{
		{
			name:   "responses_standard",
			base:   "https://api.openai.com/v1",
			flavor: openaicompat.FlavorResponses,
			want:   "https://api.openai.com/v1/responses",
		},
		{
			name:   "responses_trailing_slash",
			base:   "https://api.openai.com/v1/",
			flavor: openaicompat.FlavorResponses,
			want:   "https://api.openai.com/v1/responses",
		},
		{
			name:   "chat_standard",
			base:   "https://api.openai.com/v1",
			flavor: openaicompat.FlavorChat,
			want:   "https://api.openai.com/v1/chat/completions",
		},
		{
			name:   "chat_gateway_prefix",
			base:   "https://gateway.example.com/team/v1",
			flavor: openaicompat.FlavorChat,
			want:   "https://gateway.example.com/team/v1/chat/completions",
		},
		{
			name:    "empty_base",
			base:    "",
			flavor:  openaicompat.FlavorResponses,
			wantErr: true,
		},
		{
			name:    "invalid_scheme",
			base:    "ftp://example.com",
			flavor:  openaicompat.FlavorResponses,
			wantErr: true,
		},
		{
			name:    "relative_url",
			base:    "/v1/chat",
			flavor:  openaicompat.FlavorChat,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := openaicompat.ResolveEndpoint(tt.base, tt.flavor)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveEndpoint() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("ResolveEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildOutboundHeaders(t *testing.T) {
	t.Parallel()

	t.Run("streaming_with_secret", func(t *testing.T) {
		t.Parallel()
		h := openaicompat.BuildOutboundHeaders("sk-test-secret", true, nil)
		if got := h.Get("Authorization"); got != "Bearer sk-test-secret" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer sk-test-secret")
		}
		if got := h.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}
		if got := h.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("Accept = %q, want %q", got, "text/event-stream")
		}
	})

	t.Run("non_streaming_no_secret", func(t *testing.T) {
		t.Parallel()
		h := openaicompat.BuildOutboundHeaders("", false, nil)
		if got := h.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		if got := h.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q, want %q", got, "application/json")
		}
	})

	t.Run("client_auth_leakage_probes", func(t *testing.T) {
		t.Parallel()
		extra := http.Header{
			"Authorization":       []string{"Bearer client-token-must-not-leak"},
			"Proxy-Authorization": []string{"Basic client-proxy-pass"},
			"X-Api-Key":           []string{"client-api-key"},
			"Api-Key":             []string{"azure-api-key"},
			"X-Goog-Api-Key":      []string{"gemini-api-key"},
		}
		// With backend secret: backend secret must strictly win
		h1 := openaicompat.BuildOutboundHeaders("sk-backend-secret", true, extra)
		if got := h1.Get("Authorization"); got != "Bearer sk-backend-secret" {
			t.Fatalf("Authorization = %q, want Bearer sk-backend-secret", got)
		}
		if h1.Get("Proxy-Authorization") != "" {
			t.Fatal("Proxy-Authorization must be stripped")
		}
		if h1.Get("X-Api-Key") != "" || h1.Get("Api-Key") != "" || h1.Get("X-Goog-Api-Key") != "" {
			t.Fatal("client API keys must be stripped")
		}

		// Without backend secret: authorization must be empty, not client-token
		h2 := openaicompat.BuildOutboundHeaders("", false, extra)
		if got := h2.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
	})

	t.Run("session_and_control_leakage_probes", func(t *testing.T) {
		t.Parallel()
		extra := http.Header{
			"X-Aip-Session-Id":         []string{"sess-1"},
			"X-Aip-Resume-Token":       []string{"res-1"},
			"X-Aip-Route":              []string{"route-1"},
			"X-Aip-A-Leg-Id":           []string{"aleg-1"},
			"X-Aip-Session-Hint":       []string{"hint-1"},
			"X-Aip-Diagnostics-Secret": []string{"diag-secret"},
			"X-Trace-Id":               []string{"trace-1"},
			"X-Session-Id":             []string{"s2"},
			"X-Resume-Token":           []string{"r2"},
			"X-Aleg-Id":                []string{"al2"},
			"X-Bleg-Id":                []string{"bl2"},
			"x-session-custom":         []string{"c1"},
			"x-resume-custom":          []string{"c2"},
			"x-aip-custom":             []string{"c3"},
			"x-aleg-custom":            []string{"c4"},
			"x-bleg-custom":            []string{"c5"},
		}
		h := openaicompat.BuildOutboundHeaders("sk-test", true, extra)
		for k := range extra {
			if h.Get(k) != "" {
				t.Fatalf("control/session header %q was not stripped (got %q)", k, h.Get(k))
			}
		}
	})

	t.Run("hop_by_hop_and_connection_tokens", func(t *testing.T) {
		t.Parallel()
		extra := http.Header{
			"Connection":         []string{"close, X-Custom-Hop, Keep-Alive"},
			"X-Custom-Hop":       []string{"should-be-stripped-by-connection-token"},
			"Keep-Alive":         []string{"timeout=5"},
			"Proxy-Authenticate": []string{"Basic"},
			"Te":                 []string{"trailers"},
			"Trailers":           []string{"X-Checksum"},
			"Trailer":            []string{"X-Checksum"},
			"Transfer-Encoding":  []string{"chunked"},
			"Upgrade":            []string{"websocket"},
		}
		h := openaicompat.BuildOutboundHeaders("sk-test", true, extra)
		for k := range extra {
			if h.Get(k) != "" {
				t.Fatalf("hop-by-hop header %q was not stripped (got %q)", k, h.Get(k))
			}
		}
	})

	t.Run("transport_framing_and_content_protection", func(t *testing.T) {
		t.Parallel()
		extra := http.Header{
			"Content-Length":   []string{"99999"},
			"Content-Encoding": []string{"gzip"},
			"Expect":           []string{"100-continue"},
			"Host":             []string{"evil-host.com"},
			"Content-Type":     []string{"text/plain", "image/png"},
			"Accept":           []string{"text/html"},
		}
		h := openaicompat.BuildOutboundHeaders("sk-test", true, extra)
		if h.Get("Content-Length") != "" {
			t.Fatalf("Content-Length header must be stripped from header map")
		}
		if h.Get("Content-Encoding") != "" {
			t.Fatalf("Content-Encoding header must be stripped")
		}
		if h.Get("Expect") != "" {
			t.Fatalf("Expect header must be stripped")
		}
		if h.Get("Host") != "" {
			t.Fatalf("Host header must be stripped")
		}
		if got := h.Values("Content-Type"); len(got) != 1 || got[0] != "application/json" {
			t.Fatalf("Content-Type = %v, want exactly [application/json]", got)
		}
		if got := h.Values("Accept"); len(got) != 1 || got[0] != "text/event-stream" {
			t.Fatalf("Accept = %v, want exactly [text/event-stream]", got)
		}
	})

	t.Run("allowed_headers_pass_through", func(t *testing.T) {
		t.Parallel()
		extra := http.Header{
			"X-Request-Id":     []string{"req-12345"},
			"X-Custom-Allowed": []string{"val-xyz"},
			"User-Agent":       []string{"custom-agent"},
		}
		h := openaicompat.BuildOutboundHeaders("sk-key", false, extra)
		if got := h.Get("X-Request-Id"); got != "req-12345" {
			t.Fatalf("X-Request-Id = %q, want req-12345", got)
		}
		if got := h.Get("X-Custom-Allowed"); got != "val-xyz" {
			t.Fatalf("X-Custom-Allowed = %q, want val-xyz", got)
		}
		if got := h.Get("User-Agent"); got != "custom-agent" {
			t.Fatalf("User-Agent = %q, want custom-agent", got)
		}
	})
}

func TestResolveHTTPClient(t *testing.T) {
	t.Parallel()

	custom := &http.Client{Timeout: 5 * time.Second}
	if got := openaicompat.ResolveHTTPClient(custom); got != custom {
		t.Fatalf("expected custom client returned directly")
	}

	gotDefault := openaicompat.ResolveHTTPClient(nil)
	if gotDefault == nil {
		t.Fatal("expected non-nil default client")
	}
	tr, ok := gotDefault.Transport.(*http.Transport)
	if !ok || !tr.ForceAttemptHTTP2 {
		t.Fatalf("expected Standard transport with ForceAttemptHTTP2=true")
	}
}

func TestPeekFirstEvent(t *testing.T) {
	t.Parallel()

	t.Run("prepends_event_successfully", func(t *testing.T) {
		t.Parallel()
		events := []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
			{Kind: aipapi.EventTextDelta, Delta: " world"},
		}
		base := aipapi.NewFixedEventStream(events)
		peeked, err := openaicompat.PeekFirstEvent(context.Background(), base)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if peeked == nil {
			t.Fatal("expected non-nil peeked stream")
		}

		ev1, err := peeked.Recv(context.Background())
		if err != nil || ev1.Delta != "hello" {
			t.Fatalf("ev1 = %v, err = %v, want hello", ev1, err)
		}
		ev2, err := peeked.Recv(context.Background())
		if err != nil || ev2.Delta != " world" {
			t.Fatalf("ev2 = %v, err = %v, want world", ev2, err)
		}
		_, err = peeked.Recv(context.Background())
		if err != io.EOF {
			t.Fatalf("expected EOF, got %v", err)
		}
	})

	t.Run("empty_stream_returns_eof", func(t *testing.T) {
		t.Parallel()
		base := aipapi.NewFixedEventStream(nil)
		_, err := openaicompat.PeekFirstEvent(context.Background(), base)
		if err != io.EOF {
			t.Fatalf("expected io.EOF on empty stream, got %v", err)
		}
	})
}

func TestParseStreamResponse_HTTPErrorClassification(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rec.Header().Set("Retry-After", "90")
	rec.WriteHeader(http.StatusTooManyRequests)
	_, _ = rec.WriteString(`{"error":{"message":"rate limit reached"}}`)
	resp := rec.Result()

	_, err := openaicompat.ParseStreamResponse("test-provider", resp, openaicompat.FlavorResponses, 100)
	if err == nil {
		t.Fatal("expected error for 429 status")
	}
	kind, ra := openaicred.ClassifyOpenAIAPIError(err)
	if kind != openaicred.FailureRateLimited || ra != "90" {
		t.Fatalf("kind = %v, retryAfter = %q, want FailureRateLimited, 90", kind, ra)
	}
}

func TestParseStreamResponse_ResponsesSSE(t *testing.T) {
	t.Parallel()

	ssePayload := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_123","status":"in_progress"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hello from sse"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_123","status":"completed"}}`,
		``,
		``,
	}, "\n")

	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	rec.Header().Set("Content-Type", "text/event-stream")
	_, _ = rec.WriteString(ssePayload)
	resp := rec.Result()

	stream, err := openaicompat.ParseStreamResponse("test-provider", resp, openaicompat.FlavorResponses, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream")
	}

	peeked, err := openaicompat.PeekFirstEvent(context.Background(), stream)
	if err != nil {
		t.Fatalf("PeekFirstEvent error: %v", err)
	}

	var deltas []string
	for {
		ev, err := peeked.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected recv error: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if len(deltas) == 0 || deltas[0] != "hello from sse" {
		t.Fatalf("unexpected deltas: %v", deltas)
	}
}

func TestWireOpenPrimitives_Integration(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-wire-test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"wire response\"}\n\n")
	}))
	defer server.Close()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "sk-wire-test"},
	})
	if err != nil {
		t.Fatal(err)
	}

	prims := openaicompat.WireOpenPrimitives{
		ProviderID:        "test-provider",
		BaseURL:           server.URL + "/v1",
		Flavor:            openaicompat.FlavorResponses,
		Pool:              pool,
		HTTPClient:        httpclient.Standard(),
		RateLimitFallback: time.Minute,
		MaxPending:        50,
	}

	targetURL, err := prims.ResolveURL()
	if err != nil {
		t.Fatalf("ResolveURL failed: %v", err)
	}
	if targetURL != server.URL+"/v1/responses" {
		t.Fatalf("targetURL = %q, want %q", targetURL, server.URL+"/v1/responses")
	}

	stream, err := prims.Execute(context.Background(), func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(`{}`))
		if err != nil {
			return nil, err
		}
		req.Header = prims.BuildHeaders(cred.Secret, true)
		resp, err := prims.Client().Do(req)
		if err != nil {
			return nil, err
		}
		return prims.ParseAndPeekStream(ctx, resp)
	})
	if err != nil {
		t.Fatalf("prims.Execute failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var textDelta string
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			textDelta = ev.Delta
			break
		}
	}
	if textDelta != "wire response" {
		t.Fatalf("textDelta = %q, want wire response", textDelta)
	}
}

func TestNewOutboundRequest_Framing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	targetURL := "https://api.openai.com/v1/responses"

	t.Run("exact_rewritten_length_known", func(t *testing.T) {
		t.Parallel()
		payload := `{"model":"gpt-4o","input":"hello"}`
		body := strings.NewReader(payload)
		extra := http.Header{
			"Content-Length":    []string{"999999"},
			"Transfer-Encoding": []string{"chunked"},
			"Expect":            []string{"100-continue"},
			"Host":              []string{"evil.com"},
			"Trailer":           []string{"X-Checksum"},
		}

		req, err := openaicompat.NewOutboundRequest(ctx, targetURL, body, int64(len(payload)), "sk-test", true, extra)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.ContentLength != int64(len(payload)) {
			t.Fatalf("req.ContentLength = %d, want %d", req.ContentLength, len(payload))
		}
		if req.Header.Get("Content-Length") != "" {
			t.Fatalf("req.Header must not contain Content-Length, got %q", req.Header.Get("Content-Length"))
		}
		if req.Header.Get("Transfer-Encoding") != "" {
			t.Fatalf("req.Header must not contain Transfer-Encoding, got %q", req.Header.Get("Transfer-Encoding"))
		}
		if req.Header.Get("Expect") != "" {
			t.Fatalf("req.Header must not contain Expect, got %q", req.Header.Get("Expect"))
		}
		if req.Trailer != nil {
			t.Fatalf("req.Trailer must be nil, got %v", req.Trailer)
		}
		if req.Close {
			t.Fatalf("req.Close must be false for connection reuse")
		}
		if req.Host != "" {
			t.Fatalf("req.Host must be empty so client transport derives from URL, got %q", req.Host)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q, want Bearer sk-test", got)
		}
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		if got := req.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", got)
		}
	})

	t.Run("exact_rewritten_length_with_splice", func(t *testing.T) {
		t.Parallel()
		raw := `{"model":"m1","messages":[]}`
		span := largebody.Span{Offset: 9, Length: 4} // "m1"
		splice, err := largebody.SpliceModelToken(raw, span, "longer-model-name")
		if err != nil {
			t.Fatalf("SpliceModelToken failed: %v", err)
		}
		rewrittenLen := splice.RewrittenLength()
		if rewrittenLen == int64(len(raw)) {
			t.Fatal("expected rewritten length to differ from raw length")
		}

		req, err := openaicompat.NewOutboundRequest(ctx, targetURL, splice, rewrittenLen, "sk-test", false, nil)
		if err != nil {
			t.Fatalf("NewOutboundRequest failed: %v", err)
		}
		if req.ContentLength != rewrittenLen {
			t.Fatalf("req.ContentLength = %d, want rewrittenLen %d", req.ContentLength, rewrittenLen)
		}
		if req.Header.Get("Accept") != "application/json" {
			t.Fatalf("non-streaming Accept = %q, want application/json", req.Header.Get("Accept"))
		}
	})

	t.Run("streaming_unknown_length", func(t *testing.T) {
		t.Parallel()
		body := strings.NewReader(`{}`)
		req, err := openaicompat.NewOutboundRequest(ctx, targetURL, body, -1, "sk-test", true, nil)
		if err != nil {
			t.Fatalf("NewOutboundRequest failed: %v", err)
		}
		if req.ContentLength != -1 {
			t.Fatalf("streaming req.ContentLength = %d, want -1", req.ContentLength)
		}
		if req.Header.Get("Content-Length") != "" {
			t.Fatalf("Content-Length header must be empty, got %q", req.Header.Get("Content-Length"))
		}
	})

	t.Run("nil_context_returns_error", func(t *testing.T) {
		t.Parallel()
		_, err := openaicompat.NewOutboundRequest(nil, targetURL, nil, 0, "", false, nil) //nolint:staticcheck // deliberately exercises nil-context rejection
		if !errors.Is(err, aipapi.ErrNilContext) {
			t.Fatalf("expected ErrNilContext, got %v", err)
		}
	})

	t.Run("prims_new_request", func(t *testing.T) {
		t.Parallel()
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "test-p",
			BaseURL:    "https://api.openai.com/v1",
			Flavor:     openaicompat.FlavorChat,
		}
		req, err := prims.NewRequest(ctx, strings.NewReader(`{}`), 2, "sk-k", true, nil)
		if err != nil {
			t.Fatalf("prims.NewRequest failed: %v", err)
		}
		wantURL := "https://api.openai.com/v1/chat/completions"
		if req.URL.String() != wantURL {
			t.Fatalf("req.URL = %q, want %q", req.URL.String(), wantURL)
		}
		if req.ContentLength != 2 {
			t.Fatalf("ContentLength = %d, want 2", req.ContentLength)
		}
	})
}

func TestWireOpen_TransportConformance(t *testing.T) {
	t.Parallel()

	ssePayload := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"r-conf","status":"in_progress"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"conformance-ok"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"r-conf","status":"completed"}}`,
		``,
		``,
	}, "\n")

	t.Run("HTTP1_SuccessAndFraming", func(t *testing.T) {
		t.Parallel()

		var receivedProto string
		var receivedAuth string
		var receivedLen int64
		var receivedTrailers int
		var receivedHost string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedProto = r.Proto
			receivedAuth = r.Header.Get("Authorization")
			receivedLen = r.ContentLength
			receivedTrailers = len(r.Trailer)
			receivedHost = r.Host

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ssePayload)
		}))
		defer srv.Close()

		pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-h1-test"}})
		if err != nil {
			t.Fatal(err)
		}
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "h1-provider",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			Pool:       pool,
			HTTPClient: httpclient.Standard(),
			MaxPending: 50,
		}

		payload := `{"model":"gpt-4o","input":"test"}`
		stream, err := prims.Execute(context.Background(), func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			req, err := prims.NewRequest(ctx, strings.NewReader(payload), int64(len(payload)), cred.Secret, true, nil)
			if err != nil {
				return nil, err
			}
			resp, err := prims.Client().Do(req)
			if err != nil {
				return nil, err
			}
			return prims.ParseAndPeekStream(ctx, resp)
		})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		defer func() { _ = stream.Close() }()

		if receivedProto != "HTTP/1.1" {
			t.Fatalf("receivedProto = %q, want HTTP/1.1", receivedProto)
		}
		if receivedAuth != "Bearer sk-h1-test" {
			t.Fatalf("receivedAuth = %q, want Bearer sk-h1-test", receivedAuth)
		}
		if receivedLen != int64(len(payload)) {
			t.Fatalf("receivedLen = %d, want %d", receivedLen, len(payload))
		}
		if receivedTrailers != 0 {
			t.Fatalf("receivedTrailers = %d, want 0", receivedTrailers)
		}
		if receivedHost == "" {
			t.Fatal("expected non-empty received host")
		}

		var delta string
		for {
			ev, err := stream.Recv(context.Background())
			if err != nil {
				break
			}
			if ev.Kind == aipapi.EventTextDelta {
				delta = ev.Delta
				break
			}
		}
		if delta != "conformance-ok" {
			t.Fatalf("delta = %q, want conformance-ok", delta)
		}
	})

	t.Run("HTTP2_SuccessAndFraming", func(t *testing.T) {
		t.Parallel()

		var receivedProto string
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedProto = r.Proto
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ssePayload)
		}))
		srv.EnableHTTP2 = true
		srv.StartTLS()
		defer srv.Close()

		tr := httpclient.DefaultTransport()
		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"h2", "http/1.1"},
		}
		cli := &http.Client{Transport: tr, Timeout: 10 * time.Second}

		pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-h2-test"}})
		if err != nil {
			t.Fatal(err)
		}
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "h2-provider",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			Pool:       pool,
			HTTPClient: cli,
			MaxPending: 50,
		}

		payload := `{"test":"h2"}`
		stream, err := prims.Execute(context.Background(), func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			req, err := prims.NewRequest(ctx, strings.NewReader(payload), int64(len(payload)), cred.Secret, true, nil)
			if err != nil {
				return nil, err
			}
			resp, err := prims.Client().Do(req)
			if err != nil {
				return nil, err
			}
			return prims.ParseAndPeekStream(ctx, resp)
		})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		defer func() { _ = stream.Close() }()

		if receivedProto != "HTTP/2.0" {
			t.Fatalf("receivedProto = %q, want HTTP/2.0", receivedProto)
		}
		var h2Delta string
		for {
			ev, err := stream.Recv(context.Background())
			if err != nil {
				break
			}
			if ev.Kind == aipapi.EventTextDelta {
				h2Delta = ev.Delta
				break
			}
		}
		if h2Delta != "conformance-ok" {
			t.Fatalf("h2Delta = %q, want conformance-ok", h2Delta)
		}
	})

	t.Run("Cancel_DuringStream", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\"}}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			// Block until request context is canceled or timeout
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "cancel-p",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			HTTPClient: httpclient.Standard(),
			MaxPending: 10,
		}

		stream, err := prims.Execute(ctx, func(attemptCtx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			req, err := prims.NewRequest(attemptCtx, strings.NewReader(`{}`), 2, "", true, nil)
			if err != nil {
				return nil, err
			}
			resp, err := prims.Client().Do(req)
			if err != nil {
				return nil, err
			}
			return prims.ParseAndPeekStream(attemptCtx, resp)
		})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		defer func() { _ = stream.Close() }()

		// Read first peeked event successfully
		ev, err := stream.Recv(ctx)
		if err != nil || ev.Kind != aipapi.EventResponseStarted {
			t.Fatalf("ev = %v, err = %v, want EventResponseStarted", ev, err)
		}

		// Cancel client context now
		cancel()

		_, err = stream.Recv(ctx)
		if err == nil {
			t.Fatal("expected error after context cancellation")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("expected canceled error, got %v", err)
		}
	})

	t.Run("Connection_Reuse", func(t *testing.T) {
		t.Parallel()

		var remoteAddrs []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			remoteAddrs = append(remoteAddrs, r.RemoteAddr)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ssePayload)
		}))
		defer srv.Close()

		cli := httpclient.Standard()
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "reuse-p",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			HTTPClient: cli,
			MaxPending: 10,
		}

		for i := range 2 {
			stream, err := prims.Execute(context.Background(), func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
				req, err := prims.NewRequest(ctx, strings.NewReader(`{}`), 2, "sk-test", true, nil)
				if err != nil {
					return nil, err
				}
				resp, err := prims.Client().Do(req)
				if err != nil {
					return nil, err
				}
				return prims.ParseAndPeekStream(ctx, resp)
			})
			if err != nil {
				t.Fatalf("attempt %d failed: %v", i, err)
			}
			// Drain stream
			for {
				_, err := stream.Recv(context.Background())
				if err != nil {
					break
				}
			}
			_ = stream.Close()
		}

		if len(remoteAddrs) != 2 {
			t.Fatalf("expected 2 requests, got %d", len(remoteAddrs))
		}
		if remoteAddrs[0] != remoteAddrs[1] {
			t.Fatalf("expected same TCP connection reused, got %q and %q", remoteAddrs[0], remoteAddrs[1])
		}
	})

	t.Run("Redirect_Policy", func(t *testing.T) {
		t.Parallel()

		var redirectSeen atomic.Bool
		var finalSeen atomic.Bool

		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/responses/redirect" {
				redirectSeen.Store(true)
				http.Redirect(w, r, srv.URL+"/v1/responses", http.StatusTemporaryRedirect) // 307 preserves POST
				return
			}
			if r.URL.Path == "/v1/responses" {
				finalSeen.Store(true)
				// Note: unrestricted custom headers (e.g. X-Client-Secret-Key)
				// are forwarded by Go's client on same-host 307 redirects by
				// design; auth-class headers are stripped cross-origin. No
				// leakage assertion here; see Leakage_Probes_Integration.
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, ssePayload)
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "redirect-p",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			HTTPClient: httpclient.Standard(),
			MaxPending: 10,
		}

		extra := http.Header{
			"X-Client-Secret-Key": []string{"secret"},
			"X-Custom-Allowed":    []string{"keep"},
		}
		targetURL := srv.URL + "/v1/responses/redirect"
		req, err := openaicompat.NewOutboundRequest(context.Background(), targetURL, strings.NewReader(`{}`), 2, "sk-test", true, extra)
		if err != nil {
			t.Fatalf("NewOutboundRequest failed: %v", err)
		}
		resp, err := prims.Client().Do(req)
		if err != nil {
			t.Fatalf("Client.Do failed: %v", err)
		}
		stream, err := prims.ParseAndPeekStream(context.Background(), resp)
		if err != nil {
			t.Fatalf("ParseAndPeekStream failed: %v", err)
		}
		defer func() { _ = stream.Close() }()

		if !redirectSeen.Load() {
			t.Fatal("redirect endpoint was not visited")
		}
		if !finalSeen.Load() {
			t.Fatal("final endpoint was not visited")
		}
	})

	t.Run("Leakage_Probes_Integration", func(t *testing.T) {
		t.Parallel()

		var receivedHeaders http.Header
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ssePayload)
		}))
		defer srv.Close()

		extra := http.Header{
			"Authorization":            []string{"Bearer client-must-not-leak"},
			"Proxy-Authorization":      []string{"Basic leaked"},
			"X-Api-Key":                []string{"client-key"},
			"Api-Key":                  []string{"azure-key"},
			"X-Goog-Api-Key":           []string{"gemini-key"},
			"X-Aip-Session-Id":         []string{"sess-123"},
			"X-Aip-Resume-Token":       []string{"tok-abc"},
			"X-Aip-Route":              []string{"route-xyz"},
			"X-Aip-A-Leg-Id":           []string{"aleg-999"},
			"X-Aip-Session-Hint":       []string{"hint-777"},
			"X-Aip-Diagnostics-Secret": []string{"diag-secret"},
			"X-Trace-Id":               []string{"trace-001"},
			"X-Session-Id":             []string{"s-123"},
			"X-Resume-Token":           []string{"r-123"},
			"X-Aleg-Id":                []string{"a-123"},
			"X-Bleg-Id":                []string{"b-123"},
			"Content-Length":           []string{"999999"},
			"Transfer-Encoding":        []string{"chunked"},
			"Content-Encoding":         []string{"gzip"},
			"Expect":                   []string{"100-continue"},
			"Host":                     []string{"spoofed-host.com"},
			"Connection":               []string{"close, X-Custom-Hop"},
			"X-Custom-Hop":             []string{"strip-me"},
			"X-Custom-Allowed":         []string{"pass-through"},
		}

		pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-secret"}})
		if err != nil {
			t.Fatal(err)
		}
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "leak-p",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			Pool:       pool,
			HTTPClient: httpclient.Standard(),
			MaxPending: 10,
		}

		payload := `{"test":"leak"}`
		stream, err := prims.Execute(context.Background(), func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			req, err := prims.NewRequest(ctx, strings.NewReader(payload), int64(len(payload)), cred.Secret, true, extra)
			if err != nil {
				return nil, err
			}
			resp, err := prims.Client().Do(req)
			if err != nil {
				return nil, err
			}
			return prims.ParseAndPeekStream(ctx, resp)
		})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		defer func() { _ = stream.Close() }()

		// Verify backend-owned Authorization strictly won
		if got := receivedHeaders.Get("Authorization"); got != "Bearer sk-backend-secret" {
			t.Fatalf("Authorization = %q, want Bearer sk-backend-secret", got)
		}

		forbiddenKeys := []string{
			"Proxy-Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key",
			"X-Aip-Session-Id", "X-Aip-Resume-Token", "X-Aip-Route", "X-Aip-A-Leg-Id",
			"X-Aip-Session-Hint", "X-Aip-Diagnostics-Secret", "X-Trace-Id",
			"X-Session-Id", "X-Resume-Token", "X-Aleg-Id", "X-Bleg-Id",
			"Content-Encoding", "Expect", "X-Custom-Hop",
		}
		for _, k := range forbiddenKeys {
			if receivedHeaders.Get(k) != "" {
				t.Fatalf("forbidden header %q was received by upstream (got %q)", k, receivedHeaders.Get(k))
			}
		}

		if got := receivedHeaders.Get("X-Custom-Allowed"); got != "pass-through" {
			t.Fatalf("X-Custom-Allowed = %q, want pass-through", got)
		}
	})
}
