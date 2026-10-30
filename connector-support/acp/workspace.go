package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// WorkspacePolicy resolves and validates workspace directories for ACP subprocess
// agents. ACP agents require a cwd for
// session/new; this policy ensures the directory exists and is readable before
// passing it to the agent.
type WorkspacePolicy struct {
	// DefaultDir is the fallback workspace when no explicit hint is provided.
	// If empty, an error is returned when no workspace can be resolved.
	DefaultDir string
	// RequireExplicit, when true, errors if no usable workspace directory is
	// found.
	RequireExplicit bool
}

// workspaceHintKeys are the keys searched in config dicts for workspace paths,
// in priority order.
var workspaceHintKeys = []string{"project_dir", "workspace_path", "cwd", "project"}

// ErrNoWorkspace is returned when no usable workspace directory can be resolved.
var ErrNoWorkspace = errors.New("acp: no usable workspace directory")

// ErrUnusableWorkspace is returned when a workspace hint points to an unusable directory.
var ErrUnusableWorkspace = errors.New("acp: unusable workspace directory")

// ResolveWorkspace resolves the workspace directory from explicit hints (checked
// in priority order) or falls back to DefaultDir. Returns:
//   - (path, nil) on success
//   - ("", ErrNoWorkspace) when RequireExplicit is true and no hint is found
//   - ("", ErrUnusableWorkspace) when a hint is found but not a valid readable directory
//   - ("", err) for other errors
//
// Trivial paths (".", "..") are treated as unset. Relative paths are treated as
// unset so request enrichment can resolve the workspace later.
func (wp WorkspacePolicy) ResolveWorkspace(hints map[string]string) (string, error) {
	// Search hints in priority order. If an absolute hint is found but unusable,
	// return ErrUnusableWorkspace; an explicitly specified invalid absolute path
	// is an error, not silently ignored.
	for _, key := range workspaceHintKeys {
		raw, ok := hints[key]
		if !ok {
			continue
		}
		candidate := strings.TrimSpace(raw)
		if candidate == "" || isTrivialPath(candidate) {
			continue
		}
		// Relative paths are treated as unset (late-stage resolution).
		if !filepath.IsAbs(candidate) {
			continue
		}
		if !isUsableWorkspaceDirectory(candidate) {
			return "", fmt.Errorf("%w: %s", ErrUnusableWorkspace, candidate)
		}
		return candidate, nil
	}

	// No explicit hint found.
	if wp.RequireExplicit {
		return "", ErrNoWorkspace
	}

	// Fall back to default.
	def := strings.TrimSpace(wp.DefaultDir)
	if def == "" {
		return "", ErrNoWorkspace
	}
	if !filepath.IsAbs(def) {
		// Resolve relative default to absolute.
		abs, err := filepath.Abs(def)
		if err != nil {
			return "", fmt.Errorf("acp: resolve default workspace: %w", err)
		}
		def = abs
	}
	if !isUsableWorkspaceDirectory(def) {
		return "", fmt.Errorf("%w: %s (default)", ErrUnusableWorkspace, def)
	}
	return def, nil
}

// isTrivialPath returns true for ".", "..", and variants that should be ignored.
// Empty strings are NOT trivial (they're just unset) — callers should check for empty first.
func isTrivialPath(p string) bool {
	if p == "" {
		return false
	}
	clean := strings.TrimSpace(filepath.Clean(p))
	return clean == "." || clean == ".."
}

// isUsableWorkspaceDirectory checks that path exists, is a directory, and is readable
// The directory must pass the filesystem usability checks below.
func isUsableWorkspaceDirectory(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return false
	}
	// Check readability by attempting to open the directory.
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// WorkspaceHintsFromCall extracts workspace hint key-value pairs from a canonical
// call's Extensions. It checks ACP-specific keys ("acp.cwd", "acp.workspace") first
// and maps them to the generic hint keys that ResolveWorkspace searches, then falls
// back to any generic keys present in the extensions. This is the shared extraction
// logic used by both the ACP CLI connectors and the Codex app-server backend.
func WorkspaceHintsFromCall(call *aipapi.Call) map[string]string {
	hints := make(map[string]string)
	if call == nil || call.Extensions == nil {
		return hints
	}
	// ACP-specific keys take priority; map to generic keys the resolver knows.
	if raw, ok := call.Extensions[extCwdJSONKey]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
			hints["cwd"] = s
		}
	}
	if raw, ok := call.Extensions[extWorkspaceJSONKey]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
			hints["workspace_path"] = s
		}
	}
	// Generic hint keys as fallback (don't overwrite ACP-specific values).
	for _, key := range workspaceHintKeys {
		if _, exists := hints[key]; exists {
			continue
		}
		if raw, ok := call.Extensions[key]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
				hints[key] = s
			}
		}
	}
	return hints
}

// firstUsableWorkspaceDir searches multiple hint maps for the first usable directory,
// using ordered workspace hints. Maps are searched in order; within
// each map, workspaceHintKeys are checked in priority order. Relative paths are always
// treated as unset; requireAbsolute is reserved for future
// use when late-stage resolution supports relative paths.
func firstUsableWorkspaceDir(maps []map[string]string, _ bool) (string, error) {
	for _, m := range maps {
		if m == nil {
			continue
		}
		for _, key := range workspaceHintKeys {
			raw, ok := m[key]
			if !ok {
				continue
			}
			candidate := strings.TrimSpace(raw)
			if candidate == "" || isTrivialPath(candidate) {
				continue
			}
			if !filepath.IsAbs(candidate) {
				continue // relative paths treated as unset
			}
			if isUsableWorkspaceDirectory(candidate) {
				return candidate, nil
			}
		}
	}
	return "", ErrNoWorkspace
}

// firstWorkspaceHintStr returns the first non-trivial workspace hint string from
// the given maps, or empty if none found. Used for error messages.
func firstWorkspaceHintStr(maps []map[string]string) string {
	for _, m := range maps {
		if m == nil {
			continue
		}
		for _, key := range workspaceHintKeys {
			raw, ok := m[key]
			if !ok {
				continue
			}
			candidate := strings.TrimSpace(raw)
			if candidate == "" || isTrivialPath(candidate) {
				continue
			}
			return candidate
		}
	}
	return ""
}
