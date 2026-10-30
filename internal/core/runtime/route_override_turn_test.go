package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
)

const (
	overrideClientSelector = "clientbe:m"
	overrideAdminSelector  = "adminbe:m"
	overrideOtherSelector  = "otherbe:m"
	overrideHookSelector   = "hookbe:m"
)

func overrideStreamingBackend(cap *routeOpenCapture, name string) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: cap.record(name),
	}
}

func collectExecute(t *testing.T, ex *runtime.Executor, ctx context.Context, call *aipapi.Call) { //nolint:revive // test helper keeps t first
	t.Helper()
	resumeTok := call.Session.ResumeToken
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if call.Session.ResumeToken == "" && resumeTok != "" {
		call.Session.ResumeToken = resumeTok
	}
}

func resumeOverrideCall(prev *aipapi.Call, selector string) *aipapi.Call {
	return &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        prev.Session.ClientSessionID,
			ContinuityKey:          prev.Session.ContinuityKey,
			AuthoritativeSessionID: prev.Session.AuthoritativeSessionID,
			ResumeToken:            prev.Session.ResumeToken,
			ALegID:                 prev.Session.ALegID,
		},
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("next")},
		}},
	}
}

func resetRouteOpenCapture(c *routeOpenCapture) {
	c.mu.Lock()
	c.opens = nil
	c.count.Store(0)
	c.mu.Unlock()
}

func seedOverrideALeg(t *testing.T, ex *runtime.Executor, st routeoverride.Store, continuity, adminSelector string) *aipapi.Call {
	t.Helper()
	ex.RouteOverrideReader = st
	call := routePlanLifetimeCall(overrideClientSelector, continuity)
	collectExecute(t, ex, context.Background(), call)
	if strings.TrimSpace(call.Session.ALegID) == "" {
		t.Fatal("expected A-leg id after seed turn")
	}
	if adminSelector != "" {
		if _, err := st.Replace(context.Background(), call.Session.ALegID, adminSelector, time.Now().UTC()); err != nil {
			t.Fatalf("replace override: %v", err)
		}
	}
	return call
}

func waitOverrideBarrier(t *testing.T, barrier *runtime.RouteAuthoritySnapshotBarrier, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := barrier.WaitUntilArrived(waitCtx); err != nil {
		cancel()
		<-done
		t.Fatalf("snapshot barrier: %v", err)
	}
}

func waitHeldRouteOpens(t *testing.T, cap *routeOpenCapture, backends []string, cancel context.CancelFunc, releaseHold func(), done <-chan error) {
	t.Helper()
	if cap == nil || cap.entered == nil {
		t.Fatal("held Open wait requires routeOpenCapture.entered")
	}
	want := make(map[string]struct{}, len(backends))
	for _, b := range backends {
		want[b] = struct{}{}
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		got := cap.snapshot()
		seen := make(map[string]struct{}, len(got))
		for _, o := range got {
			seen[o.backend] = struct{}{}
		}
		missing := false
		for b := range want {
			if _, ok := seen[b]; !ok {
				missing = true
				break
			}
		}
		if !missing {
			return
		}
		select {
		case <-cap.entered:
		case <-timer.C:
			cancel()
			if releaseHold != nil {
				releaseHold()
			}
			<-done
			t.Fatalf("timed out waiting for held Opens %v, got %+v", backends, got)
		}
	}
}

type failClosedOverrideReader struct {
	err error
}

func (f failClosedOverrideReader) Snapshot(context.Context, string) (routeoverride.State, error) {
	return routeoverride.State{}, f.err
}

type overrideHintSpy struct {
	mu     sync.Mutex
	routes []string
}

func (s *overrideHintSpy) ID() string                        { return "override-hint-spy" }
func (s *overrideHintSpy) Order() int                        { return 0 }
func (s *overrideHintSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s *overrideHintSpy) Hint(_ context.Context, in routehint.Input) (routehint.Result, error) {
	if in.Call != nil {
		s.mu.Lock()
		s.routes = append(s.routes, in.Call.Route.Selector)
		s.mu.Unlock()
	}
	return routehint.Result{}, nil
}

