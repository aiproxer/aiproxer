package featurehost_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/state"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	compactionstate "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

type dummyRunner struct{}

func (dummyRunner) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream(nil), nil
}

func newTestScheduler(t *testing.T) *auxreq.BackgroundScheduler {
	t.Helper()
	s, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return dummyRunner{}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	if err != nil {
		t.Fatalf("NewBackgroundScheduler: %v", err)
	}
	return s
}

func TestProcess_SuccessfulClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	in := featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	}

	rt, err := featurehost.NewProcess(ctx, in)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil Runtime")
	}

	if rt.Closed() {
		t.Fatal("expected runtime not closed initially")
	}

	if err := rt.Close(); err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}

	if !rt.Closed() {
		t.Fatal("expected runtime closed after Close()")
	}
}

func TestProcess_CloseIdempotency(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	in := featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	}

	rt, err := featurehost.NewProcess(ctx, in)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	// Close twice: second Close must be idempotent and error-free.
	if err := rt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !rt.Closed() {
		t.Fatal("expected runtime closed after Close()")
	}
}

func TestProcess_BorrowedResourcesNotClosed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	in := featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	}

	rt, err := featurehost.NewProcess(ctx, in)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}

	if err := rt.Close(); err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}

	// sched must NOT be closed by Runtime.Close()
	// SubmitCollect must succeed and not return ErrSchedulerClosed.
	_, err = sched.SubmitCollect(ctx, auxiliary.Request{Call: &aipapi.Call{}}, auxiliary.SubmitOptions{CoalesceKey: "test-key"})
	if errors.Is(err, auxreq.ErrSchedulerClosed) {
		t.Fatal("borrowed BackgroundAux was closed by Runtime.Close()")
	}
}

func TestProcess_NilLoggerRejected(t *testing.T) {
	t.Parallel()

	_, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{
		Logger: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestProcess_DoesNotImplementIoCloserOnProcessInput(t *testing.T) {
	t.Parallel()

	in := featurehost.ProcessInput{}
	if _, ok := any(in).(io.Closer); ok {
		t.Fatal("ProcessInput must not implement io.Closer")
	}
}

func TestCompaction_DirectConstructorCounting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	coord1, err := compactionstate.NewBranchCoordinator(ctx, compactionstate.Config{})
	if err != nil {
		t.Fatalf("NewBranchCoordinator #1: %v", err)
	}
	coord2, err := compactionstate.NewBranchCoordinator(ctx, compactionstate.Config{})
	if err != nil {
		t.Fatalf("NewBranchCoordinator #2: %v", err)
	}
	if coord1 == coord2 {
		t.Fatalf("state.NewBranchCoordinator returned identical pointer: %p", coord1)
	}

	port1, err := compaction.NewParentPort(coord1)
	if err != nil {
		t.Fatalf("NewParentPort #1: %v", err)
	}
	port2, err := compaction.NewParentPort(coord1)
	if err != nil {
		t.Fatalf("NewParentPort #2: %v", err)
	}
	if port1 == port2 {
		t.Fatalf("compaction.NewParentPort returned identical pointer: %p", port1)
	}

	det1 := compactiondetect.New(compactiondetect.Config{})
	det2 := compactiondetect.New(compactiondetect.Config{})
	if det1 == det2 {
		t.Fatalf("compactiondetect.New returned identical pointer: %p", det1)
	}
}

func TestCompaction_ProcessLevelDistinctInstancesAcrossProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sched1 := newTestScheduler(t)
	t.Cleanup(func() { _ = sched1.Close() })
	sched2 := newTestScheduler(t)
	t.Cleanup(func() { _ = sched2.Close() })

	rt1, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched1,
	})
	if err != nil {
		t.Fatalf("NewProcess #1: %v", err)
	}
	t.Cleanup(func() { _ = rt1.Close() })

	rt2, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched2,
	})
	if err != nil {
		t.Fatalf("NewProcess #2: %v", err)
	}
	t.Cleanup(func() { _ = rt2.Close() })

	var node yaml.Node
	_ = yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &node)
	reg := aipsdk.Registration{
		ID:          "compaction-continuity",
		FactoryKind: "compaction-continuity",
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}
	genIn := featurehost.GenerationInput{
		Registrations: []aipsdk.Registration{reg},
	}
	out1, err := rt1.CompileGeneration(ctx, genIn)
	if err != nil {
		t.Fatalf("CompileGeneration #1: %v", err)
	}
	out2, err := rt2.CompileGeneration(ctx, genIn)
	if err != nil {
		t.Fatalf("CompileGeneration #2: %v", err)
	}

	if out1.CorePorts.CompactionDetector == nil || out2.CorePorts.CompactionDetector == nil {
		t.Fatal("expected non-nil CompactionDetector in CorePorts")
	}
	if out1.CorePorts.CompactionDetector == out2.CorePorts.CompactionDetector {
		t.Fatalf("CompactionDetector pointer identical across processes: %p", out1.CorePorts.CompactionDetector)
	}

	pres1 := aipfeature.Get(out1.Planes, aipfeature.PlaneCompactionPreservers)
	pres2 := aipfeature.Get(out2.Planes, aipfeature.PlaneCompactionPreservers)
	if len(pres1) == 0 || len(pres2) == 0 {
		t.Fatal("expected bound compaction preservers in generation output")
	}
	if pres1[0] == pres2[0] {
		t.Fatalf("bound compaction preserver identical across processes: %p", pres1[0])
	}
}
