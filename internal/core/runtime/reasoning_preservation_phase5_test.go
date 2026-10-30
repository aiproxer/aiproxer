package runtime_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

const (
	p5Visible = "visible answer"
	p5Thought = "phase5-thought"
)

func p5Config(t *testing.T) reasoningpreservation.Config {
	t.Helper()
	raw := `
action: restore
use_builtin_catalog: false
rules:
  - id: test-be
    backend: be
    enabled: true
  - id: test-a
    backend: a
    enabled: true
  - id: test-b
    backend: b
    enabled: true
  - id: test-slow
    backend: slow
    enabled: true
  - id: test-fast
    backend: fast
    enabled: true
  - id: test-one
    backend: one
    enabled: true
  - id: test-two
    backend: two
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 65536
  max_session_bytes: 262144
`
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	cfg, err := reasoningpreservation.DecodeConfig(n)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return cfg
}

func p5ReplayBackend(openFn func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error)) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoningReplay),
		ReplaySupport: aipapi.ReasoningReplaySupport{
			Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1},
		},
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: openFn,
	}
}

func p5ReasoningEvents(text string) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: p5Thought},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	}
}

func p5Executor(t *testing.T, mutateBundle func(*aipfeature.ContributionSet), mutateEx func(*runtime.Executor)) (*runtime.Executor, *reasoningpreservation.InstanceParts) {
	t.Helper()
	parts, bundle, err := reasoningpreservation.FeatureBundleWithParts(p5Config(t))
	if err != nil {
		t.Fatalf("FeatureBundleWithParts: %v", err)
	}
	cs := aipfeature.NewContributionSet()
	if err := bundle.PlaneSet.ReplayTo(cs, "rp"); err != nil {
		t.Fatalf("ReplayTo: %v", err)
	}
	if mutateBundle != nil {
		mutateBundle(cs)
	}
	bundle = aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := bundle.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus, snap := rpWire(t, bundle)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	if mutateEx != nil {
		mutateEx(ex)
	}
	return ex, parts
}

func p5ObserveCall() *aipapi.Call {
	return &aipapi.Call{
		ID:    "p5-observe",
		Route: aipapi.RouteIntent{Selector: "be:m"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ask")}},
		},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeNonStreaming},
	}
}

func p5RestoreCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		ID:    "p5-restore",
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(p5Visible)}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("follow-up")}},
		},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeNonStreaming},
	}
}

