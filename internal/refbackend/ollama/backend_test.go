package ollama_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/ollama"
)

func TestHandler_version(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Version == "" {
		t.Fatalf("missing version: %s", body)
	}
	if parsed.Version < "0.13.3" {
		t.Fatalf("version too old: %q", parsed.Version)
	}
}

func TestHandler_modelsList(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		LocalModels: []string{"llama3:latest", "codellama:7b"},
		CloudModels: []string{"deepseek-v3.2-cloud", "glm-5.1:cloud"},
	}))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Object != "list" {
		t.Fatalf("object: %q", parsed.Object)
	}
	want := []string{"llama3:latest", "codellama:7b", "deepseek-v3.2-cloud", "glm-5.1:cloud"}
	if len(parsed.Data) != len(want) {
		t.Fatalf("data len: got %d want %d body=%s", len(parsed.Data), len(want), body)
	}
	for i, id := range want {
		if parsed.Data[i].ID != id {
			t.Fatalf("data[%d].id: got %q want %q", i, parsed.Data[i].ID, id)
		}
	}
}

func TestHandler_showCapabilities(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		Capabilities: map[string][]string{
			"llama3.2:latest": {"completion", "vision"},
		},
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/show", strings.NewReader(`{"name":"llama3.2:latest"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Capabilities []string `json:"capabilities"`
		Details      struct {
			Format string `json:"format"`
			Family string `json:"family"`
		} `json:"details"`
		ModelInfo map[string]string `json:"model_info"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Capabilities) != 2 || parsed.Capabilities[0] != "completion" || parsed.Capabilities[1] != "vision" {
		t.Fatalf("capabilities: %v", parsed.Capabilities)
	}
	if parsed.Details.Format == "" || parsed.Details.Family == "" {
		t.Fatalf("details: %+v", parsed.Details)
	}
	if parsed.ModelInfo["general.architecture"] == "" {
		t.Fatalf("model_info: %+v", parsed.ModelInfo)
	}
}

func TestHandler_chatCompletionsNonStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ollama-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_chatCompletionsStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ollama-stream-ok") {
		t.Fatalf("body: %s", body)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatal("missing [DONE]")
	}
}

func TestHandler_responsesNonStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{ResponsesSupported: true}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"llama3:latest","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ollama-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_responsesStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{ResponsesSupported: true}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"llama3:latest","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ollama-stream-ok") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_responsesUnsupported(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ResponsesSupported:         false,
		ResponsesUnsupportedStatus: http.StatusNotFound,
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"llama3:latest","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestHandler_forced429WithRetryAfter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "60",
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After: %q", resp.Header.Get("Retry-After"))
	}
}

func TestHandler_requestCapture(t *testing.T) {
	t.Parallel()
	var capturedReq *http.Request
	var capturedBody string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequest: func(r *http.Request, body []byte) {
			capturedReq = r
			capturedBody = string(body)
		},
		OnRequestBody: func(body []byte) {
			capturedBody = string(body)
		},
	}))
	t.Cleanup(srv.Close)

	payload := `{"model":"llama3:latest","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer ollama")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if capturedReq == nil {
		t.Fatal("OnRequest not called")
	}
	if capturedReq.Method != http.MethodPost {
		t.Fatalf("method: %s", capturedReq.Method)
	}
	if !strings.Contains(capturedBody, `"model":"llama3:latest"`) {
		t.Fatalf("body: %s", capturedBody)
	}
}

func TestHandler_noAuthRequired(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestHandler_exactPathMatching(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{ResponsesSupported: true}))
	t.Cleanup(srv.Close)

	chatSuffix := srv.URL + "/prefix/v1/chat/completions"
	req, _ := http.NewRequest(http.MethodPost, chatSuffix, strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("chat suffix path status: %d", resp.StatusCode)
	}

	responsesSuffix := srv.URL + "/v1/responses/extra"
	req, _ = http.NewRequest(http.MethodPost, responsesSuffix, strings.NewReader(`{"model":"llama3:latest","input":"hi"}`))
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("responses suffix path status: %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exact chat path status: %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"llama3:latest","input":"hi"}`))
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exact responses path status: %d", resp.StatusCode)
	}
}

func TestHandler_forced500(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusInternalServerError,
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "" {
		t.Fatalf("unexpected Retry-After: %q", resp.Header.Get("Retry-After"))
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "error") {
		t.Fatalf("body: %s", body)
	}
}

func TestHandler_requireBearer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{RequireBearer: true}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bearer status: %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	req.Header.Set("Authorization", "Bearer ollama-secret")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid bearer status: %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"llama3:latest","messages":[]}`))
	req.Header.Set("Authorization", "Bearer ")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty bearer status: %d", resp.StatusCode)
	}
}
