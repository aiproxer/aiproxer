package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	secureapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingobs "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/observability"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestExecutorStreamAccountingRecordsProviderAndClientVisibleUsage(t *testing.T) {
	t.Parallel()

	ex := newStreamAccountingExecutor(t, streamAccountingOptions{})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-basic", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)

	usage := usageEventsOf(events)
	if len(usage) != 2 {
		t.Fatalf("usage events = %d, want provider + final scoped client usage", len(usage))
	}
	if usage[0].Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		t.Fatalf("first usage plane = %q, want provider_billable", usage[0].Accounting.Plane)
	}
	if hasUsageScopePlane(usage[1], aipapi.UsagePlaneProviderBillable) {
		t.Fatalf("final synthesized usage replayed provider_billable scope already emitted to client: %+v", usage[1])
	}
	if got := usage[1].UsageScopes[len(usage[1].UsageScopes)-1]; got.Accounting.Plane != aipapi.UsagePlaneClientVisible || got.OutputTokens != len("hello") {
		t.Fatalf("final usage = %+v, want client_visible output count for visible text", got)
	}
	if usageEventIndex(events, aipapi.UsagePlaneClientVisible) > eventKindIndex(events, aipapi.EventResponseFinished) {
		t.Fatalf("client_visible usage emitted after response_finished: %+v", events)
	}
	for _, ev := range usage {
		if ev.CostPresent || ev.CostNanoUnits != 0 || ev.CostSource != "" {
			t.Fatalf("protocol usage must not carry stream monetary fields after Phase 8: %+v", ev)
		}
	}
}

// TestExecutorStreamAccountingFeedsSelectedPerspectiveToClientFrontends proves
// the executor-to-frontend seam: the real executor output, with stream usage
// reconstruction enabled, reaches the Chat and Responses encoders and the
// client sees the reconstructed client-visible perspective alone.
//
// The fixture's provider reports 3 input / 11 output tokens. The local counter
// reconstructs 2 input tokens and len("hello") output tokens for the
// client-visible perspective. The client must observe the reconstructed
// counters (2/5/7), never the sum of both accounting perspectives (5/16), while
// the provider evidence keeps its original quantities for accounting consumers.
func TestExecutorStreamAccountingFeedsSelectedPerspectiveToClientFrontends(t *testing.T) {
	t.Parallel()

	for _, tc := range clientFrontendCases {
		t.Run(tc.name, func(t *testing.T) {
			ex := newStreamAccountingExecutor(t, streamAccountingOptions{})
			call := accountingCall("acct-frontend-"+tc.name, "openai:gpt-test")
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			recorder := &recordingEventStream{EventStream: stream}
			defer func() { _ = recorder.Close() }()
			got := tc.usage(t, call, recorder)
			events := recorder.recorded()

			usage := usageEventsOf(events)
			if len(usage) != 2 {
				t.Fatalf("usage events = %d, want provider + client-visible usage: %+v", len(usage), events)
			}
			if usage[0].Accounting.Plane != aipapi.UsagePlaneProviderBillable {
				t.Fatalf("first usage plane = %q, want provider_billable", usage[0].Accounting.Plane)
			}
			if usage[0].InputTokens != 3 || usage[0].OutputTokens != 11 || usage[0].TotalTokens != 14 {
				t.Fatalf("provider usage = %d/%d/%d, want the original 3/11/14 retained", usage[0].InputTokens, usage[0].OutputTokens, usage[0].TotalTokens)
			}
			if usageEventIndex(events, aipapi.UsagePlaneClientVisible) > eventKindIndex(events, aipapi.EventResponseFinished) {
				t.Fatalf("client_visible usage emitted after response_finished: %+v", events)
			}
			if want := (clientUsageCounts{input: 2, output: 5, total: 7}); got != want {
				t.Fatalf("client usage = %+v, want the selected perspective %+v instead of the provider+client sum", got, want)
			}
		})
	}
}

