package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestPhase5_realBundleStateErrorExcludesCandidate(t *testing.T) {
	t.Parallel()
	cfg := p5Config(t)
	inner, err := reasoningpreservation.NewMemoryTurnStore(reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 65536, MaxSessionBytes: 262144,
	})
	if err != nil {
		t.Fatal(err)
	}
	tel := reasoningpreservation.NewTelemetry()
	toggle := &toggleSnapshotStore{inner: inner}
	xform := reasoningpreservation.NewAttemptTransform(cfg, toggle, tel)
	obs := reasoningpreservation.NewStreamObserverFactory(cfg, toggle, tel)
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "xform", []request.AttemptTransform{xform})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "obs", []response.StreamObserverFactory{obs})
	bundle := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, bundle)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.MaxAttempts, ex.Rand = st, bus, snap, 3, routing.NewSeededRng(11)
	var opens atomic.Int64
	ex.Backends = map[string]execbackend.Backend{
		"a": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
		}),
		"b": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
		}),
	}
	mint := p5ObserveCall()
	mint.Route.Selector = "a:m"
	p5Collect(t, ex, mint)
	mintOpens := opens.Load()
	toggle.failSnapshot.Store(true)
	restore := p5RestoreCall("a:m|b:m")
	restore.Session.ResumeToken = mint.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = mint.Session.AuthoritativeSessionID
	probeCall := aipapi.CloneCall(*restore)
	dec, derr := xform.HandleAttempt(t.Context(), &probeCall, request.AttemptMeta{
		BackendID: "a", Model: "m",
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		Session:       session.SessionView{AuthoritativeSessionID: restore.Session.AuthoritativeSessionID},
	}, request.Services{})
	if derr != nil {
		t.Fatalf("HandleAttempt: %v", derr)
	}
	if dec.Kind != request.AttemptExcludeCandidate || dec.ReasonCode != "state_error" {
		t.Fatalf("transform must exclude with ReasonCode=state_error, got kind=%v reason=%q", dec.Kind, dec.ReasonCode)
	}
	_, err = ex.Execute(t.Context(), restore)
	if err == nil {
		t.Fatal("state_error reject must surface when all candidates hit store Snapshot failure")
	}
	if !errors.Is(err, aipapi.ErrAllCandidatesExcluded) {
		t.Fatalf("want ErrAllCandidatesExcluded, got %v", err)
	}
	if opens.Load() != mintOpens {
		t.Fatalf("restore candidates must not Open after state_error exclude, opens=%d mint=%d", opens.Load(), mintOpens)
	}
	if tel.Snapshot()[reasoningpreservation.OutcomeStateError] == 0 {
		t.Fatal("telemetry must record state_error")
	}
}

func TestPhase5_realBundleRestoredContextLimitExclusion(t *testing.T) {
	t.Parallel()
	cfg := p5ConfigWithExtraBackends(t, "smallctx", "bigctx")
	parts, bundle, err := reasoningpreservation.FeatureBundleWithParts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bus, snap := rpWire(t, bundle)
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot = st, bus, snap
	ex.MaxAttempts, ex.Rand = 3, routing.NewSeededRng(2)
	ex.CatalogResolver = contextLimitCatalogResolver{}
	ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	ex.RequestTokenEstimator = reasoningAwareTokenEstimator{base: 5}
	var openedSmall, openedBig atomic.Int64
	var gotBig aipapi.Call
	ex.Backends = map[string]execbackend.Backend{
		"be": p5ReplayBackend(func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished},
			}), nil
		}),
		"smallctx": p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openedSmall.Add(1)
			_ = call
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished},
			}), nil
		}),
		"bigctx": p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openedBig.Add(1)
			gotBig = call
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished},
			}), nil
		}),
	}
	mint := p5ObserveCall()
	p5Collect(t, ex, mint)
	art := rpLargeArtifact
	art.CreatedAt = time.Time{}
	if _, err := parts.Store.Append(t.Context(), reasoningpreservation.NewSessionPartition(mint.Session.AuthoritativeSessionID), art); err != nil {
		t.Fatalf("Append: %v", err)
	}
	restore := p5RestoreCall("smallctx:m|bigctx:m")
	restore.Session.ResumeToken = mint.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = mint.Session.AuthoritativeSessionID
	p5Collect(t, ex, restore)
	if openedSmall.Load() != 0 {
		t.Fatal("restored reasoning must exclude smallctx before Open")
	}
	if openedBig.Load() == 0 || !rpHasReasoningText(gotBig, rpLargeThought) {
		t.Fatal("bigctx must open with restored large reasoning after recompute")
	}
}

