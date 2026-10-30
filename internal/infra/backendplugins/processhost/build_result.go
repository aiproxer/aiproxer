package processhost

import (
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
)

// BuildResult is a composition-owned backend construction outcome.
// Lifecycle cleanup is not part of the core-consumed Backend value.
type BuildResult struct {
	Backend execbackend.Backend
	cleanup func() error
	once    sync.Once
	err     error
}

// NewBuildResult pairs a core Backend with an idempotent cleanup.
func NewBuildResult(be execbackend.Backend, cleanup func() error) *BuildResult {
	return &BuildResult{Backend: be, cleanup: cleanup}
}

// Cleanup closes process/instance resources exactly once.
func (b *BuildResult) Cleanup() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		if b.cleanup != nil {
			b.err = b.cleanup()
		}
	})
	return b.err
}