// TestExecutorStreamAccountingKeepsUnscopedUsageFallbackWithoutReconstruction
// proves the fallback of requirement 3.2 at the same seam: without stream
// usage reconstruction the turn supplies no explicit client-visible usage, so
// the unscoped provider usage stays the client fallback.
func TestExecutorStreamAccountingKeepsUnscopedUsageFallbackWithoutReconstruction(t *testing.T) {
	t.Parallel()

	for _, tc := range clientFrontendCases {
		t.Run(tc.name, func(t *testing.T) {
			ex := newStreamAccountingExecutor(t, streamAccountingOptions{DisableStreamUsage: true})
			call := accountingCall("acct-fallback-"+tc.name, "openai:gpt-test")
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			got := tc.usage(t, call, stream)
			if want := (clientUsageCounts{input: 3, output: 11, total: 14}); got != want {
				t.Fatalf("client usage = %+v, want the unscoped provider fallback %+v", got, want)
			}
		})
	}
}

// clientFrontendCases feeds the executed canonical stream to one frontend
// encoder and reports the usage the client observes on the wire.
var clientFrontendCases = []struct {
	name  string
	usage func(t *testing.T, call *aipapi.Call, es aipapi.EventStream) clientUsageCounts
}{
	{name: "chat_stream", usage: chatStreamClientUsage},
	{name: "chat_nonstream", usage: chatNonStreamClientUsage},
	{name: "responses_stream", usage: responsesStreamClientUsage},
	{name: "responses_nonstream", usage: responsesNonStreamClientUsage},
}

// clientUsageCounts are the usage counters a client observes on the wire.
type clientUsageCounts struct {
	input  int
	output int
	total  int
}

type chatUsageWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type responsesUsageWire struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// recordingEventStream captures the canonical events a frontend encoder
// consumes, so one execution proves both the emitted perspectives and the
// wire usage derived from them.
type recordingEventStream struct {
	aipapi.EventStream
	mu     sync.Mutex
	events []aipapi.Event
}

func (s *recordingEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.EventStream.Recv(ctx)
	if err != nil {
		return ev, err
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	return ev, nil
}

func (s *recordingEventStream) recorded() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]aipapi.Event(nil), s.events...)
}

func chatStreamClientUsage(t *testing.T, call *aipapi.Call, es aipapi.EventStream) clientUsageCounts {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteStreamSSE(context.Background(), rec, call, es, openailegacy.EncodeOptions{}); err != nil {
		t.Fatalf("chat stream encode: %v", err)
	}
	out := clientUsageCounts{}
	found := false
	done := false
	for _, frame := range testkit.ParseRecorderSSE(rec) {
		if frame.Data == "[DONE]" {
			done = true
			continue
		}
		var chunk struct {
			Usage *chatUsageWire `json:"usage"`
		}
		decodeUsageJSON(t, []byte(frame.Data), &chunk)
		if chunk.Usage != nil {
			out = clientUsageCounts{chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, chunk.Usage.TotalTokens}
			found = true
		}
	}
	if !done {
		t.Fatalf("chat stream missing the [DONE] terminal: %s", rec.Body.String())
	}
	if !found {
		t.Fatalf("chat stream missing usage: %s", rec.Body.String())
	}
	return out
}

func chatNonStreamClientUsage(t *testing.T, call *aipapi.Call, es aipapi.EventStream) clientUsageCounts {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(context.Background(), rec, call, es, openailegacy.EncodeOptions{}); err != nil {
		t.Fatalf("chat nonstream encode: %v", err)
	}
	var payload struct {
		Usage chatUsageWire `json:"usage"`
	}
	decodeUsageJSON(t, rec.Body.Bytes(), &payload)
	return clientUsageCounts{payload.Usage.PromptTokens, payload.Usage.CompletionTokens, payload.Usage.TotalTokens}
}

func responsesStreamClientUsage(t *testing.T, call *aipapi.Call, es aipapi.EventStream) clientUsageCounts {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(context.Background(), rec, call, es, openairesponses.EncodeOptions{}); err != nil {
		t.Fatalf("responses stream encode: %v", err)
	}
	for _, frame := range testkit.ParseRecorderSSE(rec) {
		if frame.Event != "response.completed" {
			continue
		}
		var payload struct {
			Response struct {
				Usage *responsesUsageWire `json:"usage"`
			} `json:"response"`
		}
		decodeUsageJSON(t, []byte(frame.Data), &payload)
		if payload.Response.Usage == nil {
			t.Fatalf("responses completed missing usage: %s", frame.Data)
		}
		return clientUsageCounts{payload.Response.Usage.InputTokens, payload.Response.Usage.OutputTokens, payload.Response.Usage.TotalTokens}
	}
	t.Fatalf("responses stream missing the response.completed terminal: %s", rec.Body.String())
	return clientUsageCounts{}
}