func TestPhase5_parallelCancellationDiscardsPending(t *testing.T) {
	t.Parallel()
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Rand = routing.NewSeededRng(1)
		track := func(name string) execbackend.Backend {
			return p5ReplayBackend(func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				_ = call
				return &cancelAwareReasoningStream{name: name}, nil
			})
		}
		ex.Backends = map[string]execbackend.Backend{"slow": track("slow"), "fast": track("fast")}
	})
	ctx, cancel := context.WithCancel(t.Context())
	call := p5ObserveCall()
	call.Route.Selector = "slow:m!fast:m"
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	cancel()
	for {
		_, rerr := stream.Recv(context.Background())
		if rerr != nil {
			break
		}
	}
	_ = stream.Close()
	if sid := call.Session.AuthoritativeSessionID; sid != "" {
		if snap := mustSnapshot(t, parts, sid); len(snap) != 0 {
			t.Fatalf("cancelled parallel arms must not persist artifacts, got %d", len(snap))
		}
	}
}

func TestPhase5_winnerRestoredReasoningPrepended(t *testing.T) {
	t.Parallel()
	var got aipapi.Call
	ex, parts := p5Executor(t, nil, func(ex *runtime.Executor) {
		ex.Backends = map[string]execbackend.Backend{
			"be": p5ReplayBackend(func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.ID == "p5-restore" {
					got = call
				}
				return aipapi.NewFixedEventStream(p5ReasoningEvents(p5Visible)), nil
			}),
		}
	})
	observe := p5ObserveCall()
	p5Collect(t, ex, observe)
	if len(mustSnapshot(t, parts, observe.Session.AuthoritativeSessionID)) != 1 {
		t.Fatal("observe must persist")
	}
	restore := p5RestoreCall("be:m")
	restore.Session.ResumeToken = observe.Session.ResumeToken
	restore.Session.AuthoritativeSessionID = observe.Session.AuthoritativeSessionID
	p5Collect(t, ex, restore)
	if len(got.Messages) == 0 || len(got.Messages[0].Parts) < 2 {
		t.Fatalf("restored parts=%v", got.Messages)
	}
	if got.Messages[0].Parts[0].Kind != aipapi.PartReasoning || got.Messages[0].Parts[0].Reasoning.Text != p5Thought {
		t.Fatal("winner restore must prepend reasoning before non-reasoning parts")
	}
	if got.Messages[0].Parts[1].Kind != aipapi.PartText || got.Messages[0].Parts[1].Text != p5Visible {
		t.Fatal("visible non-reasoning content must follow prepended reasoning")
	}
}

type toggleSnapshotStore struct {
	inner        reasoningpreservation.TurnStore
	failSnapshot atomic.Bool
}

func (s *toggleSnapshotStore) Append(ctx context.Context, p reasoningpreservation.SessionPartition, a reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return s.inner.Append(ctx, p, a)
}

func (s *toggleSnapshotStore) Snapshot(ctx context.Context, p reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	if s.failSnapshot.Load() {
		return nil, errors.New("snapshot boom")
	}
	return s.inner.Snapshot(ctx, p)
}

func (s *toggleSnapshotStore) Delete(ctx context.Context, p reasoningpreservation.SessionPartition, ids ...string) error {
	return s.inner.Delete(ctx, p, ids...)
}

type cancelAwareReasoningStream struct {
	name string
	i    int
}

func (s *cancelAwareReasoningStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: p5Thought},
	}
	if s.i >= len(events) {
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return aipapi.Event{}, context.Canceled
		}
	}
	ev := events[s.i]
	s.i++
	return ev, nil
}
func (s *cancelAwareReasoningStream) Close() error { return nil }
func (s *cancelAwareReasoningStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func p5ConfigWithExtraBackends(t *testing.T, extras ...string) reasoningpreservation.Config {
	t.Helper()
	cfg := p5Config(t)
	for _, be := range extras {
		enabled := true
		cfg.Rules = append(cfg.Rules, reasoningpreservation.RuleConfig{
			ID: "test-" + be, Backend: be, Enabled: &enabled,
		})
	}
	return cfg
}
