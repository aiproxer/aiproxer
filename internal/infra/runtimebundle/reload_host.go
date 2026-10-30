package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/standardplugins/legacyfeatureconfig"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"go.opentelemetry.io/otel"
)

type GenerationCompiler struct {
	Process *ProcessServices
	Compose HandlerComposer
}

func (c GenerationCompiler) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (GenerationRuntime, error) {
	if c.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if c.Compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	return CompileGeneration(ctx, GenerationCompileInput{
		Process: c.Process, Candidate: candidate, Compose: c.Compose, LiveFactoryKinds: liveFactoryKinds,
	})
}

type candidateCompilerAdapter struct{ inner GenerationCompiler }

func (a candidateCompilerAdapter) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (runtimehost.PublishedRequestPlane, error) {
	return a.inner.Compile(ctx, candidate, liveFactoryKinds)
}

type Host struct {
	coordinator                  *runtimehost.Coordinator
	manager                      *runtimehost.Manager
	process                      *ProcessServices
	executor                     *runtimehost.GenerationExecutor
	source                       *configsource.FixedSource
	effective                    *config.EffectiveConfig
	config                       *config.Config
	logger                       *slog.Logger
	fixedStreamRecovery          config.StreamRecoveryOverrides
	shutdownTracing              func(context.Context) error
	dispatcher                   *runtimehost.GenerationDispatcher
	closeMu                      sync.Mutex
	closeAttempt                 *hostCloseAttempt
	closed                       bool
	processClosed, tracingClosed bool
}

type hostCloseAttempt struct {
	done chan struct{}
	err  error
}

type bindHostInput struct {
	Manager                *runtimehost.Manager
	Process                *ProcessServices
	Compose                HandlerComposer
	Logger                 *slog.Logger
	Config                 *config.Config
	Effective              *config.EffectiveConfig
	ActiveSource           *configsource.ActiveSourceVersion
	ActiveSourceOwner      *configsource.SourceOwnerSlot
	FixedStreamRecovery    config.StreamRecoveryOverrides
	EnterpriseCapabilities config.CapabilitySet
	ShutdownTracing        func(context.Context) error
}

// bindHost wires coordinator, manager, and process services into a Host.
func bindHost(configPath string, in bindHostInput) (*Host, error) {
	if in.Manager == nil {
		return nil, fmt.Errorf("runtimebundle: nil GenerationManager")
	}
	if in.Process == nil {
		return nil, fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if in.Compose == nil {
		return nil, fmt.Errorf("runtimebundle: nil HandlerComposer")
	}
	src, err := configsource.NewFixedSource(configPath, 0)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: fixed source: %w", err)
	}

	fixed := in.FixedStreamRecovery
	// Reload validates every candidate against the startup capability snapshot.
	capabilities := in.EnterpriseCapabilities
	loader := runtimehost.FuncEffectiveLoader(func(ctx context.Context, raw []byte) (*config.EffectiveConfig, error) {
		merged := fixed
		eff, err := config.LoadEffective(ctx, raw, config.LoadEffectiveOptions{
			ConfigDir: filepath.Dir(src.AbsolutePath()), FixedStreamRecovery: &merged,
			Capabilities:   capabilities,
			NormalizeYAML:  legacyfeatureconfig.NormalizeYAML,
			InjectFeatures: injectStandardBootstrapFeatures, ExtraValidate: extraBootstrapValidate,
		})
		if err != nil {
			return nil, err
		}
		// A reload that turns on required authority evidence must be receivable
		// by the already-captured port; an ordinary load error rejects the
		// candidate (ResultInvalid/StageLoad) and retains the live generation.
		if err := validateRequiredAuthorityEvidenceWiring(eff.Config, in.Process.UsageAuthority); err != nil {
			return nil, err
		}
		return eff, nil
	})

	obsDeps := runtimehost.ReloadObserverDeps{Logger: in.Logger, Tracer: otel.Tracer("aip.runtimehost.reload")}
	if in.Process.Metrics != nil {
		obsDeps.Metrics = in.Process.Metrics.Reload
	}
	observer := runtimehost.NewReloadObserver(obsDeps)
	in.Manager.SetLifecycleObserver(observer) // Manager-owned retirement telemetry

	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source: src, Loader: loader, Classify: configreload.ClassifyEffective,
		Compile: candidateCompilerAdapter{inner: GenerationCompiler{Process: in.Process, Compose: in.Compose}},
		Manager: in.Manager, Timeout: runtimehost.DefaultReloadTimeout,
		ActiveEffective: in.Effective, ActiveSource: in.ActiveSource, ActiveSourceOwner: in.ActiveSourceOwner, Observer: observer,
	})
	if err != nil {
		return nil, err
	}
	return &Host{
		coordinator: coord, manager: in.Manager, process: in.Process,
		executor: runtimehost.NewGenerationExecutor(in.Manager), dispatcher: runtimehost.NewGenerationDispatcher(in.Manager),
		source: src, effective: in.Effective, config: in.Config, logger: in.Logger,
		fixedStreamRecovery: in.FixedStreamRecovery, shutdownTracing: in.ShutdownTracing,
	}, nil
}