func (s *overrideHintSpy) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.routes) == 0 {
		return ""
	}
	return s.routes[len(s.routes)-1]
}

func (s *overrideHintSpy) reset() {
	s.mu.Lock()
	s.routes = nil
	s.mu.Unlock()
}

type overrideRouteObserver struct {
	mu  sync.Mutex
	got []overrideRouteObs
}

type overrideRouteObs struct {
	decision string
	detail   string
}

func (o *overrideRouteObserver) ObserveRouteDecision(_ context.Context, _ string, decision, detail string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, overrideRouteObs{decision: decision, detail: detail})
}

func (o *overrideRouteObserver) snapshot() []overrideRouteObs {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]overrideRouteObs, len(o.got))
	copy(out, o.got)
	return out
}

type cancelWatchStream struct {
	inner   aipapi.ManagedEventStream
	cancels *atomic.Int32
	closes  *atomic.Int32
}

func (s *cancelWatchStream) Recv(ctx context.Context) (aipapi.Event, error) {
	return s.inner.Recv(ctx)
}

func (s *cancelWatchStream) Close() error {
	s.closes.Add(1)
	return s.inner.Close()
}

func (s *cancelWatchStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancels.Add(1)
	return s.inner.Cancel(ctx, cause)
}

func TestExecutor_activeOverrideRoutesBLegAndKeepsCTPClientSelector(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ctp := &ctpSelectorCapture{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"adminbe":  overrideStreamingBackend(cap, "adminbe"),
	})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:       voidWorkspaceResolver{},
		TrafficObserver: ctp,
	})
	seed := seedOverrideALeg(t, ex, st, "ov-active-ctp", overrideAdminSelector)
	resetRouteOpenCapture(cap)

	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	opens := cap.snapshot()
	if len(opens) != 1 || opens[0].backend != "adminbe" || opens[0].selector != overrideAdminSelector {
		t.Fatalf("overridden B-leg: %+v want admin selector %q", opens, overrideAdminSelector)
	}
	ctpSels := ctp.selectors()
	if len(ctpSels) == 0 {
		t.Fatal("expected CTP selector observations")
	}
	for i, sel := range ctpSels {
		if sel != overrideClientSelector {
			t.Fatalf("CTP[%d] selector: got %q want client %q", i, sel, overrideClientSelector)
		}
	}
}

func TestExecutor_preRequestRewriteThenOverrideWinsForHintAndBaseline(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ctp := &ctpSelectorCapture{}
	hint := &overrideHintSpy{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"hookbe":   overrideStreamingBackend(cap, "hookbe"),
		"adminbe":  overrideStreamingBackend(cap, "adminbe"),
	})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:       voidWorkspaceResolver{},
		TrafficObserver: ctp,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms:  []request.Transform{selectorRewriteTransform{from: overrideClientSelector, to: overrideHookSelector}},
			RouteHintProviders: []routehint.Provider{hint},
		}),
	})
	seed := seedOverrideALeg(t, ex, st, "ov-rewrite-hint", overrideAdminSelector)
	resetRouteOpenCapture(cap)
	hint.reset()

	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	if hint.last() != overrideAdminSelector {
		t.Fatalf("route hint saw %q want snapshotted admin %q", hint.last(), overrideAdminSelector)
	}
	opens := cap.snapshot()
	if len(opens) != 1 || opens[0].backend != "adminbe" || opens[0].selector != overrideAdminSelector {
		t.Fatalf("effective baseline B-leg: %+v want admin %q", opens, overrideAdminSelector)
	}
	ctpSels := ctp.selectors()
	if len(ctpSels) == 0 {
		t.Fatal("expected CTP selector observations")
	}
	for i, sel := range ctpSels {
		if sel != overrideClientSelector {
			t.Fatalf("CTP[%d] selector: got %q want client %q", i, sel, overrideClientSelector)
		}
	}
}

func TestExecutor_inactiveOverrideKeepsClientSelector(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
	})
	ex.RouteOverrideReader = st
	collectExecute(t, ex, context.Background(), routePlanLifetimeCall(overrideClientSelector, "ov-inactive"))
	opens := cap.snapshot()
	if len(opens) != 1 || opens[0].selector != overrideClientSelector {
		t.Fatalf("inactive override: %+v want client %q", opens, overrideClientSelector)
	}
}