func p5Collect(t *testing.T, ex *runtime.Executor, call *aipapi.Call) *aipapi.Call {
	t.Helper()
	stream, err := ex.Execute(t.Context(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(t.Context(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return call
}

func TestPhase5_observeThenRestoreSameAuthoritativeSession(t *testing.T) {
	t.Parallel()
	var gotRestore aipapi.Call
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Backends = map[string]execbackend.Backend{
			"be": p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.ID == "p5-restore" {
					gotRestore = call
				}
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
		}
	})
	observe := p5ObserveCall()
	p5Collect(t, ex, observe)
	if observe.Session.ResumeToken == "" || observe.Session.AuthoritativeSessionID == "" {
		t.Fatalf("observe must issue secure session; sid=%q resume=%q", observe.Session.AuthoritativeSessionID, observe.Session.ResumeToken)
	}
	snap, err := parts.Store.Snapshot(t.Context(), reasoningpreservation.NewSessionPartition(observe.Session.AuthoritativeSessionID))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("observe success_released must persist one artifact, got %d diag=%q", len(snap), parts.Observer.LastSafeDiagnostic())
	}

	restore := p5RestoreCall("be:m")
	restore.Session.ResumeToken = observe.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = observe.Session.AuthoritativeSessionID
	p5Collect(t, ex, restore)
	if !rpHasReasoningText(gotRestore, p5Thought) {
		t.Fatalf("restore Open must observe restored reasoning; texts=%v", rpReasoningTexts(&gotRestore))
	}
	inv := parts.Inventory()
	if inv.AggregateCounters["observed"] < 1 || inv.AggregateCounters["restored"] < 1 {
		t.Fatalf("aggregates=%v", inv.AggregateCounters)
	}
}

func TestPhase5_failoverIsolationBaselineAndObserverOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		fail func(*sync.Mutex, *[]aipapi.Call) execbackend.Backend
	}{
		{"sequential", func(mu *sync.Mutex, opens *[]aipapi.Call) execbackend.Backend {
			return p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.ID == "p5-restore" {
					mu.Lock()
					*opens = append(*opens, call)
					mu.Unlock()
				}
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp-a"))
			})
		}},
		{"recv", func(mu *sync.Mutex, opens *[]aipapi.Call) execbackend.Backend {
			return p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.ID == "p5-restore" {
					mu.Lock()
					*opens = append(*opens, call)
					mu.Unlock()
				}
				return &observerFailAfterStream{
					events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}},
					fail:   aipapi.RecoverablePreOutputError(errors.New("recv-pre-output")),
				}, nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var opens []aipapi.Call
			finishFactory := &countingFinishObserverFactory{finishes: &atomic.Int64{}, opens: &atomic.Int64{}}
			ex, parts := p5Executor(t, func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "finish", []response.StreamObserverFactory{finishFactory})
			}, func(ex *runtime.Executor) {
				ex.MaxAttempts = 3
				ex.Rand = routing.NewSeededRng(11)
				ex.Backends = map[string]execbackend.Backend{
					"a": tc.fail(&mu, &opens),
					"b": p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						if call.ID == "p5-restore" {
							mu.Lock()
							opens = append(opens, call)
							mu.Unlock()
						}
						return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
					}),
				}
			})
			// Mint an authoritative session, seed the feature store, then restore with failover.
			mint := p5ObserveCall()
			mint.Route.Selector = "b:m"
			ex.Backends["b"] = p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			})
			p5Collect(t, ex, mint)
			sid := mint.Session.AuthoritativeSessionID
			art := rpArtifact(p5Thought)
			art.CreatedAt = time.Time{}
			if _, err := parts.Store.Append(t.Context(), reasoningpreservation.NewSessionPartition(sid), art); err != nil {
				t.Fatalf("seed Append: %v", err)
			}
			ex.Backends["b"] = p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.ID == "p5-restore" {
					mu.Lock()
					opens = append(opens, call)
					mu.Unlock()
				}
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			})
			restore := p5RestoreCall("a:m|b:m")
			restore.Session.ResumeToken = mint.Session.ResumeToken
			restore.Session.AuthoritativeSessionID = sid
			if tc.name == "recv" {
				stream, err := ex.Execute(t.Context(), restore)
				if err != nil {
					t.Fatalf("execute restore: %v", err)
				}
				for {
					_, rerr := stream.Recv(t.Context())
					if rerr != nil {
						break
					}
				}
				_ = stream.Close()
			} else {
				p5Collect(t, ex, restore)
			}
			mu.Lock()
			got := append([]aipapi.Call(nil), opens...)
			mu.Unlock()
			if len(got) < 2 {
				t.Fatalf("want restore failover opens>=2 got %d", len(got))
			}
			for i, c := range got {
				if !rpHasReasoningText(c, p5Thought) {
					t.Fatalf("restore attempt %d missing restored baseline reasoning texts=%v", i, rpReasoningTexts(&c))
				}
				if n := countReasoningText(c, p5Thought); n != 1 {
					t.Fatalf("restore attempt %d must start from baseline with exactly one restored block, got %d", i, n)
				}
			}
			if finishFactory.opens.Load() == 0 || finishFactory.finishes.Load() != finishFactory.opens.Load() {
				t.Fatalf("each opened observer lifecycle must Finish exactly once; opens=%d finishes=%d",
					finishFactory.opens.Load(), finishFactory.finishes.Load())
			}
			if len(mustSnapshot(t, parts, sid)) < 1 {
				t.Fatal("winner must persist; losers must not wipe winner artifact")
			}
		})
	}
}

// p5UnrepresentablePair builds two reasoning-incapable backends (streaming +
// reasoning_replay cap, no replay dialects) sharing one Open func.
func p5UnrepresentablePair(open func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error)) map[string]execbackend.Backend {
	mk := func() execbackend.Backend {
		return execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoningReplay),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: open,
		}
	}
	return map[string]execbackend.Backend{"a": mk(), "b": mk()}
}

