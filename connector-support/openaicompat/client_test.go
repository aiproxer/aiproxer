package openaicompat_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestClient_RequiresCallerOwnedHTTPClient(t *testing.T) {
	t.Parallel()
	c := &openaicompat.Client{BaseURL: "http://127.0.0.1:9"}
	_, err := c.Open(context.Background(), sampleCall(), "m", openaicompat.FlavorChat)
	if err == nil || !strings.Contains(err.Error(), "HTTPClient") {
		t.Fatalf("err=%v", err)
	}
}

func TestClient_ChatStreamCaptureAndCanonicalEvents(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var body []byte
	var auth string
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true,
		OnRequestBody: func(b []byte) {
			mu.Lock()
			body = append([]byte(nil), b...)
			mu.Unlock()
		},
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			auth = h.Get("Authorization")
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)

	c := &openaicompat.Client{
		BaseURL:    srv.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: srv.Client(),
		Transport:  openaicompat.TransportChatAndResponses,
		Hooks: openaicompat.RequestHooks{
			PrepareHeaders: func(h http.Header, _ aipapi.Call, _ string, _ openaicompat.Flavor) {
				h.Set("X-Test-Hook", "1")
			},
		},
	}
	es, err := c.Open(context.Background(), sampleCall(), "emu-model", openaicompat.FlavorChat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	var texts []string
	var sawUsage bool
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
		if ev.Kind == aipapi.EventUsageDelta {
			sawUsage = true
		}
	}
	if strings.Join(texts, "") != "hello" {
		t.Fatalf("text=%q", texts)
	}
	if !sawUsage {
		t.Fatal("expected usage")
	}
	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer sk-test" {
		t.Fatalf("auth=%q", auth)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model"] != "emu-model" || parsed["stream"] != true {
		t.Fatalf("body=%s", body)
	}
}

func TestClient_ResponsesStreamHappyPath(t *testing.T) {
	t.Parallel()
	var path string
	emu := openaicompat.NewEmulator(openaicompat.EmulatorConfig{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		emu.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{
		BaseURL: srv.URL + "/v1", APIKey: "k", HTTPClient: srv.Client(),
		Transport: openaicompat.TransportChatAndResponses,
	}
	call := sampleCall()
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	es, err := c.Open(context.Background(), call, "emu-model", openaicompat.FlavorResponses)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()
	var text strings.Builder
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	if text.String() != "hello" {
		t.Fatalf("text=%q", text.String())
	}
	if !strings.HasSuffix(path, "/responses") {
		t.Fatalf("path=%q", path)
	}
}

func TestClient_ChatOnlyRejectsResponses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{
		BaseURL: srv.URL, HTTPClient: srv.Client(), Transport: openaicompat.TransportChatOnly,
	}
	_, err := c.Open(context.Background(), sampleCall(), "m", openaicompat.FlavorResponses)
	if err == nil || !strings.Contains(err.Error(), "responses") {
		t.Fatalf("err=%v", err)
	}
}

func TestClient_UnsupportedContentPartsFailBeforeNetwork(t *testing.T) {
	t.Parallel()
	for _, kind := range []aipapi.PartKind{aipapi.PartImageRef, aipapi.PartFileRef} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			t.Cleanup(srv.Close)
			call := sampleCall()
			call.Messages[0].Parts = []aipapi.Part{{Kind: kind, ImageRef: "img://x", FileRef: "file://x"}}
			c := &openaicompat.Client{BaseURL: srv.URL, HTTPClient: srv.Client()}
			_, err := c.Open(context.Background(), call, "m", openaicompat.FlavorChat)
			if err == nil || !strings.Contains(err.Error(), "unsupported canonical content part") {
				t.Fatalf("err=%v", err)
			}
			if called {
				t.Fatal("unsupported content reached network")
			}
		})
	}
}

func TestClient_CancelContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Open(ctx, sampleCall(), "m", openaicompat.FlavorChat)
	if err == nil {
		t.Fatal("expected cancel error")
	}
}

func TestClient_ListModelsBounded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ModelsJSON: `{"data":[{"id":"a"},{"id":"b"},{"id":"c"}]}`,
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()}
	models, err := c.ListModels(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a" {
		t.Fatalf("%+v", models)
	}
}

func TestClient_ListModelsRejectsMalformedAndOversize(t *testing.T) {
	t.Parallel()
	t.Run("malformed", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
			ModelsJSON: `{not-json`,
		}))
		t.Cleanup(srv.Close)
		c := &openaicompat.Client{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()}
		_, err := c.ListModels(context.Background(), 10)
		if err == nil || !strings.Contains(err.Error(), "json") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("http500", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
			ForcedStatus: 500, ForcedBody: `{"error":{"message":"boom","type":"server"}}`,
		}))
		t.Cleanup(srv.Close)
		c := &openaicompat.Client{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()}
		_, err := c.ListModels(context.Background(), 10)
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("oversize", func(t *testing.T) {
		t.Parallel()
		big := `{"data":[{"id":"` + strings.Repeat("x", 100) + `"}]}`
		srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ModelsJSON: big}))
		t.Cleanup(srv.Close)
		c := &openaicompat.Client{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client(), MaxBodyBytes: 32}
		_, err := c.ListModels(context.Background(), 10)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestClient_HTTPErrorMapped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ForcedStatus: 401,
		ForcedBody:   `{"error":{"message":"bad key","type":"auth","code":"invalid_api_key"}}`,
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{BaseURL: srv.URL, APIKey: "x", HTTPClient: srv.Client()}
	_, err := c.Open(context.Background(), sampleCall(), "m", openaicompat.FlavorChat)
	he, ok := err.(*openaicompat.HTTPError)
	if !ok || he.Status != 401 || he.Code != "invalid_api_key" {
		t.Fatalf("err=%T %v", err, err)
	}
}

func TestSupportSource_NoProviderNamesOrInternalImports(t *testing.T) {
	t.Parallel()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"openrouter", "nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm", "internal/core/", "internal/plugins/"}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(b))
		for _, f := range forbidden {
			if strings.Contains(lower, strings.ToLower(f)) {
				t.Fatalf("%s contains forbidden %q", e.Name(), f)
			}
		}
	}
}

func TestCollectPrefixedExtraBody_bounds(t *testing.T) {
	t.Parallel()
	prefix := "vendor.extra_body."
	ext := map[string]json.RawMessage{
		prefix + "safe":       json.RawMessage(`1`),
		prefix + "bad.nested": json.RawMessage(`1`),
		prefix:                json.RawMessage(`1`),
		prefix + "nullish":    json.RawMessage(`null`),
		"unrelated":           json.RawMessage(`1`),
	}
	got := openaicompat.CollectPrefixedExtraBody(ext, prefix)
	if len(got) != 1 || got["safe"] == nil {
		t.Fatalf("%+v", got)
	}
}

func sampleCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}},
		}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}
}
