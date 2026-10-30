package runtimehost_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

type fakePlane struct {
	closed  atomic.Bool
	kinds   map[string]int
	closeCh chan struct{}
	handler http.Handler
}

func newFakePlane(kinds map[string]int) *fakePlane {
	return &fakePlane{kinds: kinds, handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
}

func (p *fakePlane) Handler() http.Handler         { return p.handler }
func (p *fakePlane) Quiesce(context.Context) error { return nil }
func (p *fakePlane) Close() error {
	if p.closed.Swap(true) {
		return runtimehost.ErrAlreadyClosed
	}
	if p.closeCh != nil {
		close(p.closeCh)
	}
	return nil
}

func (p *fakePlane) BackendFactoryKindCounts() map[string]int {
	if p == nil || p.kinds == nil {
		return nil
	}
	out := make(map[string]int, len(p.kinds))
	maps.Copy(out, p.kinds)
	return out
}

type stageGate struct {
	mu      sync.Mutex
	enter   chan struct{}
	release chan struct{}
}

func newStageGate() *stageGate {
	return &stageGate{enter: make(chan struct{}), release: make(chan struct{})}
}

func (g *stageGate) WaitEnter(t *testing.T) {
	t.Helper()
	select {
	case <-g.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stage enter")
	}
}

func (g *stageGate) Release() { close(g.release) }

func (g *stageGate) Hold(ctx context.Context) error {
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

type fakeSource struct {
	path   string
	snap   configsource.SourceSnapshot
	atomic configsource.AtomicResult
	err    error
	gate   *stageGate
	calls  atomic.Int64
}

func (s *fakeSource) AbsolutePath() string { return s.path }
func (s *fakeSource) ReadStable(ctx context.Context, _ *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
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

type controllableCompiler struct {
	gate     *stageGate
	err      error
	panicMsg string
	kinds    map[string]int
	onPlane  func(*fakePlane)
	calls    atomic.Int64
	liveSeen atomic.Value // map[string]int
}

func (c *controllableCompiler) Compile(ctx context.Context, _ *config.Config, live map[string]int) (runtimehost.PublishedRequestPlane, error) {
	c.calls.Add(1)
	if live != nil {
		cp := make(map[string]int, len(live))
		maps.Copy(cp, live)
		c.liveSeen.Store(cp)
	}
	if c.panicMsg != "" {
		panic(c.panicMsg)
	}
	if c.gate != nil {
		if err := c.gate.Hold(ctx); err != nil {
			return nil, err
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	p := newFakePlane(c.kinds)
	if c.onPlane != nil {
		c.onPlane(p)
	}
	return p, nil
}

func baseEffective(fp string, digest byte) *config.EffectiveConfig {
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

func newTestCoordinator(t *testing.T, mgr *runtimehost.Manager, src *fakeSource, loader runtimehost.EffectiveLoader, compile runtimehost.CandidateCompiler, active *config.EffectiveConfig) *runtimehost.Coordinator {
	t.Helper()
	if mgr == nil {
		mgr = runtimehost.NewManager(8, nil)
		g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
		g0.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-startup", TriggerKind: "startup"})
		if err := mgr.Publish(g0); err != nil {
			t.Fatalf("publish gen1: %v", err)
		}
	}
	if src == nil {
		src = &fakeSource{path: "/fixed/startup/config.yaml", snap: configsource.SourceSnapshot{Bytes: []byte("x: 1")}, atomic: configsource.AtomicEligible}
	}
	if loader == nil {
		loader = runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return baseEffective("fp-new", 2), nil
		})
	}
	if compile == nil {
		compile = &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	}
	if active == nil {
		active = baseEffective("fp-startup", 1)
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Compile:         compile,
		Manager:         mgr,
		Timeout:         time.Second,
		ActiveEffective: active,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

func TestCoordinator_BusyRejectsAPIWhileActive(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
		atomic: configsource.AtomicEligible,
		gate:   gate,
	}
	c := newTestCoordinator(t, nil, src, nil, nil, nil)

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)

	busy := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if busy.Category != sdkreload.ResultBusy {
		t.Fatalf("busy category=%q", busy.Category)
	}
	if st := c.Status(); !st.Busy || st.ActiveGeneration != 1 {
		t.Fatalf("status=%+v", st)
	}

	gate.Release()
	res := <-done
	if res.Category != sdkreload.ResultPublished {
		t.Fatalf("first result=%q", res.Category)
	}
	if c.Status().Busy {
		t.Fatal("expected not busy after completion")
	}
}

func TestCoordinator_BusyAndCoalesceNeverExceedOneActiveCompile(t *testing.T) {
	t.Parallel()
	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64
	gate := newStageGate()
	compile := &controllableCompiler{
		gate:  gate,
		kinds: map[string]int{"local-stub": 1},
	}
	// Wrap compile to count concurrency around the gated Hold.
	base := compile
	wrapped := runtimehost.FuncCompiler(func(ctx context.Context, cfg *config.Config, live map[string]int) (runtimehost.PublishedRequestPlane, error) {
		n := concurrent.Add(1)
		for {
			cur := maxConcurrent.Load()
			if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
				break
			}
		}
		defer concurrent.Add(-1)
		return base.Compile(ctx, cfg, live)
	})
	digest := byte(10)
	loaderN := atomic.Int64{}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loaderN.Add(1)
		return baseEffective("fp-c"+string(rune('a'+n)), digest+byte(n)), nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, loader, wrapped, baseEffective("fp-old", 1))

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kind := sdkreload.TriggerAPI
			if i%2 == 0 {
				kind = sdkreload.TriggerSIGHUP
			}
			_ = c.Reload(context.Background(), sdkreload.Trigger{Kind: kind})
		}(i)
	}
	time.Sleep(20 * time.Millisecond) // let hammer land while first is in compile
	gate.Release()
	<-done
	wg.Wait()

	// Drain any coalesced follow-up that may still be finishing.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && c.Status().Busy {
		time.Sleep(5 * time.Millisecond)
	}
	if maxConcurrent.Load() > 1 {
		t.Fatalf("max concurrent compiles=%d want <=1", maxConcurrent.Load())
	}
}

