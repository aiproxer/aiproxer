package runtimebundle

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/osenv"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Task 7.4: after bindHost returns a complete Host, every remaining
// startup failure must roll back through exactly one Host.Close rather than
// decomposing Manager / ProcessServices / tracing shutdown primitives.

// TestHostBuild_PostBindRollbackUsesOneHostClose proves the post-bind
// coordinator-probe failure path closes the complete Host once and keeps the
// stage-probe cleaned accounting intact.
func TestHostBuild_PostBindRollbackUsesOneHostClose(t *testing.T) {
	t.Parallel()
	in := hostBuildInput{
		ConfigPath:      MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}
	out, err := productionHostBuilder{}.BuildFaulting(context.Background(), in, hostBuildStageCoordinator)
	if err == nil {
		cleanupHost(t, out.Host)
		t.Fatal("expected coordinator stage failure")
	}
	if out.Host != nil {
		t.Fatal("post-bind rollback must return nil Host")
	}
	wantCleaned := "compile,process,tracing"
	if got := strings.Join(out.Journal.Cleaned, ","); got != wantCleaned {
		t.Fatalf("cleaned=%s want %s", got, wantCleaned)
	}
}

func TestHostBuild_AfterBindPanicClosesHostThenPreservesPanic(t *testing.T) {
	t.Parallel()
	in := hostBuildInput{
		ConfigPath:      MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}
	ops := defaultHostBuildOps()
	baseLoad := ops.load
	baseTracing, baseProcess, basePublisher := ops.tracing, ops.process, ops.publisher
	var activeSourceHandle *configsource.ActiveSourceVersion
	var sourceOwner *configsource.SourceOwnerSlot
	var processServices *ProcessServices
	var manager *runtimehost.Manager
	var initialGeneration *runtimehost.Generation
	var processCloseCalls, tracingCloseCalls atomic.Int32
	ops.load = func(ctx context.Context, path string, overrides config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		effective, active, owner, fixed, err := baseLoad(ctx, path, overrides, capabilities)
		activeSourceHandle, sourceOwner = active, owner
		return effective, active, owner, fixed, err
	}
	ops.tracing = func(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
		result, err := baseTracing(ctx, cfg)
		if err == nil {
			inner := result.Shutdown
			result.Shutdown = func(ctx context.Context) error {
				tracingCloseCalls.Add(1)
				if inner != nil {
					return inner(ctx)
				}
				return nil
			}
		}
		return result, err
	}
	ops.process = func(ctx context.Context, in processBuildInput) (*ProcessServices, error) {
		process, err := baseProcess(ctx, in)
		processServices = process
		if err == nil && process != nil {
			process.closers = append(process.closers, func() error {
				processCloseCalls.Add(1)
				return nil
			})
		}
		return process, err
	}
	ops.publisher = func(ctx context.Context, in initialPublishInput) (*runtimehost.Manager, *runtimehost.Generation, error) {
		gotManager, gotGeneration, err := basePublisher(ctx, in)
		manager, initialGeneration = gotManager, gotGeneration
		return gotManager, gotGeneration, err
	}
	ops.afterBind = func() error { panic("injected afterBind panic") }
	var got any
	func() {
		defer func() { got = recover() }()
		_, _ = buildHost(context.Background(), in, ops, osenv.Process{})
	}()
	if got != "injected afterBind panic" {
		t.Fatalf("panic=%v want original afterBind panic", got)
	}
	if processServices == nil || !processServices.Closed() || processCloseCalls.Load() != 1 {
		t.Fatalf("panic rollback process close: service=%p closed=%v test closer calls=%d", processServices, processServices != nil && processServices.Closed(), processCloseCalls.Load())
	}
	if manager == nil || initialGeneration == nil {
		t.Fatalf("panic rollback did not retain manager/generation evidence: manager=%p generation=%p", manager, initialGeneration)
	}
	if initialGeneration.Lifecycle() != runtimehost.GenClosed || initialGeneration.CloseCount() != 1 {
		t.Fatalf("panic rollback generation lifecycle/count: manager=%p generation=%p lifecycle=%v closes=%d", manager, initialGeneration, initialGeneration.Lifecycle(), initialGeneration.CloseCount())
	}
	if tracingCloseCalls.Load() != 1 {
		t.Fatalf("panic rollback tracing close calls=%d want once", tracingCloseCalls.Load())
	}
	if activeSourceHandle != nil && activeSourceHandle.HandleIdentity.Scheme == "linux-ext4-dev-ino-lease-v1" {
		if sourceOwner == nil || !sourceOwner.IsEmpty() {
			t.Fatal("panic rollback did not transfer then consume the accepted source owner")
		}
		if _, err := activeSourceHandle.Borrow(); err == nil {
			t.Fatal("panic rollback left the accepted source lease borrowable")
		}
	}
}

// TestHostBuild_RollbackSourceHasNoHostDecomposition locks the post-bind
// rollback to the single Host.Close seam at the source level.
func TestHostBuild_RollbackSourceHasNoHostDecomposition(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("host_build.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "host_build.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var closeCalls int
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "buildHost" || fd.Body == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			base, ok := sel.X.(*ast.Ident)
			if !ok || base.Name != "host" {
				return true
			}
			switch sel.Sel.Name {
			case "Close":
				closeCalls++
			case "Manager", "Process", "ShutdownTracing", "Coordinator", "manager", "process", "shutdownTracing", "coordinator":
				t.Fatalf("post-bind rollback must not decompose Host field %s", sel.Sel.Name)
			}
			return true
		})
		return false
	})
	if closeCalls != 1 {
		t.Fatalf("buildHost host.Close calls=%d want exactly 1", closeCalls)
	}
}

// TestHostClose_HTTPHandlerIsStableDataPlaneSeam proves the host exposes one
// stable dispatcher for the serving adapter instead of handing out its Manager.
func TestHostClose_HTTPHandlerIsStableDataPlaneSeam(t *testing.T) {
	t.Parallel()
	host, err := BuildHost(context.Background(), BuildHostInput{
		ConfigPath:      MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { cleanupHost(t, host) })

	first := host.HTTPHandler()
	if first == nil {
		t.Fatal("HTTPHandler must expose the stable generation dispatcher")
	}
	if second := host.HTTPHandler(); second != first {
		t.Fatal("HTTPHandler must return one long-lived dispatcher")
	}
	_ = first
}
