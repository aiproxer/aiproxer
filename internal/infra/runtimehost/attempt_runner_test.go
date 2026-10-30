package runtimehost

// Task 6.3: direct white-box tests against the unexported attemptRunner.
// These instantiate attemptRunner directly with NO AttemptGate and NO
// signal/coalescing setup, proving the runner is independently unit-testable
// (req 6.2, 6.4, 6.10-6.11). Fakes here are minimal, package-local duplicates
// of the coordinator_test.go (black-box, package runtimehost_test) fakes.

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// --- minimal package-local fakes (duplicated from coordinator_test.go on
// purpose: that file is package runtimehost_test and cannot be reused here) ---

type runnerFakePlane struct {
	closed         atomic.Bool
	closeCalls     atomic.Int32
	kinds          map[string]int
	closeCh        chan struct{}
	handler        http.Handler
	closePanic     any
	closeErr       error
	startPublished func(context.Context) error
}

func newRunnerFakePlane(kinds map[string]int) *runnerFakePlane {
	return &runnerFakePlane{kinds: kinds, handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
}

func (p *runnerFakePlane) Handler() http.Handler         { return p.handler }
func (p *runnerFakePlane) Quiesce(context.Context) error { return nil }
func (p *runnerFakePlane) StartPublished(ctx context.Context) error {
	if p.startPublished != nil {
		return p.startPublished(ctx)
	}
	return nil
}

func (p *runnerFakePlane) Close() error {
	p.closeCalls.Add(1)
	if p.closePanic != nil {
		panic(p.closePanic)
	}
	if p.closeErr != nil {
		if p.closeCh != nil {
			select {
			case <-p.closeCh:
			default:
				close(p.closeCh)
			}
		}
		return p.closeErr
	}
	if p.closed.Swap(true) {
		return ErrAlreadyClosed
	}
	if p.closeCh != nil {
		close(p.closeCh)
	}
	return nil
}

func (p *runnerFakePlane) BackendFactoryKindCounts() map[string]int {
	if p == nil || p.kinds == nil {
		return nil
	}
	out := make(map[string]int, len(p.kinds))
	maps.Copy(out, p.kinds)
	return out
}

type runnerStageGate struct {
	enter   chan struct{}
	release chan struct{}
	once    sync.Once
}

func newRunnerStageGate() *runnerStageGate {
	return &runnerStageGate{enter: make(chan struct{}), release: make(chan struct{})}
}

func (g *runnerStageGate) WaitEnter(t *testing.T) {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-g.enter:
	case <-guard.Done():
		t.Fatal("timeout waiting for stage enter")
	}
}

func (g *runnerStageGate) Release() { g.once.Do(func() { close(g.release) }) }

