package backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/refbackend/bedrock"
	"github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	"github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

type standardProbe struct {
	mu    sync.Mutex
	count int
	last  CapturedRequest
}

func (p *standardProbe) RequestCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.count }
func (p *standardProbe) LastRequest() CapturedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func (p *standardProbe) Reset() { p.mu.Lock(); p.count = 0; p.last = CapturedRequest{}; p.mu.Unlock() }

// TestStandardComposition_FalseCapabilityMutationIsCaught proves the
// certification validates a subject's declared capabilities against the
// adapter's effective ones. The pairing is the evidence: the same subject
// certifies against a reachable emulator with matching credentials when the
// declaration matches the adapter, and must fail when the adapter loses the
// declared streaming capability. Only the mutation differs between the two
// runs, so a failure cannot be attributed to an unreachable origin or missing
// credentials.
func TestStandardComposition_FalseCapabilityMutationIsCaught(t *testing.T) {
	probe := &standardProbe{}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{AllowMissingBearer: true})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{OpenAI: []string{"test-openai"}}); err != nil {
		t.Fatal(err)
	}
	// certify runs one certification pass; dropStreaming mutates the adapter's
	// capability set while the subject keeps declaring streaming. The adapter is
	// rebuilt per boundary call (isolatedFamilyView) so credential cooldown
	// cannot leak between scenarios or passes.
	certify := func(t *testing.T, dropStreaming bool) error {
		t.Helper()
		be, _ := buildStandardFamily(t, reg, "openai-responses", srv.URL, srv.Client())
		caps := make([]aipapi.Capability, 0, len(be.Caps))
		for c := range be.Caps {
			caps = append(caps, c)
		}
		build := func(t *testing.T) ExecBackendView {
			t.Helper()
			fresh, freshModel := buildStandardFamily(t, reg, "openai-responses", srv.URL, srv.Client())
			if dropStreaming {
				delete(fresh.Caps, aipapi.CapabilityStreaming)
			}
			return ExecBackendView{Backend: fresh, Candidate: routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses", Model: freshModel}}, Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, TransportMode: aipapi.TransportModeStreaming}}
		}
		h := realExecHarness{subject: semantic.SubjectDescriptor{ID: "openai-responses", Kind: semantic.KindBackendFamily, Capabilities: caps, Dialects: be.DialectSupport, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming}}, view: isolatedFamilyView{build: build, t: t}, probe: probe}
		_, err := CertifyBackend(context.Background(), h)
		return err
	}
	if err := certify(t, false); err != nil {
		t.Fatalf("declaration matching the adapter did not certify: %v", err)
	}
	if err := certify(t, true); err == nil {
		t.Fatal("false streaming capability declaration was not caught")
	}
}

