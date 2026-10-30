package backendplugin_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// TestHostIntegration_OpenAIChatGPTPlanNonStreamCollectsOneStreamingAttempt is
// the host-facing observable-completion proof of task 5.3.
//
// The real staged connector executable is configured through the host's
// discover→trust→catalog→install→activate path, then driven through the
// backend-plugin Execute ABI with a NON-streaming downstream delivery mode.
// The host's canonical collection returns the complete downstream result while
// the reference server proves the provider saw exactly ONE ordinary streaming
// request (`stream:true`, `store:false`) with the selected registration's
// bearer token and no private Codex headers.
//
// The connector binary is its TEST-ONLY reference executable: the production
// configuration surface has no endpoint key (the bearer credential is only
// ever sent to the documented public Responses resource), so the local endpoint
// is supplied out of band by the staging helper. The ABI, the descriptor, the
// manifest and the connector service are the production ones.
//
// Nothing here adds a provider non-stream path: the downstream mode is carried
// on the invocation, the connector does not read it, and the local reference
// server observes the streaming request the connector actually sends.
func TestHostIntegration_OpenAIChatGPTPlanNonStreamCollectsOneStreamingAttempt(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	storePath := stagePlanHostStore(t)
	root := bpkit.StageOpenAIChatGPTPlanReference(t, reference.URL())

	res, reg := resolveCoexistenceCatalog(t, root)
	if res.CatalogErr != nil {
		t.Fatalf("catalog error: %v", res.CatalogErr)
	}
	host := processhost.NewHost(processhost.Config{})
	t.Cleanup(func() { _ = host.Close() })
	if err := runtimebundle.InstallDiscoveredExports(reg, host, res.Installable, runtimebundle.DiscoveredInstallOptions{}); err != nil {
		t.Fatalf("install discovered exports: %v", err)
	}

	// Only production keys reach Configure: the endpoint is not one of them.
	instanceConfig, err := yaml.Marshal(map[string]string{
		"profile_store_path": storePath,
	})
	if err != nil {
		t.Fatalf("marshal instance config: %v", err)
	}
	backend := buildCoexistenceBackend(t, reg, planKind, "plan-execute", string(instanceConfig))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	call := aipapi.Call{
		ID:      "plan-host-nonstream",
		Session: aipapi.SessionRef{ALegID: "aleg-plan-host"},
		// The downstream client asked for one complete result, not a stream.
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIResponses,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello plan")}}},
	}
	stream, err := backend.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "plan-execute", Model: planKind + "/gpt-5"},
		Key:     "plan-execute:" + planKind + "/gpt-5",
	})
	if err != nil {
		t.Fatalf("open the staged connector: %v", err)
	}

	// This is the host's non-streaming delivery path: collection over the
	// canonical stream, not a provider request of another shape.
	collected, err := aipapi.Collect(ctx, stream)
	if err != nil {
		t.Fatalf("collect the downstream non-stream result: %v", err)
	}
	if got := collected.Text.String(); got != "Hello from the plan." {
		t.Fatalf("downstream non-stream text = %q, want the complete provider text %q", got, "Hello from the plan.")
	}
	if !collected.FinishReceived {
		t.Fatal("the downstream non-stream result must carry the terminal completion")
	}
	if collected.InputTokens != 10 || collected.OutputTokens != 5 || collected.TotalTokens != 15 {
		t.Fatalf("downstream usage = %d/%d/%d, want the provider-reported 10/5/15", collected.InputTokens, collected.OutputTokens, collected.TotalTokens)
	}

	// The completed stream's usage reached the host-only accounting sideband
	// exactly once, through the negotiated V1 record. The canonical usage
	// event above stays client-visible; this is the separate host drain.
	source, ok := stream.(aipapi.UsageEvidenceSource)
	if !ok {
		t.Fatal("the host stream exposes no accounting evidence sideband")
	}
	evidence := source.DrainUsageEvidence()
	if len(evidence) != 1 {
		t.Fatalf("host accounting evidence records = %d, want exactly one per completed stream", len(evidence))
	}
	if evidence[0].Kind != aipapi.EventUsageDelta ||
		evidence[0].InputTokens != 10 || evidence[0].OutputTokens != 5 || evidence[0].TotalTokens != 15 {
		t.Fatalf("host accounting evidence = %+v, want the provider-reported 10/5/15 usage", evidence[0])
	}
	if evidence[0].Accounting.DedupeKey == "" {
		t.Fatal("host accounting evidence carries no dedupe identity")
	}

	requests := reference.captured()
	if len(requests) != 1 {
		t.Fatalf("the provider must observe exactly ONE request for a downstream non-stream call, got %d", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost {
		t.Fatalf("provider method = %s, want POST", request.Method)
	}
	if request.Path != "/v1/responses" {
		t.Fatalf("provider path = %q, want the public Responses path", request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer host-test-access-token" {
		t.Fatalf("provider Authorization = %q, want the staged registration's bearer token", got)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("decode provider request body: %v", err)
	}
	if stream, ok := body["stream"].(bool); !ok || !stream {
		t.Fatalf("provider request stream = %v, want true: the downstream non-stream mode must not create a provider non-stream path", body["stream"])
	}
	if store, ok := body["store"].(bool); !ok || store {
		t.Fatalf("provider request store = %v, want false", body["store"])
	}
	if model, _ := body["model"].(string); model != "gpt-5" {
		t.Fatalf("provider request model = %q, want the native provider slug %q", model, "gpt-5")
	}
	for _, forbidden := range []string{"originator", "chatgpt-account-id", "conversation_id", "session_id", "x-codex-task-type"} {
		if value := request.Header.Get(forbidden); value != "" {
			t.Fatalf("provider request must not carry the private header %q (value %q)", forbidden, value)
		}
	}
}

// planHostReference is one local provider answer and the requests it observed.
type planHostReference struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []planHostRequest
}

type planHostRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

func newPlanHostReference(t *testing.T) *planHostReference {
	t.Helper()
	reference := &planHostReference{}
	reference.server = httptest.NewServer(http.HandlerFunc(reference.handle))
	t.Cleanup(reference.server.Close)
	return reference
}

func (p *planHostReference) URL() string { return p.server.URL }

func (p *planHostReference) captured() []planHostRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]planHostRequest(nil), p.requests...)
}

