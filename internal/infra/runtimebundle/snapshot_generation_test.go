package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

func TestBuild_PublishesSnapshotGeneration(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Pricing.CatalogVersion = "prices-v3"
	cfg.Accounting.Pricing.Currency = "USD"
	_, built := mustProcessAndCandidate(t, cfg, baseAuthorityOptions(t))
	if runtimebundle.CandidateSnapshotGeneration(built) == nil || runtimebundle.CandidateSnapshotGeneration(built).Current() == nil {
		t.Fatal("expected published snapshot generation")
	}
	cur := runtimebundle.CandidateSnapshotGeneration(built).Current()
	if cur.Rating.Version != "prices-v3" || cur.State != economics.SnapshotReady {
		t.Fatalf("cur=%+v", cur)
	}
	held := cur
	runtimebundle.CandidateSnapshotGeneration(built).MarkUnusable(economics.SnapshotDegraded, "refresh_failed")
	if held.Rating.Version != "prices-v3" {
		t.Fatalf("in-flight generation mutated: %+v", held)
	}
	if runtimebundle.CandidateSnapshotGeneration(built).Current().State != economics.SnapshotDegraded {
		t.Fatalf("expected degraded current")
	}
	if built.Executor() == nil || built.Executor().SnapshotGeneration == nil {
		t.Fatal("executor must receive SnapshotGeneration for admit-time binding")
	}
	if built.Executor().SnapshotGeneration != runtimebundle.CandidateSnapshotGeneration(built) {
		t.Fatal("executor SnapshotGeneration must be the same publisher instance")
	}
}

type mutableUsageSource struct {
	mu  sync.Mutex
	ver string
	err error
}

type blockingUsageSource struct {
	mu      sync.Mutex
	version string
	started chan string
	release map[string]chan struct{}
}