// Claim: the attempt policy unifies restored and client-sent reasoning
// (req 3.1, 3.4). Under reject the restore path keeps its existing
// on_unrepresentable exclude with a stable error; under the default log_skip
// the unrepresentable restore is withheld and the route succeeds degraded.
func TestPhase5_allCandidatesUnrepresentable(t *testing.T) {
	t.Parallel()

	t.Run("reject excludes with stable error", func(t *testing.T) {
		t.Parallel()
		ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
			ex.MaxAttempts = 3
			ex.Rand = routing.NewSeededRng(11)
			ex.AttemptPolicy = aipapi.AttemptReject
			ex.Backends = map[string]execbackend.Backend{
				"be": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
				}),
			}
			maps.Copy(ex.Backends, p5UnrepresentablePair(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Error("excluded candidate must not Open")
				return nil, nil
			}))
		})
		observe := p5ObserveCall()
		p5Collect(t, ex, observe)
		if len(mustSnapshot(t, parts, observe.Session.AuthoritativeSessionID)) < 1 {
			t.Fatal("observe must seed artifact")
		}
		restore := p5RestoreCall("a:m|b:m")
		restore.Session.ResumeToken = observe.Session.ResumeToken
		restore.Session.AuthoritativeSessionID = observe.Session.AuthoritativeSessionID
		_, err := ex.Execute(t.Context(), restore)
		if err == nil {
			t.Fatal("all unrepresentable candidates must error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "unrepresentable_replay") {
			t.Fatalf("stable error want unrepresentable_replay, got %v", err)
		}
	})

	t.Run("log_skip succeeds degraded", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var opens []aipapi.Call
		ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
			ex.MaxAttempts = 3
			ex.Rand = routing.NewSeededRng(11)
			ex.Backends = map[string]execbackend.Backend{
				"be": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
				}),
			}
			maps.Copy(ex.Backends, p5UnrepresentablePair(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				opens = append(opens, call)
				mu.Unlock()
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}))
		})
		observe := p5ObserveCall()
		p5Collect(t, ex, observe)
		if len(mustSnapshot(t, parts, observe.Session.AuthoritativeSessionID)) < 1 {
			t.Fatal("observe must seed artifact")
		}
		restore := p5RestoreCall("a:m|b:m")
		restore.Session.ResumeToken = observe.Session.ResumeToken
		restore.Session.AuthoritativeSessionID = observe.Session.AuthoritativeSessionID
		stream, err := ex.Execute(t.Context(), restore)
		if err != nil {
			t.Fatalf("log_skip must succeed degraded instead of erroring: %v", err)
		}
		if _, err := aipapi.Collect(t.Context(), stream); err != nil {
			t.Fatalf("collect: %v", err)
		}
		mu.Lock()
		got := append([]aipapi.Call(nil), opens...)
		mu.Unlock()
		if len(got) < 1 {
			t.Fatal("withheld candidate must open")
		}
		for _, c := range got {
			if rpHasReasoningText(c, p5Thought) {
				t.Fatalf("withheld restore must not reach the backend: %v", rpReasoningTexts(&c))
			}
		}
	})
}

func TestPhase5_parallelWinnerOnlyPersistence(t *testing.T) {
	t.Parallel()
	var openArrived atomic.Int32
	gate := make(chan struct{})
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Rand = routing.NewSeededRng(1)
		track := func(name string) execbackend.Backend {
			return p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				_ = call
				if openArrived.Add(1) == 2 {
					close(gate)
				}
				select {
				case <-gate:
				case <-time.After(3 * time.Second):
					return nil, errors.New("parallel barrier timeout")
				}
				return aipapi.NewFixedEventStream(p5ReasoningEvents(name + "-" + p5Visible)), nil
			})
		}
		ex.Backends = map[string]execbackend.Backend{"slow": track("slow"), "fast": track("fast")}
	})
	call := p5ObserveCall()
	call.Route.Selector = "slow:m!fast:m"
	p5Collect(t, ex, call)
	if openArrived.Load() < 2 {
		t.Fatalf("both arms must Open, got %d", openArrived.Load())
	}
	sid := call.Session.AuthoritativeSessionID
	snap, err := parts.Store.Snapshot(t.Context(), reasoningpreservation.NewSessionPartition(sid))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("winner-only persistence want 1 artifact, got %d", len(snap))
	}
}

