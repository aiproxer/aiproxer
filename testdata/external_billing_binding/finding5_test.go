package main

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// versionedRuleSource is a controllable public usage-authority snapshot
// source: RefreshSnapshots publishes whatever version it currently holds,
// letting tests observe generation-carried frozen snapshots change.
type versionedRuleSource struct {
	mu      sync.Mutex
	version string
}

func (s *versionedRuleSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage-authority", Version: s.version, EffectiveAt: now, FetchedAt: now,
		State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

func (s *versionedRuleSource) set(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = v
}

// liveCall builds the admitted-then-open-fails request shape: bounded output
// lets admission quote, while the stock config has no backend to open, so the
// runtime deterministically exercises credit, quote, admit, and the abort
// terminal append without any network.
func liveCall() *aipapi.Call {
	maxOut := 64
	return &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai-responses:gpt-4o-mini"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		Options:  aipapi.GenerationOptions{MaxOutputTokens: &maxOut},
	}
}

func allowScopePrincipal(fix *OfferFixture) {
	fix.Screen.allowedAccount = "user-external-1"
}

// workEvidence captures the economically relevant snapshot/ref identity one
// unit of live work observed, for old/new generation comparison.
type workEvidence struct {
	callID       string
	usageVersion string
	snapGen      int64
	hostGen      int64
	envelope     billing.TerminalEnvelope
	revision     uint64
}

// TestSameHostFailedReloadPreservesActiveGeneration certifies requirement
// 15.4 on one live Host: a genuine candidate validation failure is not
// published, the prior generation stays Ready and usable, binding-owned
// resources and the worker run untouched with no duplicate start/close,
// and normal final close still occurs exactly once.
func TestSameHostFailedReloadPreservesActiveGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	body := readRepoConfig(t)
	_, path := writeTempConfig(t, body)

	tracker := &lifecycleTracker{}
	fix, err := AssembleBinding(tracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	allowScopePrincipal(fix)
	rt, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: path}, fix.Binding)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	gen1 := rt.ReloadStatus().ActiveGeneration

	// Baseline: the worker is alive before the failed candidate.
	worker := tracker.Worker()
	worker.Submit("pre-failure-job")
	if got := worker.WaitProcessed(t, 1); got != 1 {
		t.Fatalf("pre-failure jobs = %d, want 1", got)
	}
	before := tracker.snapshot()

	// Genuine same-Host candidate failure: unloadable config.
	writeTempConfigTo(t, dirOf(t, path), "cfg.yaml", "::: not yaml :::{{")
	res := rt.Reload(ctx, reloadAPI())
	if res.Category != aipruntime.ResultInvalid {
		t.Fatalf("reload category=%q reason=%q, want invalid", res.Category, res.ReasonCategory)
	}
	if got := rt.ReloadStatus().ActiveGeneration; got != gen1 {
		t.Fatalf("failed candidate moved generation: %d -> %d", gen1, got)
	}
	if !rt.Ready() || rt.ExecutorView() == nil {
		t.Fatal("prior generation must stay Ready and usable after failed reload")
	}
	// The worker and owned resources run untouched through the failure.
	worker.Submit("post-failure-job")
	if got := worker.WaitProcessed(t, 1); got != 1 {
		t.Fatalf("post-failure jobs = %d, want 1 (worker alive)", got)
	}
	if got := worker.Processed(); got != 2 {
		t.Fatalf("processed jobs = %d, want 2 without loss or replay", got)
	}
	after := tracker.snapshot()
	if !reflect.DeepEqual(before.starts, after.starts) || !reflect.DeepEqual(before.closes, after.closes) {
		t.Fatalf("failed reload touched owned resources: before=%+v after=%+v", before, after)
	}
	if fix.Terminal.recorded() != 0 {
		t.Fatalf("failed reload appended terminal records: %d", fix.Terminal.recorded())
	}

	// The prior generation still publishes afterward: restore and reload.
	changed := strings.Replace(body, "max_attempts: 3", "max_attempts: 4", 1)
	writeTempConfigTo(t, dirOf(t, path), "cfg.yaml", changed)
	res = rt.Reload(ctx, reloadAPI())
	if res.Category != aipruntime.ResultPublished {
		t.Fatalf("recovery reload category=%q reason=%q, want published", res.Category, res.ReasonCategory)
	}

	if err := rt.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	final := tracker.snapshot()
	if final.closes["worker-test"] != 1 || final.closes["worker-plain"] != 1 {
		t.Fatalf("final close must retire each owned resource once: %+v", final)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	if again := tracker.snapshot(); !reflect.DeepEqual(final.closes, again.closes) {
		t.Fatalf("repeated close duplicated cleanup: %+v", again)
	}
	if n := tracker.borrowedCount(); n != 1 {
		t.Fatalf("borrowed declarations = %d, want 1 untouched handle", n)
	}
}

