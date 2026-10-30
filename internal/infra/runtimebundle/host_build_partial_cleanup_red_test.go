package runtimebundle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type hostBuildStage string

const (
	hostBuildStageLoader           hostBuildStage = "loader"
	hostBuildStageTracing          hostBuildStage = "tracing"
	hostBuildStageProcess          hostBuildStage = "process"
	hostBuildStageCompile          hostBuildStage = "compile"
	hostBuildStagePublish          hostBuildStage = "publish"
	hostBuildStageCoordinator      hostBuildStage = "coordinator"
	hostBuildStageCoordinatorPanic hostBuildStage = "coordinator_panic"
	hostBuildStageSuccess          hostBuildStage = "success"
)

// hostBuilder is the Task 5.2 startup transaction seam used by RED matrices.
type hostBuilder interface {
	Build(ctx context.Context, in hostBuildInput) (hostBuildOutcome, error)
}

// hostBuilderStageFault injects a stage failure and returns acquire/cleanup evidence.
type hostBuilderStageFault interface {
	hostBuilder
	BuildFaulting(ctx context.Context, in hostBuildInput, faultAt hostBuildStage) (hostBuildOutcome, error)
}

type productionHostBuilder struct{}

func (productionHostBuilder) Build(ctx context.Context, in hostBuildInput) (hostBuildOutcome, error) {
	return buildHostOutcome(ctx, in, LoadBootstrapEffectiveWithSource)
}

