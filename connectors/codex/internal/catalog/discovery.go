package catalog

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Discover runs `codex debug models` via the resolved codex binary and parses
// the JSON stdout. Returns an error on any failure (binary missing, timeout,
// non-zero exit, empty/malformed output) so the caller can fall back.
func Discover(ctx context.Context, executable string, timeout time.Duration) (*Catalog, error) {
	if strings.TrimSpace(executable) == "" {
		return nil, fmt.Errorf("codexcatalog: no codex executable")
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executable, "debug", "models")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("codexcatalog: `codex debug models` failed: %w (stderr: %s)", err, msg)
		}
		return nil, fmt.Errorf("codexcatalog: `codex debug models` failed: %w", err)
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("codexcatalog: `codex debug models` returned empty stdout")
	}
	return Parse(trimmed)
}

// ResolveExecutable finds the Codex CLI binary, with cross-platform fallbacks:
// configured path -> CODEX_BIN env -> PATH (codex/codex.cmd/codex.exe) ->
// npm-global locations. Mirrors the codexappserver resolver but uses stdlib
// only (no plugin-package imports) so it is safe to call from core/infra.
func ResolveExecutable(configured string) (string, error) {
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := checkExecutable(c); ok {
			return resolved, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv("CODEX_BIN")); env != "" {
		if resolved, ok := checkExecutable(env); ok {
			return resolved, nil
		}
	}
	for _, name := range []string{"codex", "codex.cmd", "codex.exe"} {
		if resolved, err := exec.LookPath(name); err == nil {
			return resolved, nil
		}
	}
	if runtime.GOOS == "windows" {
		for _, envVar := range []string{"APPDATA", "LOCALAPPDATA"} {
			if dir := strings.TrimSpace(os.Getenv(envVar)); dir != "" {
				candidate := filepath.Join(dir, "npm", "codex.cmd")
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate, nil
				}
			}
		}
	} else {
		if home, err := os.UserHomeDir(); err == nil {
			for _, rel := range []string{".local/bin/codex", ".npm-global/bin/codex"} {
				candidate := filepath.Join(home, rel)
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate, nil
				}
			}
		}
	}
	return "", fmt.Errorf("codex CLI executable not found; install Codex CLI and ensure `codex` is on PATH, or set CODEX_BIN")
}

func checkExecutable(candidate string) (string, bool) {
	c := strings.TrimSpace(candidate)
	if c == "" {
		return "", false
	}
	if filepath.IsAbs(c) {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, true
		}
		return "", false
	}
	if resolved, err := exec.LookPath(c); err == nil {
		return resolved, true
	}
	return "", false
}
