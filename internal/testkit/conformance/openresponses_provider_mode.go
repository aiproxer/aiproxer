package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
)

// OpenRouter/NVIDIA connector-column deployment.
//
// The authoritative 5×9 matrix references the OpenRouter and NVIDIA connector
// columns. The connectors stay optional modules (never essential backend kinds
// and never root go.mod requirements), so the matrix drives each cell through
// the actual relocated connector executable via the backendplugin host adapter:
// DeployConnectorColumnFor launches connectors/openrouter or connectors/nvidia
// (built once per test binary), configures the process with the cell's observing
// origin as base_url plus the synthetic api_key secret, and builds the
// execbackend.Backend through the same host-adapter APIs the production
// composition uses (connector_host.go). No connector protocol code is duplicated
// in the harness.

// ConnectorColumnModel is the canonical model used by connector-column
// deployments (OpenRouter/NVIDIA). The value is shared with the route-selector
// the frontend default routes to.
const ConnectorColumnModel = "gpt-4o-mini"

// connectorColumnResource builds a completed OpenAI-compatible Responses resource
// with deterministic connector-column text. The payload is a typed value encoded
// with encoding/json so no interpolated string can corrupt the wire format.
func connectorColumnResource(created int64) string {
	payload := map[string]any{
		"id":         "resp_provider_1",
		"object":     "response",
		"created_at": created,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output": []any{
			map[string]any{
				"type":    "message",
				"id":      "msg_provider_1",
				"status":  "completed",
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "provider-mode-ok"}},
			},
		},
		"usage": map[string]any{
			"input_tokens":  1,
			"output_tokens": 1,
			"total_tokens":  2,
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// connectorColumnSSE builds the streaming SSE trajectory of the connector-column
// origin. Every event payload is constructed as a typed value and encoded with
// encoding/json so no interpolated string can corrupt the wire format.
func connectorColumnSSE(created int64) string {
	txt := "provider-mode-ok"
	item := func(status, text string) map[string]any {
		return map[string]any{
			"type":    "message",
			"id":      "msg_provider_1",
			"status":  status,
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}
	}
	response := func(status string, output []any) map[string]any {
		return map[string]any{
			"id":         "resp_provider_stream",
			"object":     "response",
			"created_at": created,
			"status":     status,
			"model":      "gpt-4o-mini",
			"output":     output,
		}
	}
	events := []struct {
		name string
		data map[string]any
	}{
		{"response.created", map[string]any{
			"type":            "response.created",
			"sequence_number": 1,
			"response":        response("in_progress", []any{}),
		}},
		{"response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": 2,
			"output_index":    0,
			"item":            item("in_progress", ""),
		}},
		{"response.content_part.added", map[string]any{
			"type":            "response.content_part.added",
			"sequence_number": 3,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
			"part":            map[string]any{"type": "output_text", "text": ""},
		}},
		{"response.output_text.delta", map[string]any{
			"type":            "response.output_text.delta",
			"sequence_number": 4,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
			"delta":           txt,
		}},
		{"response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"sequence_number": 5,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
			"text":            txt,
		}},
		{"response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"sequence_number": 6,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
		}},
		{"response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": 7,
			"output_index":    0,
			"item":            item("completed", txt),
		}},
		{"response.completed", map[string]any{
			"type":            "response.completed",
			"sequence_number": 8,
			"response":        response("completed", []any{item("completed", txt)}),
		}},
	}
	var buf bytes.Buffer
	for _, ev := range events {
		data, _ := json.Marshal(ev.data)
		buf.WriteString("event: " + ev.name + "\n")
		buf.WriteString("data: " + string(data) + "\n\n")
	}
	buf.WriteString("data: [DONE]\n\n")
	return buf.String()
}

