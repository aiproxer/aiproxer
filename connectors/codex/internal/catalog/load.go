package catalog

import (
	"context"
	"errors"
	"time"
)

// DefaultDiscoveryTimeout is the default timeout for `codex debug models`.
const DefaultDiscoveryTimeout = 10 * time.Second

// LoadOptions configures catalog resolution.
type LoadOptions struct {
	// Enabled controls whether `codex debug models` is attempted at startup.
	// When false (or discovery fails), the shipped/override snapshot is used.
	Enabled bool
	// FallbackPath overrides the shipped snapshot path. Empty = shipped embed.
	FallbackPath string
	// CodexBinaryPath is an explicit codex binary path. Empty = resolve via
	// CODEX_BIN / PATH / npm-global.
	CodexBinaryPath string
	// Timeout is the discovery subprocess timeout. Zero = DefaultDiscoveryTimeout.
	Timeout time.Duration
}

// Load resolves the Codex model catalog: runs `codex debug models` when
// enabled, falling back to the shipped (or override) snapshot on discovery
// failure or when discovery is disabled. The returned Source indicates where
// the catalog came from.
//
// Caller cancellation is never masked by fallback: a canceled context returns
// the context error. Only discovery-attempt failures (binary, timeout, bad
// output) fall back, and discovery-disabled returns the snapshot by operator
// choice.
func Load(ctx context.Context, opts LoadOptions) (*Catalog, Source, error) {
	if ctx == nil {
		return nil, SourceUnknown, errors.New("codexcatalog: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, SourceUnknown, err
	}
	if opts.Enabled {
		exe, err := ResolveExecutable(opts.CodexBinaryPath)
		if err == nil {
			timeout := opts.Timeout
			if timeout <= 0 {
				timeout = DefaultDiscoveryTimeout
			}
			if cat, derr := Discover(ctx, exe, timeout); derr == nil {
				return cat, SourceDiscovered, nil
			} else if ctx.Err() != nil {
				return nil, SourceUnknown, ctx.Err()
			}
		}
	}
	cat, err := LoadFallback(opts.FallbackPath)
	if err != nil {
		return nil, SourceUnknown, err
	}
	if opts.FallbackPath == "" {
		return cat, SourceShippedFallback, nil
	}
	return cat, SourceOverrideFallback, nil
}

// Source describes where a loaded catalog came from.
type Source string

const (
	SourceUnknown          Source = ""
	SourceDiscovered       Source = "discovered"
	SourceShippedFallback  Source = "shipped_fallback"
	SourceOverrideFallback Source = "override_fallback"
)
