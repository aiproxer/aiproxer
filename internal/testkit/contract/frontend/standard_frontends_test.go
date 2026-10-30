package frontend

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
)

func TestBundledFrontends_CertifyIndependentlyWithCapturingExecutor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		mount        funcFrontendMount
		path         string
		body         string
		negativeBody func(semantic.ScenarioDescriptor) []byte
	}{
		{"openai-responses", openairesponses.Mount, "/v1/responses", `{"model":"m","input":"hi"}`, openAIResponsesNegativeBody},
		{"openresponses", openresponses.Mount, "/openresponses/v1/responses", `{"model":"m","input":"hi"}`, openResponsesNegativeBody},
		{"openai-legacy", openailegacy.Mount, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, openAILegacyNegativeBody},
		{"anthropic", anthropic.Mount, "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, anthropicNegativeBody},
		{"gemini", gemini.Mount, "/v1beta/models/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, geminiNegativeBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			executor := &CapturingExecutor{Script: EventScript{Events: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "ok"},
				{Kind: aipapi.EventResponseFinished},
			}}}
			caps := getFrontendCapabilities(tc.name)
			dialects := getFrontendDialects(tc.name)
			h := &MountedHarness{
				Descriptor: semantic.SubjectDescriptor{
					ID:           tc.name,
					Kind:         semantic.KindFrontend,
					Capabilities: caps,
					Dialects:     dialects,
					Transports:   []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming, semantic.TransportWebSocket},
				},
				Mount: tc.mount, Path: func(sc semantic.ScenarioDescriptor) string {
					if tc.name == "gemini" && (sc.Transport == semantic.TransportStreaming || sc.ID == "cancellation") {
						return "/v1beta/models/m:streamGenerateContent?alt=sse"
					}
					if tc.name == "openresponses" && sc.ID == "compaction-lifecycle" {
						return "/openresponses/v1/responses/compact"
					}
					return tc.path
				}, Body: func(sc semantic.ScenarioDescriptor) []byte {
					return []byte(frontendScenarioBody(tc.name, string(sc.ID)))
				},
				NegativeBody:     tc.negativeBody,
				Decorate:         withFrontendCancellation,
				ExecutorBoundary: executor, ContinuationStore: aipcont.NewMemoryStore(),
				AuthProvider: allowFrontendTCKAuth{},
			}
			cert, err := CertifyFrontend(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			if err := cert.ValidateReleaseReady(); err != nil {
				t.Fatal(err)
			}
			if len(executor.Calls) == 0 {
				t.Fatal("frontend TCK captured no canonical calls")
			}
		})
	}
}

func TestBundledFrontends_ProveAuthenticationBeforeSensitiveWork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mount funcFrontendMount
		path  string
		body  string
	}{
		{"openai-responses", openairesponses.Mount, "/v1/responses", `{"model":"m","input":"hi"}`},
		{"openresponses", openresponses.Mount, "/openresponses/v1/responses", `{"model":"m","input":"hi"}`},
		{"openai-legacy", openailegacy.Mount, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic", anthropic.Mount, "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"gemini", gemini.Mount, "/v1beta/models/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := &CapturingExecutor{}
			h := &MountedHarness{
				Descriptor:       semantic.SubjectDescriptor{ID: tc.name, Kind: semantic.KindFrontend},
				Mount:            tc.mount,
				Path:             func(semantic.ScenarioDescriptor) string { return tc.path },
				Body:             func(semantic.ScenarioDescriptor) []byte { return []byte(tc.body) },
				ExecutorBoundary: executor,
				AuthProvider:     denyFrontendTCKAuth{},
			}
			if err := h.Reset(); err != nil {
				t.Fatalf("mount failed: %v", err)
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.Handler.ServeHTTP(rec, req)
			if len(executor.Calls) != 0 {
				t.Fatalf("%s executor reached despite denied request (calls=%d)", tc.name, len(executor.Calls))
			}
			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("%s denied request was not rejected with 4xx, got %d", tc.name, rec.Code)
			}
		})
	}
}

type allowFrontendTCKAuth struct{}

func (allowFrontendTCKAuth) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "frontend-tck"}}, nil
}

type denyFrontendTCKAuth struct{}

func (denyFrontendTCKAuth) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized, Body: []byte("authentication required")}, nil
}

