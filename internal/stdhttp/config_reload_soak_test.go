//go:build precommit

package stdhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"go.uber.org/goleak"
)

// TestRuntimeConfigReloadSoak is a bounded precommit reload soak mixing
// valid / invalid / noop / restart-required / retention-pressure triggers while
// a blocked old stream remains pinned. It proves generations stay within the
// retention budget and accepted work is never dropped (req 10.8-10.11, 15.7-15.10, 16.9).
func TestRuntimeConfigReloadSoak(t *testing.T) {
	defer goleak.VerifyNone(
		t, goleak.IgnoreCurrent(),
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)

	const (
		rounds      = 48
		maxRetained = 2
		workers     = 4
	)

	mgr := runtimehost.NewManager(maxRetained, nil)
	var serveCount atomic.Int64
	planeHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serveCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})

	boot := mgr.PrepareRequestPlane("boot", &soakPlane{h: planeHandler})
	if err := mgr.Publish(boot); err != nil {
		t.Fatal(err)
	}
	disp := runtimehost.NewGenerationDispatcher(mgr)

	// Blocked old stream: pin boot generation across the soak.
	lease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("acquire boot")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		lease.Release()
		t.Fatal("transfer pin")
	}
	t.Cleanup(func() { pin.Release() })

	src := &soakSource{
		path:   "/fixed/startup/config.yaml",
		atomic: configsource.AtomicEligible,
		snap:   soakSnapshot([]byte("x: 1")),
	}
	var digest byte = 1
	loader := &soakLoader{eff: soakEffective("fp-boot", digest)}
	compile := runtimehost.FuncCompiler(func(context.Context, *config.Config, map[string]int) (runtimehost.PublishedRequestPlane, error) {
		return &soakPlane{h: planeHandler}, nil
	})
	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source:          src,
		Loader:          loader,
		Compile:         compile,
		Manager:         mgr,
		Timeout:         2 * time.Second,
		ActiveEffective: soakEffective("fp-boot", digest),
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		accepted        atomic.Int64
		failed          atomic.Int64
		unavailable     atomic.Int64
		maxRetainedSeen atomic.Int64
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	var trafficReady sync.WaitGroup
	trafficReady.Add(workers)
	for range workers {
		wg.Go(func() {
			first := true
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/soak", nil)
				disp.ServeHTTP(rr, req)
				switch rr.Code {
				case http.StatusOK:
					accepted.Add(1)
				case http.StatusServiceUnavailable:
					unavailable.Add(1)
				default:
					failed.Add(1)
				}
				if first {
					first = false
					trafficReady.Done()
				}
			}
		})
	}
	trafficReady.Wait()

	categories := make(map[sdkreload.ResultCategory]int)
	for i := range rounds {
		switch i % 5 {
		case 0: // valid publish via coordinator
			digest++
			loader.set(soakEffective(fmt.Sprintf("fp-%d", digest), digest), nil)
			src.set(soakSnapshot([]byte("x: 1")), configsource.AtomicEligible, nil)
			res := coord.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			categories[res.Category]++
			if res.Category != sdkreload.ResultPublished && res.Category != sdkreload.ResultRetentionBlocked {
				t.Fatalf("round %d valid path: category=%q", i, res.Category)
			}
		case 1: // invalid source
			src.set(configsource.SourceSnapshot{}, "", errors.New("torn-source"))
			res := coord.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			categories[res.Category]++
			if res.Category == sdkreload.ResultPublished {
				t.Fatalf("round %d invalid must not publish", i)
			}
		case 2: // noop (AtomicNoop)
			src.set(soakSnapshot([]byte("x: 1")), configsource.AtomicNoop, nil)
			res := coord.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			categories[res.Category]++
			if res.Category != sdkreload.ResultNoop {
				t.Fatalf("round %d noop: category=%q", i, res.Category)
			}
		case 3: // restart-required
			digest++
			eff := soakEffective(fmt.Sprintf("fp-rr-%d", digest), digest)
			eff.Config = &config.Config{
				Server: config.ServerConfig{Address: "127.0.0.1:8080"},
				Access: config.AccessConfig{Mode: "multi_user"},
				Auth:   config.AuthConfig{Handler: "none"},
			}
			loader.set(eff, nil)
			src.set(soakSnapshot([]byte("x: 1")), configsource.AtomicEligible, nil)
			res := coord.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			categories[res.Category]++
			if res.Category != sdkreload.ResultRestartRequired {
				t.Fatalf("round %d restart-required: category=%q fields=%v", i, res.Category, res.RestartFields)
			}
		case 4: // retention pressure while boot pin held
			for range maxRetained + 4 {
				digest++
				loader.set(soakEffective(fmt.Sprintf("fp-ret-%d", digest), digest), nil)
				src.set(soakSnapshot([]byte("x: 1")), configsource.AtomicEligible, nil)
				res := coord.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
				categories[res.Category]++
				if res.Category == sdkreload.ResultRetentionBlocked {
					break
				}
			}
		}

		retained := int64(mgr.RetainedCount())
		for {
			cur := maxRetainedSeen.Load()
			if retained <= cur || maxRetainedSeen.CompareAndSwap(cur, retained) {
				break
			}
		}
		if mgr.RetainedCount() > maxRetained {
			t.Fatalf("round %d retained=%d exceeds max=%d", i, mgr.RetainedCount(), maxRetained)
		}
		if boot.Refs() < 1 {
			t.Fatalf("round %d boot pin refs lost: refs=%d", i, boot.Refs())
		}
	}

	cancel()
	wg.Wait()

	if unavailable.Load() != 0 {
		t.Fatalf("dropped accepted work (503 with active generation): unavailable=%d accepted=%d", unavailable.Load(), accepted.Load())
	}
	if accepted.Load() == 0 || serveCount.Load() == 0 {
		t.Fatalf("expected concurrent traffic; accepted=%d served=%d", accepted.Load(), serveCount.Load())
	}
	if failed.Load() != 0 {
		t.Fatalf("unexpected handler failures: %d", failed.Load())
	}
	if maxRetainedSeen.Load() > int64(maxRetained) {
		t.Fatalf("observed retained=%d > max=%d", maxRetainedSeen.Load(), maxRetained)
	}
	for _, want := range []sdkreload.ResultCategory{
		sdkreload.ResultPublished,
		sdkreload.ResultNoop,
		sdkreload.ResultRestartRequired,
		sdkreload.ResultRetentionBlocked,
	} {
		if categories[want] == 0 {
			t.Fatalf("soak missing category %q in %+v", want, categories)
		}
	}
	st := coord.Status()
	if st.RetainedGenerations > maxRetained {
		t.Fatalf("status retained=%d", st.RetainedGenerations)
	}
}

