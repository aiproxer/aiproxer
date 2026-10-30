package product

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/agycliacp/internal/product/wrapperinstall"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// ID is the reserved plugin identifier for the AGY CLI ACP backend.
const ID = "agycliacp"

// vendorPrefix is the model prefix for AGY models.
const vendorPrefix = "agy"

// DefaultTimeoutSeconds is the per-turn ceiling forwarded to the wrapper.
// agy's own --print-timeout default is 5 minutes; keep this aligned with
// go-agy-acp-wrapper so long tool waits (test suites) survive.
const DefaultTimeoutSeconds = 4 * 60 * 60

// Config configures the AGY CLI ACP backend. The shared acp.ConnectorConfig
// embeds the local-agent fields common to every ACP CLI connector; AGY-specific
// fields (wrapper, binary, permissions, timeout, mcp) stay directly on Config.
type Config struct {
	acp.ConnectorConfig
	// WrapperExecutable is the path to the go-agy-acp-wrapper binary.
	// If empty, resolves from AGY_ACP_WRAPPER_BIN, PATH, or managed installation.
	WrapperExecutable string
	// WrapperAutoDownload installs the latest stable wrapper when local resolution fails.
	WrapperAutoDownload bool
	// WrapperCacheDir overrides the managed wrapper cache directory.
	WrapperCacheDir string
	// AGYBinary is an optional --agy-binary flag value pointing to the agy binary.
	AGYBinary string
	// SkipPermissions controls the --skip-permissions vs --no-skip-permissions flag.
	SkipPermissions bool
	// TimeoutSeconds is forwarded to go-agy-acp-wrapper as --timeout-seconds
	// (and from there to agy --print-timeout). Zero/omitted uses DefaultTimeoutSeconds.
	TimeoutSeconds int
	// MCPServers is the JSON array for session/new mcpServers.
	MCPServers json.RawMessage
	// Inventory overrides ModelInventory when non-nil (tests / NewWithStarter).
	// Production New wires a live `agy models` provider (or ErrorProvider on resolve failure).
	Inventory modelinventory.Provider
	// ExeCache is instance-owned LookPath cache. When nil, New allocates one.
	ExeCache *acp.ExecutableCache
}

// agySpec implements acp.SubprocessConnectorSpec for the AGY CLI.
type agySpec struct {
	cfg   Config
	index *acp.ModelIndex
	exe   string // resolved wrapper path; BuildCommand never re-resolves
}

func (s *agySpec) VendorID() string                { return ID }
func (s *agySpec) VendorPrefix() string            { return vendorPrefix }
func (s *agySpec) RequiresExplicitWorkspace() bool { return true }

// BuildCommand returns the subprocess command for the go-agy-acp-wrapper.
func (s *agySpec) BuildCommand(model string, workspace string) ([]string, string, []string, error) {
	model = strings.TrimSpace(model)
	if s == nil {
		return nil, "", nil, ErrUnknownModel
	}
	if model == "" || model == "auto" || !s.index.IsKnownNative(model) {
		return nil, "", nil, ErrUnknownModel
	}
	if s.exe == "" {
		return nil, "", nil, fmt.Errorf("agycliacp: wrapper executable not resolved")
	}
	cmd := []string{s.exe}
	if s.cfg.AGYBinary != "" {
		cmd = append(cmd, "--agy-binary", s.cfg.AGYBinary)
	}
	cmd = append(cmd, "--model", model)
	cmd = append(cmd, "--timeout-seconds", strconv.Itoa(normalizeTimeoutSeconds(s.cfg.TimeoutSeconds)))
	if s.cfg.SkipPermissions {
		cmd = append(cmd, "--skip-permissions")
	} else {
		cmd = append(cmd, "--no-skip-permissions")
	}
	if len(s.cfg.ExtraArgs) > 0 {
		cmd = append(cmd, s.cfg.ExtraArgs...)
	}
	return cmd, workspace, agyProcessEnv(), nil
}