func getFrontendCapabilities(name string) []aipapi.Capability {
	switch name {
	case "openai-responses":
		return []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
		}
	case "openresponses":
		return []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityStructuredOutputs,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityOrderedItems,
			aipapi.CapabilityItemReferences,
			aipapi.CapabilityCompaction,
			aipapi.CapabilityOpaqueExtensions,
			aipapi.CapabilityReasoningReplay,
		}
	case "anthropic":
		return []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
		}
	case "openai-legacy":
		return []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
		}
	case "gemini":
		return []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
		}
	default:
		return []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
		}
	}
}

func getFrontendDialects(name string) aipapi.DialectSupport {
	if name == "openresponses" {
		return aipapi.DialectSupport{
			ItemDialects: []aipapi.DialectRequirement{
				{Kind: "item", Dialect: "item_reference"},
			},
			ReasoningDialects: []aipapi.DialectRequirement{
				{Kind: "reasoning", Dialect: "reasoning_replay"},
			},
			ExtensionTypes: []aipapi.ExtensionRequirement{
				{Namespace: "com.example", Type: "custom"},
			},
		}
	}
	return aipapi.DialectSupport{}
}

func frontendScenarioBody(frontend, scenario string) string {
	legacy := frontend == "openai-legacy"
	anthropic := frontend == "anthropic"
	gemini := frontend == "gemini"
	if scenario == "text-baseline" || scenario == "text-streaming" || scenario == "usage-present" || scenario == "usage-zero" || scenario == "recoverable-error" || scenario == "terminal-error" || scenario == "cancellation" || scenario == "terminal-clean-text" || scenario == "terminal-output-limit" || scenario == "terminal-upstream-error" || scenario == "terminal-usage-reported" || scenario == "terminal-content-filter" || scenario == "terminal-empty-turn" || scenario == "terminal-error-taxonomy" {
		if anthropic {
			return `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		if gemini {
			return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
		}
		if legacy {
			return `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		return `{"model":"m","input":"hi","stream":true}`
	}
	if anthropic {
		switch scenario {
		case "tools-execution", "tool-call-replay", "tool-result-replay", "terminal-clean-tool-call":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}]}`
		case "vision-input":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}`
		case "documents-input":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"SGVsbG8="}}]}]}`
		case "reasoning-output":
			return `{"model":"m","max_tokens":16,"thinking":{"type":"enabled","budget_tokens":8},"messages":[{"role":"user","content":"think"}]}`
		case "structured-output":
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"json"}],"metadata":{"output_format":"json"}}`
		default:
			return `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
		}
	}
	if gemini {
		if scenario == "tools-execution" || scenario == "tool-call-replay" || scenario == "tool-result-replay" || scenario == "terminal-clean-tool-call" {
			return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"object"}}]}]}`
		}
		return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	}
	if legacy {
		if scenario == "tools-execution" || scenario == "tool-call-replay" || scenario == "tool-result-replay" || scenario == "terminal-clean-tool-call" {
			return `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`
		}
		return `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	}
	switch scenario {
	case "tools-execution", "tool-call-replay", "tool-result-replay", "terminal-clean-tool-call":
		return `{"model":"m","input":"hi","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`
	case "vision-input":
		return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}]}`
	case "documents-input":
		return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read"},{"type":"input_file","file_data":"SGVsbG8=","filename":"note.txt"}]}]}`
	case "structured-output":
		return `{"model":"m","input":"json","text":{"format":{"type":"json_object"}}}`
	case "reasoning-output":
		return `{"model":"m","input":"think","reasoning":{"effort":"medium"}}`
	case "compaction-lifecycle":
		if frontend == "openresponses" {
			return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"compact"}]}]}`
		}
		return `{"model":"m","input":"compact"}`
	case "ordered-items":
		return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	case "item-reference-dialect":
		return `{"model":"m","input":[{"type":"item_reference","id":"msg_prev"}]}`
	case "opaque-extension-type":
		if frontend == "openresponses" {
			return `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"com.example:custom","data":{}}]}]}`
		}
		return `{"model":"m","input":"hi"}`
	case "reasoning-replay-dialect":
		if frontend == "openresponses" {
			return `{"model":"m","input":[{"type":"reasoning","id":"reas_1","reasoning":"Thinking step..."}]}`
		}
		return `{"model":"m","input":"hi"}`
	default:
		return `{"model":"m","input":[{"type":"unknown_semantic_item"}]}`
	}
}

func openAIResponsesNegativeBody(semantic.ScenarioDescriptor) []byte {
	return []byte(`{"model":"m","input":[{"type":"unsupported_semantic_item"}]}`)
}

func openResponsesNegativeBody(semantic.ScenarioDescriptor) []byte {
	return []byte(`{"model":"m","input":[{"type":"unsupported_semantic_item"}]}`)
}

func openAILegacyNegativeBody(semantic.ScenarioDescriptor) []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"invalid_tool"}]}`)
}

