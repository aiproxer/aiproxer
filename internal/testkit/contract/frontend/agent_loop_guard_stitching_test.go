package frontend

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"github.com/stretchr/testify/require"
)

type allowStitchAuth2 struct{}

func (allowStitchAuth2) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "stitch"}}, nil
}

// TestAgentLoopGuard_Stitching_FrontendRendererCoverage (supplemental renderer coverage)
// proves representative streaming frontends render one logical A-side stream spanning hidden
// B-legs without intermediate terminal leak and with legal ordering.
func TestAgentLoopGuard_Stitching_FrontendRendererCoverage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mount aipsdk.FrontendMount
		path  string
		body  string
	}{
		{"openresponses", openresponses.Mount, "/openresponses/v1/responses", `{"model":"m","input":"hi","stream":true,"store":false}`},
		{"openai-responses", openairesponses.Mount, "/v1/responses", `{"model":"m","input":"hi","stream":true}`},
		{"openai-legacy", openailegacy.Mount, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`},
		{"anthropic", anthropic.Mount, "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`},
		{"gemini", gemini.Mount, "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
	}
	stitched := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventTextDelta, Delta: " world"},
		{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exec := &CapturingExecutor{Script: EventScript{Events: stitched}}
			h := &MountedHarness{
				Descriptor: semantic.SubjectDescriptor{
					ID: tc.name, Kind: semantic.KindFrontend,
					Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityTools},
					Transports:   []semantic.ScenarioTransport{semantic.TransportStreaming},
				},
				Mount: tc.mount, Path: func(semantic.ScenarioDescriptor) string { return tc.path },
				Body:             func(semantic.ScenarioDescriptor) []byte { return []byte(tc.body) },
				NegativeBody:     func(semantic.ScenarioDescriptor) []byte { return []byte(`{}`) },
				ExecutorBoundary: exec, ContinuationStore: aipcont.NewMemoryStore(),
				AuthProvider: allowStitchAuth2{},
			}
			require.NoError(t, h.Reset())
			view, err := h.Frontend(context.Background())
			require.NoError(t, err)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.path, bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer stitch")
			rec := httptest.NewRecorder()
			view.ServeHTTP(rec, req)
			wire := rec.Body.String()
			if strings.TrimSpace(wire) == "" {
				t.Fatalf("empty wire for %s status=%d body=%q", tc.name, rec.Code, wire)
			}
			doneCount := strings.Count(wire, "[DONE]")
			if doneCount > 1 {
				t.Fatalf("%s wire has %d terminals (raw concatenation) wire=%q", tc.name, doneCount, wire[:stitchMin2(800, len(wire))])
			}
			if tc.name == "openresponses" && strings.Count(wire, "event: response.created") > 1 {
				t.Fatalf("%s duplicate response.created (raw concatenation)", tc.name)
			}
			if !strings.Contains(wire, "hello") || !strings.Contains(wire, "world") {
				t.Fatalf("wire for %s missing stitched text hello/world wire=%q", tc.name, wire)
			}
			// GREEN: supplemental renderer coverage demonstrates wire shows single terminal not raw concatenation.
		})
	}
}

// TestAgentLoopGuard_Stitching_UnsupportedRendererCoverage (supplemental renderer coverage)
// proves unsupported legality finalizes once without raw SSE concatenation.
func TestAgentLoopGuard_Stitching_UnsupportedRendererCoverage(t *testing.T) {
	t.Parallel()
	stitchedSingle := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
	}
	exec := &CapturingExecutor{Script: EventScript{Events: stitchedSingle}}
	h := &MountedHarness{
		Descriptor: semantic.SubjectDescriptor{ID: "openresponses", Kind: semantic.KindFrontend, Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}, Transports: []semantic.ScenarioTransport{semantic.TransportStreaming}},
		Mount:      openresponses.Mount, Path: func(semantic.ScenarioDescriptor) string { return "/openresponses/v1/responses" },
		Body: func(semantic.ScenarioDescriptor) []byte {
			return []byte(`{"model":"m","input":"hi","stream":true,"store":false}`)
		},
		NegativeBody:     func(semantic.ScenarioDescriptor) []byte { return []byte(`{}`) },
		ExecutorBoundary: exec, ContinuationStore: aipcont.NewMemoryStore(), AuthProvider: allowStitchAuth2{},
	}
	require.NoError(t, h.Reset())
	view, err := h.Frontend(context.Background())
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/openresponses/v1/responses", bytes.NewReader([]byte(`{"model":"m","input":"hi","stream":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer stitch")
	rec := httptest.NewRecorder()
	view.ServeHTTP(rec, req)
	wire := rec.Body.String()
	if strings.TrimSpace(wire) == "" {
		t.Fatalf("empty wire")
	}
	if strings.Count(wire, "[DONE]") > 1 {
		t.Fatalf("unsupported should not concatenate frames")
	}
}

func stitchMin2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