// HandshakeProfile returns the AGY-specific handshake configuration.
// AGY uses a full handshake: initialize → authenticate (methodId: "agy") → session/new.
func (s *agySpec) HandshakeProfile() acp.HandshakeProfile {
	mcp := json.RawMessage("[]")
	if len(s.cfg.MCPServers) > 0 {
		mcp = s.cfg.MCPServers
	}
	return acp.HandshakeProfile{
		ProtocolVersion:      1,
		SkipAuthenticate:     false,
		AuthenticateParams:   json.RawMessage(`{"methodId":"agy"}`),
		SessionNewCwd:        s.cfg.DefaultWorkspace,
		SessionNewMCPServers: mcp,
		ClientCapabilities:   json.RawMessage(`{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}`),
		ClientInfo:           json.RawMessage(`{"name":"aiproxer","version":"1"}`),
	}
}

func (s *agySpec) CancelProfile() acp.CancelProfile {
	return acp.CancelProfile{
		Methods:          []string{"session/cancel", "session/stop", "session/end"},
		IncludeRequestID: true,
		IncludeMessageID: true,
	}
}

// ServerRequestHandler returns the AGY-specific handler.
// agy/* methods get empty result replies; unknown methods get a method-not-found error.
func (s *agySpec) ServerRequestHandler() acp.ServerRequestHandler {
	return &agyServerRequestHandler{}
}

// ResolveModel strips the route-level "agy:" / "agy/" prefix, applies configured
// or package defaults for empty/auto, and maps known canonical IDs to the exact
// AGY pretty name required by `--model` when present in the active allowlist.
// Unknown or unadvertised identities resolve to "" so Open/BuildCommand can
// reject them explicitly without substituting a default.
func (s *agySpec) ResolveModel(effectiveModel string) string {
	native, err := s.resolveNativeModel(effectiveModel)
	if err != nil {
		return ""
	}
	return native
}

// agyServerRequestHandler handles inbound JSON-RPC requests from the AGY wrapper.
type agyServerRequestHandler struct{}

func (h *agyServerRequestHandler) HandleServerRequest(_ context.Context, method string, _ json.RawMessage, _ json.RawMessage) (any, error) {
	if strings.HasPrefix(method, "agy/") {
		// Unhandled AGY extension — reply with empty result (headless proxy).
		return map[string]any{}, nil
	}
	// Unknown method — return method-not-found error.
	return nil, fmt.Errorf("method not handled: %s", method)
}