func TestCoordinator_NoopLeavesActiveGenerationUnchanged(t *testing.T) {
	t.Parallel()
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicNoop,
		snap:   configsource.SourceSnapshot{Bytes: []byte("same")},
	}
	c := newTestCoordinator(t, nil, src, nil, nil, nil)
	before := c.Status().ActiveGeneration
	res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultNoop {
		t.Fatalf("category=%q", res.Category)
	}
	if res.ActiveGeneration != before || c.Status().ActiveGeneration != before {
		t.Fatalf("active changed: res=%d status=%d before=%d", res.ActiveGeneration, c.Status().ActiveGeneration, before)
	}
}

func TestCoordinator_NoopOnMatchingEffectiveFingerprint(t *testing.T) {
	t.Parallel()
	active := baseEffective("fp-same", 7)
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("reordered")},
	}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return baseEffective("fp-same", 7), nil
	})
	compile := &controllableCompiler{kinds: nil}
	c := newTestCoordinator(t, nil, src, loader, compile, active)
	res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultNoop {
		t.Fatalf("category=%q", res.Category)
	}
	if compile.calls.Load() != 0 {
		t.Fatalf("compile called on noop: %d", compile.calls.Load())
	}
}

// Effective no-op after an atomic path replace must still advance the coordinator's
// active source identity. Otherwise a later in-place rewrite of the new inode is
// compared against the pre-rename handle and incorrectly classified as AtomicEligible
// (req 2.9 / source_non_atomic_update).
func TestCoordinator_EffectiveNoopAdvancesActiveSource_RejectsInPlaceEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	bodyA := []byte("server:\n  address: \"127.0.0.1:0\"\n")
	if err := os.WriteFile(path, bodyA, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snap1, res, err := src.ReadStable(ctx, nil)
	if err != nil || res != configsource.AtomicEligible {
		t.Fatalf("startup read: res=%q err=%v", res, err)
	}
	activeSrc, activeOwner := snap1.TakeBaseline()
	activeEff := baseEffective("fp-same", 7)
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return baseEffective("fp-same", 7), nil
	})
	compile := &controllableCompiler{kinds: nil}
	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
	g0.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-startup", TriggerKind: "startup"})
	if err := mgr.Publish(g0); err != nil {
		t.Fatalf("publish gen1: %v", err)
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:            src,
		Loader:            loader,
		Compile:           compile,
		Manager:           mgr,
		Timeout:           time.Second,
		ActiveEffective:   activeEff,
		ActiveSource:      activeSrc,
		ActiveSourceOwner: activeOwner,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	defer func() {
		c.BeginShutdown()
		_ = c.WaitForIdle(context.Background())
	}()
	if runtime.GOOS == "linux" && activeSrc.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
		res := c.Reload(ctx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if res.Category != sdkreload.ResultSourceIntegrity {
			t.Fatalf("unsupported Linux runtime comparison category=%q want source-integrity", res.Category)
		}
		return
	}

	bodyB := []byte("server:\n  address: \"127.0.0.1:0\"\n# reorder-noop\n")
	tmp := filepath.Join(dir, "config.yaml.tmp")
	if err := os.WriteFile(tmp, bodyB, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	noop := c.Reload(ctx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if noop.Category != sdkreload.ResultNoop {
		t.Fatalf("effective noop category=%q", noop.Category)
	}
	if compile.calls.Load() != 0 {
		t.Fatalf("compile called on effective noop: %d", compile.calls.Load())
	}

	bodyC := []byte("server:\n  address: \"127.0.0.1:9\"\n")
	if err := os.WriteFile(path, bodyC, 0o600); err != nil {
		t.Fatal(err)
	}
	rejected := c.Reload(ctx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if rejected.Category != sdkreload.ResultSourceIntegrity {
		t.Fatalf("in-place rewrite after effective noop: category=%q want %q", rejected.Category, sdkreload.ResultSourceIntegrity)
	}
	if rejected.ReasonCategory != configreload.StageRead {
		t.Fatalf("reason=%q want %q", rejected.ReasonCategory, configreload.StageRead)
	}
}

func TestCoordinator_FaultMatrixPrePublication(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      *fakeSource
		loader   runtimehost.EffectiveLoader
		classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
		compile  *controllableCompiler
		want     sdkreload.ResultCategory
	}{
		{
			name: "read_source_integrity",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryMissing},
			},
			want: sdkreload.ResultSourceIntegrity,
		},
		{
			name: "load_invalid",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
				return nil, &config.LoadError{Category: config.CategoryMalformedYAML}
			}),
			want: sdkreload.ResultInvalid,
		},
		{
			name: "classify_restart_required",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			classify: func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error) {
				return nil, &configreload.RestartRequiredError{
					RestartRequiredFields: []string{"server.address"},
					TotalBlocked:          1,
				}
			},
			want: sdkreload.ResultRestartRequired,
		},
		{
			name: "compile_preparation_failed",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			compile: &controllableCompiler{err: errors.New("factory boom")},
			want:    sdkreload.ResultPreparationFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := runtimehost.NewManager(8, nil)
			g0 := mgr.PrepareRequestPlane("startup", newFakePlane(nil))
			if err := mgr.Publish(g0); err != nil {
				t.Fatal(err)
			}
			loader := tc.loader
			if loader == nil {
				loader = runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
					return baseEffective("fp-cand", 9), nil
				})
			}
			compile := tc.compile
			if compile == nil {
				compile = &controllableCompiler{}
			}
			deps := runtimehost.CoordinatorDeps{
				Source:          tc.src,
				Loader:          loader,
				Classify:        tc.classify,
				Compile:         compile,
				Manager:         mgr,
				Timeout:         time.Second,
				ActiveEffective: baseEffective("fp-old", 1),
			}
			c, err := runtimehost.NewCoordinator(deps)
			if err != nil {
				t.Fatal(err)
			}
			res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			if res.Category != tc.want {
				t.Fatalf("category=%q want %q", res.Category, tc.want)
			}
			if mgr.Active().ID() != 1 {
				t.Fatalf("active generation changed to %d", mgr.Active().ID())
			}
		})
	}
}