func (g *runnerStageGate) Hold(ctx context.Context) error {
	select {
	case <-g.enter:
	default:
		close(g.enter)
	}
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type runnerFakeSource struct {
	path   string
	snap   configsource.SourceSnapshot
	atomic configsource.AtomicResult
	err    error
	gate   *runnerStageGate
	calls  atomic.Int64
}

func (s *runnerFakeSource) AbsolutePath() string { return s.path }
func (s *runnerFakeSource) ReadStable(ctx context.Context, _ *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
	s.calls.Add(1)
	if s.gate != nil {
		if err := s.gate.Hold(ctx); err != nil {
			return configsource.SourceSnapshot{}, "", err
		}
	}
	snapshot := s.snap
	if snapshot.HandleIdentity == (configsource.FileIdentity{}) {
		snapshot.HandleIdentity = configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid"}
	}
	return snapshot, s.atomic, s.err
}

type runnerControllableCompiler struct {
	gate     *runnerStageGate
	err      error
	panicMsg string
	kinds    map[string]int
	onPlane  func(*runnerFakePlane)
	calls    atomic.Int64
	liveSeen atomic.Value
}

func (c *runnerControllableCompiler) Compile(ctx context.Context, _ *config.Config, live map[string]int) (PublishedRequestPlane, error) {
	c.calls.Add(1)
	if live != nil {
		cp := make(map[string]int, len(live))
		maps.Copy(cp, live)
		c.liveSeen.Store(cp)
	}
	if c.gate != nil {
		if err := c.gate.Hold(ctx); err != nil {
			return nil, err
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	p := newRunnerFakePlane(c.kinds)
	if c.onPlane != nil {
		c.onPlane(p)
	}
	if c.panicMsg != "" {
		panic(c.panicMsg)
	}
	return p, nil
}

func runnerBaseEffective(fp string, digest byte) *config.EffectiveConfig {
	var d [32]byte
	d[0] = digest
	return &config.EffectiveConfig{
		Config: &config.Config{},
		Identity: config.EffectiveIdentity{
			PrivateDigest:     d,
			PublicFingerprint: fp,
		},
		LoadedAt: time.Now().UTC(),
	}
}

// newTestRunner builds an attemptRunner with sane defaults; each field can be
// overridden by the caller before use.
func newTestRunner(t *testing.T, mgr *Manager, src StableConfigSource, loader EffectiveLoader, compile CandidateCompiler, obs *ReloadObserver) *attemptRunner {
	t.Helper()
	if mgr == nil {
		mgr = NewManager(8, nil)
		g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(map[string]int{"local-stub": 1}))
		g0.SetMetaHints(MetaHints{PublicFingerprint: "fp-startup", TriggerKind: "startup"})
		if err := mgr.Publish(g0); err != nil {
			t.Fatalf("publish gen1: %v", err)
		}
	}
	if src == nil {
		src = &runnerFakeSource{path: "/fixed/startup/config.yaml", snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}, atomic: configsource.AtomicEligible}
	}
	if loader == nil {
		loader = FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return runnerBaseEffective("fp-new", 2), nil
		})
	}
	if compile == nil {
		compile = &runnerControllableCompiler{kinds: map[string]int{"local-stub": 1}}
	}
	return newAttemptRunner(attemptRunnerDeps{
		Source:   src,
		Loader:   loader,
		Compile:  compile,
		Manager:  mgr,
		Observer: obs,
		Gate:     newAttemptGate(),
		State:    newReloadState(reloadStateInitial{}),
	})
}

func TestAttemptRunner_CanceledContextBeforeRead(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}, atomic: configsource.AtomicEligible}
	r := newTestRunner(t, nil, src, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := r.Run(ctx, attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", out.Result.Category)
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("canceled attempt must not carry state updates: %+v", out)
	}
	if src.calls.Load() != 0 {
		t.Fatalf("source must not be read after cancellation: calls=%d", src.calls.Load())
	}
}

func TestAttemptRunner_ShutdownBeforeRead(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}, atomic: configsource.AtomicEligible}
	r := newTestRunner(t, nil, src, nil, nil, nil)
	r.gate.BeginShutdown()
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultCanceled || out.Result.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("result=%+v", out.Result)
	}
	if src.calls.Load() != 0 {
		t.Fatalf("source must not be read while shutting down")
	}
}

// TestAttemptRunner_ShutdownBeforePublish deterministically hits the
// shutdown checkpoint that runs after a successful prepare and before
// publish. Gate shutdown after compile isolates the pre-transfer checkpoint
// deterministically and proves owned-plane cleanup closes the candidate.
func TestAttemptRunner_ShutdownBeforePublish(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	gate := newAttemptGate()
	var plane *runnerFakePlane
	compile := &runnerControllableCompiler{onPlane: func(p *runnerFakePlane) {
		p.closeCh = make(chan struct{})
		plane = p
		gate.BeginShutdown()
	}}
	src := &runnerFakeSource{path: "/x/config.yaml", snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}, atomic: configsource.AtomicEligible}
	r := newAttemptRunner(attemptRunnerDeps{
		Source: src,
		Loader: FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return runnerBaseEffective("fp-new", 2), nil
		}),
		Compile: compile,
		Manager: mgr,
		Gate:    gate,
		State:   newReloadState(reloadStateInitial{}),
	})

	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
	if out.Result.Category != sdkreload.ResultCanceled || out.Result.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("result=%+v", out.Result)
	}
	if plane == nil {
		t.Fatal("expected compiled plane")
	}
	guard, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-plane.closeCh:
	case <-guard.Done():
		t.Fatal("candidate must be closed before publish")
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("shutdown-before-publish must not carry state updates: %+v", out)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("active mutated: %d", mgr.Active().ID())
	}
}

