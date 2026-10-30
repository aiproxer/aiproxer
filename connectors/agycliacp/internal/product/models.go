package product

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

const defaultCanonicalModel = "google/gemini-3.5-flash"

// ErrUnknownModel is returned when a route model is not a known AGY pretty
// name or mapped canonical identity in the active provider allowlist.
var ErrUnknownModel = errors.New("agycliacp: unknown model")

type modelIdentity struct {
	native    string
	canonical string
}

const (
	ReasoningLow    = "low"
	ReasoningMedium = "medium"
	ReasoningHigh   = "high"
)

// knownModelTable is the fixed pretty↔canonical conversion table for AGY
// discovery parsing. Open/BuildCommand allowlists come from the active provider
// snapshot/override, not this table alone.
var knownModelTable = [...]modelIdentity{
	{native: "gemini-3.7-flash-low", canonical: "google/gemini-3.7-flash"},
	{native: "gemini-3.7-flash-medium", canonical: "google/gemini-3.7-flash"},
	{native: "gemini-3.7-flash-high", canonical: "google/gemini-3.7-flash"},
	{native: "gemini-3.6-flash-low", canonical: "google/gemini-3.6-flash"},
	{native: "gemini-3.6-flash-medium", canonical: "google/gemini-3.6-flash"},
	{native: "gemini-3.6-flash-high", canonical: "google/gemini-3.6-flash"},
	{native: "gemini-3.5-flash-low", canonical: "google/gemini-3.5-flash"},
	{native: "gemini-3.5-flash-medium", canonical: "google/gemini-3.5-flash"},
	{native: "gemini-3.5-flash-high", canonical: "google/gemini-3.5-flash"},
	{native: "gemini-3.1-pro-low", canonical: "google/gemini-3.1-pro"},
	{native: "gemini-3.1-pro-high", canonical: "google/gemini-3.1-pro"},
	{native: "claude-sonnet-4-6", canonical: "anthropic/claude-sonnet-4.6"},
	{native: "claude-opus-4-6-thinking", canonical: "anthropic/claude-opus-4.6"},
	{native: "gpt-oss-120b-medium", canonical: "openai/gpt-oss-120b"},
}

func nativePrettyForCanonical(canonical string) (string, bool) {
	canonical = strings.TrimSpace(canonical)
	for i := range knownModelTable {
		if knownModelTable[i].canonical == canonical {
			return knownModelTable[i].native, true
		}
	}
	if pretty, ok := defaultNativeForCanonical(canonical); ok {
		return pretty, true
	}
	return "", false
}

func canonicalForPretty(pretty string) (string, bool) {
	pretty = strings.TrimSpace(pretty)
	for i := range knownModelTable {
		if knownModelTable[i].native == pretty {
			return knownModelTable[i].canonical, true
		}
	}
	if canonical, _, _, _, ok := parseNativeModelID(pretty); ok {
		return canonical, true
	}
	return "", false
}

func defaultNativeForCanonical(canonical string) (string, bool) {
	canonical = strings.TrimSpace(canonical)
	switch {
	case strings.HasPrefix(canonical, "google/gemini-"):
		name := strings.TrimPrefix(canonical, "google/gemini-")
		return "gemini-" + name, true
	case strings.HasPrefix(canonical, "anthropic/claude-"):
		name := strings.TrimPrefix(canonical, "anthropic/claude-")
		name = strings.ReplaceAll(name, ".", "-")
		return "claude-" + name, true
	case strings.HasPrefix(canonical, "openai/gpt-"):
		name := strings.TrimPrefix(canonical, "openai/gpt-")
		return "gpt-" + name, true
	}
	return "", false
}

func parseNativeModelID(native string) (canonical, display, effort string, thinking, ok bool) {
	if native == "" {
		return "", "", "", false, false
	}
	base := native
	for _, candidate := range []string{ReasoningLow, ReasoningMedium, ReasoningHigh} {
		if strings.HasSuffix(base, "-"+candidate) {
			effort = candidate
			base = strings.TrimSuffix(base, "-"+candidate)
			break
		}
	}
	if strings.HasSuffix(base, "-thinking") {
		thinking = true
		base = strings.TrimSuffix(base, "-thinking")
	}

	switch {
	case strings.HasPrefix(base, "gemini-"):
		name := strings.TrimPrefix(base, "gemini-")
		canonical = "google/gemini-" + name
		display = "Gemini " + displayModelTail(name)
	case strings.HasPrefix(base, "claude-"):
		name := strings.TrimPrefix(base, "claude-")
		name = normalizeClaudeVersion(name)
		canonical = "anthropic/claude-" + name
		display = "Claude " + displayModelTail(name)
	case strings.HasPrefix(base, "gpt-"):
		name := strings.TrimPrefix(base, "gpt-")
		canonical = "openai/gpt-" + name
		display = "GPT " + displayModelTail(name)
	default:
		return "", "", "", false, false
	}
	return canonical, display, effort, thinking, true
}

