package gemini_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	refcli "github.com/aiproxer/aiproxer/internal/refclient/gemini"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"google.golang.org/genai"
)

func TestHandler_nonStreaming_refclientSmoke(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
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
	if len(parts) == 0 || parts[0].Text != "ok" {
		t.Fatalf("parts: %+v", parts)
	}
}

func TestHandler_streaming_refclientReadsChunk(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
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
	if got.String() != "stream-ok" {
		t.Fatalf("stream text: %q", got.String())
	}
}

func TestHandler_requiresAPIKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	// genai.New rejects an empty API key before any request; exercise the handler directly.
	resp, err := http.Post(srv.URL+"/v1beta/models/gemini-2.0-flash:generateContent", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without x-goog-api-key: %d", resp.StatusCode)
	}
}

func TestHandler_multimodalRequest_customJSON(t *testing.T) {
	t.Parallel()
	const mmJSON = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [{"text": "multimodal-out:image+pdf"}]
      }
    }
  ]
}`

	var sawIn bool
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(body []byte) {
			s := string(body)
			if strings.Contains(s, "inlineData") || strings.Contains(s, "inline_data") {
				sawIn = true
			}
		},
		NonStreamJSON: mmJSON,
	}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
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

	out, err := cli.GenerateContent(context.Background(), "gemini-2.0-flash", contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sawIn {
		t.Fatal("expected inline image/pdf payload in request body")
	}
	if out.Candidates[0].Content.Parts[0].Text != "multimodal-out:image+pdf" {
		t.Fatalf("output text: %q", out.Candidates[0].Content.Parts[0].Text)
	}
}

func TestHandler_multimodalResponse_inlineImage(t *testing.T) {
	t.Parallel()
	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	imgB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)
	mmJSON := `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {"text": "here-is-image"},
          {"inlineData": {"mimeType": "image/png", "data": "` + imgB64 + `"}},
          {"inlineData": {"mimeType": "application/pdf", "data": "` + pdfB64 + `"}}
        ]
      }
    }
  ]
}`

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		NonStreamJSON: mmJSON,
	}))
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
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
	if len(parts) != 3 || parts[0].Text != "here-is-image" {
		t.Fatalf("parts: %+v", parts)
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "image/png" {
		t.Fatalf("inline part: %+v", parts[1])
	}
	if string(parts[1].InlineData.Data) != string(png) {
		t.Fatalf("inline bytes len got %d want %d", len(parts[1].InlineData.Data), len(png))
	}
	if parts[2].InlineData == nil || parts[2].InlineData.MIMEType != "application/pdf" {
		t.Fatalf("pdf inline part: %+v", parts[2])
	}
	if string(parts[2].InlineData.Data) != string(pdf) {
		t.Fatalf("pdf bytes len got %d want %d", len(parts[2].InlineData.Data), len(pdf))
	}
}

func TestHandler_wrongPath_404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/other")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

const geminiGenerateMinimalPath = "/v1beta/models/gemini-2.0-flash:generateContent"

func TestHandler_forced401_jsonError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusUnauthorized,
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+geminiGenerateMinimalPath, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", "k401")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "API key") && !strings.Contains(string(b), "401") {
		t.Fatalf("body: %s", b)
	}
}

func TestHandler_forced429_retryAfter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "60",
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+geminiGenerateMinimalPath, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", "k429")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After: %q", got)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "RESOURCE_EXHAUSTED") && !strings.Contains(string(b), "429") {
		t.Fatalf("body: %s", b)
	}
}

func TestHandler_onAuthorizedCredential_seesAPIKey(t *testing.T) {
	t.Parallel()
	var seen string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnAuthorizedCredential: func(s string) { seen = s },
	}))
	t.Cleanup(srv.Close)

	key := "gemini-probe-key"
	req, err := http.NewRequest(http.MethodPost, srv.URL+geminiGenerateMinimalPath, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if seen != key {
		t.Fatalf("credential: got %q", seen)
	}
}

func TestHandler_forced429_streamPath_returnsJSONError(t *testing.T) {
	t.Parallel()
	streamPath := "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse"
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "2",
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+streamPath, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", "ks")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type: %q", ct)
	}
}

func TestHandler_bodyTooLarge_returns400(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		AllowMissingAPIKey: true,
	}))
	t.Cleanup(srv.Close)

	// maxBodyBytes is 10 MB, so send 10 MB + 1 byte
	body := bytes.NewReader(make([]byte, 10<<20+1))
	req, err := http.NewRequest(http.MethodPost, srv.URL+geminiGenerateMinimalPath, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "read body") {
		t.Fatalf("body: %s", b)
	}
}