func responsesNonStreamClientUsage(t *testing.T, call *aipapi.Call, es aipapi.EventStream) clientUsageCounts {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, es, openairesponses.EncodeOptions{}); err != nil {
		t.Fatalf("responses nonstream encode: %v", err)
	}
	var payload struct {
		Usage responsesUsageWire `json:"usage"`
	}
	decodeUsageJSON(t, rec.Body.Bytes(), &payload)
	return clientUsageCounts{payload.Usage.InputTokens, payload.Usage.OutputTokens, payload.Usage.TotalTokens}
}

func decodeUsageJSON(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode usage body %s: %v", body, err)
	}
}

func TestExecutorStreamAccountingRecordsSynthesizedClientUsage(t *testing.T) {
	t.Parallel()

	recorder := &recordingSecureRecorder{}
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{
		SecureSessionRecorder: recorder,
	})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-secure-usage", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)
	if got := len(usageEventsOf(events)); got != 2 {
		t.Fatalf("client usage_delta events = %d, want provider + synthesized client usage: %+v", got, events)
	}

	records := recorder.usageRecords()
	if len(records) != 2 {
		t.Fatalf("secure-session usage records = %d, want provider + synthesized client usage: %+v", len(records), records)
	}
	if got := (clientUsageCounts{input: int(records[0].InputTokens), output: int(records[0].OutputTokens), total: int(records[0].TotalTokens)}); got != (clientUsageCounts{input: 3, output: 11, total: 14}) {
		t.Fatalf("first secure-session usage record = %+v, want the provider quantities 3/11/14", got)
	}
	if got := (clientUsageCounts{input: int(records[1].InputTokens), output: int(records[1].OutputTokens), total: int(records[1].TotalTokens)}); got != (clientUsageCounts{input: 2, output: 5, total: 7}) {
		t.Fatalf("second secure-session usage record = %+v, want the synthesized client perspective 2/5/7", got)
	}
}

func TestExecutorStreamAccountingCountsTransformedClientVisibleOutputSeparately(t *testing.T) {
	t.Parallel()

	hook := responseHookFunc(func(_ context.Context, ev *aipapi.Event, _ sdkhooks.PartMeta) error {
		if ev.Kind == aipapi.EventTextDelta {
			ev.Delta = "redacted"
		}
		return nil
	})
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{ResponseHooks: []sdkhooks.ResponsePartHook{hook}})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-transform", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)

	if !containsTextDelta(events, "redacted") || containsTextDelta(events, "hello") {
		t.Fatalf("events did not expose transformed text only: %+v", events)
	}
	usage := usageEventsOf(events)
	if len(usage) != 2 {
		t.Fatalf("usage events = %d, want provider + client-visible usage: %+v", len(usage), events)
	}
	if usage[0].Accounting.Plane != aipapi.UsagePlaneProviderBillable || usage[0].InputTokens != 3 || usage[0].OutputTokens != 11 || usage[0].TotalTokens != 14 {
		t.Fatalf("provider usage = %+v, want the original 3/11/14 retained", usage[0])
	}
	client := usage[len(usage)-1]
	scope := client.UsageScopes[len(client.UsageScopes)-1]
	if scope.Accounting.Plane != aipapi.UsagePlaneClientVisible || scope.InputTokens != 2 || scope.OutputTokens != 8 || scope.TotalTokens != 10 {
		t.Fatalf("client-visible usage = %+v, want 2/8/10 for the transformed %q", client, "redacted")
	}
}

