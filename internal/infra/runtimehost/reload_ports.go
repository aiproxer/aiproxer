package runtimehost

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
)

// DefaultReloadTimeout is the host-owned reload attempt bound when unset.
const DefaultReloadTimeout = time.Minute

// StableConfigSource is the fixed-path source seam (typically configsource.FixedSource).
// Callers never supply a path or YAML through the canonical Trigger envelope.
type StableConfigSource interface {
	AbsolutePath() string
	ReadStable(ctx context.Context, active *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error)
}

// EffectiveLoader runs the shared strict effective-load pipeline on accepted bytes.
type EffectiveLoader interface {
	LoadEffective(ctx context.Context, raw []byte) (*config.EffectiveConfig, error)
}

// CandidateCompiler builds one isolated immutable request-plane candidate.
// It must not mutate active generations or process-service ownership.
type CandidateCompiler interface {
	Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (PublishedRequestPlane, error)
}

// BackendFactoryKindCounter is optionally implemented by published request planes
// so the coordinator can populate LiveFactoryKinds from active/retained generations
// (tasks.md Implementation Notes; req 8.8).
type BackendFactoryKindCounter interface {
	BackendFactoryKindCounts() map[string]int
}

// CoordinatorDeps wires production or test seams for the serialized reload coordinator.
type CoordinatorDeps struct {
	Source            StableConfigSource
	Loader            EffectiveLoader
	Classify          func(active, candidate *config.EffectiveConfig) ([]configreload.SafeChange, error)
	Compile           CandidateCompiler
	Manager           *Manager
	Timeout           time.Duration
	ActiveEffective   *config.EffectiveConfig
	ActiveSource      *configsource.ActiveSourceVersion
	ActiveSourceOwner *configsource.SourceOwnerSlot
	Observer          *ReloadObserver
}

// FuncEffectiveLoader adapts a function to EffectiveLoader.
type FuncEffectiveLoader func(ctx context.Context, raw []byte) (*config.EffectiveConfig, error)

// LoadEffective implements EffectiveLoader.
func (f FuncEffectiveLoader) LoadEffective(ctx context.Context, raw []byte) (*config.EffectiveConfig, error) {
	return f(ctx, raw)
}

// FuncCompiler adapts a function to CandidateCompiler.
type FuncCompiler func(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (PublishedRequestPlane, error)

// Compile implements CandidateCompiler.
func (f FuncCompiler) Compile(ctx context.Context, candidate *config.Config, liveFactoryKinds map[string]int) (PublishedRequestPlane, error) {
	return f(ctx, candidate, liveFactoryKinds)
}
