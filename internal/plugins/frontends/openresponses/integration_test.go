package openresponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	refcli "github.com/aiproxer/aiproxer/internal/refclient/openresponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

var (
	_ = front.ID
	_ = aipapi.CapabilityStreaming
	_ = testkit.LocalTestServerHTTPClient
	_ = aipsdk.FrontendMountOptions{}
)

// mountORFrontend mounts the real OpenResponses frontend over a stub executor and
// returns the refclient base URL plus the full-path server origin.
func mountORFrontend(t *testing.T, ex *runtime.Executor) (baseURL, origin string) {
	t.Helper()
	var cfg yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := front.Mount(mux, aipsdk.FrontendMountOptions{
		AllowUnauthenticated: true,
		PluginCfg:            cfg,
		Exec:                 ex,
		DefaultRoute:         "stub:gpt-4o-mini",
	}); err != nil {
		t.Fatalf("mount openresponses frontend: %v", err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/openresponses/v1", srv.URL
}

func stubExecutor(t *testing.T, caps aipapi.BackendCaps, text string) *runtime.Executor {
	t.Helper()
	ex := testkit.NewStubExecutor(t, caps, text, nil)
	ex.DefaultBackend = "stub"
	return ex
}

var storeFalse = false

func outputText(t *testing.T, res *refcli.ResponseResource) string {
	t.Helper()
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, it := range res.Output {
		if it.Type != "message" {
			continue
		}
		for _, p := range it.Content {
			if p.Type == "output_text" {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

func TestIntegration_refclientNonStreaming(t *testing.T) {
	t.Parallel()
	ex := stubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "integration-ok")
	base, _ := mountORFrontend(t, ex)

	cli := refcli.New(refcli.Config{BaseURL: base, APIKey: "sk-test", HTTPClient: testkit.LocalTestServerHTTPClient()})
	res, err := cli.Create(context.Background(), refcli.CreateParams{
		Model: "gpt-4o-mini",
		Store: &storeFalse,
		Input: refcli.Input{Text: "ping", TextSet: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outputText(t, res), "integration-ok") {
		t.Fatalf("output text: %q", outputText(t, res))
	}
}

func TestIntegration_refclientStreaming(t *testing.T) {
	t.Parallel()
	ex := stubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "stream-ok")
	base, _ := mountORFrontend(t, ex)

	cli := refcli.New(refcli.Config{BaseURL: base, APIKey: "sk-test", HTTPClient: testkit.LocalTestServerHTTPClient()})
	terminal, err := cli.CreateStream(context.Background(), refcli.CreateParams{
		Model: "gpt-4o-mini",
		Store: &storeFalse,
		Input: refcli.Input{Text: "hi", TextSet: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outputText(t, terminal), "stream-ok") {
		t.Fatalf("terminal output text: %q", outputText(t, terminal))
	}
}

func TestIntegration_malformedJSON_returns400(t *testing.T) {
	t.Parallel()
	ex := stubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x")
	_, origin := mountORFrontend(t, ex)

	resp, err := testkit.LocalTestServerHTTPClient().Post(origin+"/openresponses/v1/responses", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestIntegration_unknownPrefixedExtensionRejected(t *testing.T) {
	t.Parallel()
	ex := stubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x")
	base, _ := mountORFrontend(t, ex)

	cli := refcli.New(refcli.Config{BaseURL: base, APIKey: "sk-test", HTTPClient: testkit.LocalTestServerHTTPClient()})
	_, err := cli.Create(context.Background(), refcli.CreateParams{
		Model: "gpt-4o-mini",
		Store: &storeFalse,
		Input: refcli.Input{Text: "x", TextSet: true},
		Extensions: map[string]json.RawMessage{
			"acme:telemetry": json.RawMessage(`{"namespace":"acme","data":{"x":1}}`),
		},
	})
	if err == nil {
		t.Fatal("expected rejection of undeclared extension type")
	}
}