func TestExecutorStreamAccountingStripsProviderReportedStreamCostFromProtocolUsage(t *testing.T) {
	t.Parallel()

	ex := newStreamAccountingExecutor(t, streamAccountingOptions{EmitProviderCost: true})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-strip-cost", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)
	if eventKindIndex(events, aipapi.EventResponseFinished) == len(events) {
		t.Fatalf("response_finished missing: %+v", events)
	}
	for _, ev := range usageEventsOf(events) {
		if !hasUsageScopePlane(ev, aipapi.UsagePlaneClientVisible) {
			continue
		}
		if ev.CostPresent || ev.CostNanoUnits != 0 || ev.Currency != "" || ev.CostSource != "" {
			t.Fatalf("client-visible usage retained stream cost after Phase 8: %+v", ev)
		}
	}
}

func TestExecutorStreamAccountingOutputCountTimeoutStillEmitsProviderUsageAndFinished(t *testing.T) {
	t.Parallel()

	counter := timeoutStreamCounter{inner: blockingStreamCounter{}, timeout: 10 * time.Millisecond}
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Counter: counter})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-output-timeout", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)
	usage := usageEventsOf(events)
	if len(usage) != 2 {
		t.Fatalf("usage events = %d, want provider usage plus the input-only client fallback: %+v", len(usage), events)
	}
	if usage[0].Accounting.Plane != aipapi.UsagePlaneProviderBillable || usage[0].InputTokens != 3 || usage[0].OutputTokens != 11 || usage[0].TotalTokens != 14 {
		t.Fatalf("provider usage = %+v, want 3/11/14 preserved despite the output-count timeout", usage[0])
	}
	client := usage[1]
	scope := client.UsageScopes[len(client.UsageScopes)-1]
	if scope.Accounting.Plane != aipapi.UsagePlaneClientVisible || scope.InputTokens != 2 || scope.OutputTokens != 0 || scope.TotalTokens != 2 {
		t.Fatalf("client fallback = %+v, want the input-only 2/0/2 perspective", client)
	}
	if eventKindIndex(events, aipapi.EventResponseFinished) == len(events) {
		t.Fatalf("response_finished missing after an output-count timeout: %+v", events)
	}
}

func TestExecutorStreamAccountingRecordsPreOutputFailedAttemptUsageSeparately(t *testing.T) {
	t.Parallel()

	ex := newStreamAccountingExecutor(t, streamAccountingOptions{FailFirstPreOutputWithUsage: true})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-pre-output-fail", "openai:gpt-test|other:gpt-test-2"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)
	usage := usageEventsOf(events)
	if len(usage) != 3 {
		t.Fatalf("usage events = %d, want failed-attempt, retry, and client usage: %+v", len(usage), events)
	}
	if got := usage[0]; got.Accounting.Plane != aipapi.UsagePlaneProviderBillable || got.InputTokens != 7 || got.OutputTokens != 0 || got.TotalTokens != 7 {
		t.Fatalf("failed-attempt usage = %+v, want its own provider record 7/0/7", got)
	}
	if got := usage[1]; got.Accounting.Plane != aipapi.UsagePlaneProviderBillable || got.InputTokens != 3 || got.OutputTokens != 11 || got.TotalTokens != 14 {
		t.Fatalf("retry usage = %+v, want the retry's provider record 3/11/14", got)
	}
	if eventKindIndex(events, aipapi.EventUsageDelta) > eventKindIndex(events, aipapi.EventResponseStarted) {
		t.Fatalf("failed-attempt usage must reach the client before the retry starts: %+v", events)
	}
	if eventKindIndex(events, aipapi.EventResponseFinished) == len(events) {
		t.Fatalf("response_finished missing after pre-output failover: %+v", events)
	}
}