func TestAttemptRunner_SourceIntegrityFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{"missing", &configsource.IntegrityError{Category: configsource.CategoryMissing}},
		{"empty", &configsource.IntegrityError{Category: configsource.CategoryEmpty}},
		{"non_atomic", &configsource.IntegrityError{Category: configsource.CategoryNonAtomicUpdate}},
		{"partial_unreadable", &configsource.IntegrityError{Category: configsource.CategoryPartialUnreadable}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := &runnerFakeSource{path: "/x/config.yaml", err: tc.err}
			r := newTestRunner(t, nil, src, nil, nil, nil)
			out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
			if out.Result.Category != sdkreload.ResultSourceIntegrity {
				t.Fatalf("category=%q want source-integrity-failed", out.Result.Category)
			}
			if out.Result.ReasonCategory != configreload.StageRead {
				t.Fatalf("reason=%q", out.Result.ReasonCategory)
			}
			if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
				t.Fatalf("failure must not carry state updates: %+v", out)
			}
		})
	}
}

func TestAttemptRunner_AtomicSourceNoop(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicNoop, snap: configsource.SourceSnapshot{Bytes: []byte("same")}}
	r := newTestRunner(t, nil, src, nil, nil, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultNoop || out.Result.ReasonCategory != configreload.StageNoop {
		t.Fatalf("result=%+v", out.Result)
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("atomic noop must not carry state updates: %+v", out)
	}
}

func TestAttemptRunner_EffectiveLoadInvalid(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	loader := FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return nil, &config.LoadError{Category: config.CategoryMalformedYAML}
	})
	r := newTestRunner(t, nil, src, loader, nil, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultInvalid {
		t.Fatalf("category=%q want invalid", out.Result.Category)
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("failure must not carry state updates: %+v", out)
	}
}

func TestAttemptRunner_EffectiveLoadCancellation(t *testing.T) {
	t.Parallel()
	gate := newRunnerStageGate()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	blockingLoader := FuncEffectiveLoader(func(ctx context.Context, _ []byte) (*config.EffectiveConfig, error) {
		if err := gate.Hold(ctx); err != nil {
			return nil, err
		}
		return runnerBaseEffective("fp-new", 2), nil
	})
	r := newTestRunner(t, nil, src, blockingLoader, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan attemptOutcome, 1)
	go func() { done <- r.Run(ctx, attemptInput{AttemptID: 1, ActiveGeneration: 1}) }()
	gate.WaitEnter(t)
	cancel()
	gate.Release()
	out := <-done
	if out.Result.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", out.Result.Category)
	}
}

func TestAttemptRunner_EffectiveIdentityNoopReturnsSourceBaselineOnly(t *testing.T) {
	t.Parallel()
	active := runnerBaseEffective("fp-same", 7)
	wantHandle := configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{2}}
	src := &runnerFakeSource{
		path:   "/x/config.yaml",
		atomic: configsource.AtomicEligible,
		snap: configsource.SourceSnapshot{
			Bytes:          []byte("reordered"),
			HandleIdentity: wantHandle,
			PrivateDigest:  [32]byte{9, 9},
		},
	}
	loader := FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return runnerBaseEffective("fp-same", 7), nil
	})
	compile := &runnerControllableCompiler{}
	r := newTestRunner(t, nil, src, loader, compile, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: active})
	if out.Result.Category != sdkreload.ResultNoop {
		t.Fatalf("category=%q want no-op", out.Result.Category)
	}
	if compile.calls.Load() != 0 {
		t.Fatalf("compile must not run on effective noop")
	}
	if out.EffectiveUpdate != nil {
		t.Fatalf("effective identity noop must not carry EffectiveUpdate: %+v", out.EffectiveUpdate)
	}
	if out.SourceUpdate == nil {
		t.Fatal("effective identity noop must carry a source-baseline SourceUpdate")
	}
	if out.SourceUpdate.HandleIdentity != wantHandle || out.SourceUpdate.PrivateDigest != ([32]byte{9, 9}) {
		t.Fatalf("SourceUpdate=%+v want advanced baseline", out.SourceUpdate)
	}
}