func TestCoordinator_ShutdownCancelsAndPreventsLatePublication(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	compile := &controllableCompiler{gate: gate}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, nil, compile, nil)

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	c.BeginShutdown()
	gate.Release()
	res := <-done
	if res.Category != sdkreload.ResultCanceled {
		t.Fatalf("category=%q want canceled", res.Category)
	}
	if c.Status().ActiveGeneration != 1 {
		t.Fatalf("active=%d", c.Status().ActiveGeneration)
	}

	late := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if late.Category != sdkreload.ResultCanceled {
		t.Fatalf("late category=%q", late.Category)
	}
}

func TestCoordinator_CoalesceAtMostOnePendingSignal(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	compile := &controllableCompiler{gate: gate, kinds: map[string]int{"local-stub": 1}}
	digest := byte(2)
	loaderCalls := atomic.Int64{}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loaderCalls.Add(1)
		return baseEffective("fp-"+string(rune('a'+n)), digest+byte(n)), nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, loader, compile, baseEffective("fp-old", 1))

	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)

	b1 := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	b2 := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	b3 := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if b1.Category != sdkreload.ResultBusy || b2.Category != sdkreload.ResultBusy || b3.Category != sdkreload.ResultBusy {
		t.Fatalf("expected busy coalesce responses: %q %q %q", b1.Category, b2.Category, b3.Category)
	}
	st := c.Status()
	if !st.PendingSignal {
		t.Fatal("expected pending signal")
	}
	if st.CoalescedSignals < 1 {
		t.Fatalf("expected coalesced extras, got %d", st.CoalescedSignals)
	}

	// Unblock first attempt; follow-up coalesced attempt should run once more.
	gate.Release()
	first := <-done
	if first.Category != sdkreload.ResultPublished {
		t.Fatalf("first=%q", first.Category)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if compile.calls.Load() >= 2 && !c.Status().Busy && !c.Status().PendingSignal {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if compile.calls.Load() != 2 {
		t.Fatalf("compile calls=%d want 2 (one active + one coalesced)", compile.calls.Load())
	}
	if c.Status().PendingSignal {
		t.Fatal("pending signal should be drained")
	}
	if c.Status().ActiveGeneration != 3 {
		t.Fatalf("active=%d want 3", c.Status().ActiveGeneration)
	}
}

func TestCoordinator_RetentionRejectionRollsBack(t *testing.T) {
	t.Parallel()
	mgr := runtimehost.NewManager(0, nil) // max retained 0 → second publish blocked when active exists
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(nil))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	var closedPlane atomic.Pointer[fakePlane]
	compile := &controllableCompiler{
		kinds: nil,
		onPlane: func(p *fakePlane) {
			p.closeCh = make(chan struct{})
			closedPlane.Store(p)
		},
	}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, mgr, src, nil, compile, baseEffective("fp-old", 1))
	res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultRetentionBlocked {
		t.Fatalf("category=%q", res.Category)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("active=%d", mgr.Active().ID())
	}
	p := closedPlane.Load()
	if p == nil {
		t.Fatal("expected compiled plane")
	}
	select {
	case <-p.closeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate plane not discarded on retention rejection")
	}
}