func TestExecutorStreamAccountingPostOutputFailureDoesNotRetry(t *testing.T) {
	t.Parallel()

	ex := newStreamAccountingExecutor(t, streamAccountingOptions{FailAfterOutputWithUsage: true})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-post-output-fail", "openai:gpt-test|other:gpt-test-2"))
	if err != nil {
		t.Fatal(err)
	}
	events := []aipapi.Event{}
	var terminalErr error
	for {
		ev, recvErr := stream.Recv(context.Background())
		if recvErr != nil {
			terminalErr = recvErr
			break
		}
		events = append(events, ev)
	}
	if closeErr := stream.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if terminalErr == nil || errors.Is(terminalErr, io.EOF) {
		t.Fatalf("post-output failure = %v, want a surfaced failure instead of a clean EOF", terminalErr)
	}
	if aipapi.IsRecoverablePreOutput(terminalErr) {
		t.Fatalf("post-output failure = %v, must not stay recoverable pre-output after committed content", terminalErr)
	}
	var upstream *aipapi.UpstreamFailureError
	if !errors.As(terminalErr, &upstream) || upstream.Phase != aipapi.PhasePostOutput || upstream.Recoverable {
		t.Fatalf("post-output failure = %T %v, want a non-recoverable UpstreamFailureError in PhasePostOutput", terminalErr, terminalErr)
	}
	if got := countEventKind(events, aipapi.EventResponseStarted); got != 1 {
		t.Fatalf("response_started events = %d, want exactly one attempt: %+v", got, events)
	}
	if got := countEventKind(events, aipapi.EventTextDelta); got != 1 {
		t.Fatalf("text_delta events = %d, want exactly one attempt's content: %+v", got, events)
	}
	usage := usageEventsOf(events)
	if len(usage) != 1 || usage[0].InputTokens != 3 || usage[0].OutputTokens != 4 || usage[0].TotalTokens != 7 {
		t.Fatalf("usage events = %+v, want only the failed attempt's 3/4/7", usage)
	}
}

type streamAccountingOptions struct {
	ResponseHooks               []sdkhooks.ResponsePartHook
	Counter                     accountingstream.Counter
	Observability               *accountingobs.Stats
	SecureSessionRecorder       secureapp.GateRecording
	FailFirstPreOutputWithUsage bool
	FailAfterOutputWithUsage    bool
	EmitProviderCost            bool
	DisableStreamUsage          bool
}

func newStreamAccountingExecutor(t *testing.T, opts streamAccountingOptions) *runtime.Executor {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	counter := accountingstream.Counter(streamCountFunc(func(_ context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
		return accountingapp.CountResult{OutputTokens: len(input.Text), TotalTokens: len(input.Text), Accounting: localAccountingMeta()}, nil
	}))
	if opts.Counter != nil {
		counter = opts.Counter
	}
	obs := opts.Observability
	if obs == nil {
		obs = accountingobs.NewStats()
	}
	providerUsage := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 11, TotalTokens: 14,
		Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative},
	}
	if opts.EmitProviderCost {
		providerUsage.CostPresent = true
		providerUsage.CostNanoUnits = 99
		providerUsage.Currency = "USD"
		providerUsage.CostSource = string(aipapi.UsageSourceProviderReported)
	}
	var opens int
	open := func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		opens++
		if opts.FailFirstPreOutputWithUsage && opens == 1 {
			return &errAfterEventsStream{events: []aipapi.Event{
				{Kind: aipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 0, TotalTokens: 7, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative}},
			}, err: &aipapi.UpstreamFailureError{Phase: aipapi.PhasePreOutput, Recoverable: true, Reason: "pre-output accounting failure"}}, nil
		}
		if opts.FailAfterOutputWithUsage && opens == 1 {
			// The upstream reports a recoverable pre-output classification after
			// it already emitted visible content. Commitment, not the upstream
			// label, must decide that no retry happens.
			return &errAfterEventsStream{events: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hello"},
				{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 4, TotalTokens: 7, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative}},
			}, err: &aipapi.UpstreamFailureError{Phase: aipapi.PhasePreOutput, Recoverable: true, Reason: "post-output accounting failure"}}, nil
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
			providerUsage,
			{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
		}), nil
	}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{ResponsePartHooks: opts.ResponseHooks})
	ex.Rand = routing.NewSeededRng(1)
	if !opts.DisableStreamUsage {
		ex.StreamUsage = accountingstream.New(counter, accountingstream.Config{})
	}
	ex.TokenAccountingObservability = obs
	ex.SecureSessionRecordingMandatory = false
	ex.SecureSessionRecorder = opts.SecureSessionRecorder
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: open,
		},
		"other": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: open,
		},
	}
	return ex
}

