package openresponsescompat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func mustBuildORBackend(t *testing.T, origin string, client *http.Client) execbackend.Backend {
	t.Helper()
	raw := "backend_prefix: or-int\nbase_url: " + origin + "\n"
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	be, err := backend.Build("or-int", n, client)
	if err != nil {
		t.Fatal(err)
	}
	return be
}

func itemAuthorityTextCall(text string) aipapi.Call {
	return aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenResponsesCreate,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindMessage,
			ID:     "msg_1",
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleUser,
			Content: []aipapi.ContentPart{{
				Kind: aipapi.ContentPartText,
				Text: "ping",
			}},
		}},
	}
}

func newScriptedORServer(t *testing.T, script *refbackend.Script) *httptest.Server {
	t.Helper()
	srv := refbackend.NewServer(refbackend.Options{AllowMissingBearer: true})
	if err := srv.Register(script); err != nil {
		t.Fatal(err)
	}
	if err := srv.Select(script.ID); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(srv.Handler())
	t.Cleanup(s.Close)
	return s
}

func refResource(text string) *refbackend.Resource {
	return refbackend.NewResource(
		"resp_int_1",
		"gpt-4o-mini",
		1715620000,
		[]refbackend.Item{refbackend.NewMessageItem("assistant", "output_text", text)},
	)
}

func TestIntegration_genericBackendNonStreaming(t *testing.T) {
	t.Parallel()
	origin := newScriptedORServer(t, &refbackend.Script{
		ID:          "int-json-text",
		Description: "integration json text",
		Mode:        refbackend.ModeJSON,
		Resource:    refResource("integration-ok"),
	})
	be := mustBuildORBackend(t, origin.URL, origin.Client())

	es, err := be.Open(context.Background(), itemAuthorityTextCall("ping"), routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "integration-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_genericBackendStreaming(t *testing.T) {
	t.Parallel()
	origin := newScriptedORServer(t, &refbackend.Script{
		ID:          "int-sse-text",
		Description: "integration sse text",
		Mode:        refbackend.ModeSSE,
		Resource:    refResource("stream-int-ok"),
	})
	be := mustBuildORBackend(t, origin.URL, origin.Client())
	call := itemAuthorityTextCall("hi")
	call.Invocation.TransportMode = aipapi.TransportModeStreaming
	call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming

	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-int-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_genericBackendUpstreamError(t *testing.T) {
	t.Parallel()
	origin := newScriptedORServer(t, &refbackend.Script{
		ID:          "int-error",
		Description: "integration upstream error",
		Mode:        refbackend.ModeJSON,
		Resource:    refResource("unused"),
		Error: &refbackend.ErrorStep{
			Status:  http.StatusBadRequest,
			Type:    "invalid_request",
			Code:    "bad_request",
			Message: "bad request",
		},
	})
	be := mustBuildORBackend(t, origin.URL, origin.Client())
	if _, err := be.Open(context.Background(), itemAuthorityTextCall("ping"), routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}); err == nil {
		t.Fatal("expected upstream error")
	}
}