func TestStandardComposition_CertifiesEveryInProcessFamily(t *testing.T) {
	probe := &standardProbe{}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{AllowMissingBearer: true})))
	mux.Handle("/responses", countingHandler(probe, openResponsesCompatHandler()))
	mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
	mux.Handle("/systemone", countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","answers":{"q":{"type":"noul","noul":0.7}},"usage":{"input_tokens":1,"output_tokens":2}}`)
	})))
	mux.Handle("/v1/chat/completions", countingHandler(probe, openaichat.NewHandler(openaichat.Config{AllowMissingBearer: true})))
	mux.Handle("/v1/messages", countingHandler(probe, anthropicmessages.NewHandler(anthropicmessages.Config{AllowMissingAPIKey: true})))
	mux.Handle("/v1beta/models/m:generateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{AllowMissingAPIKey: true})))
	mux.Handle("/v1beta/models/m:streamGenerateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{AllowMissingAPIKey: true})))
	mux.Handle("/model/m/converse", countingHandler(probe, bedrock.NewHandler(bedrock.Config{AllowMissingAuthorization: true})))
	mux.Handle("/model/m/converse-stream", countingHandler(probe, bedrock.NewHandler(bedrock.Config{AllowMissingAuthorization: true})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tckClient := srv.Client()

	keys := standardplugins.UpstreamAPIKeys{OpenAI: []string{"test-openai"}, Anthropic: []string{"test-anthropic"}, Gemini: []string{"test-gemini"}, AlibabaTokenPlan: []string{"test-alibaba"}}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	for _, id := range standardplugins.EssentialBackendKinds() {
		t.Run(id, func(t *testing.T) {
			be, model := buildStandardFamily(t, reg, id, srv.URL, tckClient)
			if id == standardplugins.CustomSystemOneCompatibleID {
				// Decision-only families do not claim the legacy chat scenario corpus.
				// Execute their typed positive and hard-negative boundary contract.
				certifyStandardDecisionFamily(t, be, model, probe)
				return
			}
			if id == standardplugins.CustomOpenResponsesCompatibleID {
				delete(be.Caps, aipapi.CapabilityStreaming)
			}
			caps := make([]aipapi.Capability, 0, len(be.Caps))
			for c := range be.Caps {
				caps = append(caps, c)
			}
			mode := aipapi.TransportModeStreaming
			if id == standardplugins.CustomOpenResponsesCompatibleID {
				mode = aipapi.TransportModeNonStreaming
			}
			transports := []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming, semantic.TransportConnector}
			if id == standardplugins.CustomOpenResponsesCompatibleID {
				transports = []semantic.ScenarioTransport{semantic.TransportHTTP}
			}
			dialects := be.DialectSupport
			build := func(t *testing.T) ExecBackendView {
				t.Helper()
				fresh, freshModel := buildStandardFamily(t, reg, id, srv.URL, tckClient)
				if id == standardplugins.CustomOpenResponsesCompatibleID {
					delete(fresh.Caps, aipapi.CapabilityStreaming)
				}
				return ExecBackendView{Backend: fresh, Candidate: routing.AttemptCandidate{Primary: routing.Primary{Backend: id, Model: freshModel}}, Invocation: aipapi.Invocation{Operation: operationForFamily(id), TransportMode: mode}}
			}
			h := realExecHarness{subject: semantic.SubjectDescriptor{ID: id, Kind: semantic.KindBackendFamily, Capabilities: caps, Dialects: dialects, Transports: transports}, view: isolatedFamilyView{build: build, t: t}, probe: probe}
			cert, err := CertifyBackend(context.Background(), h)
			if err != nil {
				t.Fatalf("%v; last upstream request=%s", err, string(probe.LastRequest().Body))
			}
			if err := cert.ValidateReleaseReady(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type realExecHarness struct {
	subject semantic.SubjectDescriptor
	view    BackendView
	probe   UpstreamProbe
}

func (h realExecHarness) Subject() semantic.SubjectDescriptor          { return h.subject }
func (h realExecHarness) Backend(context.Context) (BackendView, error) { return h.view, nil }
func (h realExecHarness) Upstream() UpstreamProbe                      { return h.probe }
func (h realExecHarness) Reset(context.Context) error                  { h.probe.Reset(); return nil }

// isolatedFamilyView rebuilds the backend family for every boundary call so
// each TCK scenario certifies from a clean slate. Intentional error
// injection in one scenario (emulator temperature triggers) cools the
// single test credential, and without isolation every later scenario would
// inherit the cooldown instead of proving its own terminal semantics.
// Upstream accounting stays exact because the shared probe counts every
// call and CertifyBackend diffs per scenario.
type isolatedFamilyView struct {
	build func(t *testing.T) ExecBackendView
	t     *testing.T
}

func (v isolatedFamilyView) Open(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return v.build(v.t).Open(ctx, call)
}

func (v isolatedFamilyView) EffectiveCapabilities(ctx context.Context, call *aipapi.Call) BackendFacts {
	return v.build(v.t).EffectiveCapabilities(ctx, call)
}

func (v isolatedFamilyView) Probe(ctx context.Context, scenario semantic.ScenarioDescriptor, probe UpstreamProbe) (semantic.ExecutionEvidence, error) {
	return v.build(v.t).Probe(ctx, scenario, probe)
}

func (v isolatedFamilyView) Reject(ctx context.Context, call *aipapi.Call, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return v.build(v.t).Reject(ctx, call, scenario)
}

// TCKExecutesBoundary marks this adapter as executing Open itself during Probe.
func (v isolatedFamilyView) TCKExecutesBoundary() bool { return true }

// TCKInvocation exposes the operation metadata used by scenario construction.
func (v isolatedFamilyView) TCKInvocation() aipapi.Invocation {
	return v.build(v.t).TCKInvocation()
}

func countingHandler(probe *standardProbe, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		probe.mu.Lock()
		probe.count++
		probe.last = CapturedRequest{Method: r.Method, Path: r.URL.Path, Body: append([]byte(nil), body...)}
		probe.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func openResponsesCompatHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/compact") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c","object":"response.compaction","status":"completed","model":"m","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1}}`)
			return
		}
		if !strings.Contains(string(body), "stream") {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(string(body), "tools") {
				_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]},{"type":"function_call","id":"call_1","name":"weather","arguments":"{}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		usage := `{"input_tokens":10,"output_tokens":5,"total_tokens":15}`
		if strings.Contains(string(body), `"max_output_tokens":1`) {
			usage = `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`
		}
		stream := `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"r","status":"in_progress","model":"m"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"message","id":"msg","role":"assistant","status":"in_progress","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","item_id":"msg","content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg","content_index":0,"delta":"ok"}

event: response.completed
data: {"type":"response.completed","sequence_number":2,"response":{"id":"r","status":"completed","model":"m","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":USAGE}}`
		stream = strings.Replace(stream, "USAGE", usage, 1)
		_, _ = io.WriteString(w, stream)
	})
}

func buildStandardFamily(t *testing.T, reg *pluginreg.Registry, id, baseURL string, client *http.Client) (execbackend.Backend, string) {
	t.Helper()
	raw := ""
	model := "m"
	switch id {
	case "openai-responses", "openai-legacy":
		raw = "base_url: " + baseURL + "/v1\n"
	case "anthropic", "alibaba-token-plan-intl", "gemini":
		raw = "base_url: " + baseURL + "\n"
	case "bedrock":
		raw = "region: us-east-1\naccess_key_id: AKID\nsecret_access_key: SECRET\nbase_endpoint: " + baseURL + "\ndisable_https: true\n"
	case standardplugins.CustomOpenAILegacyCompatibleID, standardplugins.CustomOpenAIResponsesCompatibleID:
		raw = "backend_prefix: " + id + "\nbase_url: " + baseURL + "/v1\napi_key_env_var_root: TCK_KEY\n"
		t.Setenv("TCK_KEY", "test-key")
	case standardplugins.CustomAnthropicCompatibleID, standardplugins.CustomOpenResponsesCompatibleID:
		raw = "backend_prefix: " + id + "\nbase_url: " + baseURL + "\napi_key_env_var_root: TCK_KEY\n"
		t.Setenv("TCK_KEY", "test-key")
	case standardplugins.CustomSystemOneCompatibleID:
		raw = "backend_prefix: " + id + "\nbase_url: " + baseURL + "\nmodels:\n  source: inline\n  items:\n    - canonical_id: m\n      native_id: m\n"
	default:
		t.Fatalf("unhandled standard family %q", id)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	result, err := reg.BuildBackendWithLifecycle(id, id+"-tck", node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleanup != nil {
		t.Cleanup(func() { _ = result.Cleanup() })
	}
	return result.Backend, model
}

func operationForFamily(id string) aipapi.Operation {
	switch id {
	case "openai-responses", standardplugins.CustomOpenAIResponsesCompatibleID:
		return aipapi.OperationOpenAIResponses
	case "openai-legacy", standardplugins.CustomOpenAILegacyCompatibleID:
		return aipapi.OperationOpenAIChatCompletions
	default:
		return ""
	}
}

func certifyStandardDecisionFamily(t *testing.T, be execbackend.Backend, model string, probe *standardProbe) {
	t.Helper()
	probe.Reset()
	call := aipapi.Call{Decision: &aipapi.DecisionRequest{Evidence: []byte(`"tck-evidence"`), Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindNoul}}}, Invocation: aipapi.Invocation{
		Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming,
	}}
	candidate := routing.AttemptCandidate{Primary: routing.Primary{Backend: standardplugins.CustomSystemOneCompatibleID, Model: model}}
	stream, err := be.Open(t.Context(), call, candidate)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var events []aipapi.Event
	for {
		event, err := stream.Recv(t.Context())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if aipapi.ValidateEventSequence(events) != nil || len(events) != 4 || events[1].Kind != aipapi.EventDecisionResult || events[1].Decision == nil || events[1].Decision.Answers[0].PTrue != 0.7 || events[2].InputTokens != 1 || events[2].OutputTokens != 2 || probe.RequestCount() != 1 {
		t.Fatalf("decision family failed canonical/wire contract: %+v, requests=%d", events, probe.RequestCount())
	}
	if body := string(probe.LastRequest().Body); !strings.Contains(body, `"questions"`) || !strings.Contains(body, `"tck-evidence"`) {
		t.Fatalf("decision semantics missing upstream: %s", body)
	}
	chat := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
	streaming := call
	streaming.Invocation.TransportMode = aipapi.TransportModeStreaming
	for _, unsupported := range []aipapi.Call{chat, streaming} {
		stream, err := be.Open(t.Context(), unsupported, candidate)
		if stream != nil || !aipapi.IsReject(err) || probe.RequestCount() != 1 {
			t.Fatalf("unsupported decision-family semantics reached upstream: err=%v requests=%d", err, probe.RequestCount())
		}
	}
}
