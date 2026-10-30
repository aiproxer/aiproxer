package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

func routePlanLifetimeTextStream() aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	})
}

func routePlanLifetimeCall(selector, continuity string) *aipapi.Call {
	return &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: continuity},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func routePlanLifetimeExecutor(t *testing.T, backends map[string]execbackend.Backend) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 4
	ex.Backends = backends
	return ex, st
}

type routeOpenCapture struct {
	mu      sync.Mutex
	opens   []routeOpen
	count   atomic.Int32
	hold    chan struct{}
	entered chan struct{}
}

type routeOpen struct {
	backend  string
	selector string
	model    string
	native   string
}

func (c *routeOpenCapture) record(backend string) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	return func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		c.count.Add(1)
		c.mu.Lock()
		c.opens = append(c.opens, routeOpen{
			backend:  backend,
			selector: call.Route.Selector,
			model:    cand.Primary.Model,
			native:   cand.Primary.NativeModel,
		})
		c.mu.Unlock()
		if c.entered != nil {
			select {
			case c.entered <- struct{}{}:
			default:
			}
		}
		if c.hold != nil {
			<-c.hold
		}
		return routePlanLifetimeTextStream(), nil
	}
}

func (c *routeOpenCapture) snapshot() []routeOpen {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]routeOpen, len(c.opens))
	copy(out, c.opens)
	return out
}

func TestExecutor_failoverReusesOneRequestLocalSelector(t *testing.T) {
	t.Parallel()
	const clientSelector = "bad:m|ok:m"
	cap := &routeOpenCapture{}
	ex, _ := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				cap.count.Add(1)
				cap.mu.Lock()
				cap.opens = append(cap.opens, routeOpen{backend: "bad", selector: call.Route.Selector, model: cand.Primary.Model})
				cap.mu.Unlock()
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: cap.record("ok"),
		},
	})
	stream, err := ex.Execute(context.Background(), routePlanLifetimeCall(clientSelector, "rp-failover"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := cap.snapshot()
	if len(got) != 2 {
		t.Fatalf("opens: got %d want 2 (%+v)", len(got), got)
	}
	if got[0].backend != "bad" || got[1].backend != "ok" {
		t.Fatalf("open order: %+v", got)
	}
	for i, o := range got {
		if o.selector != clientSelector {
			t.Fatalf("open[%d] selector: got %q want client selector %q", i, o.selector, clientSelector)
		}
	}
}

func TestExecutor_recvRetryReusesSameSelectorAndPlan(t *testing.T) {
	t.Parallel()
	const clientSelector = "bad:m|ok:m"
	cap := &routeOpenCapture{}
	ex, _ := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				cap.count.Add(1)
				cap.mu.Lock()
				cap.opens = append(cap.opens, routeOpen{backend: "bad", selector: call.Route.Selector, model: cand.Primary.Model})
				cap.mu.Unlock()
				return &oneThenFailStream{
					first: aipapi.Event{Kind: aipapi.EventResponseStarted},
					then:  aipapi.RecoverablePreOutputError(errors.New("recv")),
				}, nil
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: cap.record("ok"),
		},
	})
	stream, err := ex.Execute(context.Background(), routePlanLifetimeCall(clientSelector, "rp-recv-retry"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for {
		_, rerr := stream.Recv(context.Background())
		if rerr != nil {
			break
		}
	}
	_ = stream.Close()
	got := cap.snapshot()
	if len(got) != 2 {
		t.Fatalf("opens: got %d want 2 (%+v)", len(got), got)
	}
	for i, o := range got {
		if o.selector != clientSelector {
			t.Fatalf("open[%d] selector: got %q want %q", i, o.selector, clientSelector)
		}
	}
}

func TestExecutor_postOutputFailureDoesNotOpenFailoverLeg(t *testing.T) {
	t.Parallel()
	var secondary atomic.Int32
	ex, _ := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"one": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &deltaThenErrStream{n: 0}, nil
			},
		},
		"two": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				secondary.Add(1)
				return routePlanLifetimeTextStream(), nil
			},
		},
	})
	stream, err := ex.Execute(context.Background(), routePlanLifetimeCall("one:m|two:m", "rp-post-output"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	ctx := context.Background()
	for {
		_, rerr := stream.Recv(ctx)
		if rerr != nil {
			if aipapi.IsRecoverablePreOutput(rerr) {
				t.Fatalf("post-output failure must not remain recoverable pre-output: %v", rerr)
			}
			break
		}
	}
	_ = stream.Close()
	if secondary.Load() != 0 {
		t.Fatalf("secondary backend opened after committed output: %d", secondary.Load())
	}
}

func TestExecutor_aLegBarrierHoldsBeforeRoutePlanAndBackendOpen(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ex, _ := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: cap.record("openai"),
		},
	})
	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)

	const clientSelector = "openai:gpt-4"
	call := routePlanLifetimeCall(clientSelector, "rp-barrier")
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := ex.Execute(ctx, call)
		stream = s
		done <- err
	}()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := barrier.WaitUntilArrived(waitCtx); err != nil {
		cancel()
		<-done
		t.Fatalf("wait for A-leg barrier: %v", err)
	}
	if barrier.ALegID() == "" {
		cancel()
		barrier.Release()
		<-done
		t.Fatal("expected A-leg id at snapshot barrier")
	}
	if cap.count.Load() != 0 {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("backend opened before route-plan construction, opens=%d", cap.count.Load())
	}

	barrier.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("execute did not finish after barrier release")
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("opens after release: %+v", got)
	}
	if got[0].selector != clientSelector {
		t.Fatalf("no-override turn used %q, want client selector %q", got[0].selector, clientSelector)
	}
}

