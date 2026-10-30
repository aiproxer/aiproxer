package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/tracing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Test-only ValidateDistribution stage identifiers (req 5.4). Kept out of
// production validate_distribution.go (PR B4).
type validateDistributionStage string

const (
	validateStageLoader       validateDistributionStage = "loader"
	validateStageTracing      validateDistributionStage = "tracing"
	validateStageRegistry     validateDistributionStage = "registry"
	validateStageProcess      validateDistributionStage = "process"
	validateStageCompile      validateDistributionStage = "compile"
	validateStageRollback     validateDistributionStage = "rollback"
	validateStageProcessClose validateDistributionStage = "process_close"
	validateStageTracingClose validateDistributionStage = "tracing_close"
)

func (j *validateDistributionJournal) acquire(stage string) {
	j.Acquired = append(j.Acquired, stage)
}

func (j *validateDistributionJournal) clean(stage string) {
	if slices.Contains(j.Cleaned, stage) {
		return
	}
	j.Cleaned = append(j.Cleaned, stage)
}

type validateJournalBundle struct {
	inner   GenerationRuntime
	journal *validateDistributionJournal
	faultAt validateDistributionStage
}

func (j *validateJournalBundle) Handler() http.Handler {
	if j == nil || j.inner == nil {
		return nil
	}
	return j.inner.Handler()
}

func (j *validateJournalBundle) ExecutorView() aipsdk.ExecutorView {
	if j == nil || j.inner == nil {
		return nil
	}
	return j.inner.ExecutorView()
}

func (j *validateJournalBundle) BindModelViews(ctx context.Context) context.Context {
	if j == nil || j.inner == nil {
		return ctx
	}
	return j.inner.BindModelViews(ctx)
}

func (j *validateJournalBundle) BackendFactoryKindCounts() map[string]int {
	if j == nil || j.inner == nil {
		return nil
	}
	return j.inner.BackendFactoryKindCounts()
}

func (j *validateJournalBundle) TerminalProviders() terminalworkapp.TerminalProviderView {
	if j == nil || j.inner == nil {
		return terminalworkapp.SnapshotTerminalProviders(nil)
	}
	return j.inner.TerminalProviders()
}

func (j *validateJournalBundle) ReadinessReport() controlplane.ReadinessReportReader {
	if j == nil || j.inner == nil {
		return nil
	}
	return j.inner.ReadinessReport()
}

func (j *validateJournalBundle) StartPublished(ctx context.Context) error {
	if j == nil || j.inner == nil {
		return nil
	}
	return j.inner.StartPublished(ctx)
}

func (j *validateJournalBundle) Quiesce(ctx context.Context) error {
	if j == nil || j.inner == nil {
		return nil
	}
	return j.inner.Quiesce(ctx)
}

func (j *validateJournalBundle) Close() error {
	if j == nil || j.inner == nil {
		return nil
	}
	j.journal.clean("rollback")
	err := j.inner.Close()
	if j.faultAt == validateStageRollback {
		return errors.Join(err, fmt.Errorf("runtimebundle: validate distribution fault: rollback"))
	}
	return err
}

