package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// ID is the reserved plugin identifier for the Codex App Server backend.
const ID = "openai-codex-app-server"

// vendorPrefix is the model prefix for Codex App Server models.
const vendorPrefix = "openai"

// handshakeClientName is the client name sent during initialize.
const handshakeClientName = "aiproxer"

// autoModelSentinel is the routing sentinel meaning "let the Codex app-server
// pick the model server-side". It is a protocol sentinel, not a catalog slug.
const autoModelSentinel = "auto"

// autoAcceptMethods are server-initiated JSON-RPC request methods that this
// headless proxy auto-accepts. Every other method fails closed via decline.
var autoAcceptMethods = map[string]bool{
	"execCommandApproval":                   true,
	"applyPatchApproval":                    true,
	"item/commandExecution/requestApproval": true,
	"item/fileChange/requestApproval":       true,
	"item/permissions/requestApproval":      true,
}

// Config configures the Codex App Server backend. The shared
// acp.ConnectorConfig embeds the local-agent fields common to every ACP CLI
// connector; Codex-specific fields stay directly on Config.
type Config struct {
	acp.ConnectorConfig
	// ConfigOverrides are -c key=value overrides passed between "app-server" and "--stdio".
	ConfigOverrides []string
	// ModelCatalog is the auto-discovered Codex model catalog used for the
	// built-in model inventory. May be nil (e.g. tests without DI).
	ModelCatalog *catalog.Catalog
	// ModelCatalogSource reports how ModelCatalog was obtained. Slugs from the
	// catalog are advertised only when SourceDiscovered; otherwise inventory is
	// the openai/auto sentinel alone so shipped/override snapshots are not
	// presented as proven App Server models.
	ModelCatalogSource catalog.Source
	// Inventory overrides ModelInventory when non-nil (tests / NewWithStarter).
	Inventory modelinventory.Provider
	// CatalogLoadOpts re-resolves the model catalog on every inventory load
	// when Enabled, instead of freezing the Configure-time ModelCatalog.
	// Zero value keeps the frozen-catalog inventory.
	CatalogLoadOpts catalog.LoadOptions
	// ExeCache is instance-owned executable resolution cache.
	ExeCache *acp.ExecutableCache
	// DefaultVerbosity is the process-scoped Codex model_verbosity default
	// (low, medium, or high) when the request does not set verbosity.
	DefaultVerbosity aipapi.VerbosityLevel
}

// resolveExecutable finds the Codex CLI binary, with cross-platform fallbacks.
func resolveExecutable(cache *acp.ExecutableCache, configured string) (string, error) {
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	// 1. Check configured path.
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := cache.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	// 2. Check CODEX_BIN env.
	if env := strings.TrimSpace(os.Getenv("CODEX_BIN")); env != "" {
		if resolved, ok := cache.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	// 3. Check PATH for codex variants.
	for _, name := range []string{"codex", "codex.cmd", "codex.exe"} {
		if resolved, err := cache.LookPath(name); err == nil {
			return resolved, nil
		}
	}
	// 4. Check npm-global locations.
	if isWindows() {
		for _, envVar := range []string{"APPDATA", "LOCALAPPDATA"} {
			if dir := strings.TrimSpace(os.Getenv(envVar)); dir != "" {
				candidate := filepath.Join(dir, "npm", "codex.cmd")
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					return candidate, nil
				}
			}
		}
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
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

func isWindows() bool {
	return runtime.GOOS == "windows"
}

// buildCodexCommand builds the "codex app-server --stdio" launch command.
// Global flags precede "app-server"; -c overrides sit between "app-server" and
// "--stdio"; extra args are appended last. The model is passed via thread/start.
func buildCodexCommand(exe string, cfgOverrides, extraArgs []string) []string {
	cmd := []string{
		exe,
		"--dangerously-bypass-approvals-and-sandbox",
		"--search",
		"app-server",
	}
	for _, override := range cfgOverrides {
		cmd = append(cmd, "-c", override)
	}
	cmd = append(cmd, "--stdio")
	cmd = append(cmd, extraArgs...)
	return cmd
}

func buildCodexCommandWithVerbosity(exe string, cfgOverrides []string, verbosity aipapi.VerbosityLevel, extraArgs []string) []string {
	overrides := make([]string, 0, len(cfgOverrides)+1)
	if verbosity == "" {
		overrides = append(overrides, cfgOverrides...)
	} else {
		for _, override := range cfgOverrides {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(override)), "model_verbosity=") {
				continue
			}
			overrides = append(overrides, override)
		}
		overrides = append(overrides, "model_verbosity="+string(verbosity))
	}
	return buildCodexCommand(exe, overrides, extraArgs)
}

// codexServerRequestHandler handles inbound JSON-RPC approval requests from the
// Codex app-server. Auto-accepts known approval methods; declines everything else.
type codexServerRequestHandler struct{}

