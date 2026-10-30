package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftoolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftraffictranscript"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// dropToolNamed removes tools matching name (catalog filter test).
type dropToolNamed struct{ name string }

func (d dropToolNamed) ID() string                        { return "drop-" + d.name }
func (d dropToolNamed) Order() int                        { return 0 }
func (d dropToolNamed) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (d dropToolNamed) Handle(_ context.Context, call *aipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	out := call.Tools[:0]
	for _, t := range call.Tools {
		if t.Name != d.name {
			out = append(out, t)
		}
	}
	call.Tools = out
	return nil
}

func TestExecutor_toolCatalogFilter_beforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCatalogFilters: []toolcatalog.Filter{dropToolNamed{name: "b"}},
		}),
	})
	var toolsSeen int
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				toolsSeen = len(call.Tools)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{
			{Name: "a", Parameters: []byte(`{}`)},
			{Name: "b", Parameters: []byte(`{}`)},
		},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if toolsSeen != 1 {
		t.Fatalf("backend saw %d tools want 1", toolsSeen)
	}
}

type denyToolPolicy struct{ name string }

func (d denyToolPolicy) ID() string                        { return "deny-" + d.name }
func (d denyToolPolicy) Order() int                        { return 0 }
func (d denyToolPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (d denyToolPolicy) Handle(_ context.Context, ev aipapi.ToolEvent, _ toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	if ev.ToolName == d.name {
		return toolpolicy.DecisionDeny, nil
	}
	return toolpolicy.DecisionAllow, nil
}

// captureToolPolicyMeta records the last [toolpolicy.Meta] passed to Handle and allows the call.
type captureToolPolicyMeta struct{ last toolpolicy.Meta }

func (c *captureToolPolicyMeta) ID() string                        { return "capture-tool-meta" }
func (c *captureToolPolicyMeta) Order() int                        { return 0 }
func (c *captureToolPolicyMeta) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (c *captureToolPolicyMeta) Handle(_ context.Context, _ aipapi.ToolEvent, meta toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	c.last = meta
	return toolpolicy.DecisionAllow, nil
}

func TestExecutor_toolPolicy_recvHydratesMetaFromExecctx(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	spy := &captureToolPolicyMeta{}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCallPolicies: []toolpolicy.Policy{spy},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "t"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "client-hint-1"},
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools:      []aipapi.ToolDef{{Name: "t", Parameters: []byte(`{}`)}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	if spy.last.Principal.ID != "local-dev" {
		t.Fatalf("tool policy meta Principal.ID: got %q want local-dev", spy.last.Principal.ID)
	}
	if spy.last.Session.ALegID == "" {
		t.Fatalf("tool policy meta Session.ALegID empty")
	}
	if spy.last.Session.ClientSessionHint != "client-hint-1" {
		t.Fatalf("tool policy meta Session.ClientSessionHint: got %q", spy.last.Session.ClientSessionHint)
	}
	for {
		_, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
	}
	_ = stream.Close()
}

func TestExecutor_toolPolicy_deniesStreamToolCall(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCallPolicies: []toolpolicy.Policy{denyToolPolicy{name: "blocked"}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "blocked"}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "ck-tool-policy-deny-lineage"},
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools:      []aipapi.ToolDef{{Name: "blocked", Parameters: []byte(`{}`)}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(context.Background())
	if err == nil {
		t.Fatal("Recv succeeded, want policy denial")
	}
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected ALegID on call after Execute")
	}
	atts, lerr := st.LoadAttempts(context.Background(), alegID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(atts) != 1 {
		t.Fatalf("LoadAttempts: want 1 row got %d %#v", len(atts), atts)
	}
	if atts[0].Outcome != aipapi.AttemptSurfacedFailure {
		t.Fatalf("Outcome: got %s want surfaced_failure", atts[0].Outcome)
	}
	if !strings.Contains(atts[0].Reason, "tool policy") || !strings.Contains(atts[0].Reason, "denied") {
		t.Fatalf("Reason: %q", atts[0].Reason)
	}
}

func TestExecutor_reftoolpolicy_proofBundle_deniesEmittedBlockedTool(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	cfg := reftoolpolicy.Config{
		BlockNames:    []string{"blocked"},
		BlockPrefixes: nil,
	}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCatalogFilters: []toolcatalog.Filter{reftoolpolicy.NewToolCatalogFilter(cfg)},
			ToolCallPolicies:   []toolpolicy.Policy{reftoolpolicy.NewToolCallPolicy(cfg)},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "blocked"}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools:      []aipapi.ToolDef{{Name: "allowed", Parameters: []byte(`{}`)}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(context.Background())
	if err == nil {
		t.Fatal("Recv succeeded, want ref-tool-policy denial")
	}
}

type captureUsage struct{ events []usage.Event }

func (c *captureUsage) OnUsage(_ context.Context, ev usage.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func TestExecutor_usageObserverReceivesUsageDeltas(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{
						Kind:             aipapi.EventUsageDelta,
						InputTokens:      3,
						OutputTokens:     5,
						CacheReadTokens:  2,
						CacheWriteTokens: 1,
						ReasoningTokens:  4,
						TotalTokens:      8,
						CostNanoUnits:    9,
						Currency:         "USD",
						CostSource:       "estimated",
						RawUsageJSON:     `{"usage":true}`,
					},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if obs.events[0].InputTokens != 3 || obs.events[0].OutputTokens != 5 || obs.events[0].BackendID != "openai" {
		t.Fatalf("unexpected usage event: %+v", obs.events[0])
	}
	if obs.events[0].CacheReadTokens != 2 || obs.events[0].CacheWriteTokens != 1 ||
		obs.events[0].ReasoningTokens != 4 || obs.events[0].TotalTokens != 8 ||
		obs.events[0].CostNanoUnits != 9 || obs.events[0].Currency != "USD" ||
		obs.events[0].CostSource != "estimated" || obs.events[0].RawUsageJSON != `{"usage":true}` {
		t.Fatalf("unexpected detailed usage event: %+v", obs.events[0])
	}
}

func TestExecutor_reftraffictranscript_usageLedgerCapturesAttemptLineage(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ledger := reftraffictranscript.NewUsageLedger()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: ledger})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 1},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	evs := ledger.EventsSnapshot()
	if len(evs) != 1 {
		t.Fatalf("usage events %d want 1", len(evs))
	}
	ev := evs[0]
	if ev.AttemptSeq != 1 {
		t.Fatalf("AttemptSeq: %+v", ev)
	}
	if ev.BLegID == "" || ev.ALegID == "" {
		t.Fatalf("want leg ids: %+v", ev)
	}
	if ev.BackendID != "openai" {
		t.Fatalf("BackendID: %+v", ev)
	}
}