func TestPhase5_weightedIndependentClonesAndDialectEligibility(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var opens []aipapi.Call
	var backends []string
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.MaxAttempts = 3
		ex.Rand = routing.NewSeededRng(7)
		// Strict policy: dialect-ineligible arms keep the existing
		// restore-path exclude (req 3.4).
		ex.AttemptPolicy = aipapi.AttemptReject
		ex.Backends = map[string]execbackend.Backend{
			"be": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
			"a": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoningReplay),
				TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
					Operation: aipapi.OperationOpenAIChatCompletions,
					Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
				}),
				Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					mu.Lock()
					opens = append(opens, call)
					backends = append(backends, cand.Primary.Backend)
					mu.Unlock()
					return nil, aipapi.RecoverablePreOutputError(errors.New("temp-a"))
				},
			},
			"b": p5ReplayBackend(func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				opens = append(opens, call)
				backends = append(backends, cand.Primary.Backend)
				mu.Unlock()
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
		}
	})
	observe := p5ObserveCall()
	p5Collect(t, ex, observe)
	if len(mustSnapshot(t, parts, observe.Session.AuthoritativeSessionID)) < 1 {
		t.Fatal("observe must seed artifact")
	}
	restore := p5RestoreCall("[weight=1]a:m^[weight=1]b:m")
	restore.Session.ResumeToken = observe.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = observe.Session.AuthoritativeSessionID
	p5Collect(t, ex, restore)
	mu.Lock()
	got := append([]aipapi.Call(nil), opens...)
	gotBE := append([]string(nil), backends...)
	mu.Unlock()
	for _, be := range gotBE {
		if be == "a" {
			t.Fatal("dialect-ineligible weighted arm must be excluded before Open")
		}
	}
	if len(got) < 1 || gotBE[0] != "b" {
		t.Fatalf("eligible weighted arm b must open with restore, backends=%v", gotBE)
	}
	if !rpHasReasoningText(got[0], p5Thought) {
		t.Fatal("eligible weighted arm must observe restored reasoning on independent clone")
	}
}

func TestPhase5_responseHookAndGateLifecycleWithFeatureStore(t *testing.T) {
	t.Parallel()
	ex, parts := p5Executor(t, func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, "hook", []sdkhooks.ResponsePartHook{mutateTextResponseHook{}})
	}, func(ex *runtime.Executor) {
		ex.Backends = map[string]execbackend.Backend{
			"be": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventReasoningDelta, Delta: p5Thought},
					{Kind: aipapi.EventTextDelta, Delta: "orig"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			}),
		}
	})
	call := p5ObserveCall()
	stream, err := ex.Execute(t.Context(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	col, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Text.String() != observerHookMarker {
		t.Fatalf("hook text=%q", col.Text.String())
	}
	snap, err := parts.Store.Snapshot(t.Context(), reasoningpreservation.NewSessionPartition(call.Session.AuthoritativeSessionID))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("hook-mutated capture must persist, got %d", len(snap))
	}
	wantAnchor, err := reasoningpreservation.ComputeAnchor(aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{aipapi.TextPart(observerHookMarker)},
	})
	if err != nil {
		t.Fatalf("ComputeAnchor: %v", err)
	}
	if snap[0].Anchor != wantAnchor {
		t.Fatal("stored artifact anchor must equal post-hook non-reasoning content")
	}
	if len(snap[0].Reasoning) == 0 || snap[0].Reasoning[0].Part.Reasoning == nil || snap[0].Reasoning[0].Part.Reasoning.Text != p5Thought {
		t.Fatal("stored reasoning payload must remain from observed deltas")
	}

	ex2, parts2 := p5Executor(t, func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "gate", []completion.Gate{replaceAllGate{}})
	}, func(ex *runtime.Executor) {
		ex.Backends = map[string]execbackend.Backend{"be": fixedSuccessBackend("orig")}
	})
	call2 := p5ObserveCall()
	stream2, err := ex2.Execute(t.Context(), call2)
	if err != nil {
		t.Fatalf("execute2: %v", err)
	}
	col2, err := aipapi.Collect(t.Context(), stream2)
	if err != nil {
		t.Fatalf("collect2: %v", err)
	}
	if col2.Text.String() != "gate-replaced-text" {
		t.Fatalf("gate text=%q", col2.Text.String())
	}
	snap2, err := parts2.Store.Snapshot(t.Context(), reasoningpreservation.NewSessionPartition(call2.Session.AuthoritativeSessionID))
	if err != nil {
		t.Fatalf("Snapshot2: %v", err)
	}
	if len(snap2) != 0 {
		t.Fatal("gate-replaced original without reasoning must not persist reasoning artifact")
	}
}

