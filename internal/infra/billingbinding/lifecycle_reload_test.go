package billingbinding_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// This exercises the real host/coordinator, not manual adapter close calls.
// Like the internal post-usage workers, the binding worker belongs to the
// process: generation retirement must neither cancel nor replace it.
func TestBindingWorker_SurvivesConfigReloadAndJoinsBeforeDependencyClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	work := make(chan chan struct{})
	stopping, done, drain := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(drain) }) }
	defer release()
	var starts, stops, dependencyCloses atomic.Int32
	var blockDrain atomic.Bool
	var stop context.CancelFunc
	ports := lifecyclePorts{}
	binding := billing.Binding{
		ID: "worker-reload", Version: billing.BindingVersionV1,
		Scope: billing.BindingScope{
			StoreID: "worker-store",
			Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: "v1"}},
			Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "policy"},
		},
		CreditScreen: ports, Quoter: ports, Admission: ports, Terminal: ports,
		Lifecycle: billing.Lifecycle{Owned: []billing.OwnedResource{
			{ID: "worker-dependency", Start: func(context.Context) error { return nil }, Close: func(context.Context) error {
				dependencyCloses.Add(1)
				select {
				case <-done:
					return nil
				default:
					return errors.New("dependency closed before worker joined")
				}
			}},
			{ID: "post-usage-worker", Start: func(parent context.Context) error {
				if starts.Add(1) != 1 {
					return errors.New("worker restarted")
				}
				var workerCtx context.Context
				workerCtx, stop = context.WithCancel(parent)
				go func() {
					defer close(done)
					for {
						select {
						case ack := <-work:
							close(ack)
						case <-workerCtx.Done():
							close(stopping)
							if blockDrain.Load() {
								<-drain // Model in-progress work that must finish before Stop returns.
							}
							return
						}
					}
				}()
				return nil
			}, Close: func(closeCtx context.Context) error {
				stops.Add(1)
				stop()
				select {
				case <-done:
					return nil
				case <-closeCtx.Done():
					return closeCtx.Err()
				}
			}},
		}},
	}
	startupCtx, cancelStartup := context.WithCancel(ctx)
	defer cancelStartup()
	rt, err := aipruntime.BuildWithBilling(startupCtx, aipruntime.Options{ConfigPath: path, LogWriter: io.Discard}, binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		release()
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelCleanup()
		if err := rt.Close(cleanupCtx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	cancelStartup()
	checkWork := func() {
		t.Helper()
		ack := make(chan struct{})
		select {
		case work <- ack:
		case <-stopping:
			t.Fatal("process worker stopped before host shutdown")
		case <-ctx.Done():
			t.Fatal("worker did not accept work:", ctx.Err())
		}
		select {
		case <-ack:
		case <-ctx.Done():
			t.Fatal("worker did not complete work:", ctx.Err())
		}
	}
	checkWork()
	before := rt.ReloadStatus().ActiveGeneration
	updated := strings.Replace(string(raw), `default_route: "openai-responses:gpt-4o-mini"`, `default_route: "openai-responses:gpt-4o"`, 1)
	if updated == string(raw) {
		t.Fatal("reload fixture did not change routing")
	}
	next := path + ".next"
	if err := os.WriteFile(next, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	result := rt.Reload(ctx, aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	if result.Category != aipruntime.ResultPublished || result.ActiveGeneration <= before || rt.ReloadStatus().ActiveGeneration != result.ActiveGeneration {
		t.Fatalf("reload did not publish a replacement generation: before=%d result=%+v", before, result)
	}
	checkWork()
	if starts.Load() != 1 || stops.Load() != 0 || dependencyCloses.Load() != 0 {
		t.Fatalf("reload changed process resources: starts=%d stops=%d dependencies=%d", starts.Load(), stops.Load(), dependencyCloses.Load())
	}

	closed := make(chan error, 1)
	blockDrain.Store(true)
	go func() { closed <- rt.Close(ctx) }()
	select {
	case <-stopping:
	case <-ctx.Done():
		t.Fatal("shutdown did not cancel worker:", ctx.Err())
	}
	select {
	case err := <-closed:
		t.Fatalf("host close returned before worker drain: %v", err)
	default:
	}
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("host did not join worker:", ctx.Err())
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || stops.Load() != 1 || dependencyCloses.Load() != 1 {
		t.Fatalf("resources not cleaned exactly once: starts=%d stops=%d dependencies=%d", starts.Load(), stops.Load(), dependencyCloses.Load())
	}
}

// No request is issued: only complete attachment ports are needed to exercise
// the actual host build, generation publication and process shutdown.
type lifecyclePorts struct{}

func (lifecyclePorts) Check(context.Context, billing.CreditScreenInput) (billing.CreditScreenResult, error) {
	return billing.CreditScreenResult{}, nil
}

func (lifecyclePorts) Quote(context.Context, economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{}, nil
}

func (lifecyclePorts) Admit(context.Context, billing.ExposureAdmissionInput) (billing.ExposureHandle, error) {
	return billing.ExposureHandle{}, nil
}

func (lifecyclePorts) AppendTerminal(context.Context, billing.TerminalEnvelope) (billing.TerminalAck, error) {
	return billing.TerminalAck{}, nil
}