func TestCoordinator_PanicIsolationDoesNotMutateActive(t *testing.T) {
	t.Parallel()
	compile := &controllableCompiler{panicMsg: "boom-in-compile"}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, nil, compile, nil)
	res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultInternalFailed {
		t.Fatalf("category=%q", res.Category)
	}
	if res.ReasonCategory != configreload.StagePanic {
		t.Fatalf("reason=%q", res.ReasonCategory)
	}
	if c.Status().ActiveGeneration != 1 {
		t.Fatalf("active=%d", c.Status().ActiveGeneration)
	}
}

func TestCoordinator_LiveFactoryKindsFromActiveAndRetained(t *testing.T) {
	t.Parallel()
	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1, "shared": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	// Pin g0 so Manager's automatic post-publish retirement (task 7.3) cannot
	// quiesce/close/sweep it before Reload sums live factory kinds across
	// active+retained generations below.
	lease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("acquire g0")
	}
	pin, ok := lease.TransferPin(runtimehost.PinProvider)
	if !ok {
		t.Fatal("pin g0")
	}
	t.Cleanup(pin.Release)
	// Occupy retention with another generation so LiveFactoryKinds sums both.
	g1plane := newFakePlane(map[string]int{"local-stub": 1})
	g1 := mgr.PrepareRequestPlane("prev", g1plane)
	if err := mgr.Publish(g1); err != nil {
		t.Fatal(err)
	}
	compile := &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, mgr, src, nil, compile, baseEffective("fp-old", 1))
	res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultPublished {
		t.Fatalf("category=%q", res.Category)
	}
	live, _ := compile.liveSeen.Load().(map[string]int)
	if live["local-stub"] < 2 {
		t.Fatalf("liveFactoryKinds=%v want local-stub>=2 from active+retained", live)
	}
	if live["shared"] < 1 {
		t.Fatalf("liveFactoryKinds=%v missing shared from retained", live)
	}
}