type ctpSelectorCapture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *ctpSelectorCapture) OnObservation(_ context.Context, obs sdktraffic.Observation) error {
	if obs.Leg != sdktraffic.LegCTP {
		return nil
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, bytes.Clone(obs.Body))
	c.mu.Unlock()
	return nil
}

func (c *ctpSelectorCapture) selectors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.bodies))
	for _, body := range c.bodies {
		var payload struct {
			Route struct {
				Selector string
			}
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			continue
		}
		out = append(out, payload.Route.Selector)
	}
	return out
}

type selectorRewriteTransform struct {
	from, to string
}

func (s selectorRewriteTransform) ID() string                        { return "selector-rewrite" }
func (s selectorRewriteTransform) Order() int                        { return 10 }
func (s selectorRewriteTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (s selectorRewriteTransform) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	if call != nil && call.Route.Selector == s.from {
		call.Route.Selector = s.to
	}
	return nil
}

type capturingClientTurnRecorder struct {
	mu sync.Mutex
	in app.ClientTurnRecordInput
}

func (c *capturingClientTurnRecorder) RecordClientTurnAfterGate(_ context.Context, in app.ClientTurnRecordInput) error {
	c.mu.Lock()
	c.in = in
	c.mu.Unlock()
	return nil
}

func (c *capturingClientTurnRecorder) RecordPostHookStreamEvent(context.Context, app.StreamEventRecordInput) error {
	return nil
}

func TestExecutor_ctpRecordsClientSelectorSeparateFromEffectiveBaseline(t *testing.T) {
	t.Parallel()
	const clientSelector = "clientbe:m"
	const hookSelector = "hookbe:m"
	ctp := &ctpSelectorCapture{}
	cap := &routeOpenCapture{}
	rec := &capturingClientTurnRecorder{}
	ex, _ := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Error("client backend must not open after request-transform selector rewrite")
				return routePlanLifetimeTextStream(), nil
			},
		},
		"hookbe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: cap.record("hookbe"),
		},
	})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:       voidWorkspaceResolver{},
		TrafficObserver: ctp,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{selectorRewriteTransform{from: clientSelector, to: hookSelector}},
		}),
	})
	ex.SecureSessionRecorder = rec

	stream, err := ex.Execute(context.Background(), routePlanLifetimeCall(clientSelector, "rp-ctp"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}

	ctpSelectors := ctp.selectors()
	if len(ctpSelectors) == 0 {
		t.Fatal("expected CTP traffic observation containing the client call")
	}
	for i, sel := range ctpSelectors {
		if sel != clientSelector {
			t.Fatalf("CTP[%d] selector: got %q want client %q (traffic must keep client evidence)", i, sel, clientSelector)
		}
	}
	opens := cap.snapshot()
	if len(opens) != 1 {
		t.Fatalf("backend opens: %+v", opens)
	}
	if opens[0].selector != hookSelector {
		t.Fatalf("effective baseline selector: got %q want rewritten %q", opens[0].selector, hookSelector)
	}
	rec.mu.Lock()
	lines := rec.in.Lines
	rec.mu.Unlock()
	if len(lines) == 0 {
		t.Fatal("expected client-turn recording of accepted input lines")
	}
	for _, line := range lines {
		if line.Role == clientSelector || line.Role == hookSelector {
			t.Fatalf("client-turn recording must not treat route selector as input evidence: %+v", line)
		}
	}
}