// TestCrossGenerationFrozenSnapshots certifies requirement 15.3: in-flight
// old-generation work completes with its frozen snapshot identity while new
// work observes the new candidate identity. Identities are economically
// relevant: the usage-authority snapshot version, the snapshot generation,
// and the per-call BillingCallID chain across quote, admission, terminal,
// and acknowledgement.
func TestCrossGenerationFrozenSnapshots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	body := readRepoConfig(t)
	_, path := writeTempConfig(t, body)
	src := &versionedRuleSource{version: "usage-v1"}

	tracker := &lifecycleTracker{}
	fix, err := AssembleBinding(tracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	allowScopePrincipal(fix)
	rt, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: path, UsageSnapshotSource: src}, fix.Binding)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	old := doLiveWork(ctx, t, rt, fix)
	if old.usageVersion != "usage-v1" {
		t.Fatalf("old work usage version = %q, want usage-v1", old.usageVersion)
	}

	src.set("usage-v2")
	changed := strings.Replace(body, "max_attempts: 3", "max_attempts: 4", 1)
	writeTempConfigTo(t, dirOf(t, path), "cfg.yaml", changed)
	res := rt.Reload(ctx, reloadAPI())
	if res.Category != aipruntime.ResultPublished {
		t.Fatalf("reload category=%q reason=%q, want published", res.Category, res.ReasonCategory)
	}
	if err := rt.RefreshSnapshots(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	recent := doLiveWork(ctx, t, rt, fix)
	if recent.usageVersion != "usage-v2" {
		t.Fatalf("new work usage version = %q, want usage-v2", recent.usageVersion)
	}
	if recent.hostGen <= old.hostGen {
		t.Fatalf("host generation did not advance: %d -> %d", old.hostGen, recent.hostGen)
	}
	if recent.snapGen <= old.snapGen {
		t.Fatalf("snapshot generation did not advance: %d -> %d", old.snapGen, recent.snapGen)
	}
	if recent.callID == old.callID {
		t.Fatal("new work must carry a fresh BillingCallID")
	}
	if recent.revision <= old.revision {
		t.Fatalf("terminal revisions did not advance: %d -> %d", old.revision, recent.revision)
	}
	// The old work's frozen evidence is immutable across the generation move.
	frozen := fix.Terminal.envelopeAt(0)
	if !reflect.DeepEqual(frozen, old.envelope) {
		t.Fatal("old-generation terminal evidence mutated across reload")
	}
	// Ownership never moved: one start, zero closes until final Close.
	if got := tracker.snapshot(); got.starts["worker-test"] != 1 || got.closes["worker-test"] != 0 {
		t.Fatalf("generations must not restart owned resources: %+v", got)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := tracker.snapshot(); got.closes["worker-test"] != 1 || got.closes["worker-plain"] != 1 {
		t.Fatalf("retirement must close each owned resource once: %+v", got)
	}
}

// doLiveWork executes one admitted-then-open-fails request through the real
// host and captures the full cross-port identity chain plus the generation
// snapshot context it ran under.
func doLiveWork(ctx context.Context, t *testing.T, rt *aipruntime.Runtime, fix *OfferFixture) workEvidence {
	t.Helper()
	sctx := scope.WithScope(ctx, validScope())
	screenBefore, quoterBefore := fix.Screen.callsValue(), fix.Quoter.callsValue()
	admitBefore, recordedBefore := fix.Admission.callsValue(), fix.Terminal.recorded()
	stream, err := rt.ExecutorView().Execute(sctx, liveCall())
	if stream != nil {
		_ = stream.Close()
	}
	if err == nil {
		t.Fatal("stock config has no backend: expected deterministic route failure")
	}
	if got := fix.Screen.callsValue() - screenBefore; got != 1 {
		t.Fatalf("credit checks = %d, want exactly 1", got)
	}
	if got := fix.Quoter.callsValue() - quoterBefore; got != 1 {
		t.Fatalf("quotes = %d, want exactly 1 (single quote path)", got)
	}
	if got := fix.Admission.callsValue() - admitBefore; got != 1 {
		t.Fatalf("admissions = %d, want exactly 1 (single atomic admit)", got)
	}
	if got := fix.Terminal.recorded() - recordedBefore; got != 1 {
		t.Fatalf("terminal appends = %d, want exactly 1 abort closure", got)
	}
	quoteCall := fix.Quoter.lastSubject()
	admitCall := fix.Admission.lastCall()
	admitPrincipal, admitAccount := fix.Admission.lastScopeAccount()
	env, rev := fix.Terminal.envelopeAt(recordedBefore), fix.Terminal.ackAt(recordedBefore)
	if quoteCall == "" || quoteCall != admitCall || quoteCall != env.Subject.BillingCallID {
		t.Fatalf("call chain = %q/%q/%q, want one BillingCallID across quote/admit/terminal", quoteCall, admitCall, env.Subject.BillingCallID)
	}
	if admitPrincipal != "user-external-1" || admitAccount != "user-external-1" {
		t.Fatalf("admission scope/account = %q/%q, want exact credited customer scope/account", admitPrincipal, admitAccount)
	}
	if !rev.MatchesEnvelope(env) {
		t.Fatal("host-delivered acknowledgement must bind its envelope")
	}
	if env.Subject.StoreID != testStore || env.Outcome != billing.TerminalFailed {
		t.Fatalf("terminal lineage = %+v/%q, want bound store and failed abort outcome", env.Subject, env.Outcome)
	}
	return workEvidence{
		callID:       quoteCall,
		usageVersion: rt.SnapshotUsageVersion(),
		snapGen:      rt.SnapshotGenerationID(),
		hostGen:      rt.ReloadStatus().ActiveGeneration,
		envelope:     env,
		revision:     rev.Revision,
	}
}

// TestPostTerminalWorkerRatesHostDeliveredEnvelopes certifies requirement 7.1
// through the explicit public post-terminal worker seam: durably acknowledged
// envelopes drain into the custom rater, the synthetic non-token component
// becomes valuation lines, and observation-less closures are skipped without
// inventing evidence.
func TestPostTerminalWorkerRatesHostDeliveredEnvelopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, path := writeTempConfig(t, readRepoConfig(t))

	tracker := &lifecycleTracker{}
	fix, err := AssembleBinding(tracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	allowScopePrincipal(fix)
	rt, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: path}, fix.Binding)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	// Host-delivered work: the abort closure carries no observations.
	sctx := scope.WithScope(ctx, validScope())
	stream, err := rt.ExecutorView().Execute(sctx, liveCall())
	if stream != nil {
		_ = stream.Close()
	}
	if err == nil {
		t.Fatal("expected deterministic route failure")
	}

	// Synthetic evidence through the identical terminal port: no backend
	// exists to produce observations, so the widget envelope is supplied to
	// the same port instance the host uses, traversing validation and
	// durable acknowledgement before rating.
	widgetObs := observationFixture(t, "5", "100")
	widgetEnv, err := terminalEnvelopeFixture("env-widget-seam-1", []metering.Observation{widgetObs})
	if err != nil {
		t.Fatalf("widget envelope: %v", err)
	}
	widgetAck, err := fix.Terminal.AppendTerminal(ctx, widgetEnv)
	if err != nil {
		t.Fatalf("widget append: %v", err)
	}
	if !widgetAck.MatchesEnvelope(widgetEnv) {
		t.Fatal("widget acknowledgement must bind its envelope")
	}

	worker := newPostTerminalWorker(fix)
	if err := worker.Drain(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	rated, skipped := worker.Counts()
	if rated != 1 || skipped != 1 {
		t.Fatalf("drain = rated %d skipped %d, want 1/1 (widget rated, abort skipped)", rated, skipped)
	}
	valuations := worker.Valuations()
	if len(valuations) != 1 {
		t.Fatalf("valuations = %d, want 1", len(valuations))
	}
	valuation := valuations[0]
	if err := valuation.Validate(); err != nil {
		t.Fatalf("valuation invalid: %v", err)
	}
	if len(valuation.Lines) != 2 {
		t.Fatalf("lines = %d, want submission fee + widget passthrough", len(valuation.Lines))
	}
	if len(valuation.Totals) != 1 || valuation.Totals[0].RoundedAmount.NanoUnits != submissionFeeNano+5*widgetPriceNano {
		t.Fatalf("totals = %+v, want 300 nano USD", valuation.Totals)
	}
	// The seam is idempotent: a second drain consumes nothing new.
	if err := worker.Drain(); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if rated, skipped := worker.Counts(); rated != 1 || skipped != 1 {
		t.Fatalf("second drain = rated %d skipped %d, want unchanged 1/1", rated, skipped)
	}
}
