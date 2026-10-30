package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/protobuf/proto"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Every counter a connector reports must survive the host conversion; a
// presence flag without its value reports a fabricated zero.
func TestEventToAipapi_UsageKeepsAllReportedCounters(t *testing.T) {
	t.Parallel()
	n := func(v int64) *int64 { return &v }
	ev, err := eventToAipapi(&backendplugin.CanonicalEvent{
		Kind: backendplugin.EventUsageDelta,
		Usage: &backendplugin.UsageEvidence{
			InputTokens:      n(8),
			OutputTokens:     n(1574),
			CacheReadTokens:  n(41000),
			CacheWriteTokens: n(2300),
			ReasoningTokens:  n(120),
			TotalTokens:      n(44882),
			Presence: backendplugin.UsagePresence{
				InputTokens: true, OutputTokens: true, CacheReadTokens: true,
				CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
			},
			RawUsageJSON: backendplugin.RawJSONAbsentValue(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 8, OutputTokens: 1574,
		CacheReadTokens: 41000, CacheWriteTokens: 2300, ReasoningTokens: 120, TotalTokens: 44882,
	}
	if ev.InputTokens != want.InputTokens || ev.OutputTokens != want.OutputTokens ||
		ev.CacheReadTokens != want.CacheReadTokens || ev.CacheWriteTokens != want.CacheWriteTokens ||
		ev.ReasoningTokens != want.ReasoningTokens || ev.TotalTokens != want.TotalTokens {
		t.Fatalf("usage counters = in %d out %d cacheR %d cacheW %d reas %d total %d, want %+v",
			ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheWriteTokens, ev.ReasoningTokens, ev.TotalTokens, want)
	}
}

// The input relation must reach the host with the counters: a connector that
// reports its cache reads outside the input counter keeps that declaration, so
// a frontend can translate the counters into its own input semantics.
func TestEventToAipapi_UsageKeepsCacheReadExclusion(t *testing.T) {
	t.Parallel()
	n := func(v int64) *int64 { return &v }
	for _, excluded := range []bool{true, false} {
		ev, err := eventToAipapi(&backendplugin.CanonicalEvent{
			Kind: backendplugin.EventUsageDelta,
			Usage: &backendplugin.UsageEvidence{
				InputTokens:                 n(300),
				CacheReadTokens:             n(600),
				Presence:                    backendplugin.UsagePresence{InputTokens: true, CacheReadTokens: true},
				RawUsageJSON:                backendplugin.RawJSONAbsentValue(),
				InputTokensExcludeCacheRead: excluded,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if ev.Accounting.InputTokensExcludeCacheRead != excluded {
			t.Fatalf("excluded=%v host event=%v", excluded, ev.Accounting.InputTokensExcludeCacheRead)
		}
	}
}

// The native quantities every host boundary must keep unchanged: the provider
// reports its cache reads and its cache writes outside the input counter.
const (
	hostUsageInput, hostUsageOutput, hostUsageRead, hostUsageWrite = 300, 20, 600, 100
	// hostUsageDedupe is the provider charge identity every boundary keeps.
	hostUsageDedupe = "host-usage-charge"
)

// hostUsageScenario is one provider usage report: the two independent
// cache-exclusion declarations and the total the provider claims. A nil total
// means the provider omitted it.
type hostUsageScenario struct {
	name         string
	excludeRead  bool
	excludeWrite bool
	total        *int64
}

// hostUsageScenarios covers both declarations independently in all four
// states, with an omitted and an explicit-zero provider total.
var hostUsageScenarios = []hostUsageScenario{
	{"inclusive omitted", false, false, nil},
	{"read-exclusive omitted", true, false, nil},
	{"write-exclusive omitted", false, true, nil},
	{"both-exclusive omitted", true, true, nil},
	{"inclusive zero", false, false, new(int64(0))},
	{"read-exclusive zero", true, false, new(int64(0))},
	{"write-exclusive zero", false, true, new(int64(0))},
	{"both-exclusive zero", true, true, new(int64(0))},
}

// hostUsageBillable is the provider-billable provenance the host-only
// accounting sideband and the billing finalizer must keep.
var hostUsageBillable = aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: hostUsageDedupe}

func hostUsageTotalValue(total *int64) int {
	if total == nil {
		return 0
	}
	return int(*total)
}

// hostUsageProducerEvent is the canonical usage event a connector publishes
// for the reported native quantities and the scenario's declarations.
func hostUsageProducerEvent(s hostUsageScenario, base aipapi.UsageAccountingMetadata) aipapi.Event {
	base.InputTokensExcludeCacheRead = s.excludeRead
	base.InputTokensExcludeCacheWrite = s.excludeWrite
	return aipapi.Event{
		Kind:             aipapi.EventUsageDelta,
		InputTokens:      hostUsageInput,
		OutputTokens:     hostUsageOutput,
		CacheReadTokens:  hostUsageRead,
		CacheWriteTokens: hostUsageWrite,
		TotalTokens:      hostUsageTotalValue(s.total),
		UsagePresence: aipapi.UsagePresence{
			InputTokens: true, OutputTokens: true, CacheReadTokens: true,
			CacheWriteTokens: true, TotalTokens: s.total != nil,
		},
		Accounting: base,
	}
}

// hostUsageMust fails the test when a wire hop fails.
func hostUsageMust(t *testing.T, err error, step string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

// hostEventThroughConnectorWire crosses the real connector wire for one
// canonical event: the SDK DTO, a real protobuf marshal and unmarshal, the
// decode back to the DTO and the real host conversion.
func hostEventThroughConnectorWire(t *testing.T, producer aipapi.Event) aipapi.Event {
	t.Helper()
	wire, err := backendplugin.CanonicalEventToProto(backendplugin.CanonicalEventFromAipapi(producer))
	hostUsageMust(t, err, "canonical event to proto")
	encoded, err := proto.Marshal(wire)
	hostUsageMust(t, err, "marshal canonical event")
	var decoded backendpluginv1.CanonicalEvent
	hostUsageMust(t, proto.Unmarshal(encoded, &decoded), "unmarshal canonical event")
	back, err := backendplugin.CanonicalEventFromProto(&decoded)
	hostUsageMust(t, err, "canonical event from proto")
	ev, err := eventToAipapi(back)
	hostUsageMust(t, err, "eventToAipapi")
	return ev
}

// hostEventFromNormalStream crosses the real connector wire for a canonical
// usage event and returns the host event the normal stream path builds.
func hostEventFromNormalStream(t *testing.T, s hostUsageScenario) aipapi.Event {
	t.Helper()
	return hostEventThroughConnectorWire(t, hostUsageProducerEvent(s, aipapi.UsageAccountingMetadata{}))
}

// hostEventFromSideband crosses the real connector sideband: the canonical
// usage event becomes a provider-billable accounting record, travels as a
// server frame and returns as the host accounting event.
func hostEventFromSideband(t *testing.T, s hostUsageScenario) aipapi.Event {
	t.Helper()
	producer := hostUsageProducerEvent(s, hostUsageBillable)
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.AddUsageEvent(producer, "host-usage-fallback")
	records := buffer.DrainAccountingEvidence()
	if len(records) != 1 {
		t.Fatalf("sideband records = %d, want 1", len(records))
	}
	wire, err := backendplugin.ServerFrameToProto(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameAccountingEvidence, Accounting: &records[0],
	})
	hostUsageMust(t, err, "server frame to proto")
	encoded, err := proto.Marshal(wire)
	hostUsageMust(t, err, "marshal server frame")
	var decoded backendpluginv1.ExecuteServerFrame
	hostUsageMust(t, proto.Unmarshal(encoded, &decoded), "unmarshal server frame")
	back, err := backendplugin.ServerFrameFromProto(&decoded)
	hostUsageMust(t, err, "server frame from proto")
	ev, err := accountingEvidenceToEvent(back.Accounting)
	hostUsageMust(t, err, "accountingEvidenceToEvent")
	return ev
}

// hostEventFromFinalizer crosses the real connector finalizer response and
// returns the host usage event the billing finalizer builds.
func hostEventFromFinalizer(t *testing.T, s hostUsageScenario) aipapi.Event {
	t.Helper()
	wire, err := backendplugin.FinalizeBillingResponseToProto(backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens:      new(int64(hostUsageInput)),
			OutputTokens:     new(int64(hostUsageOutput)),
			CacheReadTokens:  new(int64(hostUsageRead)),
			CacheWriteTokens: new(int64(hostUsageWrite)),
			TotalTokens:      s.total,
			Presence: backendplugin.UsagePresence{
				InputTokens: true, OutputTokens: true, CacheReadTokens: true,
				CacheWriteTokens: true, TotalTokens: s.total != nil,
			},
			RawUsageJSON:                 backendplugin.RawJSONAbsentValue(),
			InputTokensExcludeCacheRead:  s.excludeRead,
			InputTokensExcludeCacheWrite: s.excludeWrite,
		},
		EvidenceQuality: "provider",
	})
	hostUsageMust(t, err, "finalize response to proto")
	encoded, err := proto.Marshal(wire)
	hostUsageMust(t, err, "marshal finalize response")
	var decoded backendpluginv1.FinalizeBillingResponse
	hostUsageMust(t, proto.Unmarshal(encoded, &decoded), "unmarshal finalize response")
	back, err := backendplugin.FinalizeBillingResponseFromProto(&decoded)
	hostUsageMust(t, err, "finalize response from proto")
	ev, err := finalizeBillingResponseToEvent(back, hostUsageDedupe)
	hostUsageMust(t, err, "finalizeBillingResponseToEvent")
	return ev
}

// hostUsageAssert proves one host boundary kept the provider's declared
// relations and native quantities: a lost declaration or a changed counter
// changes the input a frontend reports.
func hostUsageAssert(t *testing.T, name string, ev aipapi.Event, s hostUsageScenario, want aipapi.UsageAccountingMetadata) {
	t.Helper()
	if ev.Accounting.InputTokensExcludeCacheRead != s.excludeRead ||
		ev.Accounting.InputTokensExcludeCacheWrite != s.excludeWrite {
		t.Fatalf("%s: declared exclusions = %+v, want read=%v write=%v", name, ev.Accounting, s.excludeRead, s.excludeWrite)
	}
	if ev.InputTokens != hostUsageInput || ev.OutputTokens != hostUsageOutput ||
		ev.CacheReadTokens != hostUsageRead || ev.CacheWriteTokens != hostUsageWrite {
		t.Fatalf("%s: native quantities changed: %+v", name, ev)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens ||
		!ev.UsagePresence.CacheReadTokens || !ev.UsagePresence.CacheWriteTokens {
		t.Fatalf("%s: counter presence changed: %+v", name, ev.UsagePresence)
	}
	if ev.UsagePresence.TotalTokens != (s.total != nil) || ev.TotalTokens != hostUsageTotalValue(s.total) {
		t.Fatalf("%s: total = %d presence = %v", name, ev.TotalTokens, ev.UsagePresence.TotalTokens)
	}
	if ev.Accounting.Plane != want.Plane || ev.Accounting.Source != want.Source ||
		ev.Accounting.Authority != want.Authority || ev.Accounting.DedupeKey != want.DedupeKey {
		t.Fatalf("%s: accounting metadata changed: %+v", name, ev.Accounting)
	}
	// The canonical projection of the two independent declarations: the input
	// quantity including the excluded reads and writes.
	wantInclusive := hostUsageInput
	if s.excludeRead {
		wantInclusive += hostUsageRead
	}
	if s.excludeWrite {
		wantInclusive += hostUsageWrite
	}
	var collected aipapi.Collected
	collected.AccumulateUsage(ev)
	if got := collected.InclusiveInputTokens(); got != wantInclusive {
		t.Fatalf("%s: inclusive input = %d, want %d", name, got, wantInclusive)
	}
}

// Every host usage boundary must restore both declarations next to unchanged
// native quantities, presence, plane, provenance and charge identity. All
// three boundaries are checked against one expected interpretation of the same
// provider report, so normal, sideband and finalized usage cannot disagree.
func TestHostUsageBoundariesPreserveIndependentCacheExclusions(t *testing.T) {
	t.Parallel()
	for _, s := range hostUsageScenarios {
		t.Run(s.name, func(t *testing.T) {
			hostUsageAssert(t, "normal stream", hostEventFromNormalStream(t, s), s, aipapi.UsageAccountingMetadata{})
			hostUsageAssert(t, "accounting sideband", hostEventFromSideband(t, s), s, hostUsageBillable)
			hostUsageAssert(t, "finalizer", hostEventFromFinalizer(t, s), s, hostUsageBillable)
		})
	}
}

// hostUsageChatWire is the usage a client observes on the Chat wire.
type hostUsageChatWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// The client-visible perspective the local reconstruction estimates: two input tokens and one output token per released output character of "hello".
const hostUsageClientInput, hostUsageClientOutput = 2, 5

// hostUsageCounter reconstructs the explicit client-visible perspective from
// the released content. It is a deterministic local estimate, no provider
// counters.
type hostUsageCounter struct{}

func (hostUsageCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: hostUsageClientInput, TotalTokens: hostUsageClientInput}, nil
}

func (hostUsageCounter) CountOutput(_ context.Context, in accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{OutputTokens: len(in.Text), TotalTokens: len(in.Text)}, nil
}

// hostUsageExecutor builds a real executor whose only provider output is the
// connector-round-tripped events; reconstruction adds the client perspective.
func hostUsageExecutor(t *testing.T, output []aipapi.Event, reconstruct bool) *runtime.Executor {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	hostUsageMust(t, err, "memory store")
	open := func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream(output), nil
	}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	testkit.WireConformanceExecutorSecureSession(t, ex)
	if reconstruct {
		ex.StreamUsage = accountingstream.New(hostUsageCounter{}, accountingstream.Config{})
	}
	ex.Backends = map[string]execbackend.Backend{"connector": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: open}}
	return ex
}

// hostUsageExecute runs the executor, encodes the executed canonical stream
// with the exported Chat encoder and returns the wire usage and the provider
// evidence the executor kept for the accounting consumers.
func hostUsageExecute(t *testing.T, s hostUsageScenario, reconstruct bool) (hostUsageChatWire, aipapi.Event) {
	t.Helper()
	text := hostEventThroughConnectorWire(t, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"})
	if text.Accounting.InputTokensExcludeCacheRead || text.Accounting.InputTokensExcludeCacheWrite {
		t.Fatalf("ordinary event carries a cache-exclusion declaration: %+v", text.Accounting)
	}
	output := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, text, hostEventFromNormalStream(t, s), {Kind: aipapi.EventResponseFinished, FinishReason: "stop"}}
	call := &aipapi.Call{ID: "host-usage-executor", Route: aipapi.RouteIntent{Selector: "connector:model-x"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
	stream, err := hostUsageExecutor(t, output, reconstruct).Execute(context.Background(), call)
	hostUsageMust(t, err, "execute")
	defer func() { _ = stream.Close() }()
	events := []aipapi.Event{}
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		hostUsageMust(t, err, "recv")
		events = append(events, ev)
	}
	rec := httptest.NewRecorder()
	err = openailegacy.WriteNonStreamJSON(context.Background(), rec, call, aipapi.NewFixedEventStream(events), openailegacy.EncodeOptions{})
	if err != nil {
		t.Fatalf("chat encode: %v", err)
	}
	var payload struct {
		Usage hostUsageChatWire `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode chat usage %s: %v", rec.Body.String(), err)
	}
	var provider aipapi.Event
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta && len(ev.UsageScopes) == 0 {
			provider = ev
		}
	}
	if provider.Kind != aipapi.EventUsageDelta {
		t.Fatalf("no provider usage evidence in the executed stream: %+v", events)
	}
	return payload.Usage, provider
}

// TestConnectorUsageReachesClientThroughExecutorProviderFallback proves the
// connector-to-client hop nothing covered yet: a usage event that crossed the
// real connector wire and the real host conversion, ran through a real
// executor, and reached the client through the exported Chat encoder.
//
// Without an explicit client-visible usage the provider fallback wins: the
// client observes the inclusive input, that is the input quantity plus the
// excluded cache reads and the excluded cache writes, and the wire total is
// that inclusive input plus the reported output. The provider total keeps its
// presence: an omitted total stays absent, an explicit zero stays present.
func TestConnectorUsageReachesClientThroughExecutorProviderFallback(t *testing.T) {
	t.Parallel()
	for _, s := range hostUsageScenarios {
		t.Run(s.name, func(t *testing.T) {
			got, provider := hostUsageExecute(t, s, false)
			inclusive := hostUsageInput
			if s.excludeRead {
				inclusive += hostUsageRead
			}
			if s.excludeWrite {
				inclusive += hostUsageWrite
			}
			if got != (hostUsageChatWire{PromptTokens: inclusive, CompletionTokens: hostUsageOutput, TotalTokens: inclusive + hostUsageOutput}) {
				t.Fatalf("chat usage = %+v, want prompt %d, completion %d, total %d", got, inclusive, hostUsageOutput, inclusive+hostUsageOutput)
			}
			hostUsageAssert(t, "provider evidence", provider, s, aipapi.UsageAccountingMetadata{})
		})
	}
}

// TestConnectorUsageReachesClientThroughExecutorSelectedPerspective proves the
// selection rule at the same seam: with the explicit client-visible usage
// supplied, the client observes that perspective alone, never the provider
// counters added to it, while the provider evidence keeps its own quantities
// and declarations.
func TestConnectorUsageReachesClientThroughExecutorSelectedPerspective(t *testing.T) {
	t.Parallel()
	s := hostUsageScenario{name: "both-exclusive omitted", excludeRead: true, excludeWrite: true}
	got, provider := hostUsageExecute(t, s, true)
	if got != (hostUsageChatWire{PromptTokens: hostUsageClientInput, CompletionTokens: hostUsageClientOutput, TotalTokens: hostUsageClientInput + hostUsageClientOutput}) {
		t.Fatalf("chat usage = %+v, want the selected client-visible perspective %d/%d/%d", got, hostUsageClientInput, hostUsageClientOutput, hostUsageClientInput+hostUsageClientOutput)
	}
	hostUsageAssert(t, "provider evidence", provider, s, aipapi.UsageAccountingMetadata{})
}