func TestCoordinator_HostTimeoutIndependentOfClientCancel(t *testing.T) {
	t.Parallel()
	gate := newStageGate()
	compile := &controllableCompiler{gate: gate}
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
	}
	c := newTestCoordinator(t, nil, src, nil, compile, nil)

	clientCtx, cancel := context.WithCancel(context.Background())
	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(clientCtx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	cancel() // client disconnect must not cancel host-owned attempt
	gate.Release()
	res := <-done
	if res.Category != sdkreload.ResultPublished {
		t.Fatalf("category=%q after client cancel", res.Category)
	}
}

// Phase 6.2 certification: last-good retention across the pre-publication fault
// matrix (source empty/partial, decode/validate/classify/compile/retention).
func TestCoordinator_LastGoodFaultMatrix_SourceDecodeClassifyCompileRetention(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      *fakeSource
		loader   runtimehost.EffectiveLoader
		classify func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
		compile  *controllableCompiler
		mgrMax   int
		want     sdkreload.ResultCategory
	}{
		{
			name: "source_empty",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryEmpty},
			},
			want: sdkreload.ResultSourceIntegrity,
		},
		{
			name: "source_partial_unreadable",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryPartialUnreadable},
			},
			want: sdkreload.ResultSourceIntegrity,
		},
		{
			name: "source_non_atomic_inplace",
			src: &fakeSource{
				path: "/fixed/startup/config.yaml",
				err:  &configsource.IntegrityError{Category: configsource.CategoryNonAtomicUpdate},
			},
			want: sdkreload.ResultSourceIntegrity,
		},
		{
			name: "decode_malformed_yaml",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("server: [")},
			},
			loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
				return nil, &config.LoadError{Category: config.CategoryMalformedYAML}
			}),
			want: sdkreload.ResultInvalid,
		},
		{
			name: "validate_unknown_core_field",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
				return nil, &config.LoadError{Category: config.CategoryUnknownCoreField}
			}),
			want: sdkreload.ResultInvalid,
		},
		{
			name: "classify_restart_required",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			classify: func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error) {
				return nil, &configreload.RestartRequiredError{
					RestartRequiredFields: []string{"server.address", "plugins.backends"},
					TotalBlocked:          2,
				}
			},
			want: sdkreload.ResultRestartRequired,
		},
		{
			name: "compile_preparation_failed",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			compile: &controllableCompiler{err: errors.New("factory boom")},
			want:    sdkreload.ResultPreparationFailed,
		},
		{
			name: "retention_blocked",
			src: &fakeSource{
				path:   "/fixed/startup/config.yaml",
				atomic: configsource.AtomicEligible,
				snap:   configsource.SourceSnapshot{Bytes: []byte("x: 1")},
			},
			compile: &controllableCompiler{},
			mgrMax:  0,
			want:    sdkreload.ResultRetentionBlocked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			maxRetained := 8
			if tc.mgrMax != 0 || tc.name == "retention_blocked" {
				maxRetained = tc.mgrMax
			}
			mgr := runtimehost.NewManager(maxRetained, nil)
			g0 := mgr.PrepareRequestPlane("startup", newFakePlane(nil))
			if err := mgr.Publish(g0); err != nil {
				t.Fatal(err)
			}
			loader := tc.loader
			if loader == nil {
				loader = runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
					return baseEffective("fp-cand", 9), nil
				})
			}
			compile := tc.compile
			if compile == nil {
				compile = &controllableCompiler{}
			}
			deps := runtimehost.CoordinatorDeps{
				Source:          tc.src,
				Loader:          loader,
				Classify:        tc.classify,
				Compile:         compile,
				Manager:         mgr,
				Timeout:         time.Second,
				ActiveEffective: baseEffective("fp-old", 1),
			}
			c, err := runtimehost.NewCoordinator(deps)
			if err != nil {
				t.Fatal(err)
			}
			res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			if res.Category != tc.want {
				t.Fatalf("category=%q want %q", res.Category, tc.want)
			}
			if mgr.Active().ID() != 1 {
				t.Fatalf("last-good active mutated to %d", mgr.Active().ID())
			}
		})
	}
}

