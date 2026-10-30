package gemini_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	refcli "github.com/aiproxer/aiproxer/internal/refclient/gemini"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"google.golang.org/genai"
)

func TestIntegration_refclientNonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "integration-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
		BaseURL:    srv.URL,
		APIKey:     "fake-key",
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
	if len(parts) == 0 || parts[0].Text != "integration-ok" {
		t.Fatalf("parts: %+v", parts)
	}
}

func TestIntegration_refclientStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "stream-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
		BaseURL:    srv.URL,
		APIKey:     "fake-key",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for res, serr := range cli.GenerateContentStream(context.Background(), "gemini-2.0-flash",
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, nil) {
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
	if !strings.Contains(got.String(), "stream-ok") {
		t.Fatalf("stream text: %q", got.String())
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
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")

	cli, err := refcli.New(context.Background(), refcli.Config{
		BaseURL:    srv.URL,
		APIKey:     "fake-key",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := []*genai.Content{{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: "describe attachments"},
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: png}},
			{InlineData: &genai.Blob{MIMEType: "application/pdf", Data: pdf}},
		},
	}}
	_, err = cli.GenerateContent(context.Background(), "gemini-2.0-flash", contents, nil)
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

func TestIntegration_malformedJSON_returns400(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u := srv.URL + "/v1beta/models/gemini-2.0-flash:generateContent"
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(u, "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
}

func TestIntegration_invalidPath_returns404(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1beta/foo", "application/json", strings.NewReader(`{}`))
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
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u := srv.URL + "/v1beta/models/gemini-2.0-flash:generateContent"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
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

func TestIntegration_capabilityReject_returns400(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "nope", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	cli, err := refcli.New(context.Background(), refcli.Config{
		BaseURL:    srv.URL,
		APIKey:     "fake-key",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := []*genai.Content{{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: "x"},
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: png}},
		},
	}}
	_, err = cli.GenerateContent(context.Background(), "gemini-2.0-flash", contents, nil)
	if err == nil {
		t.Fatal("expected error from capability reject")
	}
}

func TestIntegration_toolStubRoundTrip_streaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli, err := refcli.New(context.Background(), refcli.Config{
		BaseURL:    srv.URL,
		APIKey:     "fake-key",
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "todo_add",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
			},
		}},
	}}
	cfg := &genai.GenerateContentConfig{Tools: tools}
	var sawFunctionCall bool
	var textParts []string
	for res, serr := range cli.GenerateContentStream(context.Background(), "gemini-2.0-flash",
		[]*genai.Content{genai.NewContentFromText("use the tool", genai.RoleUser)}, cfg) {
		if serr != nil {
			t.Fatal(serr)
		}
		for _, c := range res.Candidates {
			if c.Content == nil {
				continue
			}
			for _, p := range c.Content.Parts {
				if p.FunctionCall != nil && p.FunctionCall.Name == "todo_add" {
					sawFunctionCall = true
				}
				if p.Text != "" {
					textParts = append(textParts, p.Text)
				}
			}
		}
	}
	if !sawFunctionCall {
		t.Fatal("expected functionCall in streaming response")
	}
	if len(textParts) == 0 {
		t.Fatal("expected text in streaming response")
	}
}

func TestIntegration_toolStubRoundTrip_nonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := `{
  "contents": [{"role":"user","parts":[{"text":"hi"}]}],
  "tools": [{"functionDeclarations":[{"name":"todo_add","parameters":{"type":"object"}}]}]
}`
	url := srv.URL + "/v1beta/models/gemini-2.0-flash:generateContent"
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	var v map[string]any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	cands, _ := v["candidates"].([]any)
	if len(cands) < 1 {
		t.Fatalf("candidates: %v", v)
	}
	c0 := testkit.MustMapStringAny(t, cands[0])
	content := testkit.MustMapStringAny(t, c0["content"])
	parts := testkit.MustSliceAny(t, content["parts"])
	var saw bool
	for _, p := range parts {
		pm := testkit.MustMapStringAny(t, p)
		if fc, ok := pm["functionCall"].(map[string]any); ok {
			if fc["name"] == "todo_add" {
				saw = true
			}
		}
	}
	if !saw {
		t.Fatalf("missing functionCall in parts %#v", parts)
	}
}

func TestIntegration_routeHeaderOverridesDefault(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:default-route", RoutePrefixes: routeselect.NewPrefixSet([]string{"stub"})}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u := srv.URL + "/v1beta/models/gemini-2.0-flash:generateContent"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u,
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`))
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
	mux.Handle("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u := srv.URL + "/v1beta/models/stub:model-from-path:generateContent"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u,
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`))
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
	if call.Route.Selector != "stub:model-from-path" {
		t.Fatalf("route selector %q", call.Route.Selector)
	}
}