func TestExecutor_overrideReaderErrorFailsClosed(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ex, _ := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
	})
	ex.RouteOverrideReader = failClosedOverrideReader{err: errors.New("override store unavailable")}
	_, err := ex.Execute(context.Background(), routePlanLifetimeCall(overrideClientSelector, "ov-fail-closed"))
	if err == nil {
		t.Fatal("configured reader failure must fail request preparation")
	}
	if !strings.Contains(err.Error(), "snapshot route override") {
		t.Fatalf("want fail-closed snapshot error, got %v", err)
	}
	if cap.count.Load() != 0 {
		t.Fatalf("fail-closed must not open a B-leg, opens=%d", cap.count.Load())
	}
}

func TestExecutor_inFlightFailoverKeepsSnapshottedOverrideAfterReplace(t *testing.T) {
	t.Parallel()
	const snapSelector = "bad:m|ok:m"
	cap := &routeOpenCapture{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
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
		"ok":    overrideStreamingBackend(cap, "ok"),
		"other": overrideStreamingBackend(cap, "other"),
	})
	seed := seedOverrideALeg(t, ex, st, "ov-failover-hold", snapSelector)
	resetRouteOpenCapture(cap)

	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)
	turn := resumeOverrideCall(seed, overrideClientSelector)
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := ex.Execute(ctx, turn)
		stream = s
		done <- err
	}()
	waitOverrideBarrier(t, barrier, cancel, done)
	if cap.count.Load() != 0 {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("B-leg opened before barrier release, opens=%d", cap.count.Load())
	}
	if _, err := st.Replace(context.Background(), seed.Session.ALegID, overrideOtherSelector, time.Now().UTC()); err != nil {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("replace after snapshot: %v", err)
	}
	if cap.count.Load() != 0 {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("mutation before first B-leg opened a backend, opens=%d", cap.count.Load())
	}
	barrier.Release()
	if err := <-done; err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := cap.snapshot()
	if len(got) != 2 {
		t.Fatalf("failover opens: %+v want 2 on snapshotted selector", got)
	}
	for i, o := range got {
		if o.selector != snapSelector {
			t.Fatalf("open[%d] selector: got %q want snapshotted %q", i, o.selector, snapSelector)
		}
	}
	if got[0].backend != "bad" || got[1].backend != "ok" {
		t.Fatalf("failover order: %+v", got)
	}
}

func TestExecutor_inFlightRaceKeepsSnapshottedOverrideAfterReplace(t *testing.T) {
	t.Parallel()
	const snapSelector = "left:m!right:m"
	cap := &routeOpenCapture{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"left":     overrideStreamingBackend(cap, "left"),
		"right":    overrideStreamingBackend(cap, "right"),
		"other":    overrideStreamingBackend(cap, "other"),
	})
	seed := seedOverrideALeg(t, ex, st, "ov-race-hold", snapSelector)
	resetRouteOpenCapture(cap)
	cap.entered = make(chan struct{}, 2)
	cap.hold = make(chan struct{})
	var releaseHold sync.Once
	releaseHoldFn := func() { releaseHold.Do(func() { close(cap.hold) }) }
	defer releaseHoldFn()

	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)
	turn := resumeOverrideCall(seed, overrideClientSelector)
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := ex.Execute(ctx, turn)
		stream = s
		done <- err
	}()
	waitOverrideBarrier(t, barrier, cancel, done)
	if _, err := st.Replace(context.Background(), seed.Session.ALegID, overrideOtherSelector, time.Now().UTC()); err != nil {
		cancel()
		barrier.Release()
		releaseHoldFn()
		<-done
		t.Fatalf("replace after snapshot: %v", err)
	}
	barrier.Release()
	waitHeldRouteOpens(t, cap, []string{"left", "right"}, cancel, releaseHoldFn, done)
	held := cap.snapshot()
	for i, o := range held {
		if o.selector != snapSelector {
			t.Fatalf("held open[%d] selector: got %q want snapshotted %q", i, o.selector, snapSelector)
		}
		if o.backend == "other" {
			t.Fatalf("post-snapshot replace must not open the new selector: %+v", held)
		}
	}
	releaseHoldFn()
	if err := <-done; err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := cap.snapshot()
	if len(got) < 2 {
		t.Fatalf("race opens: %+v want both snapshotted legs", got)
	}
	seen := map[string]bool{}
	for i, o := range got {
		if o.selector != snapSelector {
			t.Fatalf("open[%d] selector: got %q want snapshotted %q", i, o.selector, snapSelector)
		}
		if o.backend == "other" {
			t.Fatalf("post-snapshot replace must not open the new selector: %+v", got)
		}
		seen[o.backend] = true
	}
	if !seen["left"] || !seen["right"] {
		t.Fatalf("want left and right race B-legs, got %+v", got)
	}
}