func anthropicNegativeBody(semantic.ScenarioDescriptor) []byte {
	return []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"invalid_semantic_part"}]}]}`)
}

func geminiNegativeBody(semantic.ScenarioDescriptor) []byte {
	return []byte(`{"contents":[{"role":"user","parts":[{"type":"invalid_semantic_part"}]}]}`)
}

func withFrontendCancellation(view FrontendView) FrontendView {
	mounted, ok := view.(mountedView)
	if !ok {
		return view
	}
	return cancellationView{mountedView: mounted}
}

type cancellationView struct {
	mountedView
}

func (v cancellationView) Probe(ctx context.Context, scenario semantic.ScenarioDescriptor, executor *CapturingExecutor) (semantic.ExecutionEvidence, error) {
	if !scenario.IsCancellation() {
		return v.mountedView.Probe(ctx, scenario, executor)
	}
	body := v.body(scenario)
	return frontendCancellationProbe(ctx, v, v.path, executor, body, len(executor.Calls))
}

func frontendCancellationProbe(ctx context.Context, handler http.Handler, path func(semantic.ScenarioDescriptor) string, executor *CapturingExecutor, body []byte, beforeCalls int) (semantic.ExecutionEvidence, error) {
	type blockingState struct {
		recv   chan struct{}
		seen   chan struct{}
		closed chan struct{}
	}
	state := blockingState{recv: make(chan struct{}), seen: make(chan struct{}), closed: make(chan struct{})}
	var recvOnce, seenOnce, closeOnce sync.Once
	executor.Script.Stream = func(streamCtx context.Context) aipapi.EventStream {
		return &blockingEventStream{ctx: streamCtx, recv: func() { recvOnce.Do(func() { close(state.recv) }) }, seen: func() { seenOnce.Do(func() { close(state.seen) }) }, closed: func() { closeOnce.Do(func() { close(state.closed) }) }}
	}
	defer func() { executor.Script.Stream = nil }()

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	req := httptest.NewRequestWithContext(reqCtx, http.MethodPost, path(semantic.ScenarioDescriptor{ID: "cancellation"}), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Session-ID", "frontend-tck-session")
	rec := httptest.NewRecorder()
	go func() { handler.ServeHTTP(rec, req); close(done) }()
	if err := waitSignal(state.recv, "cancellation never reached the stream"); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	cancel()
	if err := waitSignal(state.seen, "stream did not observe cancellation"); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	if err := waitSignal(state.closed, "frontend did not close canceled stream"); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	if err := waitSignal(done, "cancellation request stalled"); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	if len(executor.Calls)-beforeCalls != 1 {
		return semantic.ExecutionEvidence{}, fmt.Errorf("cancellation boundary calls=%d, want 1", len(executor.Calls)-beforeCalls)
	}
	return semantic.ExecutionEvidence{ScenarioID: "cancellation", Executed: true, BoundaryCalls: 1, Accepted: true, CanonicalCall: true, ErrorMapped: true, Cancelled: true}, nil
}

type blockingEventStream struct {
	ctx    context.Context
	recv   func()
	seen   func()
	closed func()
}

func (s *blockingEventStream) Recv(context.Context) (aipapi.Event, error) {
	s.recv()
	<-s.ctx.Done()
	s.seen()
	return aipapi.Event{}, s.ctx.Err()
}
func (s *blockingEventStream) Close() error { s.closed(); return nil }

func waitSignal(ch <-chan struct{}, message string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("frontend TCK: %s", message)
	}
}

type funcFrontendMount = aipsdk.FrontendMount
