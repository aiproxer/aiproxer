package openaichat_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	refbackendchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/refclient/openaichat"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/openai/openai-go/v3"
)

func TestCreateChatCompletion_nonStreaming(t *testing.T) {
	t.Parallel()
	const body = `{
  "id": "chatcmpl-test",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("expected Bearer auth, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	cli := openaichat.New(openaichat.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("ping"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "chatcmpl-test" {
		t.Fatalf("id: %q", res.ID)
	}
}

func TestCreateChatCompletion_multimodal_imageAndFile(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		if !strings.Contains(s, "image_url") || !strings.Contains(s, `"type":"file"`) {
			t.Errorf("expected image_url and file parts, got: %s", s)
		}
		const body = `{"id":"chatcmpl-mm","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
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

	cli := openaichat.New(openaichat.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	_, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(parts),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateChatCompletionStream_readsChunk(t *testing.T) {
	t.Parallel()
	chunk := `{"id":"chatcmpl-s","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Z"},"finish_reason":null}]}`
	var sb strings.Builder
	sb.WriteString("data: ")
	sb.WriteString(chunk)
	sb.WriteString("\n\n")
	sb.WriteString("data: [DONE]\n\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sb.String())
	}))
	t.Cleanup(srv.Close)

	cli := openaichat.New(openaichat.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	stream := cli.CreateChatCompletionStream(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("stream"),
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
	if got.String() != "Z" {
		t.Fatalf("delta content: got %q", got.String())
	}
}

func TestRefclient_disableSDKRetries_singleHTTPAttemptOn429(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	rb := refbackendchat.NewHandler(refbackendchat.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "1",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		rb.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cli := openaichat.New(openaichat.Config{
		BaseURL:           srv.URL + "/v1",
		APIKey:            "sk",
		HTTPClient:        srv.Client(),
		DisableSDKRetries: true,
	})
	_, err := cli.CreateChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("x"),
		},
	})
	if err == nil {
		t.Fatal("expected error from 429 refbackend")
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1", n)
	}
}