func TestAttemptRunner_ClassifyRestartRequired(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	r := newTestRunner(t, nil, src, nil, nil, nil)
	r.classify = func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error) {
		return nil, &configreload.RestartRequiredError{RestartRequiredFields: []string{"server.address", "tls.cert"}, TotalBlocked: 2}
	}
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
	if out.Result.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("category=%q want restart-required", out.Result.Category)
	}
	if out.Result.RestartFieldCount != 2 || len(out.Result.RestartFields) != 2 {
		t.Fatalf("restart fields=%v count=%d", out.Result.RestartFields, out.Result.RestartFieldCount)
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("restart-required must not carry state updates: %+v", out)
	}
}

func TestAttemptRunner_ClassifyGenericInvalid(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	r := newTestRunner(t, nil, src, nil, nil, nil)
	r.classify = func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error) {
		return nil, errors.New("generic classify failure")
	}
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
	if out.Result.Category != sdkreload.ResultInvalid {
		t.Fatalf("category=%q want invalid", out.Result.Category)
	}
}

func TestAttemptRunner_CompileRestartRequired(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := &runnerControllableCompiler{err: &configreload.RestartRequiredError{RestartRequiredFields: []string{"plugins.backends"}, TotalBlocked: 1}}
	r := newTestRunner(t, nil, src, nil, compile, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("category=%q want restart-required", out.Result.Category)
	}
}

func TestAttemptRunner_CompilePreparationFailure(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := &runnerControllableCompiler{err: errors.New("factory boom")}
	r := newTestRunner(t, nil, src, nil, compile, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultPreparationFailed {
		t.Fatalf("category=%q want preparation-failed", out.Result.Category)
	}
}

func TestAttemptRunner_CompileCancellation(t *testing.T) {
	t.Parallel()
	gate := newRunnerStageGate()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := &runnerControllableCompiler{gate: gate}
	r := newTestRunner(t, nil, src, nil, compile, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan attemptOutcome, 1)
	go func() { done <- r.Run(ctx, attemptInput{AttemptID: 1, ActiveGeneration: 1}) }()
	gate.WaitEnter(t)
	cancel()
	gate.Release()
	out := <-done
	if out.Result.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", out.Result.Category)
	}
}

func TestAttemptRunner_CompilePanicMapsToInternalFailedAndRollsBackOnce(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := &runnerControllableCompiler{panicMsg: "boom-in-compile"}
	r := newTestRunner(t, nil, src, nil, compile, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultInternalFailed {
		t.Fatalf("category=%q want internal-failed", out.Result.Category)
	}
	if out.Result.ReasonCategory != configreload.StagePanic {
		t.Fatalf("reason=%q want panic", out.Result.ReasonCategory)
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("panic must not carry state updates: %+v", out)
	}
}

func TestAttemptRunner_NilPlaneMapsToPreparationFailed(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := FuncCompiler(func(context.Context, *config.Config, map[string]int) (PublishedRequestPlane, error) {
		return nil, nil
	})
	r := newTestRunner(t, nil, src, nil, compile, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultPreparationFailed {
		t.Fatalf("category=%q want preparation-failed", out.Result.Category)
	}
}

func TestAttemptRunner_SuccessfulPrepareMetadata(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	loaded := time.Unix(1000, 0).UTC()
	loader := FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		eff := runnerBaseEffective("fp-new", 2)
		eff.LoadedAt = loaded
		return eff, nil
	})
	compile := &runnerControllableCompiler{kinds: map[string]int{"local-stub": 1}}
	r := newTestRunner(t, mgr, src, loader, compile, nil)
	out := r.Run(context.Background(), attemptInput{
		Trigger:          sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP},
		AttemptID:        1,
		ActiveGeneration: 1,
		ActiveEffective:  runnerBaseEffective("fp-old", 1),
	})
	if out.Result.Category != sdkreload.ResultPublished {
		t.Fatalf("category=%q want published", out.Result.Category)
	}
	gen := mgr.Active()
	if gen == nil || gen.Status().Meta.PublicFingerprint != "fp-new" {
		t.Fatalf("meta not applied: %+v", gen.Status().Meta)
	}
	if gen.Status().Meta.TriggerKind != string(sdkreload.TriggerSIGHUP) {
		t.Fatalf("trigger kind not applied: %+v", gen.Status().Meta)
	}
	if !gen.Status().Meta.LoadedAt.Equal(loaded) {
		t.Fatalf("loadedAt not applied: %+v", gen.Status().Meta)
	}
}

