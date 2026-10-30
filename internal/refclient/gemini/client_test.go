package gemini_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	refbackendgemini "github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	"github.com/aiproxer/aiproxer/internal/refclient/gemini"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"google.golang.org/genai"
)

func TestGenerateContent_nonStreaming(t *testing.T) {
	t.Parallel()
	const respJSON = `{"candidates":[{"content":{"parts":[{"text":"hello-gemini"}],"role":"model"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if !strings.Contains(r.URL.Path, "generateContent") {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") == "" {
			t.Error("expected x-goog-api-key header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respJSON))
	}))
	t.Cleanup(srv.Close)

	cli, err := gemini.New(context.Background(), gemini.Config{
		APIKey:     "fake-key",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cli.GenerateContent(context.Background(), "gemini-2.0-flash", []*genai.Content{
		genai.NewContentFromText("ping", genai.RoleUser),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) == 0 || out.Candidates[0].Content == nil {
		t.Fatalf("candidates: %+v", out.Candidates)
	}
	parts := out.Candidates[0].Content.Parts
	if len(parts) == 0 || parts[0].Text != "hello-gemini" {
		t.Fatalf("parts: %+v", parts)
	}
}

func TestGenerateContent_multimodal_inlineImageAndPDF(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		if !strings.Contains(s, "inlineData") && !strings.Contains(s, "inline_data") {
			t.Fatalf("expected inline image/pdf payload in body, got: %s", s)
		}
		const respJSON = `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"}}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respJSON))
	}))
	t.Cleanup(srv.Close)

	cli, err := gemini.New(context.Background(), gemini.Config{
		APIKey:     "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	contents := []*genai.Content{{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: "describe"},
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: png}},
			{InlineData: &genai.Blob{MIMEType: "application/pdf", Data: pdf}},
		},
	}}
	_, err = cli.GenerateContent(context.Background(), "gemini-2.0-flash", contents, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateContent_multimodalResponse_inlineImageAndPDF(t *testing.T) {
	t.Parallel()
	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	respJSON := `{"candidates":[{"content":{"role":"model","parts":[{"text":"here-are-files"},{"inlineData":{"mimeType":"image/png","data":"` +
		base64.StdEncoding.EncodeToString(png) +
		`"}},{"inlineData":{"mimeType":"application/pdf","data":"` +
		base64.StdEncoding.EncodeToString(pdf) +
		`"}}]}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respJSON))
	}))
	t.Cleanup(srv.Close)

	cli, err := gemini.New(context.Background(), gemini.Config{
		APIKey:     "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cli.GenerateContent(context.Background(), "gemini-2.0-flash", []*genai.Content{
		genai.NewContentFromText("show", genai.RoleUser),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := out.Candidates[0].Content.Parts
	if len(parts) != 3 {
		t.Fatalf("parts len: got %d want 3", len(parts))
	}
	if parts[0].Text != "here-are-files" {
		t.Fatalf("text part: %+v", parts[0])
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "image/png" {
		t.Fatalf("image part: %+v", parts[1])
	}
	if string(parts[1].InlineData.Data) != string(png) {
		t.Fatalf("image bytes len got %d want %d", len(parts[1].InlineData.Data), len(png))
	}
	if parts[2].InlineData == nil || parts[2].InlineData.MIMEType != "application/pdf" {
		t.Fatalf("pdf part: %+v", parts[2])
	}
	if string(parts[2].InlineData.Data) != string(pdf) {
		t.Fatalf("pdf bytes len got %d want %d", len(parts[2].InlineData.Data), len(pdf))
	}
}

func TestGenerateContentStream_readsChunk(t *testing.T) {
	t.Parallel()
	line := `data: {"candidates":[{"content":{"parts":[{"text":"Z"}],"role":"model"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, line+"\n")
	}))
	t.Cleanup(srv.Close)

	cli, err := gemini.New(context.Background(), gemini.Config{
		APIKey:     "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for res, serr := range cli.GenerateContentStream(context.Background(), "gemini-2.0-flash",
		[]*genai.Content{genai.NewContentFromText("s", genai.RoleUser)}, nil) {
		if serr != nil {
			t.Fatal(serr)
		}
		for _, c := range res.Candidates {
			if c.Content != nil {
				for _, p := range c.Content.Parts {
					got.WriteString(p.Text)
				}
			}
		}
	}
	if got.String() != "Z" {
		t.Fatalf("stream text: %q", got.String())
	}
}

func TestGenai_refbackend429_singleHTTPAttempt(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	rb := refbackendgemini.NewHandler(refbackendgemini.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "1",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		rb.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cli, err := gemini.New(context.Background(), gemini.Config{
		APIKey:     "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cli.GenerateContent(context.Background(), "gemini-2.0-flash",
		[]*genai.Content{genai.NewContentFromText("x", genai.RoleUser)}, nil)
	if err == nil {
		t.Fatal("expected error from 429 refbackend")
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1 (genai must not transparently retry generateContent)", n)
	}
}
