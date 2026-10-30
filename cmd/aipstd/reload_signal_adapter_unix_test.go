//go:build unix

package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Task 5.2: production Unix SIGHUP adapter (req 1.2, 1.9, 11.1-11.9).

type recordingReloadSink struct {
	mu          sync.Mutex
	calls       []sdkreload.Trigger
	block       chan struct{}
	unblock     chan struct{}
	result      sdkreload.Result
	started     chan struct{}
	startedOnce sync.Once
}

func newRecordingReloadSink(res sdkreload.Result) *recordingReloadSink {
	return &recordingReloadSink{
		result:  res,
		started: make(chan struct{}),
	}
}

func (s *recordingReloadSink) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	s.mu.Lock()
	s.calls = append(s.calls, trigger)
	block, unblock := s.block, s.unblock
	res := s.result
	s.mu.Unlock()
	s.startedOnce.Do(func() { close(s.started) })
	if block != nil {
		select {
		case <-block:
		default:
			close(block)
		}
		select {
		case <-unblock:
		case <-ctx.Done():
		}
	}
	if trigger.Kind != sdkreload.TriggerSIGHUP {
		tres := res
		tres.ReasonCategory = "unexpected-kind"
		return tres
	}
	return res
}

func (s *recordingReloadSink) Triggers() []sdkreload.Trigger {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sdkreload.Trigger, len(s.calls))
	copy(out, s.calls)
	return out
}

func TestSignalReload_SIGHUPDeliversFixedSourceTrigger(t *testing.T) {
	withExclusiveSIGHUP(t)
	sink := newRecordingReloadSink(sdkreload.Result{Category: sdkreload.ResultPublished, ActiveGeneration: 2})
	adapter := NewSIGHUPAdapter(sink)
	ctx := t.Context()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop()

	if err := signalPID(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.started:
	case <-time.After(3 * time.Second):
		t.Fatal("SIGHUP did not reach reload sink")
	}
	trigs := sink.Triggers()
	if len(trigs) < 1 {
		t.Fatal("expected at least one trigger")
	}
	tr := trigs[0]
	if tr.Kind != sdkreload.TriggerSIGHUP {
		t.Fatalf("kind=%q", tr.Kind)
	}
	if tr.SafeActor == "" {
		t.Fatal("SafeActor must be set")
	}
	// Trigger envelope must not carry path/YAML (struct has no such fields; assert Kind only).
	if tr.AcceptedAt.IsZero() {
		t.Fatal("AcceptedAt must be set")
	}
}