// validateDistributionFaulting invokes the real production validateDistribution
// transaction with ops wrappers that inject a fault at faultAt and journal
// acquire/cleanup evidence.
func validateDistributionFaulting(ctx context.Context, in ValidateDistributionInput, faultAt validateDistributionStage) (validateDistributionJournal, error) {
	var journal validateDistributionJournal
	ops := defaultValidateDistributionOps()

	baseLoad := ops.load
	ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error) {
		eff, src, owner, fixed, err := baseLoad(ctx, path, cli, capabilities)
		if err != nil {
			return nil, nil, owner, fixed, err
		}
		journal.acquire("loader")
		journal.Loads++
		if faultAt == validateStageLoader {
			return nil, nil, owner, fixed, fmt.Errorf("runtimebundle: validate distribution fault: loader")
		}
		return eff, src, owner, fixed, nil
	}

	baseTracing := ops.tracing
	ops.tracing = func(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
		res, err := baseTracing(ctx, cfg)
		if err != nil {
			return res, err
		}
		journal.acquire("tracing")
		inner := res.Shutdown
		res.Shutdown = func(ctx context.Context) error {
			journal.clean("tracing_close")
			var shutErr error
			if inner != nil {
				shutErr = inner(ctx)
			}
			if faultAt == validateStageTracingClose {
				return errors.Join(shutErr, fmt.Errorf("runtimebundle: validate distribution fault: tracing_close"))
			}
			return shutErr
		}
		if faultAt == validateStageTracing {
			_ = res.Shutdown(context.WithoutCancel(ctx))
			return tracing.Result{}, fmt.Errorf("runtimebundle: validate distribution fault: tracing")
		}
		return res, nil
	}

	baseRegistry := ops.registry
	ops.registry = func(cfg *config.Config, mandatory []aipsdk.Requirement) (*pluginreg.Registry, []aipsdk.Registration, error) {
		reg, regs, err := baseRegistry(cfg, mandatory)
		if err != nil {
			return nil, nil, err
		}
		journal.acquire("registry")
		if faultAt == validateStageRegistry {
			return nil, nil, fmt.Errorf("runtimebundle: validate distribution fault: registry")
		}
		return reg, regs, nil
	}

	baseProcess := ops.process
	ops.process = func(ctx context.Context, in processBuildInput) (*ProcessServices, error) {
		ps, err := baseProcess(ctx, in)
		if err != nil {
			return nil, err
		}
		journal.acquire("process")
		ps.closers = append([]func() error{func() error {
			journal.clean("process_close")
			if faultAt == validateStageProcessClose {
				return fmt.Errorf("runtimebundle: validate distribution fault: process_close")
			}
			return nil
		}}, ps.closers...)
		if faultAt == validateStageProcess {
			_ = ps.Close()
			return nil, fmt.Errorf("runtimebundle: validate distribution fault: process")
		}
		return ps, nil
	}

	baseCompile := ops.compile
	ops.compile = func(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error) {
		bundle, err := baseCompile(ctx, ps, cfg, compose)
		if err != nil {
			return nil, err
		}
		journal.acquire("compile")
		wrapped := &validateJournalBundle{inner: bundle, journal: &journal, faultAt: faultAt}
		if faultAt == validateStageCompile {
			return wrapped, fmt.Errorf("runtimebundle: validate distribution fault: compile")
		}
		return wrapped, nil
	}

	err := validateDistribution(ctx, in, nil, ops)
	return journal, err
}

// TestPartialCleanup_ValidateDistributionFaultSeamUsesProductionTransaction
// guards against a second test-only ownership engine regressing under the
// ValidateDistribution PartialCleanup matrix.
func TestPartialCleanup_ValidateDistributionFaultSeamUsesProductionTransaction(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("validate_distribution_fault_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "validate_distribution_fault_test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name == nil || f.Name.Name != "runtimebundle" {
		t.Fatal("fault seam must stay in package runtimebundle to call validateDistribution with ops")
	}
	var faulting *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "validateDistributionFaulting" {
			return true
		}
		faulting = fd
		return false
	})
	if faulting == nil || faulting.Body == nil {
		t.Fatal("missing validateDistributionFaulting")
	}
	forbidden := []string{"NewProcessServices", "CompileGeneration", "installRegistryAndRegistrations", "initProcessTracing"}
	ast.Inspect(faulting.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		for _, tok := range forbidden {
			if name == tok {
				t.Fatalf("validateDistributionFaulting must not duplicate production step %s", tok)
			}
		}
		return true
	})
	body := src[fset.Position(faulting.Body.Pos()).Offset:fset.Position(faulting.Body.End()).Offset]
	if !strings.Contains(string(body), "validateDistribution(") {
		t.Fatal("validateDistributionFaulting must invoke production validateDistribution")
	}
	if !strings.Contains(string(src), "defaultValidateDistributionOps") {
		t.Fatal("validateDistributionFaulting must use the validateDistributionOps seam")
	}
}