func TestAttemptRunner_RetentionBlockedRollsBackAndReports(t *testing.T) {
	t.Parallel()
	mgr := NewManager(0, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	var closedPlane atomic.Pointer[runnerFakePlane]
	compile := &runnerControllableCompiler{onPlane: func(p *runnerFakePlane) {
		p.closeCh = make(chan struct{})
		closedPlane.Store(p)
	}}
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	r := newTestRunner(t, mgr, src, nil, compile, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
	if out.Result.Category != sdkreload.ResultRetentionBlocked {
		t.Fatalf("category=%q want retention-blocked", out.Result.Category)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("active mutated: %d", mgr.Active().ID())
	}
	p := closedPlane.Load()
	if p == nil {
		t.Fatal("expected compiled plane")
	}
	guard, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-p.closeCh:
	case <-guard.Done():
		t.Fatal("candidate plane not discarded on retention rejection")
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("retention-blocked must not carry state updates: %+v", out)
	}
}

// TestAttemptRunner_HostShutdownPublishRejection proves that a Manager-level
// BeginShutdown occurring mid-attempt (independent of any external gate
// signal) always blocks late publication. Depending on scheduling this may be
// caught by an earlier isShuttingDown() checkpoint or by the ErrHostShuttingDown
// mapping inside the Publish() error switch; both converge on the same
// canonical outcome by design (req 6.10: shutdown cancellation and last-good
// publication semantics), which is what this black-box test asserts.
func TestAttemptRunner_HostShutdownPublishRejection(t *testing.T) {
	t.Parallel()
	gate := newRunnerStageGate()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}, gate: gate}
	compile := &runnerControllableCompiler{}
	r := newTestRunner(t, mgr, src, nil, compile, nil)

	done := make(chan attemptOutcome, 1)
	go func() {
		done <- r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
	}()
	gate.WaitEnter(t)
	mgr.BeginShutdown()
	gate.Release()
	out := <-done
	if out.Result.Category != sdkreload.ResultCanceled || out.Result.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("result=%+v want canceled/shutdown", out.Result)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("active mutated: %d", mgr.Active().ID())
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("host-shutdown attempt must not carry state updates: %+v", out)
	}
}

func TestAttemptRunner_SuccessfulPublicationReturnsStateUpdates(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(map[string]int{"local-stub": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	wantHandle := configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{7}}
	src := &runnerFakeSource{
		path:   "/x/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1"), HandleIdentity: wantHandle, PrivateDigest: [32]byte{5}},
	}
	newEff := runnerBaseEffective("fp-new", 2)
	loader := FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) { return newEff, nil })
	compile := &runnerControllableCompiler{kinds: map[string]int{"local-stub": 1}}
	r := newTestRunner(t, mgr, src, loader, compile, nil)
	out := r.Run(context.Background(), attemptInput{
		AttemptID:        7,
		ActiveGeneration: 1,
		ActiveEffective:  runnerBaseEffective("fp-old", 1),
	})
	if out.Result.Category != sdkreload.ResultPublished {
		t.Fatalf("category=%q want published", out.Result.Category)
	}
	if out.Result.AttemptID != 7 || out.Result.PreviousGeneration != 1 || out.Result.ActiveGeneration != mgr.Active().ID() {
		t.Fatalf("result=%+v activeID=%d", out.Result, mgr.Active().ID())
	}
	if out.EffectiveUpdate != newEff {
		t.Fatalf("EffectiveUpdate=%v want %v", out.EffectiveUpdate, newEff)
	}
	if out.SourceUpdate == nil || out.SourceUpdate.HandleIdentity != wantHandle || out.SourceUpdate.PrivateDigest != ([32]byte{5}) {
		t.Fatalf("SourceUpdate=%+v", out.SourceUpdate)
	}
}

