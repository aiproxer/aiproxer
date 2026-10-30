package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

func TestBackendInstance_OptionalHooksIdempotentCloseAndIdleCleanup(t *testing.T) {
	t.Parallel()
	var closes, stops, idles atomic.Int32
	inst := runtimebundle.WrapBackendInstance(execbackend.Backend{
		Close: func() error {
			closes.Add(1)
			return nil
		},
	}, runtimebundle.OptionalBackendHooks{
		Stop: func(context.Context) error {
			stops.Add(1)
			return nil
		},
		CleanupIdleTransports: func(context.Context) error {
			idles.Add(1)
			return nil
		},
	})

	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("closes=%d stops=%d want 1 each", closes.Load(), stops.Load())
	}
	if idles.Load() != 1 {
		t.Fatalf("Close must run idle cleanup once before Stop/Close, idles=%d", idles.Load())
	}
	// Standalone CleanupIdleTransports remains available for explicit probes.
	if err := inst.CleanupIdleTransports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if idles.Load() != 2 {
		t.Fatalf("standalone idle after Close idles=%d want 2", idles.Load())
	}
}

func TestBackendInstance_StartExactlyOnceRaceSafe(t *testing.T) {
	t.Parallel()
	var starts atomic.Int32
	startErr := errors.New("start-partial")
	inst := runtimebundle.WrapBackendInstance(execbackend.Backend{
		Close: func() error { return nil },
	}, runtimebundle.OptionalBackendHooks{
		Start: func(context.Context) error {
			starts.Add(1)
			return startErr
		},
		Stop: func(context.Context) error { return nil },
	})

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			errs[i] = inst.Start(context.Background())
		}(i)
	}
	wg.Wait()
	if starts.Load() != 1 {
		t.Fatalf("start hook runs=%d want 1", starts.Load())
	}
	for i, err := range errs {
		if !errors.Is(err, startErr) {
			t.Fatalf("caller %d: got %v want %v", i, err, startErr)
		}
	}
	if err := inst.Start(context.Background()); !errors.Is(err, startErr) {
		t.Fatalf("repeated start: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("after repeat starts=%d", starts.Load())
	}
	// Cleanup after attempted/partially failed Start must still run once.
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBackendInstance_CompatibleWithoutHooks(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	inst := runtimebundle.WrapBackendInstance(execbackend.Backend{
		Close: func() error {
			closes.Add(1)
			return nil
		},
	}, runtimebundle.OptionalBackendHooks{})
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := inst.CleanupIdleTransports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.PreflightCapability(context.Background()); !errors.Is(err, runtimebundle.ErrPreflightUnsupported) {
		t.Fatalf("preflight: %v", err)
	}
	if closes.Load() != 1 {
		t.Fatalf("closes=%d", closes.Load())
	}
}

func TestBackendInstance_NonBillablePreflight(t *testing.T) {
	t.Parallel()
	inst := runtimebundle.WrapBackendInstance(execbackend.Backend{}, runtimebundle.OptionalBackendHooks{
		PreflightCapability: func(context.Context) (runtimebundle.BackendPreflightResult, error) {
			return runtimebundle.BackendPreflightResult{
				Ready:       true,
				Billable:    false,
				Description: "local capability probe",
			}, nil
		},
	})
	res, err := inst.PreflightCapability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready || res.Billable {
		t.Fatalf("result=%+v must be ready and non-billable", res)
	}

	billable := runtimebundle.WrapBackendInstance(execbackend.Backend{}, runtimebundle.OptionalBackendHooks{
		PreflightCapability: func(context.Context) (runtimebundle.BackendPreflightResult, error) {
			return runtimebundle.BackendPreflightResult{Ready: true, Billable: true}, nil
		},
	})
	if _, err := billable.PreflightCapability(context.Background()); !errors.Is(err, runtimebundle.ErrBillablePreflightForbidden) {
		t.Fatalf("billable preflight: %v", err)
	}
}

type unsafeLife struct{}

func (unsafeLife) Start(context.Context) error     { return nil }
func (unsafeLife) Stop(context.Context) error      { return nil }
func (unsafeLife) SafeUnderCandidateOverlap() bool { return false }

type safeLife struct {
	started, stopped atomic.Bool
}

func (s *safeLife) Start(context.Context) error {
	s.started.Store(true)
	return nil
}

func (s *safeLife) Stop(context.Context) error {
	s.stopped.Store(true)
	return nil
}
func (s *safeLife) SafeUnderCandidateOverlap() bool { return true }

func TestLifecycle_UnsafeOverlapRejectedBeforePublication(t *testing.T) {
	t.Parallel()
	err := runtimebundle.ClassifyFeatureLifecycles([]aipplugin.Lifecycle{unsafeLife{}})
	if !errors.Is(err, runtimebundle.ErrUnsafeLifecycleOverlap) {
		t.Fatalf("got %v", err)
	}
}

func TestLifecycle_SafeOverlapAdaptedToLedgerPhases(t *testing.T) {
	t.Parallel()
	life := &safeLife{}
	if err := runtimebundle.ClassifyFeatureLifecycles([]aipplugin.Lifecycle{life}); err != nil {
		t.Fatal(err)
	}
	ledger := runtimebundle.NewResourceLedger()
	if err := runtimebundle.AdaptOverlapSafeLifecycles(ledger, []aipplugin.Lifecycle{life}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !life.started.Load() {
		t.Fatal("prepare must Start safe lifecycle")
	}
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !life.stopped.Load() {
		t.Fatal("rollback must Stop started lifecycle")
	}
}

func TestLifecycle_PlainLifecycleWithoutOverlapMarkerRejected(t *testing.T) {
	t.Parallel()
	plain := plainLife{}
	err := runtimebundle.ClassifyFeatureLifecycles([]aipplugin.Lifecycle{plain})
	if !errors.Is(err, runtimebundle.ErrUnsafeLifecycleOverlap) {
		t.Fatalf("unmarked lifecycle must be rejected, got %v", err)
	}
}

type plainLife struct{}

func (plainLife) Start(context.Context) error { return nil }
func (plainLife) Stop(context.Context) error  { return nil }