func TestPhase5_observerFailurePreservesCommittedOutputNoRetry(t *testing.T) {
	t.Parallel()
	var opens atomic.Int64
	tel := reasoningpreservation.NewTelemetry()
	failing := reasoningpreservation.NewStreamObserverFactory(p5Config(t), failingP5Store{}, tel)
	ex, _ := p5Executor(t, func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "failing", []response.StreamObserverFactory{failing})
	}, func(ex *runtime.Executor) {
		ex.MaxAttempts = 3
		ex.Backends = map[string]execbackend.Backend{
			"one": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
			"two": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
		}
	})
	call := p5ObserveCall()
	call.Route.Selector = "one:m|two:m"
	stream, err := ex.Execute(t.Context(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	col, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect must succeed despite observer store failure: %v", err)
	}
	if !strings.Contains(col.Text.String(), p5Visible) {
		t.Fatalf("committed output changed: %q", col.Text.String())
	}
	if opens.Load() != 1 {
		t.Fatalf("observer/store failure must not retry after output, opens=%d", opens.Load())
	}
	// The contained failure must still be observable as the bounded state-error
	// outcome an operator reads, exactly once for the one failed append.
	if got := tel.Snapshot()[reasoningpreservation.OutcomeStateError]; got != 1 {
		t.Fatalf("observer store failure must record one %s outcome, got %d",
			reasoningpreservation.OutcomeStateError, got)
	}
}

func TestPhase5_clientHintSpoofDoesNotRestoreAcrossSessions(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got aipapi.Call
	// One executor and one feature store: the seeded authoritative partition
	// stays reachable, so only the partition-key rule (never the client hint)
	// can keep the spoofed restore from finding it.
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Backends = map[string]execbackend.Backend{
			"be": p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.ID == "p5-restore" {
					mu.Lock()
					got = call
					mu.Unlock()
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				}
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
		}
	})
	observe := p5ObserveCall()
	p5Collect(t, ex, observe)
	sid := observe.Session.AuthoritativeSessionID
	snap, err := parts.Store.Snapshot(t.Context(), reasoningpreservation.NewSessionPartition(sid))
	if err != nil || len(snap) != 1 {
		t.Fatalf("seed observe failed snap=%d err=%v", len(snap), err)
	}

	spoof := p5RestoreCall("be:m")
	spoof.Session.ClientSessionID = sid
	p5Collect(t, ex, spoof)
	mu.Lock()
	defer mu.Unlock()
	if rpHasReasoningText(got, p5Thought) {
		t.Fatal("spoofed client hint must not restore foreign authoritative partition")
	}
}

func TestPhase5_stickySessionRestoreSameBackend(t *testing.T) {
	t.Parallel()
	var backends []string
	var mu sync.Mutex
	var gotRestore aipapi.Call
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Backends = map[string]execbackend.Backend{
			"be": p5ReplayBackend(func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				backends = append(backends, cand.Primary.Backend)
				mu.Unlock()
				if call.ID == "p5-restore" {
					gotRestore = call
				}
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
		}
	})
	observe := p5ObserveCall()
	observe.Route.Selector = "{session_sticky}be:m"
	p5Collect(t, ex, observe)
	if len(mustSnapshot(t, parts, observe.Session.AuthoritativeSessionID)) != 1 {
		t.Fatal("sticky observe must persist")
	}
	restore := p5RestoreCall("{session_sticky}be:m")
	restore.Session.ResumeToken = observe.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = observe.Session.AuthoritativeSessionID
	p5Collect(t, ex, restore)
	if !rpHasReasoningText(gotRestore, p5Thought) {
		t.Fatal("sticky resumed session must restore reasoning")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, be := range backends {
		if be != "be" {
			t.Fatalf("sticky must stay on be, got %v", backends)
		}
	}
}