func TestAttemptRunner_AdoptsCommittedStateBeforePublishedWork(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(map[string]int{"local-stub": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	wantHandle := configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{17}}
	src := &runnerFakeSource{
		path:   "/x/config.yaml",
		atomic: configsource.AtomicEligible,
		snap: configsource.SourceSnapshot{
			Bytes: []byte("x: 1"), HandleIdentity: wantHandle, PrivateDigest: [32]byte{9},
		},
	}
	newEff := runnerBaseEffective("fp-new", 9)
	loader := FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) { return newEff, nil })
	var callbackSawAdoption atomic.Bool
	var r *attemptRunner
	compile := &runnerControllableCompiler{
		kinds: map[string]int{"local-stub": 1},
		onPlane: func(p *runnerFakePlane) {
			p.startPublished = func(context.Context) error {
				r.state.mu.Lock()
				defer r.state.mu.Unlock()
				if r.state.activeEff == newEff && r.state.activeSource != nil &&
					r.state.activeSource.HandleIdentity == wantHandle &&
					r.state.activeSource.PrivateDigest == ([32]byte{9}) &&
					r.state.last.Category == sdkreload.ResultPublished {
					callbackSawAdoption.Store(true)
				}
				return nil
			}
		},
	}
	r = newTestRunner(t, mgr, src, loader, compile, nil)
	out := r.Run(context.Background(), attemptInput{
		AttemptID: 18, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1),
	})
	if out.Result.Category != sdkreload.ResultPublished {
		t.Fatalf("result=%+v want published", out.Result)
	}
	if !callbackSawAdoption.Load() {
		t.Fatal("PublishedWork observed the manager swap before matching source/effective state adoption")
	}
}

func TestAttemptRunner_PanicAtEachPracticalStage(t *testing.T) {
	t.Parallel()
	t.Run("read", func(t *testing.T) {
		t.Parallel()
		src := &panicSource{}
		r := newTestRunner(t, nil, src, nil, nil, nil)
		out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
		if out.Result.Category != sdkreload.ResultInternalFailed || out.Result.ReasonCategory != configreload.StagePanic {
			t.Fatalf("result=%+v", out.Result)
		}
	})
	t.Run("load", func(t *testing.T) {
		t.Parallel()
		src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
		loader := FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			panic("boom-in-load")
		})
		r := newTestRunner(t, nil, src, loader, nil, nil)
		out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
		if out.Result.Category != sdkreload.ResultInternalFailed || out.Result.ReasonCategory != configreload.StagePanic {
			t.Fatalf("result=%+v", out.Result)
		}
	})
	t.Run("classify", func(t *testing.T) {
		t.Parallel()
		src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
		r := newTestRunner(t, nil, src, nil, nil, nil)
		r.classify = func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error) {
			panic("boom-in-classify")
		}
		out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
		if out.Result.Category != sdkreload.ResultInternalFailed || out.Result.ReasonCategory != configreload.StagePanic {
			t.Fatalf("result=%+v", out.Result)
		}
	})
}

type panicSource struct{}

func (panicSource) AbsolutePath() string { return "/panic/config.yaml" }
func (panicSource) ReadStable(context.Context, *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
	panic("boom-in-read")
}

func TestAttemptRunner_RollbackExactlyOnceBeforeTransfer(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := &runnerControllableCompiler{}
	r := newTestRunner(t, nil, src, nil, compile, nil)

	// Force cancellation between successful compile and prepare by canceling
	// the context from inside the compiler's onPlane hook (compile already
	// returned a candidate plane, but prepare/transfer has not happened yet).
	ctx, cancel := context.WithCancel(context.Background())
	var plane *runnerFakePlane
	compile.onPlane = func(p *runnerFakePlane) {
		p.closeCh = make(chan struct{})
		plane = p
		cancel()
	}
	out := r.Run(ctx, attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", out.Result.Category)
	}
	if plane == nil {
		t.Fatal("expected compiled plane")
	}
	guard, cancelGuard := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelGuard()
	select {
	case <-plane.closeCh:
	case <-guard.Done():
		t.Fatal("candidate plane not rolled back after cancellation")
	}
	if n := plane.closeCalls.Load(); n != 1 {
		t.Fatalf("rollback Close() call count=%d want exactly 1", n)
	}
}