func TestExecutor_clearAfterSnapshotKeepsCurrentTurnOverrideNextTurnClient(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"adminbe":  overrideStreamingBackend(cap, "adminbe"),
	})
	seed := seedOverrideALeg(t, ex, st, "ov-clear-hold", overrideAdminSelector)
	resetRouteOpenCapture(cap)

	barrier := runtime.NewRouteAuthoritySnapshotBarrier()
	defer barrier.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = runtime.WithRouteAuthoritySnapshotBarrier(ctx, barrier)
	turnN := resumeOverrideCall(seed, overrideClientSelector)
	done := make(chan error, 1)
	var stream aipapi.EventStream
	go func() {
		s, err := ex.Execute(ctx, turnN)
		stream = s
		done <- err
	}()
	waitOverrideBarrier(t, barrier, cancel, done)
	if _, err := st.Clear(context.Background(), seed.Session.ALegID, time.Now().UTC()); err != nil {
		cancel()
		barrier.Release()
		<-done
		t.Fatalf("clear after snapshot: %v", err)
	}
	barrier.Release()
	if err := <-done; err != nil {
		t.Fatalf("turn N execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("turn N collect: %v", err)
	}
	got := cap.snapshot()
	if len(got) != 1 || got[0].selector != overrideAdminSelector {
		t.Fatalf("cleared-after-snapshot current turn: %+v want admin %q", got, overrideAdminSelector)
	}

	resetRouteOpenCapture(cap)
	collectExecute(t, ex, context.Background(), resumeOverrideCall(turnN, overrideClientSelector))
	next := cap.snapshot()
	if len(next) != 1 || next[0].selector != overrideClientSelector {
		t.Fatalf("next turn after clear: %+v want client %q", next, overrideClientSelector)
	}
}

func TestExecutor_postOutputOverrideUpdateDoesNotRebuildRoute(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	var cancels, closes atomic.Int32
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"adminbe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				cap.count.Add(1)
				cap.mu.Lock()
				cap.opens = append(cap.opens, routeOpen{backend: "adminbe", selector: call.Route.Selector, model: cand.Primary.Model})
				cap.mu.Unlock()
				return &cancelWatchStream{
					inner:   routePlanLifetimeTextStream(),
					cancels: &cancels,
					closes:  &closes,
				}, nil
			},
		},
		"other": overrideStreamingBackend(cap, "other"),
	})
	seed := seedOverrideALeg(t, ex, st, "ov-post-output", overrideAdminSelector)
	resetRouteOpenCapture(cap)

	stream, err := ex.Execute(context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	sawDelta := false
	sawFinished := false
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			break
		}
		if ev.Kind == aipapi.EventTextDelta && !sawDelta {
			sawDelta = true
			if _, err := st.Replace(context.Background(), seed.Session.ALegID, overrideOtherSelector, time.Now().UTC()); err != nil {
				t.Fatalf("replace after output: %v", err)
			}
		}
		if ev.Kind == aipapi.EventResponseFinished {
			sawFinished = true
		}
	}
	if !sawDelta || !sawFinished {
		t.Fatalf("stream disrupted after post-output replace: delta=%v finished=%v", sawDelta, sawFinished)
	}
	if cap.count.Load() != 1 {
		t.Fatalf("post-output replace rebuilt routing, opens=%d (%+v)", cap.count.Load(), cap.snapshot())
	}
	if cancels.Load() != 0 {
		t.Fatalf("post-output replace cancelled the in-flight stream: %d", cancels.Load())
	}
	_ = stream.Close()
}