func normalizeClaudeVersion(name string) string {
	parts := strings.Split(name, "-")
	for i := 0; i+1 < len(parts); i++ {
		if allDigits(parts[i]) && allDigits(parts[i+1]) {
			parts[i] += "." + parts[i+1]
			parts = append(parts[:i+1], parts[i+2:]...)
			break
		}
	}
	return strings.Join(parts, "-")
}

func displayModelTail(name string) string {
	parts := strings.Split(name, "-")
	for i, part := range parts {
		if part == "oss" {
			parts[i] = "OSS"
			continue
		}
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func agyCanonicalFallback(native string) string {
	if mapped, ok := canonicalForPretty(native); ok {
		return mapped
	}
	return ""
}

func (s *agySpec) resolveNativeModel(effective string) (string, error) {
	if s == nil {
		return "", ErrUnknownModel
	}
	identity := strings.TrimSpace(acp.ResolveVendorModel(vendorPrefix, s.cfg.Model, defaultCanonicalModel, effective))
	if identity == "" {
		return "", ErrUnknownModel
	}
	if s.index.IsKnownNative(identity) {
		return identity, nil
	}
	if native, ok := s.index.NativeForCanonical(identity); ok {
		return native, nil
	}
	// Conversion-table pretty for a known canonical, then constrain to allowlist.
	if pretty, ok := nativePrettyForCanonical(identity); ok {
		if s.index.IsKnownNative(pretty) {
			return pretty, nil
		}
	}
	return "", ErrUnknownModel
}

func parseAGYModelsListing(stdout string) ([]modelinventory.Model, []string) {
	models := make([]modelinventory.Model, 0, 8)
	warnings := make([]string, 0)
	seen := make(map[string]struct{})
	for rawLine := range strings.SplitSeq(stdout, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "Fetching") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		modelID := fields[0]
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		canonical, ok := canonicalForPretty(modelID)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("agy models: unrecognized model name omitted: %q", line))
			continue
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		models = append(models, modelinventory.Model{
			CanonicalID: canonical,
			NativeID:    canonical,
			DisplayName: canonical,
		})
	}
	return models, warnings
}

func resolveAGYBinary(cache *acp.ExecutableCache, configured string) (string, error) {
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := cache.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv("AGY_BINARY")); env != "" {
		if resolved, ok := cache.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	for _, name := range []string{"agy", "agy.exe", "agy.cmd"} {
		if resolved, err := cache.LookPath(name); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("agy executable not found; install Antigravity CLI and ensure `agy` is on PATH, or set AGY_BINARY / agy_binary")
}

type modelsProvider struct {
	binary string
	run    func(ctx context.Context, binary string) ([]byte, error)
}

func newModelsProvider(binary string) modelinventory.Provider {
	return modelsProvider{binary: binary, run: defaultModelsCommandRunner}
}

func sanitizeBoundStderr(b []byte) string {
	return acp.SanitizeBoundStderr(b)
}

func defaultModelsCommandRunner(ctx context.Context, binary string) ([]byte, error) {
	if ctx == nil {
		return nil, modelinventory.ErrNilContext
	}
	cmd := exec.CommandContext(ctx, binary, "models")
	cmd.Env = agyProcessEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%w: %s", err, sanitizeBoundStderr(stderr.Bytes()))
		}
		return nil, err
	}
	return out, nil
}

func agyProcessEnv() []string {
	env := os.Environ()
	home := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if home == "" {
		home = strings.TrimSpace(os.Getenv("HOME"))
	}
	if home == "" {
		if current, err := user.Current(); err == nil {
			home = strings.TrimSpace(current.HomeDir)
		}
	}
	if home != "" {
		if os.Getenv("USERPROFILE") == "" {
			env = append(env, "USERPROFILE="+home)
		}
		if os.Getenv("HOME") == "" {
			env = append(env, "HOME="+home)
		}
	}
	return env
}

func (p modelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	run := p.run
	if run == nil {
		run = defaultModelsCommandRunner
	}
	out, err := run(ctx, p.binary)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return modelinventory.Snapshot{}, err
		}
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  fmt.Errorf("agy models: %w", err),
		}
	}
	models, warnings := parseAGYModelsListing(string(out))
	if models == nil {
		models = []modelinventory.Model{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now().UTC(),
		Models:   models,
		Warnings: warnings,
	}, nil
}
