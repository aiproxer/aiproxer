package openairesponses_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestHandler_NamespaceProjectionWarnsOnce pins the operator-visible
// degradation notice: bounded (one line per handler generation), emitted only
// when a namespaced request is actually projected, and free of tool names or
// arguments.
func TestHandler_NamespaceProjectionWarnsOnce(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, caps, "unused", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini", Log: logger}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	const body = `{"model":"gpt-4o-mini","tools":[{"type":"namespace","name":"multi_agent_v1","description":"NS","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}],"input":"hi"}`
	for range 2 {
		status, resp := postNamespaceResponses(t, srv, body)
		if status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", status, resp)
		}
	}
	const notice = "namespace tool declarations projected"
	if got := strings.Count(logs.String(), notice); got != 1 {
		t.Fatalf("namespace projection warning count = %d, want exactly 1; logs:\n%s", got, logs.String())
	}
	for _, forbidden := range []string{"multi_agent_v1", "close_agent"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("the bounded warning must not log tool identity %q; logs:\n%s", forbidden, logs.String())
		}
	}
}

// TestHandler_NamespaceHistoryProjectionWarns pins the visibility of the
// undeclared-history case: no namespace declaration is present, so only the
// history mapping occurs and the warning must still name it through its count.
func TestHandler_NamespaceHistoryProjectionWarns(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, caps, "unused", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini", Log: logger}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	const body = `{"model":"gpt-4o-mini","tools":[{"type":"function","name":"x","parameters":{"type":"object"}}],"input":[{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"}]}`
	status, resp := postNamespaceResponses(t, srv, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, resp)
	}
	if got := strings.Count(logs.String(), "namespace tool declarations projected"); got != 1 {
		t.Fatalf("history projection warning count = %d, want 1; logs:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "history_members=1") {
		t.Fatalf("the warning must count history-only projections; logs:\n%s", logs.String())
	}
	for _, forbidden := range []string{"multi_agent_v1", "close_agent"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("the bounded warning must not log tool identity %q; logs:\n%s", forbidden, logs.String())
		}
	}
}