func TestExecutor_overrideRecordsSelectorAuthorityAndBLegEffectiveModel(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	obs := &overrideRouteObserver{}
	trace := diag.NewRouteTraceBuffer(32)
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"adminbe":  overrideStreamingBackend(cap, "adminbe"),
	})
	seed := seedOverrideALeg(t, ex, st, "ov-diag-auth", overrideAdminSelector)
	resetRouteOpenCapture(cap)
	ex.RouteTrace = trace
	ex.RouteObserver = obs

	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, overrideClientSelector))

	var auth *diag.RouteTraceEntry
	for _, e := range trace.Snapshot() {
		if e.Decision == "selector_authority" {
			cp := e
			auth = &cp
		}
	}
	if auth == nil {
		t.Fatal("missing selector_authority route-trace entry")
	}
	if auth.SelectorSource != diag.RouteSelectorSourceAdmin {
		t.Fatalf("selector_source=%q want %q", auth.SelectorSource, diag.RouteSelectorSourceAdmin)
	}
	if auth.OverrideRevision < 1 {
		t.Fatalf("override_revision=%d want >= 1", auth.OverrideRevision)
	}
	if auth.Detail != diag.RouteSelectorSourceAdmin {
		t.Fatalf("authority detail=%q must be bounded source, not raw selector", auth.Detail)
	}
	if strings.Contains(auth.Detail, overrideAdminSelector) || strings.Contains(auth.Detail, overrideClientSelector) {
		t.Fatalf("route-trace detail leaked selector: %#v", auth)
	}

	foundObs := false
	for _, o := range obs.snapshot() {
		if o.decision != "selector_authority" {
			continue
		}
		foundObs = true
		if o.detail != diag.RouteSelectorSourceAdmin {
			t.Fatalf("observer detail=%q want bounded source %q", o.detail, diag.RouteSelectorSourceAdmin)
		}
		if strings.Contains(o.detail, overrideAdminSelector) || strings.Contains(o.detail, seed.Session.ALegID) {
			t.Fatalf("observer leaked selector or A-leg: %+v", o)
		}
	}
	if !foundObs {
		t.Fatal("missing selector_authority observer decision")
	}

	attempts, err := st.LoadAttempts(context.Background(), seed.Session.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts: %v", err)
	}
	if len(attempts) == 0 {
		t.Fatal("expected attempt lineage")
	}
	last := attempts[len(attempts)-1]
	if last.BackendID != "adminbe" {
		t.Fatalf("BackendID=%q want adminbe", last.BackendID)
	}
	if last.EffectiveModel != "m" {
		t.Fatalf("EffectiveModel=%q want B-leg model m, not selector %q", last.EffectiveModel, overrideAdminSelector)
	}
	if last.EffectiveModel == overrideAdminSelector || last.EffectiveModel == overrideClientSelector {
		t.Fatalf("EffectiveModel must not be the route selector: %q", last.EffectiveModel)
	}
}

func TestExecutor_inactiveOverrideRecordsClientSelectorSource(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	trace := diag.NewRouteTraceBuffer(16)
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
	})
	ex.RouteOverrideReader = st
	ex.RouteTrace = trace
	collectExecute(t, ex, context.Background(), routePlanLifetimeCall(overrideClientSelector, "ov-diag-client"))
	var auth *diag.RouteTraceEntry
	for _, e := range trace.Snapshot() {
		if e.Decision == "selector_authority" {
			cp := e
			auth = &cp
		}
	}
	if auth == nil {
		t.Fatal("missing selector_authority route-trace entry")
	}
	if auth.SelectorSource != diag.RouteSelectorSourceClient {
		t.Fatalf("selector_source=%q want %q", auth.SelectorSource, diag.RouteSelectorSourceClient)
	}
	if auth.OverrideRevision != 0 {
		t.Fatalf("inactive override_revision=%d want 0", auth.OverrideRevision)
	}
	if auth.Detail != diag.RouteSelectorSourceClient {
		t.Fatalf("detail=%q want %q", auth.Detail, diag.RouteSelectorSourceClient)
	}
}