func (h *Host) HTTPHandler() http.Handler {
	if h == nil || h.dispatcher == nil {
		return nil
	}
	return h.dispatcher
}

func (h *Host) ExecutorView() aipsdk.ExecutorView {
	if h == nil || h.executor == nil {
		return nil
	}
	return h.executor
}

// Logger, Config, and Effective expose process-bound configuration views.
func (h *Host) Logger() *slog.Logger {
	if h == nil {
		return nil
	}
	return h.logger
}

func (h *Host) Config() *config.Config {
	if h == nil {
		return nil
	}
	return h.config
}

func (h *Host) Effective() *config.EffectiveConfig {
	if h == nil {
		return nil
	}
	return h.effective
}

func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	ctx = ctxOrBackground(ctx)
	h.closeMu.Lock()
	if h.closed {
		h.closeMu.Unlock()
		return nil
	}
	if attempt := h.closeAttempt; attempt != nil {
		done := attempt.done
		h.closeMu.Unlock()
		select {
		case <-done:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	attempt := &hostCloseAttempt{done: make(chan struct{})}
	h.closeAttempt = attempt
	h.closeMu.Unlock()

	err := h.runCloseAttempt(ctx)

	h.closeMu.Lock()
	attempt.err = err
	if err == nil {
		h.closed = true
	}
	close(attempt.done)
	h.closeAttempt = nil
	h.closeMu.Unlock()
	return err
}

func (h *Host) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if h != nil && h.coordinator != nil {
		return h.coordinator.Reload(ctx, trigger)
	}
	return sdkreload.Result{Category: sdkreload.ResultInternalFailed, ReasonCategory: "nil-coordinator"}
}

func (h *Host) Status() sdkreload.Status {
	if h != nil && h.coordinator != nil {
		return h.coordinator.Status()
	}
	return sdkreload.Status{}
}

func (h *Host) FixedSourcePath() string {
	if h != nil && h.coordinator != nil {
		return h.coordinator.FixedSourcePath()
	}
	return ""
}

func (h *Host) runCloseAttempt(ctx context.Context) error {
	h.BeginShutdown()
	var cleanupErr error
	if err := h.WaitForIdle(ctx); err != nil {
		if ctx.Err() != nil {
			return err
		}
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if h.manager != nil {
		if err := h.manager.ShutdownDetached(ctx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			if ctx.Err() != nil {
				return cleanupErr
			}
		}
		if h.manager.HasOpenGenerations() {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("runtimebundle: generations remain open after shutdown"))
			return errors.Join(cleanupErr, h.shutdownTracingOnce(ctx))
		}
	}
	// The accepted-producer fence is observed after the accepted requests and
	// generations are finished and before any process closer runs, so no
	// producer can still append into a store that is about to close. A drain
	// failure leaves every dependency open and the close retryable.
	if h.process != nil {
		if err := h.process.DrainObservationProducers(ctx); err != nil {
			return errors.Join(cleanupErr, fmt.Errorf("runtimebundle: drain observation producers: %w", err))
		}
	}
	if err := h.closeProcessOnce(); err != nil {
		return errors.Join(cleanupErr, err)
	}
	return errors.Join(cleanupErr, h.shutdownTracingOnce(ctx))
}

func (h *Host) runCloseOnce(done *bool, skip bool, closeFn func() error, after func()) error {
	h.closeMu.Lock()
	already := *done
	h.closeMu.Unlock()
	if already || skip {
		return nil
	}
	if err := closeFn(); err != nil {
		return err
	}
	h.closeMu.Lock()
	*done = true
	if after != nil {
		after()
	}
	h.closeMu.Unlock()
	return nil
}

func (h *Host) closeProcessOnce() error {
	return h.runCloseOnce(&h.processClosed, h.process == nil, func() error { return h.process.Close() }, nil)
}

func (h *Host) shutdownTracingOnce(ctx context.Context) error {
	h.closeMu.Lock()
	fn := h.shutdownTracing
	h.closeMu.Unlock()
	return h.runCloseOnce(&h.tracingClosed, fn == nil, func() error { return fn(ctx) }, func() { h.shutdownTracing = nil })
}

func (h *Host) BeginShutdown() {
	if h != nil && h.coordinator != nil {
		h.coordinator.BeginShutdown()
	}
}

func (h *Host) WaitForIdle(ctx context.Context) error {
	if h != nil && h.coordinator != nil {
		return h.coordinator.WaitForIdle(ctx)
	}
	return nil
}