func TestPhase5_disabledNonInterferenceNoFeatureTelemetry(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})
	p5AssertNoReasoningParticipants(t, snap)
	var opened atomic.Int64
	ex := runtime.TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{"be": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		opened.Add(1)
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
	})}
	call := p5ObserveCall()
	baseline := aipapi.CloneCall(*call)
	p5Collect(t, ex, call)
	if opened.Load() != 1 {
		t.Fatalf("opens=%d", opened.Load())
	}
	p5AssertNoReasoningParticipants(t, ex.RuntimeSnapshot)
	if !reflectDeepEqualCallMessages(baseline.Messages, call.Messages) {
		t.Fatal("disabled path must not mutate call messages")
	}
	inv := reasoningpreservation.BuildSafeInventory(reasoningpreservation.Config{}, nil)
	if inv.Enabled || len(inv.AggregateCounters) != 0 {
		t.Fatalf("disabled inventory=%+v", inv)
	}
	emptyGen, err := featurebundle.MergeBundlesGenerated()
	if err != nil {
		t.Fatal(err)
	}
	if len(aipfeature.Get(emptyGen.Frozen, aipfeature.PlaneAttemptTransforms)) != 0 {
		t.Fatal("absent FeatureBundle merge must not introduce reasoning participants")
	}
}

type p5Fataler interface {
	Helper()
	Fatalf(string, ...any)
}

func p5AssertNoReasoningParticipants(t p5Fataler, snap *extensions.RequestRuntimeSnapshot) {
	t.Helper()
	if snap == nil {
		return
	}
	for _, x := range snap.AttemptTransforms() {
		if x != nil && strings.Contains(x.ID(), reasoningpreservation.ID) {
			t.Fatalf("disabled snapshot must not host transform %q", x.ID())
		}
	}
	for _, f := range snap.StreamObserverFactories() {
		if f != nil && strings.Contains(f.ID(), reasoningpreservation.ID) {
			t.Fatalf("disabled snapshot must not host observer %q", f.ID())
		}
	}
}

func countReasoningText(call aipapi.Call, want string) int {
	n := 0
	for _, text := range rpReasoningTexts(&call) {
		if text == want {
			n++
		}
	}
	return n
}

func mustSnapshot(t *testing.T, parts *reasoningpreservation.InstanceParts, sid string) []reasoningpreservation.TurnArtifact {
	t.Helper()
	snap, err := parts.Store.Snapshot(t.Context(), reasoningpreservation.NewSessionPartition(sid))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return snap
}

type failingP5Store struct{}

func (failingP5Store) Append(context.Context, reasoningpreservation.SessionPartition, reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return reasoningpreservation.EvictionSummary{}, errors.New("store append failed")
}

func (failingP5Store) Snapshot(context.Context, reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return nil, nil
}

func (failingP5Store) Delete(context.Context, reasoningpreservation.SessionPartition, ...string) error {
	return nil
}

type countingFinishObserverFactory struct {
	finishes *atomic.Int64
	opens    *atomic.Int64
}

func (f *countingFinishObserverFactory) ID() string { return "p5-counting-finish" }
func (f *countingFinishObserverFactory) Order() int { return 100 }
func (f *countingFinishObserverFactory) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (f *countingFinishObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	f.opens.Add(1)
	return &countingFinishObserver{finishes: f.finishes}, nil
}

type countingFinishObserver struct {
	finishes *atomic.Int64
	once     sync.Once
}

func (o *countingFinishObserver) Observe(context.Context, aipapi.Event) error { return nil }
func (o *countingFinishObserver) Finish(context.Context, response.StreamOutcome) error {
	o.once.Do(func() { o.finishes.Add(1) })
	return nil
}

func reflectDeepEqualCallMessages(a, b []aipapi.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || len(a[i].Parts) != len(b[i].Parts) {
			return false
		}
		for j := range a[i].Parts {
			if a[i].Parts[j].Kind != b[i].Parts[j].Kind || a[i].Parts[j].Text != b[i].Parts[j].Text {
				return false
			}
		}
	}
	return true
}

var (
	_ request.AttemptTransform       = (*reasoningpreservation.AttemptTransform)(nil)
	_ response.StreamObserverFactory = (*reasoningpreservation.StreamObserverFactory)(nil)
)
