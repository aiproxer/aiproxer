//go:build linux

package runtimebundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

type closeLeaseBlockedSource struct {
	src              *configsource.FixedSource
	entered, release chan struct{}
}

func (s *closeLeaseBlockedSource) AbsolutePath() string { return s.src.AbsolutePath() }
func (s *closeLeaseBlockedSource) ReadStable(ctx context.Context, v *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
	close(s.entered)
	<-s.release
	return s.src.ReadStable(ctx, v)
}

type closeSourceLoader func(string) (*configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, *configsource.FixedSource, error)

func loadHostCloseSource(path string) (*configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, *configsource.FixedSource, error) {
	source, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	snapshot, _, err := source.ReadStable(context.Background(), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	version, slot := snapshot.TakeBaseline()
	return version, slot, source, nil
}

func TestHostCloseLiveSourceDeadlineAndFinalRelease(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "initially-idle", true: "admitted-attempt"}[admitted], func(t *testing.T) {
			exerciseHostCloseSource(t, admitted, loadHostCloseSource, nil)
		})
	}
}

func exerciseHostCloseSource(t *testing.T, admitted bool, load closeSourceLoader, wantError error) {
	t.Helper()

	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte("accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, slot, src, err := load(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slot.Close(context.Background()) }()
	if v.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
		if wantError != nil {
			t.Fatal("source-close fault lane requires supported ext4")
		}
		t.Skip("requires supported ext4")
	}
	borrow, err := v.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	defer borrow.Release()
	gate := &closeLeaseBlockedSource{src: src, entered: make(chan struct{}), release: make(chan struct{})}
	log := &hostCloseLog{}
	mgr := runtimehost.NewManager(8, nil)
	g := mgr.PrepareRequestPlane("startup", &closeTestPlane{log: log})
	if err := mgr.Publish(g); err != nil {
		t.Fatal(err)
	}
	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{Source: gate, Loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) { return closeTestEffective("a", 1), nil }), Compile: &closeHoldingCompiler{}, Manager: mgr, ActiveSource: v, ActiveSourceOwner: slot})
	if err != nil {
		t.Fatal(err)
	}
	host := &Host{coordinator: coord, manager: mgr, process: &ProcessServices{closers: []func() error{func() error { log.add("process"); return nil }}}, shutdownTracing: func(context.Context) error { log.add("tracing"); return nil }}
	reloadDone := make(chan sdkreload.Result, 1)
	if admitted {
		go func() { reloadDone <- host.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI}) }()
		<-gate.entered
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- host.Close(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close err=%v want deadline", err)
		}
	case <-time.After(3 * time.Second):
		borrow.Release()
		if admitted {
			close(gate.release)
			<-reloadDone
		}
		t.Fatal("Host.Close did not return at caller deadline")
	}
	if admitted {
		b, err := v.Borrow()
		if err != nil {
			t.Fatalf("admitted-before-read baseline closed early: %v", err)
		}
		b.Release()
		close(gate.release)
		if got := <-reloadDone; got.Category != sdkreload.ResultCanceled {
			t.Fatalf("reload result %v", got)
		}
	}

	borrow.Release()
	if _, err := v.Borrow(); err == nil {
		t.Fatal("final source cleanup was not armed after final borrow release")
	}
	for i := range 2 {
		err := host.Close(context.Background())
		if wantError == nil && err != nil {
			t.Fatalf("Close retry %d returned cleanup error: %v", i, err)
		}
		if wantError != nil && (!errors.Is(err, wantError) || !configsource.IsSourceCleanupError(err)) {
			t.Fatalf("Close retry %d lost cached source-close cause: %v", i, err)
		}
	}
	if log.count("process") != 1 || log.count("tracing") != 1 || g.CloseCount() != 1 {
		t.Fatalf("safe cleanup not once: events=%v generation closes=%d", log.snapshot(), g.CloseCount())
	}
}
