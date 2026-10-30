package frontendpipe_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/systemone"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// capabilityRejectErr is an executor terminal error matching
// aipapi.ErrCapabilityReject without being a *aipapi.RejectError: no
// candidate can serve the request (exact requirement mismatch).
func capabilityRejectErr() error {
	return fmt.Errorf("executor: %w", &aipapi.RequirementsRejectError{
		MissingCaps: []aipapi.Capability{aipapi.CapabilityVision},
	})
}

// TestCapabilityReject_chatFrontendPaths pins the current wire mapping of a
// capability-reject executor error on every chat-family frontend path that
// renders execerr.ClassifyExecute outcomes.
func TestCapabilityReject_chatFrontendPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		write func(http.ResponseWriter, execerr.Outcome) error
	}{
		{name: "openai_chat_completions_and_responses", write: frontendpipe.OpenAIWire{}.WriteExecuteError},
		{name: "anthropic", write: anthropic.WireErrors{}.WriteExecuteError},
		{name: "gemini", write: gemini.WireErrors{}.WriteExecuteError},
		{name: "systemone", write: systemone.WireErrors{}.WriteExecuteError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := execerr.ClassifyExecute(capabilityRejectErr())
			rec := httptest.NewRecorder()
			if err := tc.write(rec, out); err != nil {
				t.Fatalf("write: %v", err)
			}
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status: got %d want %d body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), execerr.BackendUnavailableWireMessage) {
				t.Fatalf("body should carry the backend-unavailable wire message, got %s", rec.Body.String())
			}
		})
	}
}

// TestCapabilityReject_openresponsesUnchanged pins that the openresponses
// frontend already maps the same underlying error to 502 backend_unavailable
// through its own classifier. This behavior must not change.
func TestCapabilityReject_openresponsesUnchanged(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(capabilityRejectErr())
	rec := httptest.NewRecorder()
	if err := (openresponses.WireErrors{}).WriteExecuteError(rec, out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "backend_unavailable") {
		t.Fatalf("body should carry backend_unavailable, got %s", rec.Body.String())
	}
}

// TestCapabilityReject_nonStreamCollectPath pins the current mapping of a
// capability rejection surfacing through Collect on the non-streaming path
// (Execute opened the stream successfully).
func TestCapabilityReject_nonStreamCollectPath(t *testing.T) {
	t.Parallel()
	es := &rejectFirstRecvStream{err: capabilityRejectErr()}
	exec := &countingStreamExec{stream: es}
	spec := nonStreamRejectSpec(exec, frontendpipe.OpenAIWire{})

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s want 502", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), execerr.BackendUnavailableWireMessage) {
		t.Fatalf("body should carry the backend-unavailable wire message, got %s", rec.Body.String())
	}
}