type soakPlane struct{ h http.Handler }

func (p *soakPlane) Handler() http.Handler {
	if p == nil || p.h == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	return p.h
}
func (p *soakPlane) Close() error                  { return nil }
func (p *soakPlane) Quiesce(context.Context) error { return nil }

type soakSource struct {
	mu     sync.Mutex
	path   string
	snap   configsource.SourceSnapshot
	atomic configsource.AtomicResult
	err    error
}

func soakSnapshot(bytes []byte) configsource.SourceSnapshot {
	return configsource.SourceSnapshot{
		Bytes:          bytes,
		HandleIdentity: configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid", Opaque: [32]byte{1}},
	}
}

func (s *soakSource) AbsolutePath() string { return s.path }
func (s *soakSource) set(snap configsource.SourceSnapshot, atomic configsource.AtomicResult, err error) {
	s.mu.Lock()
	s.snap, s.atomic, s.err = snap, atomic, err
	s.mu.Unlock()
}

func (s *soakSource) ReadStable(context.Context, *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap, s.atomic, s.err
}

type soakLoader struct {
	mu  sync.Mutex
	eff *config.EffectiveConfig
	err error
}

func (l *soakLoader) set(eff *config.EffectiveConfig, err error) {
	l.mu.Lock()
	l.eff, l.err = eff, err
	l.mu.Unlock()
}

func (l *soakLoader) LoadEffective(context.Context, []byte) (*config.EffectiveConfig, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.eff, l.err
}

func soakEffective(fp string, digest byte) *config.EffectiveConfig {
	var d [32]byte
	d[0] = digest
	return &config.EffectiveConfig{
		Config: &config.Config{
			Server: config.ServerConfig{Address: "127.0.0.1:8080"},
			Access: config.AccessConfig{Mode: "single_user"},
			Auth:   config.AuthConfig{Handler: "none"},
		},
		Identity: config.EffectiveIdentity{
			PrivateDigest:     d,
			PublicFingerprint: fp,
		},
		LoadedAt: time.Now().UTC(),
	}
}