func TestExecutor_noOverrideUsesClientSelectorWhenBarrierReleased(t *testing.T) {
	t.Parallel()
	const clientSelector = "openai:gpt-4"
	cap := &routeOpenCapture{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: cap.record("openai"),
		},
	})
	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)
	call := routePlanLifetimeCall(clientSelector, "rp-no-override")
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := ex.Execute(ctx, call)
		stream = s
		done <- err
	}()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := barrier.WaitUntilArrived(waitCtx); err != nil {
		cancel()
		<-done
		t.Fatalf("barrier: %v", err)
	}
	alegID := barrier.ALegID()
	if _, err := st.FetchALeg(context.Background(), alegID); err != nil {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("FetchALeg at barrier: %v", err)
	}
	barrier.Release()
	if err := <-done; err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	opens := cap.snapshot()
	if len(opens) != 1 || opens[0].selector != clientSelector {
		t.Fatalf("no-override routing: %+v want selector %q", opens, clientSelector)
	}
}

func TestExecutor_interleavedThinkerAndExecutorReuseOneRequestLocalSelector(t *testing.T) {
	t.Parallel()
	const clientSelector = "[thinker]thinker-be:m^exec-be:m"
	cap := &routeOpenCapture{}
	note := func(backend string) func(aipapi.Call) {
		return func(call aipapi.Call) {
			cap.count.Add(1)
			cap.mu.Lock()
			cap.opens = append(cap.opens, routeOpen{
				backend:  backend,
				selector: call.Route.Selector,
			})
			cap.mu.Unlock()
		}
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	ex, _ := interleavedExecutor(t, map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, note("exec-be"), func() aipapi.ManagedEventStream {
			return executorTextStream("executor answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, note("thinker-be"), func() aipapi.ManagedEventStream {
			return thinkerMemoStream("plan")
		}),
	})

	first := interleavedBaseCall(clientSelector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	for i, o := range cap.snapshot() {
		if o.selector != clientSelector {
			t.Fatalf("first-turn open[%d] selector: got %q want client selector %q", i, o.selector, clientSelector)
		}
	}

	cap.mu.Lock()
	cap.opens = nil
	cap.count.Store(0)
	cap.mu.Unlock()

	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)

	second := interleavedBaseCall(clientSelector)
	resumeInterleavedCall(first, second)
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, execErr := ex.Execute(ctx, second)
		stream = s
		done <- execErr
	}()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := barrier.WaitUntilArrived(waitCtx); err != nil {
		cancel()
		<-done
		t.Fatalf("continuation A-leg barrier: %v", err)
	}
	if cap.count.Load() != 0 {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("continuation B-leg opened before route-plan construction, opens=%d", cap.count.Load())
	}

	barrier.Release()
	if err := <-done; err != nil {
		t.Fatalf("continuation execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("continuation collect: %v", err)
	}

	got := cap.snapshot()
	var sawThinker, sawExec bool
	for i, o := range got {
		if o.selector != clientSelector {
			t.Fatalf("open[%d] backend=%s selector: got %q want client selector %q", i, o.backend, o.selector, clientSelector)
		}
		switch o.backend {
		case "thinker-be":
			sawThinker = true
		case "exec-be":
			sawExec = true
		}
	}
	if !sawThinker || !sawExec {
		t.Fatalf("want thinker and executor B-legs on continuation turn, got %+v", got)
	}
}