func (h *codexServerRequestHandler) HandleServerRequest(_ context.Context, method string, _ json.RawMessage, params json.RawMessage) (any, error) {
	if autoAcceptMethods[method] {
		if method == "item/permissions/requestApproval" {
			// Echo back the requested permissions as granted.
			var p struct {
				Permissions map[string]any `json:"permissions"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Permissions == nil {
				p.Permissions = map[string]any{}
			}
			return map[string]any{"permissions": p.Permissions}, nil
		}
		return map[string]any{"decision": "accept"}, nil
	}
	// Unknown method — fail closed with decline (not an error, to avoid terminating the stream).
	return map[string]any{"decision": "decline"}, nil
}

// stripOpenAIModelPrefix strips a leading "openai/" vendor prefix.
func stripOpenAIModelPrefix(model string) string {
	if model == "" {
		return ""
	}
	if strings.HasPrefix(model, "openai/") {
		return model[len("openai/"):]
	}
	return model
}

// isAutoModel returns true when the model is empty or "auto".
func isAutoModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return m == "" || m == autoModelSentinel
}

// defaultInventoryModels builds the built-in inventory. openai/auto is always
// advertised. Catalog routable slugs are included only when source is
// SourceDiscovered so shipped/override fallback snapshots are not treated as
// proven App Server models.
func defaultInventoryModels(cat *catalog.Catalog, src catalog.Source) []modelinventory.Model {
	ids := []string{autoModelSentinel}
	if src == catalog.SourceDiscovered && cat != nil {
		ids = append(ids, cat.RoutableSlugs()...)
	}
	return acp.DefaultInventoryModels(vendorPrefix, ids)
}

// New returns a runtime engine that invokes the Codex CLI app-server via stdio.
func New(cfg Config) (*Engine, error) {
	verbosity, err := aipapi.ParseVerbosityLevel(string(cfg.DefaultVerbosity))
	if err != nil {
		return nil, fmt.Errorf("%s: default_verbosity: %w", ID, err)
	}
	cfg.DefaultVerbosity = verbosity
	cfg.applyDefaults()
	return newBackend(cfg, true, nil), nil
}

// NewWithStarter is like New but injects a custom ProcessStarter for testing.
func NewWithStarter(cfg Config, starter acp.ProcessStarter) *Engine {
	clearInvalidDefaultVerbosity(&cfg)
	cfg.applyDefaults()
	return newBackend(cfg, false, starter)
}

// clearInvalidDefaultVerbosity normalizes DefaultVerbosity for test constructors.
// Unlike New, it cannot fail the call, so invalid values are cleared instead of
// being forwarded into -c model_verbosity=<raw>.
func clearInvalidDefaultVerbosity(cfg *Config) {
	verbosity, err := aipapi.ParseVerbosityLevel(string(cfg.DefaultVerbosity))
	if err != nil {
		cfg.DefaultVerbosity = ""
		return
	}
	cfg.DefaultVerbosity = verbosity
}

// applyDefaults normalizes the config model field.
func (cfg *Config) applyDefaults() {
	if cfg.Model == "" {
		cfg.Model = autoModelSentinel
	}
	cfg.Model = stripOpenAIModelPrefix(cfg.Model)
}

// newBackend builds the execbackend.Backend struct from a normalized Config.
// requireExplicitWorkspace controls whether the WorkspacePolicy requires an
// explicit workspace (production) or allows fallback to the default (tests).
// starter, if non-nil, overrides the default OS process starter (used by tests)
// and selects test-mode executable resolution: the spec.exe is set to a
// placeholder so BuildSpawnCommand does not require a real codex binary.
func newBackend(cfg Config, requireExplicitWorkspace bool, starter acp.ProcessStarter) *Engine {
	spec := &codexSpec{cfg: cfg}
	cache := cfg.ExeCache
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	var exeErr error
	var exePath string
	if starter != nil {
		spec.exe = "codex"
		exePath = spec.exe
	} else {
		spec.exe, exeErr = resolveExecutable(cache, cfg.Executable)
		exePath = spec.exe
	}
	index := acp.NewModelIndex(codexCanonicalFallback)
	spec.index = index
	inv := cfg.Inventory
	if inv == nil {
		if cfg.CatalogLoadOpts.Enabled {
			inv = catalog.InventoryProvider{
				Opts: cfg.CatalogLoadOpts,
				Map: func(cat *catalog.Catalog, src catalog.Source) []modelinventory.Model {
					return defaultInventoryModels(cat, src)
				},
			}
		} else {
			inv = modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: defaultInventoryModels(cfg.ModelCatalog, cfg.ModelCatalogSource),
			}
		}
	}
	tracking := acp.NewTrackingInventory(inv, index, ID)
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: requireExplicitWorkspace,
	}
	backendCfg := acp.SubprocessBackendConfig{
		Protocol:  &codexProtocol{spec: spec},
		Workspace: workspace,
		Pool: acp.RuntimePoolConfig{
			IdleTimeout:    cfg.IdleTimeout,
			StaleKillDelay: cfg.StaleKillDelay,
		},
	}
	if starter != nil {
		backendCfg.ProcessStarter = starter
	}
	backend := acp.NewSubprocessBackend(backendCfg)
	return &Engine{
		inventory: tracking,
		Caps:      defaultBackendCaps(),
		exeCache:  cache,
		exePath:   exePath,
		closeFn:   backend.Close,
		open: func(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
			if exeErr != nil {
				return nil, exeErr
			}
			if _, err := spec.resolveAllowedModel(call); err != nil {
				return nil, err
			}
			return backend.Open(ctx, call)
		},
	}
}

func defaultBackendCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityTools,
		aipapi.CapabilityVision,
		aipapi.CapabilityReasoning,
	)
}