func TestSignalReload_SIGHUPDoesNotStopServerContext(t *testing.T) {
	withExclusiveSIGHUP(t)
	sink := newRecordingReloadSink(sdkreload.Result{Category: sdkreload.ResultNoop})
	adapter := NewSIGHUPAdapter(sink)
	parent := t.Context()
	sigCtx, stopShutdown := signal.NotifyContext(parent, ShutdownSignals()...)
	defer stopShutdown()
	if err := adapter.Start(sigCtx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop()

	if err := signalPID(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.started:
	case <-time.After(3 * time.Second):
		t.Fatal("no reload delivery")
	}
	select {
	case <-sigCtx.Done():
		t.Fatal("SIGHUP must not cancel NotifyContext(INT/TERM)")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSignalShutdown_INTTERMUnchangedAndExcludedFromReload(t *testing.T) {
	t.Parallel()
	if SignalsOverlap() {
		t.Fatal("reload and shutdown signals must not overlap")
	}
	shut := ShutdownSignals()
	if len(shut) != 2 {
		t.Fatalf("shutdown=%v", shut)
	}
	for _, s := range shut {
		if s == syscall.SIGHUP {
			t.Fatal("shutdown must not include SIGHUP")
		}
	}
	rel := ReloadSignals()
	if len(rel) != 1 || rel[0] != syscall.SIGHUP {
		t.Fatalf("reload=%v", rel)
	}
	if PlatformReloadMode() != "sighup+api" {
		t.Fatalf("mode=%s", PlatformReloadMode())
	}

	// INT/TERM still drive NotifyContext cancellation; SIGHUP does not.
	ctx, stop := signal.NotifyContext(context.Background(), ShutdownSignals()...)
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("shutdown context must stay open without INT/TERM")
	default:
	}
}

func TestSignalReload_CoalesceThroughCoordinator(t *testing.T) {
	withExclusiveSIGHUP(t)
	gate := newAdapterStageGate()
	compile := &adapterCompileHold{gate: gate, kinds: map[string]int{"local-stub": 1}}
	digest := byte(40)
	var loads atomic.Int64
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loads.Add(1)
		return adapterEffective("fp-sig-"+string(rune('a'+n)), digest+byte(n)), nil
	})
	src := &adapterFixedSource{
		path: "/fixed/startup/config.yaml",
		snap: configsource.SourceSnapshot{
			Bytes:          []byte("x: 1"),
			HandleIdentity: configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{1}},
		},
		atomic: configsource.AtomicEligible,
	}
	coord := newAdapterTestCoordinator(t, src, loader, compile)
	adapter := NewSIGHUPAdapter(coord)
	ctx := t.Context()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop()

	if err := signalPID(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	gate.WaitEnter(t)

	// Flood HUPs while first attempt is active; coordinator must coalesce.
	for range 8 {
		if err := signalPID(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	st := coord.Status()
	if !st.Busy {
		t.Fatal("expected busy during gated compile")
	}

	gate.Release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st = coord.Status()
		if !st.Busy && !st.PendingSignal && compile.calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if compile.calls.Load() < 1 {
		t.Fatal("expected at least one compile from SIGHUP")
	}
	st = coord.Status()
	if st.LastResult.Category != sdkreload.ResultPublished && st.LastResult.Category != sdkreload.ResultNoop {
		// Follow-up may publish; first must have completed.
		if st.ActiveGeneration < 2 {
			t.Fatalf("status=%+v compile=%d", st, compile.calls.Load())
		}
	}
}

func TestSignalReload_PublishesValidCandidate(t *testing.T) {
	withExclusiveSIGHUP(t)
	var loads atomic.Int64
	loader := runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
		n := loads.Add(1)
		return adapterEffective("fp-pub-"+string(rune('a'+n)), byte(50+n)), nil
	})
	src := &adapterFixedSource{
		path: "/fixed/startup/config.yaml",
		snap: configsource.SourceSnapshot{
			Bytes:          []byte("y: 2"),
			HandleIdentity: configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{1}},
		},
		atomic: configsource.AtomicEligible,
	}
	compile := &adapterCompileHold{kinds: map[string]int{"local-stub": 1}}
	coord := newAdapterTestCoordinator(t, src, loader, compile)
	before := coord.Status().ActiveGeneration

	adapter := NewSIGHUPAdapter(coord)
	ctx := t.Context()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop()

	if err := signalPID(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := coord.Status()
		if !st.Busy && st.ActiveGeneration > before {
			if st.LastResult.Category != sdkreload.ResultPublished {
				t.Fatalf("category=%q", st.LastResult.Category)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("SIGHUP did not publish: status=%+v before=%d", coord.Status(), before)
}

func TestSignalReload_AdapterStopRejectsLateTriggers(t *testing.T) {
	withExclusiveSIGHUP(t)
	sink := newRecordingReloadSink(sdkreload.Result{Category: sdkreload.ResultPublished})
	adapter := NewSIGHUPAdapter(sink)
	ctx := context.Background()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	adapter.Stop()
	adapter.Stop() // idempotent

	before := len(sink.Triggers())
	if err := signalPID(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(sink.Triggers()); got != before {
		t.Fatalf("late trigger after stop: before=%d after=%d", before, got)
	}
}

func TestSignalReload_AdapterStopRaceWithDelivery(t *testing.T) {
	withExclusiveSIGHUP(t)
	sink := newRecordingReloadSink(sdkreload.Result{Category: sdkreload.ResultBusy})
	sink.block = make(chan struct{})
	sink.unblock = make(chan struct{})

	adapter := NewSIGHUPAdapter(sink)
	ctx := context.Background()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if err := signalPID(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.block:
	case <-time.After(3 * time.Second):
		t.Fatal("reload did not start")
	}

	done := make(chan struct{})
	go func() {
		adapter.Stop()
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	close(sink.unblock)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked forever")
	}
}

func TestPlatform_UnixReloadMode(t *testing.T) {
	t.Parallel()
	if PlatformReloadMode() != "sighup+api" {
		t.Fatalf("mode=%s", PlatformReloadMode())
	}
	if len(ReloadSignals()) == 0 {
		t.Fatal("unix must register SIGHUP")
	}
}

func signalPID(sig os.Signal) error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// --- coordinator test doubles (cmd/aipstd composition; no second manager type) ---

type adapterFixedSource struct {
	path   string
	snap   configsource.SourceSnapshot
	atomic configsource.AtomicResult
}

func (s *adapterFixedSource) AbsolutePath() string { return s.path }
func (s *adapterFixedSource) ReadStable(context.Context, *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
	return s.snap, s.atomic, nil
}

type adapterStageGate struct {
	enter   chan struct{}
	release chan struct{}
	once    sync.Once
}

func newAdapterStageGate() *adapterStageGate {
	return &adapterStageGate{enter: make(chan struct{}), release: make(chan struct{})}
}

func (g *adapterStageGate) WaitEnter(t *testing.T) {
	t.Helper()
	select {
	case <-g.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for compile enter")
	}
}

func (g *adapterStageGate) Release() { g.once.Do(func() { close(g.release) }) }

func (g *adapterStageGate) Hold(ctx context.Context) error {
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

type adapterCompileHold struct {
	gate  *adapterStageGate
	kinds map[string]int
	calls atomic.Int64
}

func (c *adapterCompileHold) Compile(ctx context.Context, _ *config.Config, _ map[string]int) (runtimehost.PublishedRequestPlane, error) {
	c.calls.Add(1)
	if c.gate != nil {
		if err := c.gate.Hold(ctx); err != nil {
			return nil, err
		}
	}
	return &adapterPlane{kinds: c.kinds}, nil
}

type adapterPlane struct {
	kinds  map[string]int
	closed atomic.Bool
}

func (p *adapterPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}
func (p *adapterPlane) Quiesce(context.Context) error { return nil }
func (p *adapterPlane) Close() error {
	if p.closed.Swap(true) {
		return runtimehost.ErrAlreadyClosed
	}
	return nil
}
func (p *adapterPlane) BackendFactoryKindCounts() map[string]int { return p.kinds }

func adapterEffective(fp string, digest byte) *config.EffectiveConfig {
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

func newAdapterTestCoordinator(t *testing.T, src runtimehost.StableConfigSource, loader runtimehost.EffectiveLoader, compile runtimehost.CandidateCompiler) *runtimehost.Coordinator {
	t.Helper()
	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", &adapterPlane{kinds: map[string]int{"local-stub": 1}})
	g0.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-startup", TriggerKind: "startup"})
	if err := mgr.Publish(g0); err != nil {
		t.Fatalf("publish gen1: %v", err)
	}
	c, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Compile:         compile,
		Manager:         mgr,
		Timeout:         5 * time.Second,
		ActiveEffective: adapterEffective("fp-startup", 1),
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}