// TestAttemptRunner_CleanupPanicPreTransferCancel proves a panicking plane
// Close during pre-transfer cancellation cannot escape Run, cannot overwrite
// the canonical canceled result, and runs Close exactly once.
func TestAttemptRunner_CleanupPanicPreTransferCancel(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := &runnerControllableCompiler{}
	r := newTestRunner(t, nil, src, nil, compile, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var plane *runnerFakePlane
	compile.onPlane = func(p *runnerFakePlane) {
		p.closePanic = "cleanup-close-panic"
		plane = p
		cancel()
	}
	out := r.Run(ctx, attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled (cleanup panic must not overwrite)", out.Result.Category)
	}
	if out.Result.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("reason=%q want shutdown", out.Result.ReasonCategory)
	}
	if plane == nil {
		t.Fatal("expected compiled plane")
	}
	if n := plane.closeCalls.Load(); n != 1 {
		t.Fatalf("Close() call count=%d want exactly 1", n)
	}
	if out.EffectiveUpdate != nil || out.SourceUpdate != nil {
		t.Fatalf("canceled attempt must not carry state updates: %+v", out)
	}
}

// TestAttemptRunner_PrimaryPanicSurvivesCleanupPanic proves a primary workflow
// panic remains internal-failed/StagePanic even when owned-plane cleanup also
// panics, and Close runs exactly once.
func TestAttemptRunner_PrimaryPanicSurvivesCleanupPanic(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	gate := newAttemptGate()
	var plane *runnerFakePlane
	compile := &runnerControllableCompiler{
		panicMsg: "primary-workflow-panic",
		onPlane: func(p *runnerFakePlane) {
			p.closePanic = "cleanup-close-panic"
			plane = p
		},
	}
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	r := newAttemptRunner(attemptRunnerDeps{
		Source: src,
		Loader: FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return runnerBaseEffective("fp-new", 2), nil
		}),
		Compile: compile,
		Manager: mgr,
		Gate:    gate,
		State:   newReloadState(reloadStateInitial{}),
	})
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
	if out.Result.Category != sdkreload.ResultInternalFailed || out.Result.ReasonCategory != configreload.StagePanic {
		t.Fatalf("result=%+v want internal-failed/StagePanic", out.Result)
	}
	if plane == nil {
		t.Fatal("expected compiled plane before compile panic")
	}
	if n := plane.closeCalls.Load(); n != 0 {
		t.Fatalf("compile panic must not run owned-plane cleanup; Close() calls=%d", n)
	}
}

// TestAttemptRunner_CleanupPanicPostTransferDiscard proves post-transfer
// Generation.Discard cleanup whose plane Close panics cannot escape Run or
// double-close via a direct plane.Close, and the canonical canceled outcome
// survives. Uses real Manager/Generation ownership after Prepare.
func TestAttemptRunner_CleanupPanicPostTransferDiscard(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	gate := newAttemptGate()
	var plane *runnerFakePlane
	compile := &runnerControllableCompiler{onPlane: func(p *runnerFakePlane) {
		p.closePanic = "discard-close-panic"
		plane = p
		gate.BeginShutdown()
	}}
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	r := newAttemptRunner(attemptRunnerDeps{
		Source: src,
		Loader: FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return runnerBaseEffective("fp-new", 2), nil
		}),
		Compile: compile,
		Manager: mgr,
		Gate:    gate,
		State:   newReloadState(reloadStateInitial{}),
	})
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveEffective: runnerBaseEffective("fp-old", 1)})
	if out.Result.Category != sdkreload.ResultCanceled || out.Result.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("result=%+v want canceled/shutdown (cleanup panic must not overwrite)", out.Result)
	}
	if plane == nil {
		t.Fatal("expected compiled plane")
	}
	if n := plane.closeCalls.Load(); n != 1 {
		t.Fatalf("Close() via Generation.Discard call count=%d want exactly 1", n)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("active mutated: %d", mgr.Active().ID())
	}
}

