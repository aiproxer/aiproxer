package openaichat_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
)

func TestResponder_jsonAndSSESequencing(t *testing.T) {
	t.Parallel()
	var seqs []int64
	var mu sync.Mutex
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder: func(req refbackend.Request) refbackend.Response {
			mu.Lock()
			seqs = append(seqs, req.Sequence)
			mu.Unlock()
			if req.Stream {
				return refbackend.Response{
					Status: http.StatusOK,
					SSE:    "data: {\"id\":\"dyn\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"sse\"}}]}\n\ndata: [DONE]\n\n",
				}
			}
			return refbackend.Response{
				Status: http.StatusOK,
				JSON:   `{"id":"dyn","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"json"},"finish_reason":"stop"}]}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"content":"json"`) {
		t.Fatalf("json resp: %d %s", resp.StatusCode, b)
	}

	streamBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"x"}]}`
	resp2, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || !strings.Contains(string(b2), "sse") || !strings.Contains(resp2.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse resp: %d ct=%q %s", resp2.StatusCode, resp2.Header.Get("Content-Type"), b2)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("sequences: %v", seqs)
	}
}

func TestResponder_statusAndHeaders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder: func(refbackend.Request) refbackend.Response {
			h := make(http.Header)
			h.Set("X-Test", "yes")
			h.Set("Retry-After", "9")
			return refbackend.Response{
				Status:  http.StatusTooManyRequests,
				Headers: h,
				JSON:    `{"error":{"message":"slow","type":"requests"}}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Test") != "yes" || resp.Header.Get("Retry-After") != "9" {
		t.Fatalf("headers: %v", resp.Header)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "slow") {
		t.Fatalf("body: %s", b)
	}
}

func TestResponder_precedence_forcedStatusWins(t *testing.T) {
	t.Parallel()
	called := false
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		ForcedHTTPStatus:   http.StatusUnauthorized,
		Responder: func(refbackend.Request) refbackend.Response {
			called = true
			return refbackend.Response{Status: http.StatusOK, JSON: `{"id":"nope"}`}
		},
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if called {
		t.Fatal("ForcedHTTPStatus must take precedence over Responder")
	}
}

func TestResponder_precedence_overFixedJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		NonStreamJSON:      `{"id":"fixed"}`,
		Responder: func(refbackend.Request) refbackend.Response {
			return refbackend.Response{
				Status: http.StatusOK,
				JSON:   `{"id":"responder","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"r"},"finish_reason":"stop"}]}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), `"id":"fixed"`) || !strings.Contains(string(b), `"id":"responder"`) {
		t.Fatalf("Responder must win over NonStreamJSON: %s", b)
	}
}

func TestResponder_nilKeepsDefaultBehavior(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "chatcmpl_refbackend_1") {
		t.Fatalf("default broken: %d %s", resp.StatusCode, b)
	}
}

func TestResponder_bodyClone_mutationDoesNotAffectHandlerBuffer(t *testing.T) {
	t.Parallel()
	var retained []byte
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody: func(body []byte) {
			retained = body
		},
		Responder: func(req refbackend.Request) refbackend.Response {
			if len(req.Body) == 0 {
				t.Error("empty Request.Body")
			} else {
				req.Body[0] = 'Z'
			}
			return refbackend.Response{
				Status: http.StatusOK,
				JSON:   `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if len(retained) == 0 {
		t.Fatal("OnRequestBody did not retain body")
	}
	if retained[0] != '{' {
		t.Fatalf("handler buffer mutated by Responder: got %q want '{'", retained[0])
	}
}

func TestResponder_precedence_overStreamSSE(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		StreamSSE:          "data: {\"id\":\"fixed-sse\"}\n\ndata: [DONE]\n\n",
		Responder: func(refbackend.Request) refbackend.Response {
			return refbackend.Response{
				Status: http.StatusOK,
				SSE:    "data: {\"id\":\"responder-sse\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"r\"}}]}\n\ndata: [DONE]\n\n",
			}
		},
	}))
	t.Cleanup(srv.Close)

	streamBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"x"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), "fixed-sse") || !strings.Contains(string(b), "responder-sse") {
		t.Fatalf("Responder must win over StreamSSE: %s", b)
	}
}

func TestResponder_sequenceUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()
	var (
		mu   sync.Mutex
		seqs []int64
	)
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		Responder: func(req refbackend.Request) refbackend.Response {
			mu.Lock()
			seqs = append(seqs, req.Sequence)
			mu.Unlock()
			return refbackend.Response{
				Status: http.StatusOK,
				JSON:   `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
			if err != nil {
				t.Errorf("post: %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != n {
		t.Fatalf("seq count %d", len(seqs))
	}
	seen := make(map[int64]struct{}, n)
	for _, s := range seqs {
		if s < 1 || s > int64(n) {
			t.Fatalf("sequence out of range: %d", s)
		}
		if _, ok := seen[s]; ok {
			t.Fatalf("duplicate sequence %d", s)
		}
		seen[s] = struct{}{}
	}
}

func TestResponder_onRequestBodyStillInvoked(t *testing.T) {
	t.Parallel()
	var saw atomic.Bool
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingBearer: true,
		OnRequestBody:      func([]byte) { saw.Store(true) },
		Responder: func(refbackend.Request) refbackend.Response {
			return refbackend.Response{
				Status: http.StatusOK,
				JSON:   `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			}
		},
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(openaiChatMinimalBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !saw.Load() {
		t.Fatal("OnRequestBody must still run with Responder")
	}
}