func TestCoordinator_LastGood_AtomicRenameThenPublish(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	v1 := []byte("version: one\n")
	if err := os.WriteFile(path, v1, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snap1, res1, err := src.ReadStable(context.Background(), nil)
	if err != nil || res1 != configsource.AtomicEligible {
		t.Fatalf("bootstrap read: res=%q err=%v", res1, err)
	}
	activeSrc, activeOwner := snap1.TakeBaseline()

	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	compile := &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	var loadN atomic.Int64
	loader := runtimehost.FuncEffectiveLoader(func(_ context.Context, raw []byte) (*config.EffectiveConfig, error) {
		n := loadN.Add(1)
		return baseEffective("fp-"+string(raw), byte(n+1)), nil
	})
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:  src,
		Loader:  loader,
		Compile: compile,
		Manager: mgr,
		Timeout: time.Second,
		ActiveEffective: &config.EffectiveConfig{
			Config: &config.Config{},
			Identity: config.EffectiveIdentity{
				PrivateDigest:     snap1.PrivateDigest,
				PublicFingerprint: "fp-startup",
			},
		},
		ActiveSource:      activeSrc,
		ActiveSourceOwner: activeOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c.BeginShutdown()
		_ = c.WaitForIdle(context.Background())
	}()

	// In-place rewrite must retain last-good.
	if err := os.WriteFile(path, []byte("version: torn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inplace := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if inplace.Category != sdkreload.ResultSourceIntegrity {
		t.Fatalf("inplace category=%q", inplace.Category)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("inplace mutated active=%d", mgr.Active().ID())
	}
	if runtime.GOOS == "linux" && activeSrc.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
		tmp := filepath.Join(dir, "config.yaml.tmp")
		if err := os.WriteFile(tmp, []byte("version: two\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		unsupported := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if unsupported.Category != sdkreload.ResultSourceIntegrity {
			t.Fatalf("unsupported Linux atomic replacement category=%q want source-integrity", unsupported.Category)
		}
		return
	}

	// Atomic rename replacement is eligible and publishes.
	tmp := filepath.Join(dir, "config.yaml.tmp")
	if err := os.WriteFile(tmp, []byte("version: two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	pub := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if pub.Category != sdkreload.ResultPublished {
		t.Fatalf("atomic rename category=%q reason=%q", pub.Category, pub.ReasonCategory)
	}
	if mgr.Active().ID() != 2 {
		t.Fatalf("active=%d want 2", mgr.Active().ID())
	}
}

func TestCoordinatorShutdown_RequestCloseDoesNotWaitForBorrowAndWaitIsBounded(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("retained source leases are Linux-only")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := source.ReadStable(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	active, owner := snapshot.TakeBaseline()
	if active.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
		_ = owner.Close(context.Background())
		t.Skip("test filesystem does not provide supported ext4 lease evidence")
	}
	manager := runtimehost.NewManager(2, nil)
	generation := manager.PrepareRequestPlane("startup", newFakePlane(nil))
	if err := manager.Publish(generation); err != nil {
		t.Fatal(err)
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source: source, Loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return baseEffective("unused", 1), nil
		}),
		Compile: &controllableCompiler{}, Manager: manager,
		ActiveEffective: baseEffective("active", 1), ActiveSource: active, ActiveSourceOwner: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	borrow, err := active.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan struct{})
	go func() { c.BeginShutdown(); close(shutdownDone) }()
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		borrow.Release()
		t.Fatal("BeginShutdown waited for an unrelated source borrow")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WaitForIdle(canceled); !errors.Is(err, context.Canceled) {
		borrow.Release()
		t.Fatalf("WaitForIdle with outstanding borrow=%v want context canceled", err)
	}
	borrow.Release()
	if err := c.WaitForIdle(context.Background()); err != nil {
		t.Fatalf("WaitForIdle after borrow release: %v", err)
	}
}