func (p *planHostReference) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	p.mu.Lock()
	p.requests = append(p.requests, planHostRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   body,
	})
	p.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-request-id", "req_plan_host")
	w.WriteHeader(http.StatusOK)
	frames := []string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_host\",\"status\":\"in_progress\"}}\n\n",
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_host\",\"type\":\"message\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_host\",\"content_index\":0,\"delta\":\"Hello \"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_host\",\"content_index\":0,\"delta\":\"from the plan.\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_host\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_host\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello from the plan.\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n",
	}
	for _, frame := range frames {
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// stagePlanHostStore writes the connector's protected profile store with one
// selected registration.
//
// The document shape is the connector's documented local record: an explicit
// selection plus one profile per file. The token is a test value, and the
// expiry is in the future so the refresher returns the stored credential
// without a provider call.
func stagePlanHostStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "siwc-profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create profile store: %v", err)
	}
	record := map[string]any{
		"version":          1,
		"id":               "acct-host",
		"label":            "host test registration",
		"subject":          "subject-host",
		"issued_client_id": "client-host",
		"host_id":          "host-id",
		"granted_scopes":   []string{"openid", "chatgpt.tokens.use.direct"},
		"access_token":     "host-test-access-token",
		"refresh_token":    "host-test-refresh-token",
		"expiry":           time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	writePlanStoreDocument(t, filepath.Join(dir, "profile-acct-host.json"), record)
	writePlanStoreDocument(t, filepath.Join(dir, "selection.json"), map[string]any{
		"version":    1,
		"profile_id": "acct-host",
	})
	return dir
}

func writePlanStoreDocument(t *testing.T, path string, document map[string]any) {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal %s: %v", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}