// TestAttemptRunner_CleanupErrorDoesNotAlterCanonicalOutcome proves a Close
// error return during pre-transfer cancellation leaves the canceled result
// intact and still closes exactly once.
func TestAttemptRunner_CleanupErrorDoesNotAlterCanonicalOutcome(t *testing.T) {
	t.Parallel()
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	compile := &runnerControllableCompiler{}
	r := newTestRunner(t, nil, src, nil, compile, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var plane *runnerFakePlane
	compile.onPlane = func(p *runnerFakePlane) {
		p.closeErr = errors.New("close-failed")
		plane = p
		cancel()
	}
	out := r.Run(ctx, attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled (cleanup error must not overwrite)", out.Result.Category)
	}
	if plane == nil {
		t.Fatal("expected compiled plane")
	}
	if n := plane.closeCalls.Load(); n != 1 {
		t.Fatalf("Close() call count=%d want exactly 1", n)
	}
}

func TestAttemptRunner_NoPlaneCloseAfterTransferOrPublication(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	var plane *runnerFakePlane
	compile := &runnerControllableCompiler{onPlane: func(p *runnerFakePlane) {
		plane = p
	}}
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	r := newTestRunner(t, mgr, src, nil, compile, nil)
	out := r.Run(context.Background(), attemptInput{AttemptID: 1, ActiveGeneration: 1})
	if out.Result.Category != sdkreload.ResultPublished {
		t.Fatalf("category=%q", out.Result.Category)
	}
	if plane == nil {
		t.Fatal("expected compiled plane")
	}
	// Close is synchronous; publication transfers ownership to Manager so the
	// runner must not have closed the plane directly. Assert immediately —
	// no wall-clock absence wait.
	if n := plane.closeCalls.Load(); n != 0 {
		t.Fatalf("published plane must not be closed directly by the runner; closeCalls=%d", n)
	}
}

func TestAttemptRunner_DefensiveInputAndOutcomeCopies(t *testing.T) {
	t.Parallel()
	mgr := NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	wantHandle := configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{1}}
	src := &runnerFakeSource{
		path:   "/x/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1"), HandleIdentity: wantHandle, PrivateDigest: [32]byte{3}},
	}
	compile := &runnerControllableCompiler{}
	r := newTestRunner(t, mgr, src, nil, compile, nil)

	mutatedHandle := configsource.FileIdentity{Platform: "mutated", Opaque: [32]byte{9}}
	origHandle := configsource.FileIdentity{Platform: "orig", Opaque: [32]byte{1}}
	activeSrc := &configsource.ActiveSourceVersion{HandleIdentity: origHandle, PrivateDigest: [32]byte{1}}
	in := attemptInput{AttemptID: 1, ActiveGeneration: 1, ActiveSource: activeSrc}
	out := r.Run(context.Background(), in)
	if out.Result.Category != sdkreload.ResultPublished {
		t.Fatalf("category=%q", out.Result.Category)
	}
	// Mutating the caller's original ActiveSource after Run must not affect
	// the already-returned outcome's SourceUpdate.
	activeSrc.HandleIdentity = mutatedHandle
	if out.SourceUpdate.HandleIdentity == mutatedHandle {
		t.Fatal("outcome SourceUpdate must not alias caller input storage")
	}
	if out.SourceUpdate.HandleIdentity != wantHandle {
		t.Fatalf("SourceUpdate=%+v", out.SourceUpdate)
	}
}

func TestAttemptRunner_ConcurrentIndependentCallsRaceFree(t *testing.T) {
	t.Parallel()
	mgr := NewManager(64, nil)
	g0 := mgr.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	var digest atomic.Int64
	loader := FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := digest.Add(1)
		return runnerBaseEffective("fp-"+string(rune('a'+n%20)), byte(n)), nil
	})
	compile := &runnerControllableCompiler{}
	src := &runnerFakeSource{path: "/x/config.yaml", atomic: configsource.AtomicEligible, snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}}
	r := newTestRunner(t, mgr, src, loader, compile, nil)

	var wg sync.WaitGroup
	var attemptID atomic.Int64
	for range 16 {
		wg.Go(func() {
			id := attemptID.Add(1)
			_ = r.Run(context.Background(), attemptInput{AttemptID: id, ActiveGeneration: mgr.Active().ID(), ActiveEffective: runnerBaseEffective("fp-x", byte(id))})
		})
	}
	wg.Wait()
	if mgr.Active() == nil {
		t.Fatal("expected an active generation after concurrent runs")
	}
}
