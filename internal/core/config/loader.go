package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// resolveConfigPath returns the absolute startup configuration path.
func resolveConfigPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("config: empty path")
	}
	path, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", fmt.Errorf("config: resolve path: %w", err)
	}
	return path, nil
}

// LoadFile decodes typed runtime configuration from YAML, applies defaults, and runs [Validate].
// Reload candidates use the filesystem-driven configsource adapter; this compatibility entrypoint
// deliberately keeps core/config independent from driving adapters.
func LoadFile(path string) (*Config, error) {
	return LoadFileWithContext(context.Background(), path)
}

// LoadFileWithContext is the context-aware form of [LoadFile].
func LoadFileWithContext(ctx context.Context, rawPath string) (*Config, error) {
	path, err := resolveConfigPath(rawPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	f, err := os.Open(path) // #nosec G304 -- explicit operator-supplied startup config path
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("read config: %s", CategoryUnsupportedType)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, DefaultConfigMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, _, err := StrictDecode(raw)
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.ConfigDir = filepath.Dir(path)
	applyLoadDefaults(cfg)
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}