// DeployConnectorColumn deploys the OpenResponses frontend over the actual
// connectors/openrouter or connectors/nvidia executable for the connector-column
// evidence identity. The connector process is configured with a fresh origin
// that emulates an OpenAI-compatible provider endpoint; the observing origin
// counts and redacts every request so tests can prove the actual connector route
// reaches the provider endpoint. backendID must be one of BackendOpenRouter /
// BackendNVIDIA.
func DeployConnectorColumn(tb testing.TB, backendID string, transport ClientTransport) *Deployment {
	tb.Helper()
	return DeployConnectorColumnFor(tb, FrontendOpenResponses, backendID, transport)
}

// DeployConnectorColumnFor deploys an arbitrary bundled frontend over the actual
// OpenRouter/NVIDIA connector executable for the connector-column evidence
// identities. frontend must be a bundled frontend ID; only frontends that
// produce an OpenAI Responses operation (openresponses, openai-responses) can
// round-trip the Responses-wire connector; other frontend operations fail closed
// before any upstream request.
func DeployConnectorColumnFor(tb testing.TB, frontend, backendID string, transport ClientTransport) *Deployment {
	tb.Helper()
	return DeployConnectorColumnWithOrigin(tb, frontend, backendID, transport, nil)
}

// DeployConnectorColumnWithOrigin deploys a connector-column cell with a custom
// observing-origin responder (nil keeps the default connectorColumnOrigin).
// Custom origins let evidence assert on the real request headers/body the
// connector process sends.
func DeployConnectorColumnWithOrigin(tb testing.TB, frontend, backendID string, transport ClientTransport, originHandler http.Handler) *Deployment {
	tb.Helper()
	return deployConnectorColumn(tb, frontend, backendID, transport, OriginFailNone, originHandler)
}

// DeployConnectorColumnWithFail deploys a connector-column cell whose observing
// origin injects a deterministic failure mode (e.g. OriginFailUnauthorized).
func DeployConnectorColumnWithFail(tb testing.TB, frontend, backendID string, transport ClientTransport, fail OriginFailMode) *Deployment {
	tb.Helper()
	return deployConnectorColumn(tb, frontend, backendID, transport, fail, nil)
}

func deployConnectorColumn(tb testing.TB, frontend, backendID string, transport ClientTransport, fail OriginFailMode, originHandler http.Handler) *Deployment {
	tb.Helper()
	if backendID != BackendOpenRouter && backendID != BackendNVIDIA {
		tb.Fatalf("DeployConnectorColumn: unknown connector column %q", backendID)
	}
	d := &Deployment{
		Spec:     DeploymentSpec{Frontend: frontend, Backend: backendID, Transport: transport},
		origins:  map[string]*Origin{},
		backends: map[string]execbackend.Backend{},
	}
	custom := originHandler
	if custom == nil {
		custom = &connectorWire{text: "provider-mode-ok"}
	}
	primaryOrigin := newHarnessOrigin(tb, backendID, fail, nil, 100, "", nil, custom)
	d.origins[backendID] = primaryOrigin

	d.backends[backendID] = connectorHostBackend(tb, backendID, primaryOrigin.URL())

	d.RouteSelector = ConnectorColumnRouteSelector(backendID)
	d.Exec = harnessExecutor(tb, d.backends, backendID)
	d.Mux = http.NewServeMux()
	genCtx, genCancel := context.WithCancel(context.Background())
	d.genCancel = genCancel
	if err := mountHarnessFrontend(genCtx, d.Mux, frontend, d.Exec, d.RouteSelector, 0); err != nil {
		_ = d.Close()
		tb.Fatalf("harness: mount %q frontend: %v", frontend, err)
	}
	d.Server = httptest.NewServer(d.Mux)
	d.Client = harnessClientFor(tb, frontend, d)
	tb.Cleanup(func() { _ = d.Close() })
	return d
}

// ConnectorColumnRouteSelector returns the route selector a connector-column
// deployment resolves to (backendID:ConnectorColumnModel).
func ConnectorColumnRouteSelector(backendID string) string {
	return backendID + ":" + ConnectorColumnModel
}