func (s *blockingUsageSource) Snapshot(ctx context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	s.mu.Lock()
	version := s.version
	s.mu.Unlock()
	s.started <- version
	select {
	case <-ctx.Done():
		return economics.Snapshot[economics.PolicyRulesView]{}, ctx.Err()
	case <-s.release[version]:
	}
	now := time.Unix(100, 0).UTC()
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: version, EffectiveAt: now, FetchedAt: now,
		State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

func (s *blockingUsageSource) setVersion(version string) {
	s.mu.Lock()
	s.version = version
	s.mu.Unlock()
}

func (m *mutableUsageSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return economics.Snapshot[economics.PolicyRulesView]{}, m.err
	}
	now := time.Unix(100, 0).UTC()
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: m.ver, EffectiveAt: now, FetchedAt: now,
		State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

func (m *mutableUsageSource) set(ver string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ver = ver
	m.err = err
}

// Requirement 11.3/11.6: Refresh must publish newer injected snapshots for new admissions
// without mutating the in-flight generation pointer held from before refresh.
func TestSnapshotController_RefreshPublishesNewerSourceVersions(t *testing.T) {
	t.Parallel()
	src := &mutableUsageSource{ver: "ent-v1"}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{UsageSnapshotSource: src}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	if runtimebundle.CandidateSnapshotController(built) == nil {
		t.Fatal("expected SnapshotController for injectable sources")
	}
	held := runtimebundle.CandidateSnapshotGeneration(built).Current()
	if held == nil || held.Usage.Version != "ent-v1" {
		t.Fatalf("initial usage=%+v", held)
	}
	src.set("ent-v2", nil)
	if err := runtimebundle.CandidateSnapshotController(built).Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	cur := runtimebundle.CandidateSnapshotGeneration(built).Current()
	if cur == nil || cur.Usage.Version != "ent-v2" {
		t.Fatalf("after refresh usage=%+v want ent-v2", cur)
	}
	if cur.ID <= held.ID {
		t.Fatalf("refresh must publish new generation id; held=%d cur=%d", held.ID, cur.ID)
	}
	if held.Usage.Version != "ent-v1" {
		t.Fatalf("in-flight generation mutated: %+v", held)
	}
}

func TestSnapshotController_ConcurrentRefreshesPublishInInvocationOrder(t *testing.T) {
	t.Parallel()
	src := &blockingUsageSource{
		version: "ent-v1",
		started: make(chan string, 3),
		release: map[string]chan struct{}{
			"ent-v1": make(chan struct{}),
			"ent-v2": make(chan struct{}),
			"ent-v3": make(chan struct{}),
		},
	}
	close(src.release["ent-v1"])
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{UsageSnapshotSource: src}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	<-src.started

	src.setVersion("ent-v2")
	firstDone := make(chan error, 1)
	go func() { firstDone <- runtimebundle.CandidateSnapshotController(built).Refresh(context.Background()) }()
	if got := <-src.started; got != "ent-v2" {
		t.Fatalf("first refresh version=%q want ent-v2", got)
	}
	src.setVersion("ent-v3")
	secondDone := make(chan error, 1)
	go func() { secondDone <- runtimebundle.CandidateSnapshotController(built).Refresh(context.Background()) }()

	select {
	case got := <-src.started:
		t.Fatalf("second source read started before first refresh completed: %q", got)
	case <-time.After(20 * time.Millisecond):
	}
	close(src.release["ent-v2"])
	if err := <-firstDone; err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if got := <-src.started; got != "ent-v3" {
		t.Fatalf("second refresh version=%q want ent-v3", got)
	}
	close(src.release["ent-v3"])
	if err := <-secondDone; err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if got := runtimebundle.CandidateSnapshotGeneration(built).Current().Usage.Version; got != "ent-v3" {
		t.Fatalf("published usage version=%q want ent-v3", got)
	}
}

// Requirement 11.7: startup source errors must not leave a falsely ready static snapshot.
func TestSnapshotController_StartupSourceErrorExposesUnavailablePosture(t *testing.T) {
	t.Parallel()
	src := &mutableUsageSource{err: errors.New("management service unavailable")}
	cfg := baseAuthorityConfig(false, "fail_closed")
	// Static YAML would otherwise publish ready with SnapshotVersion.
	cfg.Accounting.Authority.SnapshotVersion = "yaml-static-v1"
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{UsageSnapshotSource: src}
	_, built := mustProcessAndCandidate(t, cfg, opts)
	cur := runtimebundle.CandidateSnapshotGeneration(built).Current()
	if cur == nil {
		t.Fatal("expected a published generation with explicit posture")
	}
	if cur.Usage.State == economics.SnapshotReady {
		t.Fatalf("usage plane must not remain falsely ready after injected source error; cur=%+v", cur.Usage)
	}
	if cur.Usage.State != economics.SnapshotUnavailable && cur.State != economics.SnapshotDegraded && cur.State != economics.SnapshotUnavailable {
		t.Fatalf("want unavailable/degraded posture, got usage.state=%s gen.state=%s", cur.Usage.State, cur.State)
	}
	if cur.Usage.Version == "yaml-static-v1" && cur.Usage.State == economics.SnapshotReady {
		t.Fatal("must not silently keep static ready version as success")
	}
}

// Refresh failure preserves prior Value versions and exposes degraded/unavailable posture (11.7).
func TestSnapshotController_RefreshFailurePreservesPriorVersion(t *testing.T) {
	t.Parallel()
	src := &mutableUsageSource{ver: "ent-ok"}
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t)
	opts.Production = runtimebundle.ProductionOptions{UsageSnapshotSource: src}
	_, built, _ := processAndCandidateErr(t, cfg, opts)
	before := runtimebundle.CandidateSnapshotGeneration(built).Current()
	src.set("", errors.New("refresh boom"))
	err := runtimebundle.CandidateSnapshotController(built).Refresh(context.Background())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	after := runtimebundle.CandidateSnapshotGeneration(built).Current()
	if after.Usage.Version != "ent-ok" {
		t.Fatalf("refresh failure substituted unrelated version: before=%q after=%q", before.Usage.Version, after.Usage.Version)
	}
	if after.State == economics.SnapshotReady && after.Usage.State == economics.SnapshotReady {
		t.Fatalf("refresh failure must expose non-ready posture; after=%+v", after)
	}
}