func TestCoordinator_RestartRequired_MixedNoPartialApply_RequiresRetrigger(t *testing.T) {
	t.Parallel()
	activeCfg := &config.Config{
		Access:  config.AccessConfig{Mode: "single_user"},
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "stub:m"},
	}
	mixedCfg := &config.Config{
		Access:  config.AccessConfig{Mode: "multi_user"}, // startup-only
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 5, DefaultRoute: "stub:m"}, // reloadable
	}
	fixedCfg := &config.Config{
		Access:  config.AccessConfig{Mode: "single_user"},
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 5, DefaultRoute: "stub:m"},
	}

	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", newFakePlane(map[string]int{"local-stub": 1}))
	if err := mgr.Publish(g0); err != nil {
		t.Fatal(err)
	}
	compile := &controllableCompiler{kinds: map[string]int{"local-stub": 1}}
	var candidate atomic.Pointer[config.Config]
	candidate.Store(mixedCfg)
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		cfg := candidate.Load()
		var d [32]byte
		d[0] = byte(cfg.Routing.MaxAttempts)
		if cfg.Access.Mode == "multi_user" {
			d[1] = 9
		}
		return &config.EffectiveConfig{
			Config: cfg,
			Identity: config.EffectiveIdentity{
				PrivateDigest:     d,
				PublicFingerprint: cfg.Access.Mode + "-" + string(rune('0'+cfg.Routing.MaxAttempts)),
			},
		}, nil
	})
	src := &fakeSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   configsource.SourceSnapshot{Bytes: []byte("candidate")},
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Classify:        configreload.ClassifyEffective,
		Compile:         compile,
		Manager:         mgr,
		Timeout:         time.Second,
		ActiveEffective: &config.EffectiveConfig{Config: activeCfg, Identity: config.EffectiveIdentity{PublicFingerprint: "active", PrivateDigest: [32]byte{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	blocked := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if blocked.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("mixed category=%q want restart_required", blocked.Category)
	}
	if mgr.Active().ID() != 1 {
		t.Fatalf("partial apply leaked: active=%d", mgr.Active().ID())
	}
	if compile.calls.Load() != 0 {
		t.Fatalf("compile must not run on restart-required, calls=%d", compile.calls.Load())
	}

	// Correction alone does nothing until an explicit retrigger.
	candidate.Store(fixedCfg)
	if mgr.Active().ID() != 1 {
		t.Fatal("active mutated without retrigger")
	}
	pub := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if pub.Category != sdkreload.ResultPublished {
		t.Fatalf("retrigger after correction category=%q reason=%q", pub.Category, pub.ReasonCategory)
	}
	if mgr.Active().ID() != 2 {
		t.Fatalf("active=%d want 2 after explicit retrigger", mgr.Active().ID())
	}
	if compile.calls.Load() != 1 {
		t.Fatalf("compile calls=%d want 1", compile.calls.Load())
	}
}

// TestCoordinator_StatusReturnsDefensiveCopy proves (*Coordinator).Status() is a
// real query-boundary defensive snapshot: mutating one returned Status must not
// alter coordinator-owned state or a subsequent Status() snapshot, and two
// snapshots must not share mutable nested storage (RestartFields / History /
// CurrentAttempt).
//
// Nested population notes:
//   - RestartFields: populated via RestartRequiredError on a completed attempt.
//   - CurrentAttempt: populated while a follow-up attempt is held busy; it
//     mirrors the prior last result (including RestartFields) while busy.
//   - History: populated via ReloadObserver StatusHistory on terminal attempts.
//   - HistoryEntry values have no nested mutable slices; only the History slice
//     header is independently owned after the boundary copy.
func TestCoordinator_StatusReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	digest := [32]byte{9, 9, 9}
	src := &fakeSource{
		path: "/fixed/startup/config.yaml",
		snap: configsource.SourceSnapshot{
			Bytes:         []byte("x: 1"),
			PrivateDigest: digest,
		},
		atomic: configsource.AtomicEligible,
	}
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		return &config.EffectiveConfig{
			Config:   &config.Config{Routing: config.RoutingConfig{MaxAttempts: 3}},
			Identity: config.EffectiveIdentity{PrivateDigest: digest, PublicFingerprint: "fp-cand"},
			LoadedAt: time.Unix(2, 0).UTC(),
		}, nil
	})
	obs := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{})
	mgr := runtimehost.NewManager(4, nil)
	plane := newFakePlane(map[string]int{"local-stub": 1})
	initial := mgr.PrepareRequestPlane("boot", plane)
	initial.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-boot"})
	if err := mgr.Publish(initial); err != nil {
		t.Fatal(err)
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:   src,
		Loader:   loader,
		Compile:  &controllableCompiler{kinds: map[string]int{"local-stub": 1}},
		Manager:  mgr,
		Timeout:  time.Second,
		Observer: obs,
		ActiveEffective: &config.EffectiveConfig{
			Config:   &config.Config{Routing: config.RoutingConfig{MaxAttempts: 1}},
			Identity: config.EffectiveIdentity{PrivateDigest: [32]byte{1}, PublicFingerprint: "fp-old"},
		},
		Classify: func(_, _ *config.EffectiveConfig) ([]configreload.SafeChange, error) {
			return nil, &configreload.RestartRequiredError{
				RestartRequiredFields: []string{"server.address", "tls.cert"},
				TotalBlocked:          2,
			}
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	res := c.Reload(context.Background(), sdkreload.Trigger{
		Kind:      sdkreload.TriggerAPI,
		SafeActor: "status-copy-actor",
	})
	if res.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("setup category=%q want restart-required", res.Category)
	}
	if len(res.RestartFields) < 2 {
		t.Fatalf("setup RestartFields=%v", res.RestartFields)
	}

	gate := newStageGate()
	src.gate = gate
	done := make(chan sdkreload.Result, 1)
	go func() {
		done <- c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.WaitEnter(t)
	t.Cleanup(func() {
		gate.Release()
		<-done
	})

	st1 := c.Status()
	if !st1.Busy || st1.CurrentAttempt == nil {
		t.Fatalf("expected busy CurrentAttempt, status=%+v", st1)
	}
	if len(st1.LastResult.RestartFields) < 2 {
		t.Fatalf("expected LastResult.RestartFields, got %v", st1.LastResult.RestartFields)
	}
	if len(st1.CurrentAttempt.RestartFields) < 2 {
		t.Fatalf("expected CurrentAttempt.RestartFields, got %v", st1.CurrentAttempt.RestartFields)
	}
	if len(st1.History) == 0 {
		t.Fatal("expected History from observer")
	}
	origField0 := st1.LastResult.RestartFields[0]
	origField1 := st1.LastResult.RestartFields[1]
	origActor := st1.History[0].SafeActor
	origCur0 := st1.CurrentAttempt.RestartFields[0]

	st1.LastResult.RestartFields[0] = "mutated-last"
	st1.CurrentAttempt.RestartFields[0] = "mutated-current"
	st1.History[0].SafeActor = "mutated-actor"
	st1.LastResult.Category = sdkreload.ResultPublished
	st1.CurrentAttempt.Category = sdkreload.ResultPublished

	st2 := c.Status()
	if st2.LastResult.RestartFields[0] != origField0 || st2.LastResult.RestartFields[1] != origField1 {
		t.Fatalf("mutating first Status leaked into coordinator/second snapshot RestartFields: %v", st2.LastResult.RestartFields)
	}
	if st2.LastResult.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("mutating first Status leaked LastResult.Category=%q", st2.LastResult.Category)
	}
	if st2.CurrentAttempt == nil {
		t.Fatal("second snapshot missing CurrentAttempt")
	}
	if st2.CurrentAttempt.RestartFields[0] != origCur0 {
		t.Fatalf("mutating first CurrentAttempt leaked: %v", st2.CurrentAttempt.RestartFields)
	}
	if st2.CurrentAttempt.Category != sdkreload.ResultRestartRequired {
		t.Fatalf("mutating first CurrentAttempt.Category leaked: %q", st2.CurrentAttempt.Category)
	}
	if st2.History[0].SafeActor != origActor {
		t.Fatalf("mutating first History leaked SafeActor=%q want %q", st2.History[0].SafeActor, origActor)
	}

	// Distinct mutable nested storage between the two returned snapshots.
	if len(st1.LastResult.RestartFields) > 0 && len(st2.LastResult.RestartFields) > 0 &&
		&st1.LastResult.RestartFields[0] == &st2.LastResult.RestartFields[0] {
		t.Fatal("Status snapshots must not share LastResult.RestartFields backing array")
	}
	if st1.CurrentAttempt != nil && st2.CurrentAttempt != nil {
		if st1.CurrentAttempt == st2.CurrentAttempt {
			t.Fatal("Status snapshots must not share CurrentAttempt pointer")
		}
		if len(st1.CurrentAttempt.RestartFields) > 0 && len(st2.CurrentAttempt.RestartFields) > 0 &&
			&st1.CurrentAttempt.RestartFields[0] == &st2.CurrentAttempt.RestartFields[0] {
			t.Fatal("Status snapshots must not share CurrentAttempt.RestartFields backing array")
		}
	}
	if len(st1.History) > 0 && len(st2.History) > 0 && &st1.History[0] == &st2.History[0] {
		t.Fatal("Status snapshots must not share History backing array")
	}
}
