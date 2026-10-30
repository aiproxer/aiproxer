package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

// The execution tests drive the direct factory through the public
// backend-plugin Execute path, against a local reference server that captures
// the exact wire request.
//
// Every proof below is downstream-mode aware: the reference server observes
// `stream:true` even when the invocation says non-streaming, because this
// connector has ONE provider path and the downstream mode is collection over
// it.

const (
	// referenceAccessToken is a test credential. It is not a secret and it is
	// used to prove which registration the request presented.
	referenceAccessToken  = "reference-access-token-not-a-secret"
	referenceRefreshToken = "reference-refresh-token-not-a-secret"
	referenceProfileID    = "acct-exec"
)

// completeScript is one successful provider answer: text in two deltas and one
// function call whose arguments stream in two fragments. The completed
// terminal repeats both output items and carries usage.
var completeScript = []string{
	sseFrame("response.created", `{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`),
	sseFrame("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"msg_1","type":"message"}}`),
	sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"Hello "}`),
	sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"from the plan."}`),
	sseFrame("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup"}}`),
	sseFrame("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"{\"q\":"}`),
	sseFrame("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"\"x\"}"}`),
	sseFrame("response.output_item.done", `{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`),
	sseFrame("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Hello from the plan."}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`),
}

// midStreamFailureScript delivers visible content and then ends the body
// without the completed terminal.
var midStreamFailureScript = []string{
	sseFrame("response.created", `{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`),
	sseFrame("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"msg_1","type":"message"}}`),
	sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"partial"}`),
}

// cancellationScript delivers one visible event and then holds the answer open
// until the request context is cancelled.
var cancellationScript = []string{
	sseFrame("response.created", `{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`),
	sseFrame("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"msg_1","type":"message"}}`),
	sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"held open"}`),
}

func sseFrame(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// capturedRequest is one request the reference server observed.
type capturedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// planReference is the local provider reference server.
type planReference struct {
	server *httptest.Server

	script          []string
	status          int
	stallUntilClose bool
	// errorBody is the provider error document of a non-200 answer.
	errorBody string
	// requestID is the correlation id of a non-200 answer.
	requestID string
	// requestIDs are the correlation ids of successive successful answers, in
	// request order. An exhausted or empty list keeps req_reference, and each
	// answer is one provider charge: the accounting proof scripts whether two
	// answers are distinct charges or one redelivered response.
	requestIDs []string
	// omitRequestID suppresses the successful answer's correlation header, as
	// a provider that reports no request identity would.
	omitRequestID bool

	mu         sync.Mutex
	requests   []capturedRequest
	cancelled  chan struct{}
	cancelOnce sync.Once
}

func newPlanReference(t *testing.T, script []string) *planReference {
	t.Helper()
	ref := &planReference{script: script, status: http.StatusOK, cancelled: make(chan struct{})}
	ref.server = httptest.NewServer(http.HandlerFunc(ref.handle))
	t.Cleanup(ref.server.Close)
	return ref
}

func (p *planReference) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	p.mu.Lock()
	index := len(p.requests)
	p.requests = append(p.requests, capturedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   body,
	})
	p.mu.Unlock()

	if p.status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		if p.requestID != "" {
			w.Header().Set("x-request-id", p.requestID)
		}
		w.WriteHeader(p.status)
		body := p.errorBody
		if body == "" {
			body = `{"error":{"message":"refused","type":"invalid_request_error","code":"test_refused"}}`
		}
		_, _ = io.WriteString(w, body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if !p.omitRequestID {
		w.Header().Set("x-request-id", p.successRequestID(index))
	}
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range p.script {
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if !p.stallUntilClose {
		return
	}
	select {
	case <-r.Context().Done():
		p.cancelOnce.Do(func() { close(p.cancelled) })
	case <-time.After(10 * time.Second):
	}
}

func (p *planReference) baseURL() string { return p.server.URL }

// successRequestID is the correlation id of one successful answer. A scripted
// id names one provider charge; the fallback keeps the historical constant for
// every test that does not care about charge identity.
func (p *planReference) successRequestID(index int) string {
	if index >= 0 && index < len(p.requestIDs) {
		return p.requestIDs[index]
	}
	return "req_reference"
}

func (p *planReference) captured() []capturedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedRequest(nil), p.requests...)
}

// stageProfileStore creates the protected SIWC store of one fresh record.
func stageProfileStore(t *testing.T) (string, siwc.ProfileRecord) {
	t.Helper()
	dir := t.TempDir()
	store, err := siwc.NewProfileStore(dir)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	record := siwc.ProfileRecord{
		Version:        1,
		ID:             referenceProfileID,
		Subject:        "subject-reference",
		IssuedClientID: "client-reference",
		HostID:         "host-reference",
		GrantedScopes:  []string{siwc.PlanUseScope},
		AccessToken:    referenceAccessToken,
		RefreshToken:   referenceRefreshToken,
		Expiry:         time.Now().Add(time.Hour).UTC(),
	}
	if err := store.SaveNew(record); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	return dir, record
}

// selectProfile records the explicit selection in the store.
func selectProfile(t *testing.T, storePath, profileID string) {
	t.Helper()
	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	if err := store.Select(profileID); err != nil {
		t.Fatalf("select profile %q: %v", profileID, err)
	}
}

// accountingNegotiation is the ABI session that negotiated the V1 accounting
// evidence sideband, as a current host does.
func accountingNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidence,
		EnabledFeatures: []string{backendplugin.FeatureAccountingEvidence},
	}
}

// configureForExecution configures the direct factory from an operator map and
// returns the concrete instance the test can drive.
//
// The reference server is supplied through the connector's TEST-ONLY
// constructor. The operator configuration surface has no endpoint key, so the
// store path (and the posture) are the only production settings these tests
// pass.
func configureForExecution(t *testing.T, referenceBaseURL string, cfg map[string]string) *instance {
	t.Helper()
	return configureForExecutionWithNegotiation(t, referenceBaseURL, cfg, accountingNegotiation())
}

// configureForExecutionWithNegotiation configures the direct factory for one
// explicit ABI session.
func configureForExecutionWithNegotiation(
	t *testing.T,
	referenceBaseURL string,
	cfg map[string]string,
	negotiation backendplugin.Negotiation,
) *instance {
	t.Helper()
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	service, err := NewWithReferenceEndpoint(referenceBaseURL)
	if err != nil {
		t.Fatalf("NewWithReferenceEndpoint: %v", err)
	}
	configured, err := service.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:  "execution-instance",
		FactoryKind: FactoryKind,
		ConfigYAML:  raw,
		Negotiation: negotiation,
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	inst, ok := configured.(*instance)
	if !ok {
		t.Fatalf("Configure returned %T, want the direct factory instance", configured)
	}
	return inst
}

// executeHarness is a minimal host-side ExecuteStream. The legacy coordination
// path reads the start frame and then only sends server frames.
//
// Canonical assistant text is reported on arrivedText: the harness signals the
// ACTUAL provider-derived content, not the ACCEPTED frame. ForwardExecute sends
// ACCEPTED before it opens the provider stream, so a notification on the first
// frame would prove nothing about upstream content and would make a cancellation
// test race the provider request.
type executeHarness struct {
	ctx   context.Context
	start backendplugin.ClientFrame

	mu      sync.Mutex
	started bool
	frames  []backendplugin.ServerFrame

	arrivedText chan string
}

func newExecuteHarness(ctx context.Context, inv backendplugin.Invocation) *executeHarness {
	return &executeHarness{
		ctx: ctx,
		start: backendplugin.ClientFrame{
			Kind:       backendplugin.ClientFrameStart,
			InstanceID: "execution-instance",
			Invocation: &inv,
		},
		arrivedText: make(chan string, 8),
	}
}

func (h *executeHarness) Context() context.Context { return h.ctx }

func (h *executeHarness) Recv() (backendplugin.ClientFrame, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started {
		h.started = true
		return h.start, nil
	}
	return backendplugin.ClientFrame{}, io.EOF
}

func (h *executeHarness) Send(frame backendplugin.ServerFrame) error {
	h.mu.Lock()
	h.frames = append(h.frames, frame)
	h.mu.Unlock()
	if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil &&
		frame.Event.Kind == backendplugin.EventTextDelta && frame.Event.Delta != nil {
		select {
		case h.arrivedText <- *frame.Event.Delta:
		default:
		}
	}
	return nil
}

func (h *executeHarness) snapshot() []backendplugin.ServerFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]backendplugin.ServerFrame(nil), h.frames...)
}

// invocation builds the canonical attempt the host hands to the connector.
func invocation(delivery aipapi.DeliveryMode, options backendplugin.GenerationOptions) backendplugin.Invocation {
	text := "hi"
	return backendplugin.Invocation{
		RequestID:        "req-exec",
		AttemptID:        "att-exec",
		ALegID:           "aleg-exec",
		BLegID:           "bleg-exec",
		CanonicalModelID: FactoryKind + "/gpt-5",
		NativeModelID:    "gpt-5",
		DeliveryMode:     string(delivery),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: options,
	}
}

func runExecute(ctx context.Context, inst *instance, inv backendplugin.Invocation) ([]backendplugin.ServerFrame, error) {
	h := newExecuteHarness(ctx, inv)
	err := inst.Execute(h)
	return h.snapshot(), err
}

// eventDeltas returns the concatenated assistant text carried by canonical
// event frames. Tool-argument fragments are a different carrier and are
// asserted separately, so they are not mixed into the assistant text.
func eventDeltas(frames []backendplugin.ServerFrame) string {
	var out strings.Builder
	for _, frame := range frames {
		if frame.Kind != backendplugin.ServerFrameEvent || frame.Event == nil || frame.Event.Delta == nil {
			continue
		}
		if frame.Event.Kind == backendplugin.EventTextDelta {
			out.WriteString(*frame.Event.Delta)
		}
	}
	return out.String()
}

// toolArgumentDeltas returns the concatenated tool-argument fragments.
func toolArgumentDeltas(frames []backendplugin.ServerFrame) string {
	var out strings.Builder
	for _, frame := range frames {
		if frame.Kind != backendplugin.ServerFrameEvent || frame.Event == nil || frame.Event.Delta == nil {
			continue
		}
		if frame.Event.Kind == backendplugin.EventToolCallArgsDelta {
			out.WriteString(*frame.Event.Delta)
		}
	}
	return out.String()
}

// assertUsageEvidence proves the completed stream's usage reached the V1
// accounting sideband exactly once, with provider-reported provenance and the
// non-secret local profile key. No price, rating or credential is present.
func assertUsageEvidence(t *testing.T, frames []backendplugin.ServerFrame, profileID string) {
	t.Helper()
	var found *backendplugin.AccountingEvidence
	for _, frame := range frames {
		if frame.Kind != backendplugin.ServerFrameAccountingEvidence {
			continue
		}
		if found != nil {
			t.Fatalf("more than one accounting evidence record in one stream")
		}
		if frame.Accounting == nil {
			t.Fatalf("accounting evidence frame carries no V1 record")
		}
		found = frame.Accounting
	}
	if found == nil {
		t.Fatal("the completed stream emitted no accounting evidence")
	}
	if found.Source != backendplugin.AccountingSourceProviderReported ||
		found.Authority != backendplugin.AccountingAuthorityAuthoritative ||
		found.Plane != backendplugin.AccountingPlaneProviderBillable {
		t.Fatalf("usage provenance = %s/%s/%s, want provider-reported/authoritative/provider-billable",
			found.Source, found.Authority, found.Plane)
	}
	if found.InputTokens == nil || *found.InputTokens != 10 ||
		found.OutputTokens == nil || *found.OutputTokens != 5 ||
		found.TotalTokens == nil || *found.TotalTokens != 15 {
		t.Fatalf("usage counters = %+v, want 10/5/15", found)
	}
	if !strings.Contains(found.DedupeKey, profileID) {
		t.Fatalf("dedupe key %q must name the non-secret local profile %q", found.DedupeKey, profileID)
	}
	if strings.Contains(found.DedupeKey, referenceAccessToken) || strings.Contains(found.DedupeKey, referenceRefreshToken) {
		t.Fatal("the accounting evidence key must not carry credential material")
	}
}

func countFrames(frames []backendplugin.ServerFrame) (events, evidence, terminals, failures int) {
	for _, frame := range frames {
		switch frame.Kind {
		case backendplugin.ServerFrameEvent:
			events++
		case backendplugin.ServerFrameAccountingEvidence:
			evidence++
		case backendplugin.ServerFrameTerminal:
			terminals++
			if frame.Terminal != nil && frame.Terminal.Status == backendplugin.TerminalFailure {
				failures++
			}
		}
	}
	return events, evidence, terminals, failures
}

// TestExecute_NonStreamingDownstreamUsesOneStreamingProviderRequest is the
// observable-completion proof of task 5.3: a downstream non-streaming
// invocation reaches the provider as exactly ONE ordinary streaming request,
// with the selected registration's bearer token, and the canonical text
// arrives in full.
func TestExecute_NonStreamingDownstreamUsesOneStreamingProviderRequest(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	requests := provider.captured()
	if len(requests) != 1 {
		t.Fatalf("the provider must be asked exactly once; a downstream non-streaming call must not retry or open a second path, got %d requests", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != responses.ResponsesPath {
		t.Fatalf("provider request = %s %s, want POST %s", request.Method, request.Path, responses.ResponsesPath)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("decode provider request body: %v", err)
	}
	if stream, ok := body["stream"].(bool); !ok || !stream {
		t.Fatalf("provider request stream = %v, want true: the connector has one streaming provider path for both downstream modes", body["stream"])
	}
	if store, ok := body["store"].(bool); !ok || store {
		t.Fatalf("provider request store = %v, want false", body["store"])
	}
	if model, _ := body["model"].(string); model != "gpt-5" {
		t.Fatalf("provider request model = %q, want the native slug %q", model, "gpt-5")
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+record.AccessToken {
		t.Fatalf("Authorization = %q, want the selected registration's bearer token", got)
	}
	for _, name := range []string{"Originator", "Chatgpt-Account-Id", "Conversation_id", "Session_id", "X-Codex-Task-Type"} {
		if value := request.Header.Get(name); value != "" {
			t.Fatalf("provider request must not carry the private Codex header %q (value %q)", name, value)
		}
	}

	events, evidence, terminals, failures := countFrames(frames)
	if events == 0 {
		t.Fatal("the downstream result must carry the canonical events, got none")
	}
	if evidence != 1 {
		t.Fatalf("one completed stream must emit exactly one accounting evidence record, got %d", evidence)
	}
	assertUsageEvidence(t, frames, record.ID)
	if terminals != 1 || failures != 0 {
		t.Fatalf("terminals=%d failures=%d, want one successful terminal", terminals, failures)
	}
	if text := eventDeltas(frames); text != "Hello from the plan." {
		t.Fatalf("canonical text = %q, want the complete provider text %q", text, "Hello from the plan.")
	}
	if args := toolArgumentDeltas(frames); args != `{"q":"x"}` {
		t.Fatalf("canonical tool arguments = %q, want the complete provider arguments %q", args, `{"q":"x"}`)
	}
}

// TestExecute_DoesNotEmitAccountingEvidenceWithoutNegotiation proves an older
// ABI session that did not negotiate the V1 sideband is never sent an
// accounting frame. The canonical client-visible usage event still flows
// normally.
func TestExecute_DoesNotEmitAccountingEvidenceWithoutNegotiation(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecutionWithNegotiation(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	}, backendplugin.Negotiation{})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, evidence, _, _ := countFrames(frames); evidence != 0 {
		t.Fatalf("an unnegotiated session received %d accounting evidence frame(s)", evidence)
	}
	if text := eventDeltas(frames); text != "Hello from the plan." {
		t.Fatalf("canonical text = %q, want the complete provider text", text)
	}
}

// TestOpenManagedStream_CollectionYieldsTheCompleteResult is the COLLECT
// proof: the host's non-streaming path is collection over the canonical
// stream, and the collected result carries the whole provider answer - every
// text delta and the complete tool call - not the first event.
func TestOpenManagedStream_CollectionYieldsTheCompleteResult(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	inv := invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{})
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatalf("CallFromInvocation: %v", err)
	}
	stream, err := inst.openManagedStream(context.Background(), inv, call)
	if err != nil {
		t.Fatalf("openManagedStream: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if collected.Text.String() != "Hello from the plan." {
		t.Fatalf("collected text = %q, want the complete provider text %q", collected.Text.String(), "Hello from the plan.")
	}
	if got := collected.ToolArgs["call_1"]; got == nil || got.String() != `{"q":"x"}` {
		t.Fatalf("collected tool arguments = %v, want the complete provider arguments %q", got, `{"q":"x"}`)
	}
	if !collected.FinishReceived {
		t.Fatal("the collected result must carry the terminal completion")
	}
	if collected.InputTokens != 10 || collected.OutputTokens != 5 || collected.TotalTokens != 15 {
		t.Fatalf("collected usage = %d/%d/%d, want 10/5/15", collected.InputTokens, collected.OutputTokens, collected.TotalTokens)
	}
	if requests := provider.captured(); len(requests) != 1 {
		t.Fatalf("collection must not open a second provider request, got %d", len(requests))
	}
}

// TestExecute_MidStreamFailureIsTerminalAndNotRetried proves the no-retry rule
// with visible output: once content reached the downstream client, a provider
// failure fails the attempt instead of restarting it.
func TestExecute_MidStreamFailureIsTerminalAndNotRetried(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, midStreamFailureScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("a stream that ends without response.completed must fail the attempt")
	}
	var failure *responses.StreamFailure
	if !errors.As(err, &failure) {
		t.Fatalf("Execute error %T: %v, want the typed *responses.StreamFailure", err, err)
	}
	if failure.Reason != responses.StreamFailureMissingTerminal {
		t.Fatalf("failure reason = %q, want %q", failure.Reason, responses.StreamFailureMissingTerminal)
	}
	requests := provider.captured()
	if len(requests) != 1 {
		t.Fatalf("a mid-stream failure must not restart the attempt: got %d provider requests, want exactly 1", len(requests))
	}
	_, evidence, terminals, failures := countFrames(frames)
	if evidence != 0 {
		t.Fatalf("a failed stream emitted %d accounting evidence record(s); only a completed stream may carry usage", evidence)
	}
	if terminals != 1 || failures != 1 {
		t.Fatalf("terminals=%d failures=%d, want exactly one failure terminal", terminals, failures)
	}
}

// TestExecute_CancellationAbortsTheUpstreamRequest proves a cancelled
// downstream context aborts the provider request and releases the connection,
// with no second attempt.
func TestExecute_CancellationAbortsTheUpstreamRequest(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, cancellationScript)
	provider.stallUntilClose = true
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newExecuteHarness(ctx, invocation(aipapi.DeliveryModeStreaming, backendplugin.GenerationOptions{}))
	done := make(chan error, 1)
	go func() { done <- inst.Execute(h) }()

	// Wait for the ACTUAL canonical text the reference server sent. The
	// ACCEPTED frame arrives before the provider stream exists, so waiting for
	// it (or sleeping after it) would not prove the request is in flight.
	awaitAssistantText(t, h, "held open")
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after the downstream context was cancelled")
	}
	select {
	case <-provider.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request context was not cancelled: the connection was not released")
	}
	if requests := provider.captured(); len(requests) != 1 {
		t.Fatalf("cancellation must not start another provider attempt: got %d requests", len(requests))
	}
	if _, _, _, failures := countFrames(h.snapshot()); failures != 0 {
		t.Fatalf("actual caller cancellation published %d failure terminal(s), want none", failures)
	}
}

// awaitAssistantText waits for one exact canonical assistant text delta.
//
// It is the cancellation test's synchronization barrier: the delta can only
// reach the downstream side after the provider request exists and the reference
// server flushed the text, so the cancel that follows it is issued against an
// in-flight attempt by construction.
func awaitAssistantText(t *testing.T, h *executeHarness, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	var delivered []string
	for {
		select {
		case got := <-h.arrivedText:
			if got == want {
				return
			}
			delivered = append(delivered, got)
		case <-deadline:
			t.Fatalf("the connector never delivered the canonical text %q (delivered deltas: %v)", want, delivered)
		}
	}
}

// TestExecute_ChecksTheSelectedRegistrationPerCall proves the selection is an
// Execute-time authority: an unusable selection fails before any provider I/O,
// and a later usable selection is then served.
func TestExecute_ChecksTheSelectedRegistrationPerCall(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	// No registration is selected yet.
	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("an attempt without a selected registration must fail before provider I/O")
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("an unusable selection reached the provider: %d request(s)", len(requests))
	}
	if _, _, terminals, failures := countFrames(frames); terminals != 1 || failures != 1 {
		t.Fatalf("terminals=%d failures=%d, want exactly one failure terminal", terminals, failures)
	}

	// The operator selects the registration; the next attempt uses it.
	selectProfile(t, storePath, record.ID)
	if _, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{})); err != nil {
		t.Fatalf("Execute after selecting the registration: %v", err)
	}
	if requests := provider.captured(); len(requests) != 1 {
		t.Fatalf("provider requests after the usable selection = %d, want 1", len(requests))
	}
}

// TestExecute_RefusesAnUnusableRegistrationBeforeProviderIO proves a
// registration that arrived but cannot authorize plan usage is refused, not
// treated as an absent selection.
func TestExecute_RefusesAnUnusableRegistrationBeforeProviderIO(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	storePath, _ := stageProfileStore(t)
	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	unusable := siwc.ProfileRecord{
		Version:        1,
		ID:             "acct-noscope",
		Subject:        "subject-reference",
		IssuedClientID: "client-reference",
		HostID:         "host-reference",
		GrantedScopes:  []string{"openid"},
		AccessToken:    referenceAccessToken,
		RefreshToken:   referenceRefreshToken,
		Expiry:         time.Now().Add(time.Hour).UTC(),
	}
	if err := store.SaveNew(unusable); err != nil {
		t.Fatalf("save unusable profile: %v", err)
	}
	selectProfile(t, storePath, unusable.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	_, err = runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err == nil {
		t.Fatal("a registration without the plan-use scope must fail before provider I/O")
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("an unusable registration reached the provider: %d request(s)", len(requests))
	}
}

// actionOnFirstRecord runs action on the first slog record and then discards
// every record. It is the deterministic hook the J1 regressions use: the
// attempt's omission is recorded AFTER admission and BEFORE the credential is
// acquired, so acting there schedules an operator action in exactly the window
// the earlier implementation left unbound.
type actionOnFirstRecord struct {
	once   *sync.Once
	action func()
	base   slog.Handler
}

func (h *actionOnFirstRecord) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *actionOnFirstRecord) Handle(ctx context.Context, record slog.Record) error {
	h.once.Do(h.action)
	return h.base.Handle(ctx, record)
}

func (h *actionOnFirstRecord) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *actionOnFirstRecord) WithGroup(string) slog.Handler { return h }

// useFirstRecordHook installs action on the default logger for one attempt.
func useFirstRecordHook(t *testing.T, action func()) {
	t.Helper()
	previous := slog.Default()
	slog.SetDefault(slog.New(&actionOnFirstRecord{
		once:   &sync.Once{},
		action: action,
		base:   slog.NewJSONHandler(io.Discard, nil),
	}))
	t.Cleanup(func() { slog.SetDefault(previous) })
}

// omittedGenerationOption is a supported canonical control the preview policy
// must record as an omission under the default posture. It is how these
// regressions place an operator action in the window between admission and
// credential acquisition.
func omittedGenerationOption() backendplugin.GenerationOptions {
	temperature := int32(500)
	return backendplugin.GenerationOptions{TemperatureMillis: &temperature}
}

// TestExecute_SelectionSwitchDuringAdmissionCannotBypassTheScopeCheck proves an
// attempt is bound to the registration it admitted: a selection switched to
// another, unchecked registration after admission must fail before provider
// I/O, never send a request.
//
// The switch is scheduled deterministically on the omission log record, which
// the attempt writes after its admission check and before it acquires the
// credential.
func TestExecute_SelectionSwitchDuringAdmissionCannotBypassTheScopeCheck(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	// A fresh registration that would FAIL the entry check: it holds only the
	// identity scope, never the direct plan-use grant.
	unchecked := siwc.ProfileRecord{
		Version:        1,
		ID:             "acct-switched",
		Subject:        "subject-switched",
		IssuedClientID: "client-switched",
		HostID:         record.HostID,
		GrantedScopes:  []string{"openid"},
		AccessToken:    "switched-access-token-not-a-secret",
		RefreshToken:   "switched-refresh-token-not-a-secret",
		Expiry:         time.Now().Add(time.Hour).UTC(),
	}
	if err := store.SaveNew(unchecked); err != nil {
		t.Fatalf("save the switched registration: %v", err)
	}
	useFirstRecordHook(t, func() {
		if selectErr := store.Select(unchecked.ID); selectErr != nil {
			t.Errorf("switch the selection during the attempt: %v", selectErr)
		}
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, omittedGenerationOption()))
	if err == nil {
		t.Fatalf("a selection switched after admission must fail the attempt; Execute served it with %d provider request(s)", len(provider.captured()))
	}
	if !errors.Is(err, ErrSelectedRegistrationUnusable) {
		t.Fatalf("Execute error %v, want the unusable-registration refusal", err)
	}
	// The refusal names the BOUND registration, so an operator can tell which
	// identity the attempt was authorized for. The classified wrapper never
	// renders its cause, so inspect the cause directly.
	if cause := errors.Unwrap(err); cause == nil || !strings.Contains(cause.Error(), record.ID) {
		t.Fatalf("the refusal must name the bound registration %q: %v", record.ID, err)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("an unchecked registration reached the provider: %d request(s)", len(requests))
	}
	if _, _, terminals, failures := countFrames(frames); terminals != 1 || failures != 1 {
		t.Fatalf("terminals=%d failures=%d, want exactly one failure terminal", terminals, failures)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Error == nil || terminal.Error.Code != backendplugin.ErrorCodeFailedPrecondition || terminal.Error.Retryable {
		t.Fatalf("terminal error=%+v, want a non-retryable failed_precondition for the unusable registration", terminal.Error)
	}
}

// TestExecute_CredentialReplacementDuringAdmissionIsRevalidatedBeforeProviderIO
// proves the record that would SUPPLY the credential is validated, not just the
// record read at entry: a registration replaced with a scope-less one during
// the attempt is refused before any provider I/O.
func TestExecute_CredentialReplacementDuringAdmissionIsRevalidatedBeforeProviderIO(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	// Same local registration, replaced with a fresh credential that carries
	// no direct plan-use grant. The refresh path would happily hand it out.
	replaced := record
	replaced.AccessToken = "replaced-access-token-not-a-secret"
	replaced.RefreshToken = "replaced-refresh-token-not-a-secret"
	replaced.GrantedScopes = []string{"openid"}
	replaced.Expiry = time.Now().Add(time.Hour).UTC()
	useFirstRecordHook(t, func() {
		if saveErr := store.Save(replaced); saveErr != nil {
			t.Errorf("replace the registration credential during the attempt: %v", saveErr)
		}
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, omittedGenerationOption()))
	if err == nil {
		t.Fatalf("a credential replaced with a scope-less registration must fail the attempt; Execute served it with %d provider request(s)", len(provider.captured()))
	}
	if !errors.Is(err, ErrSelectedRegistrationUnusable) {
		t.Fatalf("Execute error %v, want the unusable-registration refusal", err)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("a scope-less replacement reached the provider: %d request(s)", len(requests))
	}
	if _, _, terminals, failures := countFrames(frames); terminals != 1 || failures != 1 {
		t.Fatalf("terminals=%d failures=%d, want exactly one failure terminal", terminals, failures)
	}
}

// TestExecute_CredentialRotationDuringAdmissionUsesTheValidatedReplacement is
// the guard against over-refusal: a replacement that carries a FRESH credential
// with the plan-use grant is the credential the attempt sends, because the
// record that supplied it is validated before provider I/O.
func TestExecute_CredentialRotationDuringAdmissionUsesTheValidatedReplacement(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	store, err := siwc.NewProfileStore(storePath)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	rotated := record
	rotated.AccessToken = "rotated-access-token-not-a-secret"
	rotated.RefreshToken = "rotated-refresh-token-not-a-secret"
	rotated.Expiry = time.Now().Add(time.Hour).UTC()
	useFirstRecordHook(t, func() {
		if saveErr := store.Save(rotated); saveErr != nil {
			t.Errorf("rotate the registration credential during the attempt: %v", saveErr)
		}
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, omittedGenerationOption()))
	if err != nil {
		t.Fatalf("a valid replacement credential must still serve the attempt: %v", err)
	}
	requests := provider.captured()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want exactly 1", len(requests))
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer "+rotated.AccessToken {
		t.Fatalf("Authorization = %q, want the validated replacement credential", got)
	}
	if text := eventDeltas(frames); text != "Hello from the plan." {
		t.Fatalf("canonical text = %q, want the complete provider text", text)
	}
	assertUsageEvidence(t, frames, record.ID)
}

// TestExecute_PolicyModeIsTheOperatorPosture proves the 4.3 policy receives its
// operator posture from configuration: strict refuses the unsupported control,
// the default serves it with an explicit logged omission.
func TestExecute_PolicyModeIsTheOperatorPosture(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	temperature := int32(500)
	options := backendplugin.GenerationOptions{TemperatureMillis: &temperature}

	strict := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
		"policy_mode":        string(responses.PolicyModeStrict),
	})
	strictFrames, err := runExecute(context.Background(), strict, invocation(aipapi.DeliveryModeNonStreaming, options))
	if !errors.Is(err, responses.ErrRequestNotAdmissible) {
		t.Fatalf("strict Execute error %v, want the preview-policy refusal", err)
	}
	if cause := errors.Unwrap(err); cause == nil || !strings.Contains(cause.Error(), string(responses.PolicyReasonGenerationOption)) {
		t.Fatalf("strict Execute error %q must name the omitted generation option", err)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("a strict refusal reached the provider: %d request(s)", len(requests))
	}
	strictTerminal := terminalFrameOf(t, strictFrames)
	if strictTerminal.Error == nil || strictTerminal.Error.Code != backendplugin.ErrorCodeFailedPrecondition || strictTerminal.Error.Retryable {
		t.Fatalf("strict terminal error=%+v, want a non-retryable failed_precondition for the policy refusal", strictTerminal.Error)
	}

	// The default posture serves the same call and records the degradation.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	interoperable := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})
	if _, err := runExecute(context.Background(), interoperable, invocation(aipapi.DeliveryModeNonStreaming, options)); err != nil {
		t.Fatalf("interoperable Execute: %v", err)
	}
	if requests := provider.captured(); len(requests) != 1 {
		t.Fatalf("the interoperable posture must serve the request once, got %d requests", len(requests))
	}
	recorded := logs.String()
	for _, want := range []string{"Options.Temperature", string(responses.PolicyReasonGenerationOption)} {
		if !strings.Contains(recorded, want) {
			t.Fatalf("the omission of %q was not recorded; log=%s", want, recorded)
		}
	}
}

// TestExecute_DoesNotConfigureWithoutARegistrationStore pins the explicit
// pre-I/O failure of an instance that has no store to authorize an attempt.
func TestExecute_DoesNotConfigureWithoutARegistrationStore(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("an instance without a registration store must fail with ErrExecutionUnavailable, got %v", err)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("an instance without a store reached a provider: %d request(s)", len(requests))
	}
	events, evidence, terminals, failures := countFrames(frames)
	if events != 0 || evidence != 0 {
		t.Fatalf("events=%d evidence=%d, want no fabricated output or usage", events, evidence)
	}
	if terminals != 1 || failures != 1 {
		t.Fatalf("terminals=%d failures=%d, want exactly one failure terminal", terminals, failures)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Error == nil || terminal.Error.Code != backendplugin.ErrorCodeFailedPrecondition || terminal.Error.Retryable {
		t.Fatalf("terminal error=%+v, want a non-retryable failed_precondition for the missing registration", terminal.Error)
	}
}

// ensure the test file's reference server URL satisfies the transport's
// loopback exception rather than silently relying on it.
func TestPlanReferenceURLIsLoopback(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, nil)
	if _, err := responses.EndpointForBaseURL(provider.baseURL()); err != nil {
		t.Fatalf("reference server URL %q is not accepted by the transport seam: %v", provider.baseURL(), err)
	}
}