type errAfterEventsStream struct {
	events []aipapi.Event
	err    error
}

func (s *errAfterEventsStream) Recv(context.Context) (aipapi.Event, error) {
	if len(s.events) == 0 {
		return aipapi.Event{}, s.err
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}

func (s *errAfterEventsStream) Close() error { return nil }

func (s *errAfterEventsStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type blockingStreamCounter struct{}

func (blockingStreamCounter) CountCall(ctx context.Context, _ accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 2, TotalTokens: 2, Accounting: localAccountingMeta()}, nil
}

func (blockingStreamCounter) CountOutput(ctx context.Context, _ accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	<-ctx.Done()
	return accountingapp.CountResult{}, ctx.Err()
}

type timeoutStreamCounter struct {
	inner   accountingstream.Counter
	timeout time.Duration
}

func (c timeoutStreamCounter) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	child, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.inner.CountCall(child, input)
}

func (c timeoutStreamCounter) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	child, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.inner.CountOutput(child, input)
}

// recordingSecureRecorder captures the usage evidence the secure-session
// boundary accepted, so a test can prove both the provider and synthesized
// client usage records exist rather than only counting event names.
type recordingSecureRecorder struct {
	mu    sync.Mutex
	usage []secureapp.StreamEventRecordInput
}

func (r *recordingSecureRecorder) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (r *recordingSecureRecorder) RecordPostHookStreamEvent(_ context.Context, in secureapp.StreamEventRecordInput) error {
	if in.EventKind != string(aipapi.EventUsageDelta) {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage = append(r.usage, in)
	return nil
}

func (r *recordingSecureRecorder) usageRecords() []secureapp.StreamEventRecordInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]secureapp.StreamEventRecordInput(nil), r.usage...)
}

type streamCountFunc func(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error)

func (f streamCountFunc) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 2, TotalTokens: 2, Accounting: localAccountingMeta()}, nil
}

func (f streamCountFunc) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return f(ctx, input)
}

func localAccountingMeta() aipapi.UsageAccountingMetadata {
	return aipapi.UsageAccountingMetadata{Source: aipapi.UsageSourceLocalEstimator, Authority: aipapi.UsageAuthorityEstimated}
}

func accountingCall(id, selector string) *aipapi.Call {
	return &aipapi.Call{ID: id, Route: aipapi.RouteIntent{Selector: selector}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
}

func drainEvents(t *testing.T, stream aipapi.EventStream) []aipapi.Event {
	t.Helper()
	defer func() {
		if err := stream.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	events := []aipapi.Event{}
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
}

func usageEventsOf(events []aipapi.Event) []aipapi.Event {
	out := []aipapi.Event{}
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			out = append(out, ev)
		}
	}
	return out
}

func usageEventIndex(events []aipapi.Event, plane aipapi.UsagePlane) int {
	for i, ev := range events {
		if ev.Kind != aipapi.EventUsageDelta {
			continue
		}
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == plane {
				return i
			}
		}
	}
	return len(events)
}

func hasUsageScopePlane(ev aipapi.Event, plane aipapi.UsagePlane) bool {
	if ev.Accounting.Plane == plane {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if scope.Accounting.Plane == plane {
			return true
		}
	}
	return false
}

func eventKindIndex(events []aipapi.Event, kind aipapi.EventKind) int {
	for i, ev := range events {
		if ev.Kind == kind {
			return i
		}
	}
	return len(events)
}

func countEventKind(events []aipapi.Event, kind aipapi.EventKind) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

func containsTextDelta(events []aipapi.Event, text string) bool {
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && strings.Contains(ev.Delta, text) {
			return true
		}
	}
	return false
}

type responseHookFunc func(context.Context, *aipapi.Event, sdkhooks.PartMeta) error

func (f responseHookFunc) ID() string                        { return "test-transform" }
func (f responseHookFunc) Order() int                        { return 0 }
func (f responseHookFunc) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (f responseHookFunc) HandleEvent(ctx context.Context, ev *aipapi.Event, meta sdkhooks.PartMeta) error {
	return f(ctx, ev, meta)
}