// resolveWrapper finds the go-agy-acp-wrapper binary.
func resolveWrapper(cache *acp.ExecutableCache, configured string) (string, error) {
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	if c := strings.TrimSpace(configured); c != "" {
		if resolved, ok := cache.CheckExecutable(c); ok {
			return resolved, nil
		}
	}
	if env := strings.TrimSpace(os.Getenv("AGY_ACP_WRAPPER_BIN")); env != "" {
		if resolved, ok := cache.CheckExecutable(env); ok {
			return resolved, nil
		}
	}
	for _, name := range []string{"go-agy-acp-wrapper", "go-agy-acp-wrapper.exe"} {
		if resolved, err := cache.LookPath(name); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("go-agy-acp-wrapper executable not found; build go-agy-acp-wrapper and put it on PATH, or set AGY_ACP_WRAPPER_BIN / wrapper_executable")
}

// New returns a runtime backend that invokes the AGY CLI via ACP stdio.
func New(cfg Config) (*Engine, error) {
	if cfg.Model == "" {
		cfg.Model = defaultCanonicalModel
	}
	cfg.TimeoutSeconds = normalizeTimeoutSeconds(cfg.TimeoutSeconds)
	if cfg.AGYBinary == "" {
		cfg.AGYBinary = strings.TrimSpace(os.Getenv("AGY_BINARY"))
	}
	cache := cfg.ExeCache
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	spec := &agySpec{cfg: cfg}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	exe, exeErr := resolveWrapper(cache, cfg.WrapperExecutable)
	if exeErr != nil && cfg.WrapperAutoDownload {
		exe, exeErr = wrapperinstall.Ensure(context.Background(), wrapperinstall.Options{CacheDir: cfg.WrapperCacheDir})
	}
	spec.exe = exe
	inv := cfg.Inventory
	if inv == nil {
		bin, err := resolveAGYBinary(cache, cfg.AGYBinary)
		if err != nil {
			inv = modelinventory.ErrorProvider{Err: err}
		} else {
			inv = newModelsProvider(bin)
		}
	}
	return newAGYBackend(spec, workspace, cfg, nil, exeErr, inv, cache, exe), nil
}

// NewWithStarter is like New but injects a custom ProcessStarter for testing.
// Production code should use New; tests use this to inject a fakeProcess that
// simulates the go-agy-acp-wrapper without spawning a real binary. It skips
// wrapper executable resolution and live `agy models` discovery unless
// Config.Inventory is set.
func NewWithStarter(cfg Config, starter acp.ProcessStarter) *Engine {
	if cfg.Model == "" {
		cfg.Model = defaultCanonicalModel
	}
	cfg.TimeoutSeconds = normalizeTimeoutSeconds(cfg.TimeoutSeconds)
	if cfg.AGYBinary == "" {
		cfg.AGYBinary = strings.TrimSpace(os.Getenv("AGY_BINARY"))
	}
	cache := cfg.ExeCache
	if cache == nil {
		cache = &acp.ExecutableCache{}
	}
	exe := strings.TrimSpace(cfg.WrapperExecutable)
	if exe == "" {
		exe = "go-agy-acp-wrapper"
	}
	spec := &agySpec{cfg: cfg, exe: exe}
	workspace := acp.WorkspacePolicy{
		DefaultDir:      cfg.DefaultWorkspace,
		RequireExplicit: spec.RequiresExplicitWorkspace(),
	}
	inv := cfg.Inventory
	if inv == nil {
		inv = modelinventory.ErrorProvider{}
	}
	return newAGYBackend(spec, workspace, cfg, starter, nil, inv, cache, exe)
}

// newAGYBackend assembles the execbackend.Backend from a normalized spec,
// workspace, and config. starter, if non-nil, overrides the default OS process
// starter (used by tests). exeErr is surfaced from the Open closure so callers
// see startup errors early. inventory is wrapped in a tracking provider whose
// ModelIndex is updated by AcceptInventory after registry publish (not by
// LoadModels).
func newAGYBackend(spec *agySpec, workspace acp.WorkspacePolicy, cfg Config, starter acp.ProcessStarter, exeErr error, inventory modelinventory.Provider, cache *acp.ExecutableCache, exePath string) *Engine {
	index := acp.NewModelIndex(agyCanonicalFallback)
	spec.index = index
	tracking := acp.NewTrackingInventory(inventory, index, ID)
	backendCfg := acp.SubprocessBackendConfig{
		Protocol:  acp.NewACPProtocol(spec, nil),
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
		Inventory: tracking,
		Caps:      defaultBackendCaps(),
		exeCache:  cache,
		exePath:   exePath,
		closeFn:   backend.Close,
		open: func(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
			if exeErr != nil {
				return nil, exeErr
			}
			call = withDefaultWorkspace(call, cfg.DefaultWorkspace)
			if _, err := spec.resolveNativeModel(acp.CallRouteModel(call, "acp.model")); err != nil {
				return nil, err
			}
			return backend.Open(ctx, call)
		},
	}
}

func withDefaultWorkspace(call *aipapi.Call, workspace string) *aipapi.Call {
	workspace = strings.TrimSpace(workspace)
	if call == nil || workspace == "" {
		return call
	}
	cp := *call
	if cp.Extensions == nil {
		cp.Extensions = map[string]json.RawMessage{}
	} else {
		ext := make(map[string]json.RawMessage, len(cp.Extensions)+1)
		maps.Copy(ext, cp.Extensions)
		cp.Extensions = ext
	}
	if _, ok := cp.Extensions["acp.cwd"]; ok {
		return &cp
	}
	raw, _ := json.Marshal(workspace)
	cp.Extensions["acp.cwd"] = raw
	return &cp
}

func defaultBackendCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityVision,
		aipapi.CapabilityReasoning,
	)
}

func normalizeTimeoutSeconds(n int) int {
	if n <= 0 {
		return DefaultTimeoutSeconds
	}
	return n
}