// TestPartialCleanup_HostBuilderStageMatrix encodes the BuildHost ownership
// cleanup contract (req 4.8). Every row invokes a real current operation or the
// HostBuilder stage-fault seam and asserts acquire/cleanup evidence.
func TestPartialCleanup_HostBuilderStageMatrix(t *testing.T) {
	t.Parallel()
	cfgPath := MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	ctx := context.Background()
	in := hostBuildInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}

	cases := []struct {
		name  string
		stage hostBuildStage
		run   func(t *testing.T)
	}{
		{
			name:  "loader_failure_acquires_nothing",
			stage: hostBuildStageLoader,
			run: func(t *testing.T) {
				t.Helper()
				host, err := BuildHost(ctx, BuildHostInput{
					ConfigPath:      filepath.Join(t.TempDir(), "missing-startup.yaml"),
					Mandatory:       aipsdk.StandardDistributionRequirements(),
					LogWriter:       io.Discard,
					HandlerComposer: stubHandlerComposer,
				})
				if err == nil {
					cleanupHost(t, host)
					t.Fatal("expected loader failure")
				}
				if host != nil {
					t.Fatalf("loader failure must return nil Host")
				}
			},
		},
		{
			name:  "tracing_init_failure_no_process",
			stage: hostBuildStageTracing,
			run: func(t *testing.T) {
				t.Helper()
				assertHostBuilderStageCleanup(t, in, hostBuildStageTracing, []string{"loader", "tracing"}, []string{"tracing"})
			},
		},
		{
			name:  "process_runtime_failure_shuts_tracing_once",
			stage: hostBuildStageProcess,
			run: func(t *testing.T) {
				t.Helper()
				assertHostBuilderStageCleanup(t, in, hostBuildStageProcess, []string{"loader", "tracing", "process"}, []string{"process", "tracing"})
			},
		},
		{
			name:  "generation_compile_failure_rolls_back_once",
			stage: hostBuildStageCompile,
			run: func(t *testing.T) {
				t.Helper()
				assertBuildHostComposeCleanup(t)
				assertHostBuilderStageCleanup(t, in, hostBuildStageCompile,
					[]string{"loader", "tracing", "process", "compile"},
					[]string{"compile", "process", "tracing"})
			},
		},
		{
			name:  "publication_failure_rolls_back_once",
			stage: hostBuildStagePublish,
			run: func(t *testing.T) {
				t.Helper()
				assertHostBuilderStageCleanup(t, in, hostBuildStagePublish,
					[]string{"loader", "tracing", "process", "compile", "publish"},
					[]string{"compile", "process", "tracing"})
			},
		},
		{
			name:  "coordinator_bind_failure_leaves_no_partial_ownership",
			stage: hostBuildStageCoordinator,
			run: func(t *testing.T) {
				t.Helper()
				assertHostBuilderStageCleanup(t, in, hostBuildStageCoordinator,
					[]string{"loader", "tracing", "process", "compile", "publish", "coordinator"},
					[]string{"compile", "process", "tracing"})
			},
		},
		{
			name:  "success_returns_complete_host_without_prior_cleanup",
			stage: hostBuildStageSuccess,
			run: func(t *testing.T) {
				t.Helper()
				var b hostBuilder = productionHostBuilder{}
				out, err := b.Build(ctx, in)
				if err != nil {
					t.Fatalf("HostBuilder success path: %v", err)
				}
				t.Cleanup(func() { cleanupHost(t, out.Host) })
				if len(out.Journal.Cleaned) != 0 {
					t.Fatalf("success must not run prior cleanup; cleaned=%v", out.Journal.Cleaned)
				}
				if !hostIsComplete(out) {
					t.Fatalf("success must return one complete Host from BuildHost; incomplete outcome (req 4.1, 4.5)")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

// TestHostBuild_CompleteHostFromOneOperation fails while callers must attach a
// reload host after bootstrap (req 4.1, 4.5).
func TestHostBuild_CompleteHostFromOneOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgPath := MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	var b hostBuilder = productionHostBuilder{}
	out, err := b.Build(ctx, hostBuildInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	})
	if err != nil {
		t.Fatalf("HostBuilder: %v", err)
	}
	t.Cleanup(func() { cleanupHost(t, out.Host) })
	if hostIsComplete(out) {
		return
	}
	t.Fatalf("HostBuild must return a complete Host from one operation; got incomplete outcome (req 4.1, 4.5)")
}

// TestPartialCleanup_ComposeFailureStillClearsTracingHandoff keeps the
// BuildHost-owned compose cleanup characterization discoverable under the
// PartialCleanup filter.
func TestPartialCleanup_ComposeFailureStillClearsTracingHandoff(t *testing.T) {
	t.Parallel()
	assertBuildHostComposeCleanup(t)
}

func assertHostBuilderStageCleanup(t *testing.T, in hostBuildInput, stage hostBuildStage, wantAcquired, wantCleaned []string) {
	t.Helper()
	var b hostBuilder = productionHostBuilder{}
	fb, ok := b.(hostBuilderStageFault)
	if !ok {
		t.Fatalf("HostBuilder %T missing BuildFaulting; cannot inject stage=%s failure or observe acquire/cleanup counters (req 4.8)", b, stage)
		return
	}
	out, err := fb.BuildFaulting(context.Background(), in, stage)
	if out.Host != nil {
		t.Cleanup(func() { cleanupHost(t, out.Host) })
	}
	if err == nil {
		t.Fatalf("stage %s must fail", stage)
	}
	if got := strings.Join(out.Journal.Acquired, ","); got != strings.Join(wantAcquired, ",") {
		t.Fatalf("stage %s acquired=%v want %v", stage, out.Journal.Acquired, wantAcquired)
	}
	if got := strings.Join(out.Journal.Cleaned, ","); got != strings.Join(wantCleaned, ",") {
		t.Fatalf("stage %s cleaned=%v want reverse order %v", stage, out.Journal.Cleaned, wantCleaned)
	}
	if hostIsComplete(out) {
		t.Fatalf("stage %s must not return a complete Host", stage)
	}
}

func assertBuildHostComposeCleanup(t *testing.T) {
	t.Helper()
	cfgPath := MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	host, err := BuildHost(context.Background(), BuildHostInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
		HandlerComposer: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
			return nil, errors.New("compose boom")
		},
	})
	if err == nil {
		cleanupHost(t, host)
		t.Fatal("expected compose failure")
	}
	if host != nil {
		t.Fatal("failed BuildHost must return nil Host (no partial ownership)")
	}
}
