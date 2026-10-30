package configsource

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// LoadEffective reads one stable snapshot from s and runs the shared pure
// effective-configuration pipeline. Filesystem ownership remains in this
// driving adapter; core/config only sees bounded bytes and explicit options.
func (s *FixedSource) LoadEffective(ctx context.Context, active *ActiveSourceVersion, opts config.LoadEffectiveOptions) (effective *config.EffectiveConfig, result AtomicResult, err error) {
	if s == nil {
		return nil, "", fmt.Errorf("read config: nil fixed source")
	}
	if ctx == nil {
		return nil, "", fmt.Errorf("read config: nil context")
	}
	if opts.ConfigDir == "" {
		opts.ConfigDir = filepath.Dir(s.AbsolutePath())
	}
	snap, result, err := s.ReadStable(ctx, active)
	if err != nil {
		return nil, result, fmt.Errorf("read config: %w", err)
	}
	defer func() {
		cleanupErr := snap.Close(ctx)
		if err == nil && cleanupErr != nil {
			err = errors.Join(integrityErr(CategoryPartialUnreadable), cleanupErr)
		} else {
			err = errors.Join(err, cleanupErr)
		}
		if err != nil {
			effective = nil
		}
	}()
	effective, err = config.LoadEffective(ctx, snap.Bytes, opts)
	if err != nil {
		return nil, result, err
	}
	return effective, result, nil
}

// LoadEffectiveFromPath resolves one fixed source and delegates to LoadEffective.
func LoadEffectiveFromPath(ctx context.Context, path string, active *ActiveSourceVersion, opts config.LoadEffectiveOptions) (*config.EffectiveConfig, AtomicResult, error) {
	source, err := NewFixedSource(path, 0)
	if err != nil {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	return source.LoadEffective(ctx, active, opts)
}
